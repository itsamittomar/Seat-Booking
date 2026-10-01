package booking

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func reserve(svc *Service, showID, user, key string, seats ...string) (ReserveResult, error) {
	return svc.Reserve(context.Background(), ReserveInput{ShowID: showID, UserID: user, IdempotencyKey: key, Seats: seats})
}

func code(err error) string {
	if d, ok := AsDecline(err); ok {
		return d.Code
	}
	if err != nil {
		return "error:" + err.Error()
	}
	return ""
}

func TestReserveHappyPath(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 10, 4, time.Minute)
	res, err := reserve(svc, show.ID, "u1", "k1", "a02", "A01")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Reservation
	if r.Status != ReservationHeld || r.AmountPaise != 50000 || len(r.Seats) != 2 || r.Seats[0] != "A01" || r.ExpiresAt == nil || res.Replayed || r.UserID != "u1" {
		t.Fatalf("unexpected: %+v", r)
	}
	st, _ := svc.GetShow(context.Background(), show.ID)
	if st.Counts.Held != 2 || st.Counts.Available != 8 {
		t.Fatalf("counts %+v", st.Counts)
	}
	assertInvariant(t, svc, show.ID)
}

func TestReserveValidation(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 2, 4, time.Minute)
	if c := code(reserve2(svc, show.ID, "u1", "k1", "Z9")); c != CodeNotFound {
		t.Fatalf("unknown seat: want not_found, got %s", c)
	}
	if c := code(reserve2(svc, show.ID, "u1", "", "A01")); c != CodeValidation {
		t.Fatalf("missing key: want validation, got %s", c)
	}
	if c := code(reserve2(svc, "00000000-0000-0000-0000-000000000000", "u1", "k2", "A01")); c != CodeNotFound {
		t.Fatalf("unknown show: want not_found, got %s", c)
	}
	// a not_found is not stored: the same key can still be used afterwards
	if _, err := reserve(svc, show.ID, "u1", "k1", "A01"); err != nil {
		t.Fatalf("key reuse after not_found: %v", err)
	}
}

func reserve2(svc *Service, showID, user, key string, seats ...string) error {
	_, err := reserve(svc, showID, user, key, seats...)
	return err
}

func TestHotSeatRaceHasExactlyOneWinner(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 5, 4, time.Minute)
	const n = 200
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, taken := 0, 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := reserve(svc, show.ID, fmt.Sprintf("u%d", i), fmt.Sprintf("k%d", i), "A01")
			mu.Lock()
			defer mu.Unlock()
			switch c := code(err); c {
			case "":
				wins++
			case CodeSeatTaken:
				taken++
			default:
				t.Errorf("unexpected outcome: %s", c)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if wins != 1 || taken != n-1 {
		t.Fatalf("wins=%d taken=%d", wins, taken)
	}
	assertInvariant(t, svc, show.ID)
}

func TestPerUserLimitUnderConcurrency(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 20, 4, time.Minute)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := reserve(svc, show.ID, "greedy", fmt.Sprintf("k%d", i), seatLabels(10)[i])
			mu.Lock()
			defer mu.Unlock()
			switch c := code(err); c {
			case "":
				ok++
			case CodePerUserLimit:
				limited++
			default:
				t.Errorf("unexpected: %s", c)
			}
		}(i)
	}
	wg.Wait()
	if ok != 4 || limited != 6 {
		t.Fatalf("ok=%d limited=%d", ok, limited)
	}
	st, _ := svc.GetShow(context.Background(), show.ID)
	if st.Counts.Held != 4 {
		t.Fatalf("held=%d", st.Counts.Held)
	}
	// a multi-seat request that would cross the limit is declined as a whole
	other, _ := reserve(svc, show.ID, "pair", "p1", "A11", "A12", "A13")
	if other.Reservation.ID == "" {
		t.Fatal("3 seats should fit under limit 4")
	}
	if c := code(reserve2(svc, show.ID, "pair", "p2", "A14", "A15")); c != CodePerUserLimit {
		t.Fatalf("want per_user_limit, got %s", c)
	}
}

