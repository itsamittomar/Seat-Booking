// Command burst reproduces an on-sale stampede against a running deployment and checks the
// correctness bar: one winner per seat, per-user limit, idempotent retries, zero 5xx, and the
// reconciliation invariant during and after the burst. It exits non-zero if any check fails.
//
//	go run ./cmd/burst -base-url https://example.onrender.com -admin-token ...
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	baseURL     = flag.String("base-url", "http://localhost:8080", "service base URL")
	adminToken  = flag.String("admin-token", "", "admin token for POST /shows (required)")
	numSeats    = flag.Int("seats", 500, "seats in the test show")
	hotSeats    = flag.Int("hot-seats", 5, "number of hot seats to storm")
	hotUsers    = flag.Int("hot-users", 500, "distinct users racing for each hot seat")
	spread      = flag.Int("spread", 3000, "single-seat requests from distinct users over the remaining seats")
	retries     = flag.Int("retries", 50, "parallel retries sharing one idempotency key")
	concurrency = flag.Int("concurrency", 300, "max in-flight requests")
	timeout     = flag.Duration("timeout", 120*time.Second, "per-request timeout")
)

var client *http.Client

type job struct {
	kind  string // hot | limit | idem | spread
	user  int
	key   string
	seats []string
	group int // hot seat index for kind=hot
}

type result struct {
	job
	status        int
	code          string // response error code, "held" or "replay"
	replayed      bool
	reservationID string
	transportErr  error
	dur           time.Duration
}

