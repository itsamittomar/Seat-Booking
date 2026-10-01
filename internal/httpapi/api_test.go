package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"bookingSystem/internal/auth"
	"bookingSystem/internal/booking"
	"bookingSystem/internal/metrics"
	"bookingSystem/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *auth.Auth) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, url, 10, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	a := auth.New("test-secret", "admin")
	srv := httptest.NewServer(NewHandler(Deps{DB: s, Service: booking.NewService(s.Pool), Auth: a, Metrics: metrics.New()}))
	t.Cleanup(srv.Close)
	return srv, a
}

type call struct {
	method, url, token string
	body               any
	headers            map[string]string
}

func do(t *testing.T, c call) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if c.body != nil {
		_ = json.NewEncoder(&buf).Encode(c.body)
	}
	req, _ := http.NewRequest(c.method, c.url, &buf)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestEndToEnd(t *testing.T) {
	srv, a := newTestServer(t)
	run := fmt.Sprint(time.Now().UnixNano())
	alice, _ := a.Sign("alice-" + run)
	bob, _ := a.Sign("bob-" + run)
	u := srv.URL

	resp, _ := do(t, call{method: "POST", url: u + "/shows", token: alice, body: map[string]any{"name": "x", "seats": []string{"A1"}, "price_paise": 1}})
	if resp.StatusCode != 401 {
		t.Fatalf("user token must not create shows: %d", resp.StatusCode)
	}
	resp, show := do(t, call{method: "POST", url: u + "/shows", token: "admin", body: map[string]any{"name": "x", "seats": []string{"A1", "A2", "A3"}, "price_paise": 25000}})
	if resp.StatusCode != 201 || show["counts"].(map[string]any)["available"].(float64) != 3 {
		t.Fatalf("create show: %d %v", resp.StatusCode, show)
	}
	showID := show["id"].(string)
	reserveURL := u + "/shows/" + showID + "/reserve"

	// a spoofed user_id in the body is ignored: the reservation belongs to the token's user
	resp, res := do(t, call{method: "POST", url: reserveURL, token: alice,
		body: map[string]any{"seats": []string{"A1"}, "idempotency_key": "k1", "user_id": "bob-" + run}})
	if resp.StatusCode != 201 || res["user_id"] != "alice-"+run || res["amount_paise"].(float64) != 25000 || res["status"] != "held" {
		t.Fatalf("reserve: %d %v", resp.StatusCode, res)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
	rid := res["reservation_id"].(string)

	resp, again := do(t, call{method: "POST", url: reserveURL, token: alice, body: map[string]any{"seats": []string{"A1"}, "idempotency_key": "k1"}})
	if resp.StatusCode != 200 || resp.Header.Get("Idempotent-Replay") != "true" || again["reservation_id"] != rid {
		t.Fatalf("replay: %d %v", resp.StatusCode, again)
	}
	resp, body := do(t, call{method: "POST", url: reserveURL, token: alice, body: map[string]any{"seats": []string{"A2"}, "idempotency_key": "k1"}})
	if resp.StatusCode != 409 || body["error"] != "idempotency_mismatch" {
		t.Fatalf("mismatch: %d %v", resp.StatusCode, body)
	}
	// the Idempotency-Key header is accepted and wins over the body
	resp, body = do(t, call{method: "POST", url: reserveURL, token: bob, body: map[string]any{"seats": []string{"A1"}},
		headers: map[string]string{"Idempotency-Key": "hk"}})
	if resp.StatusCode != 409 || body["error"] != "seat_taken" {
		t.Fatalf("taken: %d %v", resp.StatusCode, body)
	}
	resp, body = do(t, call{method: "POST", url: reserveURL, token: bob, body: map[string]any{"seats": []string{"A2"}}})
	if resp.StatusCode != 422 || body["error"] != "validation" {
		t.Fatalf("missing key: %d %v", resp.StatusCode, body)
	}

	resp, _ = do(t, call{method: "POST", url: u + "/reservations/" + rid + "/cancel", token: bob})
	if resp.StatusCode != 403 {
		t.Fatalf("bob cancelling alice's hold: %d", resp.StatusCode)
	}
	resp, _ = do(t, call{method: "GET", url: u + "/reservations/" + rid, token: bob})
	if resp.StatusCode != 403 {
		t.Fatalf("bob reading alice's hold: %d", resp.StatusCode)
	}
	resp, body = do(t, call{method: "POST", url: u + "/reservations/" + rid + "/confirm", token: alice})
	if resp.StatusCode != 200 || body["status"] != "confirmed" {
		t.Fatalf("confirm: %d %v", resp.StatusCode, body)
	}
	resp, body = do(t, call{method: "GET", url: u + "/shows/" + showID})
	counts := body["counts"].(map[string]any)
	if resp.StatusCode != 200 || counts["confirmed"].(float64) != 1 || counts["available"].(float64) != 2 || counts["total"].(float64) != 3 {
		t.Fatalf("show state: %v", counts)
	}

	resp, _ = do(t, call{method: "POST", url: reserveURL, body: map[string]any{"seats": []string{"A2"}, "idempotency_key": "k9"}})
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	resp, _ = do(t, call{method: "POST", url: reserveURL, token: "alice-" + run + ".deadbeef", body: map[string]any{"seats": []string{"A2"}, "idempotency_key": "k9"}})
	if resp.StatusCode != 401 {
		t.Fatalf("forged token: %d", resp.StatusCode)
	}
	resp, _ = do(t, call{method: "GET", url: u + "/shows/not-a-uuid"})
	if resp.StatusCode != 404 {
		t.Fatalf("bad show id: %d", resp.StatusCode)
	}
	resp, body = do(t, call{method: "POST", url: u + "/auth/token", body: map[string]any{"user_id": "carol"}})
	if resp.StatusCode != 200 || body["token"] == "" {
		t.Fatalf("mint: %d %v", resp.StatusCode, body)
	}

	mresp, err := http.Get(u + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(mresp.Body)
	mresp.Body.Close()
	for _, want := range []string{
		"reservations_held_total 1",
		`reservations_declined_total{reason="idempotent_replay"} 1`,
		`reservations_declined_total{reason="idempotency_mismatch"} 1`,
		`reservations_declined_total{reason="seat_taken"} 1`,
		"reservations_confirmed_total 1",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
