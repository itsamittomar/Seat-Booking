package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExposesRequiredSeries(t *testing.T) {
	m := New()
	m.ReservationsHeld.Inc()
	m.SeatsAvailable.WithLabelValues("show-1").Set(7)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rr.Body)
	for _, want := range []string{
		"reservations_held_total 1",
		`reservations_declined_total{reason="seat_taken"} 0`,
		`reservations_declined_total{reason="per_user_limit"} 0`,
		`reservations_declined_total{reason="idempotent_replay"} 0`,
		"reservations_confirmed_total 0",
		`seats_available{show_id="show-1"} 7`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q", want)
		}
	}
}