func main() {
	flag.Parse()
	if *adminToken == "" {
		fmt.Fprintln(os.Stderr, "-admin-token is required")
		os.Exit(2)
	}
	*baseURL = strings.TrimRight(*baseURL, "/")
	client = &http.Client{Timeout: *timeout, Transport: &http.Transport{
		MaxIdleConns: *concurrency, MaxIdleConnsPerHost: *concurrency, MaxConnsPerHost: *concurrency,
	}}

	labels := seatLabels(*numSeats)
	hot := labels[11 : 11+*hotSeats] // A12, A13, ... the "good" seats
	rest := append(append([]string{}, labels[:11]...), labels[11+*hotSeats:]...)
	limitSeats, idemSeat, spreadSeats := rest[:10], rest[10], rest[11:]

	fmt.Printf("target %s\n", *baseURL)
	waitReady()
	showID := createShow(labels)
	fmt.Printf("show %s: %d seats, per_user_limit 4, hot seats %v\n", showID, len(labels), hot)

	nUsers := *hotSeats**hotUsers + *spread + 3
	fmt.Printf("minting %d user tokens...\n", nUsers)
	tokens := mintTokens(nUsers)
	limitUser, idemUser, strangerUser := nUsers-3, nUsers-2, nUsers-1

	var jobs []job
	u := 0
	for h, seat := range hot {
		for i := 0; i < *hotUsers; i++ {
			jobs = append(jobs, job{kind: "hot", user: u, key: fmt.Sprintf("hot-%d", u), seats: []string{seat}, group: h})
			u++
		}
	}
	for i := 0; i < *spread; i++ {
		jobs = append(jobs, job{kind: "spread", user: u, key: fmt.Sprintf("spread-%d", u), seats: []string{spreadSeats[i%len(spreadSeats)]}})
		u++
	}
	for i, seat := range limitSeats {
		jobs = append(jobs, job{kind: "limit", user: limitUser, key: fmt.Sprintf("limit-%d", i), seats: []string{seat}})
	}
	for i := 0; i < *retries; i++ {
		jobs = append(jobs, job{kind: "idem", user: idemUser, key: "idem-shared", seats: []string{idemSeat}})
	}
	rand.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })

	before := scrapeMetrics()

	// Poll the invariant while the burst runs.
	var samples, violations atomic.Int64
	stopPoll := make(chan struct{})
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		for {
			select {
			case <-stopPoll:
				return
			case <-time.After(250 * time.Millisecond):
			}
			if c, err := showCounts(showID); err == nil {
				samples.Add(1)
				if c.Available+c.Held+c.Confirmed != c.Total {
					violations.Add(1)
					fmt.Printf("  invariant violated mid-burst: %+v\n", c)
				}
			}
		}
	}()

	fmt.Printf("firing %d reserve requests, %d in flight...\n", len(jobs), *concurrency)
	results := make([]result, len(jobs))
	work := make(chan int)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			for i := range work {
				results[i] = reserve(showID, tokens[jobs[i].user], jobs[i])
			}
		}()
	}
	start := time.Now()
	close(gate)
	for i := range jobs {
		work <- i
	}
	close(work)
	wg.Wait()
	elapsed := time.Since(start)
	close(stopPoll)
	<-pollDone

	// Same key, different seat: must be rejected.
	mismatch := reserve(showID, tokens[idemUser], job{kind: "idem", key: "idem-shared", seats: []string{spreadSeats[0]}})

	r := newReport()
	r.distribution(results, elapsed)

	// ---- correctness checks
	fmt.Println("\n=== checks ===")
	winsPerHot := make([]int, len(hot))
	seatWinner := map[string]int{}
	doubleSold := 0
	fivexx, transport := 0, 0
	var limitHolds, idemCreated, idemReplayed int
	idemIDs := map[string]bool{}
	for _, res := range results {
		if res.status >= 500 {
			fivexx++
		}
		if res.transportErr != nil {
			transport++
		}
		if res.status == 201 {
			for _, s := range res.seats {
				if _, dup := seatWinner[s]; dup {
					doubleSold++
				}
				seatWinner[s] = res.user
			}
		}
		switch res.kind {
		case "hot":
			if res.status == 201 {
				winsPerHot[res.group]++
			}
		case "limit":
			if res.status == 201 {
				limitHolds++
			}
		case "idem":
			switch res.status {
			case 201:
				idemCreated++
				idemIDs[res.reservationID] = true
			case 200:
				idemReplayed++
				idemIDs[res.reservationID] = true
			}
		}
	}
	for i, w := range winsPerHot {
		hotLosers := 0
		for _, res := range results {
			if res.kind == "hot" && res.group == i && res.status == 409 && res.code == "seat_taken" {
				hotLosers++
			}
		}
		r.check(w == 1 && hotLosers == *hotUsers-1, "hot seat %s: exactly one 201, %d x 409 seat_taken (got %d / %d)", hot[i], *hotUsers-1, w, hotLosers)
	}
	r.check(doubleSold == 0, "no seat won by two requests across the whole burst (duplicates: %d)", doubleSold)
	r.check(limitHolds == 4, "per-user limit: 10 parallel reserves on limit 4 left exactly 4 holds (got %d)", limitHolds)
	r.check(idemCreated == 1 && idemReplayed == *retries-1 && len(idemIDs) == 1,
		"idempotency: %d parallel retries -> 1 created, %d replayed, 1 reservation id (got %d / %d / %d)", *retries, *retries-1, idemCreated, idemReplayed, len(idemIDs))
	r.check(mismatch.status == 409 && mismatch.code == "idempotency_mismatch", "same key, different seats -> 409 idempotency_mismatch (got %d %s)", mismatch.status, mismatch.code)
	r.check(fivexx == 0, "zero 5xx (got %d)", fivexx)
	r.check(transport == 0, "zero transport errors (got %d)", transport)
	r.check(violations.Load() == 0, "invariant held in all %d mid-burst samples", samples.Load())

	// ---- follow-up: confirm hot winners, stranger cannot cancel, spoofed body is ignored
	var hotWinner *result
	confirmed := 0
	for i := range results {
		res := &results[i]
		if res.kind == "hot" && res.status == 201 {
			if hotWinner == nil {
				hotWinner = res
			}
			if code, _ := post("/reservations/"+res.reservationID+"/confirm", tokens[res.user], nil); code == 200 {
				confirmed++
			}
		}
	}
	r.check(confirmed == len(hot), "owners confirmed all %d hot-seat holds (got %d)", len(hot), confirmed)
	if hotWinner != nil {
		code, _ := post("/reservations/"+hotWinner.reservationID+"/cancel", tokens[strangerUser], nil)
		r.check(code == 403, "another user cannot cancel a hold it does not own (got %d)", code)
	}
	// On a separate one-seat show so the seat is guaranteed free: a 201 must name the token's user.
	spoofShow := createShowNamed("burst-spoof", []string{"S1"})
	code, body := post("/shows/"+spoofShow+"/reserve", tokens[strangerUser],
		map[string]any{"seats": []string{"S1"}, "idempotency_key": "spoof", "user_id": "u0"})
	var spoof struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(body, &spoof)
	r.check(code == 201 && spoof.UserID == fmt.Sprintf("u%d", strangerUser),
		"spoofed body user_id is ignored; identity comes from the token (got %d user_id=%q)", code, spoof.UserID)

	// ---- final reconciliation
	final, err := showCounts(showID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get show: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n=== final show state ===\n  available=%d held=%d confirmed=%d total=%d\n", final.Available, final.Held, final.Confirmed, final.Total)
	r.check(final.Available+final.Held+final.Confirmed == final.Total, "reconciliation: available + held + confirmed == total")
	wonSeats := len(seatWinner)
	r.check(final.Held+final.Confirmed == wonSeats, "held + confirmed (%d) equals seats won by 201 responses (%d)", final.Held+final.Confirmed, wonSeats)

	time.Sleep(1500 * time.Millisecond) // let the 1s gauge refresh catch up
	spoofHeld := 0
	if code == 201 {
		spoofHeld = 1
	}
	r.metricsDeltas(before, scrapeMetrics(), results, showID, final, confirmed, spoofHeld)

	if r.failed {
		fmt.Println("\nRESULT: FAIL")
		os.Exit(1)
	}
	fmt.Println("\nRESULT: PASS")
}