func TestIdempotencyReplayAndMismatch(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 10, 4, time.Minute)
	const n = 20
	var wg sync.WaitGroup
	results := make([]ReserveResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = reserve(svc, show.ID, "u1", "same-key", "A01")
		}(i)
	}
	wg.Wait()
	originals, replays := 0, 0
	var id string
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("unexpected error: %v", errs[i])
		}
		if id == "" {
			id = results[i].Reservation.ID
		} else if results[i].Reservation.ID != id {
			t.Fatalf("different reservation ids for the same key")
		}
		if results[i].Replayed {
			replays++
		} else {
			originals++
		}
	}
	if originals != 1 || replays != n-1 {
		t.Fatalf("originals=%d replays=%d", originals, replays)
	}
	st, _ := svc.GetShow(context.Background(), show.ID)
	if st.Counts.Held != 1 {
		t.Fatalf("held=%d", st.Counts.Held)
	}
	if c := code(reserve2(svc, show.ID, "u1", "same-key", "A02")); c != CodeIdempotencyMismatch {
		t.Fatalf("want mismatch, got %s", c)
	}
	// keys are scoped per user: another user may use the same key string
	if _, err := reserve(svc, show.ID, "u9", "same-key", "A03"); err != nil {
		t.Fatalf("other user same key: %v", err)
	}
	// and per show: the same user and key on a fresh show is a new request, not a mismatch
	other := mustCreateShow(t, svc, 3, 4, time.Minute)
	if _, err := reserve(svc, other.ID, "u1", "same-key", "A02"); err != nil {
		t.Fatalf("same user and key on another show: %v", err)
	}
	// a declined request replays its decline instead of re-evaluating
	if c := code(reserve2(svc, show.ID, "u2", "k2", "A01")); c != CodeSeatTaken {
		t.Fatalf("want seat_taken, got %s", c)
	}
	_, err := reserve(svc, show.ID, "u2", "k2", "A01")
	if d, ok := AsDecline(err); !ok || d.Code != CodeSeatTaken || !d.Replayed {
		t.Fatalf("want replayed seat_taken, got %v", err)
	}
}

func TestMultiSeatAllOrNothing(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 5, 4, time.Minute)
	if _, err := reserve(svc, show.ID, "u1", "k1", "A02"); err != nil {
		t.Fatal(err)
	}
	if c := code(reserve2(svc, show.ID, "u2", "k2", "A01", "A02")); c != CodeSeatTaken {
		t.Fatalf("want seat_taken, got %s", c)
	}
	st, _ := svc.GetShow(context.Background(), show.ID)
	if st.Counts.Held != 1 || st.Seats[0].Status != SeatAvailable {
		t.Fatalf("A01 must remain available: %+v", st.Counts)
	}
}

func TestOverlappingMultiSeatNoDeadlock(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 4, 4, time.Minute)
	for round := 0; round < 50; round++ {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = reserve2(svc, show.ID, "x", fmt.Sprintf("x%d", round), "A01", "A02", "A03")
		}()
		go func() {
			defer wg.Done()
			errs[1] = reserve2(svc, show.ID, "y", fmt.Sprintf("y%d", round), "A03", "A02", "A01")
		}()
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch c := code(err); c {
			case "":
				wins++
			case CodeSeatTaken:
			default:
				t.Fatalf("round %d: unexpected %s", round, c)
			}
		}
		st, _ := svc.GetShow(context.Background(), show.ID)
		if wins != 1 || st.Counts.Held != 3 {
			t.Fatalf("round %d: wins=%d held=%d", round, wins, st.Counts.Held)
		}
		if _, err := svc.pool.Exec(context.Background(),
			`UPDATE seats SET status='available', reservation_id=NULL, held_by=NULL, hold_expires_at=NULL WHERE show_id=$1`, show.ID); err != nil {
			t.Fatal(err)
		}
	}
}
