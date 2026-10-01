package booking

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"bookingSystem/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, url, 30, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return NewService(s.Pool)
}

// seatLabels returns A01..Ann so that label order matches numeric order in assertions.
func seatLabels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "A" + pad(i+1)
	}
	return out
}

func pad(i int) string {
	s := strconv.Itoa(i)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

func mustCreateShow(t *testing.T, svc *Service, seats, limit int, ttl time.Duration) Show {
	t.Helper()
	show, err := svc.CreateShow(context.Background(), CreateShowInput{Name: t.Name(), Seats: seatLabels(seats), PricePaise: 25000, PerUserLimit: limit, HoldTTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return show
}

func assertInvariant(t *testing.T, svc *Service, showID string) {
	t.Helper()
	st, err := svc.GetShow(context.Background(), showID)
	if err != nil {
		t.Fatal(err)
	}
	c := st.Counts
	if c.Available+c.Held+c.Confirmed != c.Total {
		t.Fatalf("invariant broken: %+v", c)
	}
}