// ---------------------------------------------------------------- HTTP helpers

// seatLabels returns hall-style labels, 25 seats per row: A1..A25, B1.., Z25, AA1, AB1...
func seatLabels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", rowName(i/25), i%25+1)
	}
	return out
}

func rowName(r int) string {
	name := ""
	for r++; r > 0; r = (r - 1) / 26 {
		name = string(rune('A'+(r-1)%26)) + name
	}
	return name
}

func waitReady() {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, err := client.Get(*baseURL + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			fatal("service never became ready: %v", err)
		}
		fmt.Println("  waiting for /readyz (cold start?)...")
		time.Sleep(3 * time.Second)
	}
}

func post(path, token string, body any) (int, []byte) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("POST", *baseURL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func createShow(labels []string) string { return createShowNamed("burst", labels) }

func createShowNamed(prefix string, labels []string) string {
	code, body := post("/shows", *adminToken, map[string]any{
		"name": prefix + "-" + time.Now().UTC().Format("20060102-150405"), "seats": labels, "price_paise": 25000, "per_user_limit": 4,
	})
	if code != 201 {
		fatal("create show: %d %s", code, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &out)
	return out.ID
}

func mintTokens(n int) []string {
	tokens := make([]string, n)
	var wg sync.WaitGroup
	var failed atomic.Int64
	sem := make(chan struct{}, 100)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for attempt := 0; attempt < 3; attempt++ {
				code, body := post("/auth/token", "", map[string]string{"user_id": fmt.Sprintf("u%d", i)})
				var out struct {
					Token string `json:"token"`
				}
				if code == 200 && json.Unmarshal(body, &out) == nil && out.Token != "" {
					tokens[i] = out.Token
					return
				}
			}
			failed.Add(1)
		}(i)
	}
	wg.Wait()
	if failed.Load() > 0 {
		fatal("could not mint %d tokens", failed.Load())
	}
	return tokens
}

