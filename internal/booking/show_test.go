package booking

import (
	"context"
	"testing"
	"time"
)

func TestCreateAndGetShow(t *testing.T) {
	svc := newTestService(t)
	show := mustCreateShow(t, svc, 5, 0, 0)
	if show.PerUserLimit != DefaultPerUserLimit || show.HoldTTL != DefaultHoldTTL {
		t.Fatalf("defaults not applied: %+v", show)
	}
	st, err := svc.GetShow(context.Background(), show.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Counts.Total != 5 || st.Counts.Available != 5 || len(st.Seats) != 5 || st.Seats[0].Status != SeatAvailable {
		t.Fatalf("unexpected state: %+v", st.Counts)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "not-a-uuid"} {
		_, err := svc.GetShow(context.Background(), id)
		if d, ok := AsDecline(err); !ok || d.Code != CodeNotFound {
			t.Fatalf("want not_found for %q, got %v", id, err)
		}
	}
}

func TestCreateShowValidation(t *testing.T) {
	svc := newTestService(t)
	cases := []CreateShowInput{
		{Name: "x", Seats: []string{"A1", "a1"}, PricePaise: 1},
		{Name: "", Seats: []string{"A1"}, PricePaise: 1},
		{Name: "x", Seats: nil, PricePaise: 1},
		{Name: "x", Seats: []string{"A1"}, PricePaise: -1},
		{Name: "x", Seats: []string{"A1"}, PricePaise: 1, PerUserLimit: -1},
		{Name: "x", Seats: []string{"A1"}, PricePaise: 1, HoldTTL: time.Millisecond},
	}
	for i, in := range cases {
		_, err := svc.CreateShow(context.Background(), in)
		if d, ok := AsDecline(err); !ok || d.Code != CodeValidation {
			t.Errorf("case %d: expected validation decline, got %v", i, err)
		}
	}
}
