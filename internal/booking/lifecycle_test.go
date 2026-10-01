package booking

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestConfirmThenCancel(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 3, 4, time.Minute)
	ctx := context.Background()
	res, err := reserve(svc, show.ID, "u1", "k1", "A01")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Reservation.ID

	if _, err := svc.Confirm(ctx, id, "intruder"); code(err) != CodeForbidden {
		t.Fatalf("want forbidden, got %v", err)
	}
	r, err := svc.Confirm(ctx, id, "u1")
	if err != nil || r.Status != ReservationConfirmed || r.ExpiresAt != nil {
		t.Fatalf("confirm: %v %+v", err, r)
	}
	if r, err = svc.Confirm(ctx, id, "u1"); err != nil || r.Status != ReservationConfirmed {
		t.Fatalf("repeat confirm must be a no-op: %v", err)
	}
	st, _ := svc.GetShow(ctx, show.ID)
	if st.Counts.Confirmed != 1 || st.Counts.Held != 0 {
		t.Fatalf("counts %+v", st.Counts)
	}
	if code(reserve2(svc, show.ID, "u2", "k2", "A01")) != CodeSeatTaken {
		t.Fatal("confirmed seat must not be reservable")
	}
	if _, err := svc.Cancel(ctx, id, "intruder"); code(err) != CodeForbidden {
		t.Fatalf("want forbidden, got %v", err)
	}
	if r, err = svc.Cancel(ctx, id, "u1"); err != nil || r.Status != ReservationCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := svc.Cancel(ctx, id, "u1"); err != nil {
		t.Fatalf("repeat cancel must be a no-op: %v", err)
	}
	if _, err := svc.Confirm(ctx, id, "u1"); code(err) != CodeInvalidState {
		t.Fatalf("confirm after cancel: want invalid_state, got %v", err)
	}
	if _, err := reserve(svc, show.ID, "u2", "k3", "A01"); err != nil {
		t.Fatalf("released seat must be re-bookable: %v", err)
	}
	assertInvariant(t, svc, show.ID)
}

func TestCancelFreesPerUserLimit(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 6, 2, time.Minute)
	ctx := context.Background()
	res, _ := reserve(svc, show.ID, "u1", "k1", "A01", "A02")
	if code(reserve2(svc, show.ID, "u1", "k2", "A03")) != CodePerUserLimit {
		t.Fatal("expected limit")
	}
	if _, err := svc.Cancel(ctx, res.Reservation.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(svc, show.ID, "u1", "k3", "A03"); err != nil {
		t.Fatalf("limit should be freed after cancel: %v", err)
	}
}

func TestExpiredHoldIsRebookableAndStaleCancelDoesNotResurrect(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 2, 4, time.Second)
	ctx := context.Background()
	first, _ := reserve(svc, show.ID, "u1", "k1", "A01")
	time.Sleep(1200 * time.Millisecond)

	if _, err := svc.Confirm(ctx, first.Reservation.ID, "u1"); code(err) != CodeInvalidState {
		t.Fatalf("confirming a lapsed hold: want invalid_state, got %v", err)
	}
	// before any sweep, the reserve path treats the lapsed hold as free
	second, err := reserve(svc, show.ID, "u2", "k2", "A01")
	if err != nil {
		t.Fatalf("lapsed hold must be re-bookable: %v", err)
	}
	if _, err := svc.Confirm(ctx, second.Reservation.ID, "u2"); err != nil {
		t.Fatal(err)
	}
	// the stale first reservation still says 'held'; cancelling it must not touch A01
	if _, err := svc.Cancel(ctx, first.Reservation.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	st, _ := svc.GetShow(ctx, show.ID)
	if st.Seats[0].Status != SeatConfirmed {
		t.Fatalf("A01 was resurrected: %+v", st.Seats)
	}
	// and the sweeper must not touch it either
	if _, err := svc.SweepExpired(ctx, 100); err != nil {
		t.Fatal(err)
	}
	st, _ = svc.GetShow(ctx, show.ID)
	if st.Seats[0].Status != SeatConfirmed {
		t.Fatalf("A01 was released by the sweeper: %+v", st.Seats)
	}
	assertInvariant(t, svc, show.ID)
}

func TestSweepExpired(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 3, 4, time.Second)
	ctx := context.Background()
	res, _ := reserve(svc, show.ID, "u1", "k1", "A01", "A02")
	time.Sleep(1200 * time.Millisecond)
	n, err := svc.SweepExpired(ctx, 1000)
	if err != nil || n < 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
	r, _ := svc.GetReservation(ctx, res.Reservation.ID, "u1")
	if r.Status != ReservationExpired {
		t.Fatalf("status %s", r.Status)
	}
	st, _ := svc.GetShow(ctx, show.ID)
	if st.Counts.Available != 3 {
		t.Fatalf("counts %+v", st.Counts)
	}
	if _, err := svc.Confirm(ctx, res.Reservation.ID, "u1"); code(err) != CodeInvalidState {
		t.Fatalf("confirm after expiry: want invalid_state, got %v", err)
	}
	counts, err := svc.SeatCounts(ctx)
	if err != nil || counts[show.ID].Total != 3 || counts[show.ID].Available != 3 {
		t.Fatalf("SeatCounts: %v %+v", err, counts[show.ID])
	}
}

// Cancels, confirms, sweeps and new reserves all racing over the same seats must never
// deadlock (they share one lock order) and must keep the invariant.
func TestLifecycleRaceKeepsInvariant(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 6, 4, time.Second)
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 1000)
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			user := fmt.Sprintf("w%d", w)
			for i := 0; i < 15; i++ {
				seats := []string{"A01", "A02", "A03", "A04", "A05", "A06"}
				pick := []string{seats[(w+i)%6], seats[(w+i+3)%6]}
				res, err := reserve(svc, show.ID, user, fmt.Sprintf("%d", i), pick...)
				if c := code(err); c != "" && c != CodeSeatTaken && c != CodePerUserLimit {
					errCh <- fmt.Errorf("reserve: %v", err)
					continue
				}
				if err != nil {
					continue
				}
				switch i % 3 {
				case 0:
					if _, err := svc.Cancel(ctx, res.Reservation.ID, user); err != nil {
						errCh <- fmt.Errorf("cancel: %v", err)
					}
				case 1:
					if _, err := svc.Confirm(ctx, res.Reservation.ID, user); err != nil && code(err) != CodeInvalidState {
						errCh <- fmt.Errorf("confirm: %v", err)
					}
				}
			}
		}(w)
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := svc.SweepExpired(ctx, 100); err != nil {
					errCh <- fmt.Errorf("sweep: %v", err)
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	assertInvariant(t, svc, show.ID)
	// no seat may point at a reservation that is not held/confirmed
	var orphans int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM seats s JOIN reservations r ON r.id = s.reservation_id
		WHERE s.show_id=$1 AND (r.status NOT IN ('held','confirmed') OR (s.status='confirmed') <> (r.status='confirmed'))`, show.ID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d seats out of sync with their reservation", orphans)
	}
}