func reserve(showID, token string, j job) result {
	res := result{job: j}
	body, _ := json.Marshal(map[string]any{"seats": j.seats, "idempotency_key": j.key})
	req, _ := http.NewRequest("POST", *baseURL+"/shows/"+showID+"/reserve", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	t0 := time.Now()
	resp, err := client.Do(req)
	res.dur = time.Since(t0)
	if err != nil {
		res.transportErr = err
		res.code = "transport_error"
		return res
	}
	defer resp.Body.Close()
	res.status = resp.StatusCode
	res.replayed = resp.Header.Get("Idempotent-Replay") == "true"
	var b struct {
		Error         string `json:"error"`
		ReservationID string `json:"reservation_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&b)
	res.reservationID = b.ReservationID
	switch {
	case resp.StatusCode == 201:
		res.code = "held"
	case resp.StatusCode == 200 && res.replayed:
		res.code = "replay"
	case b.Error != "":
		res.code = b.Error
	default:
		res.code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return res
}

type counts struct{ Available, Held, Confirmed, Total int }

func showCounts(showID string) (counts, error) {
	resp, err := client.Get(*baseURL + "/shows/" + showID)
	if err != nil {
		return counts{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return counts{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Counts counts `json:"counts"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.Counts, err
}

func scrapeMetrics() map[string]float64 {
	out := map[string]float64{}
	resp, err := client.Get(*baseURL + "/metrics")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "reservations_") && !strings.HasPrefix(line, "seats_") {
			continue
		}
		name, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		var f float64
		if _, err := fmt.Sscanf(val, "%g", &f); err == nil {
			out[name] = f
		}
	}
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// ---------------------------------------------------------------- reporting

type report struct{ failed bool }

func newReport() *report { return &report{} }

func (r *report) check(ok bool, format string, args ...any) {
	mark := "PASS"
	if !ok {
		mark, r.failed = "FAIL", true
	}
	fmt.Printf("  [%s] %s\n", mark, fmt.Sprintf(format, args...))
}

func (r *report) distribution(results []result, elapsed time.Duration) {
	type key struct{ kind, outcome string }
	byKind := map[key]int{}
	byOutcome := map[string]int{}
	durs := make([]time.Duration, 0, len(results))
	for _, res := range results {
		outcome := fmt.Sprintf("%d %s", res.status, res.code)
		if res.transportErr != nil {
			outcome = "transport_error"
		}
		byKind[key{res.kind, outcome}]++
		byOutcome[outcome]++
		durs = append(durs, res.dur)
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	pct := func(p float64) time.Duration { return durs[int(float64(len(durs)-1)*p)].Round(time.Millisecond) }
	fmt.Printf("\n=== %d reserve requests in %s (%.0f req/s)  p50 %s  p95 %s  p99 %s  max %s ===\n",
		len(results), elapsed.Round(time.Millisecond), float64(len(results))/elapsed.Seconds(), pct(.5), pct(.95), pct(.99), pct(1))
	outs := make([]string, 0, len(byOutcome))
	for k := range byOutcome {
		outs = append(outs, k)
	}
	sort.Strings(outs)
	fmt.Println("  outcome                          total     hot  spread   limit    idem")
	for _, o := range outs {
		fmt.Printf("  %-30s %7d %7d %7d %7d %7d\n", o, byOutcome[o],
			byKind[key{"hot", o}], byKind[key{"spread", o}], byKind[key{"limit", o}], byKind[key{"idem", o}])
	}
}

// metricsDeltas compares server-side counter deltas with what this client observed.
// Mismatches are warnings, not failures: other traffic may hit the server at the same time.
func (r *report) metricsDeltas(before, after map[string]float64, results []result, showID string, final counts, confirmed, extraHeld int) {
	if len(after) == 0 {
		fmt.Println("\n(metrics endpoint not reachable; skipping reconciliation)")
		return
	}
	observed := map[string]int{}
	for _, res := range results {
		switch {
		case res.status == 201:
			observed["held"]++
		case res.replayed:
			observed["idempotent_replay"]++
		case res.status == 409 || res.status == 404 || res.status == 422:
			observed[res.code]++
		}
	}
	observed["idempotency_mismatch"]++ // the explicit mismatch probe
	observed["held"] += extraHeld      // the spoof probe on its own show
	delta := func(k string) float64 { return after[k] - before[k] }
	rows := []struct {
		name   string
		metric float64
		client int
	}{
		{"reservations_held_total", delta("reservations_held_total"), observed["held"]},
		{`reservations_declined_total{reason="seat_taken"}`, delta(`reservations_declined_total{reason="seat_taken"}`), observed["seat_taken"]},
		{`reservations_declined_total{reason="per_user_limit"}`, delta(`reservations_declined_total{reason="per_user_limit"}`), observed["per_user_limit"]},
		{`reservations_declined_total{reason="idempotent_replay"}`, delta(`reservations_declined_total{reason="idempotent_replay"}`), observed["idempotent_replay"]},
		{`reservations_declined_total{reason="idempotency_mismatch"}`, delta(`reservations_declined_total{reason="idempotency_mismatch"}`), observed["idempotency_mismatch"]},
		{"reservations_confirmed_total", delta("reservations_confirmed_total"), confirmed},
		{fmt.Sprintf(`seats_available{show_id="%s"}`, showID), after[fmt.Sprintf(`seats_available{show_id="%s"}`, showID)], final.Available},
		{fmt.Sprintf(`seats_held{show_id="%s"}`, showID), after[fmt.Sprintf(`seats_held{show_id="%s"}`, showID)], final.Held},
		{fmt.Sprintf(`seats_confirmed{show_id="%s"}`, showID), after[fmt.Sprintf(`seats_confirmed{show_id="%s"}`, showID)], final.Confirmed},
	}
	fmt.Println("\n=== metrics vs observed (server /metrics delta | client count) ===")
	for _, row := range rows {
		mark := "ok"
		if int(row.metric) != row.client {
			mark = "WARN mismatch (other traffic?)"
		}
		name := strings.Replace(row.name, showID, "<show>", 1)
		fmt.Printf("  %-62s %7.0f | %-7d %s\n", name, row.metric, row.client, mark)
	}
}
