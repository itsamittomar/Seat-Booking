// Package httpapi is the JSON HTTP layer: routing, auth, error mapping, logging and metrics.
package httpapi

import (
	"log/slog"
	"net/http"

	"bookingSystem/internal/auth"
	"bookingSystem/internal/booking"
	"bookingSystem/internal/metrics"
)

type Deps struct {
	DB      Pinger
	Service *booking.Service
	Auth    *auth.Auth
	Metrics *metrics.Metrics
	Logger  *slog.Logger
}

type handler struct {
	db      Pinger
	svc     *booking.Service
	auth    *auth.Auth
	metrics *metrics.Metrics
	logger  *slog.Logger
}

func NewHandler(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Metrics == nil {
		d.Metrics = metrics.New()
	}
	if d.Auth == nil {
		d.Auth = auth.New("", "")
	}
	h := &handler{db: d.DB, svc: d.Service, auth: d.Auth, metrics: d.Metrics, logger: d.Logger}
	mux := http.NewServeMux()
	route := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, observe(h.metrics, h.logger, pattern, fn))
	}
	route("GET /{$}", h.index)
	route("GET /healthz", h.healthz)
	route("GET /readyz", h.readyz)
	mux.Handle("GET /metrics", h.metrics.Handler())
	route("POST /auth/token", h.mintToken)
	route("POST /shows", h.createShow)
	route("GET /shows/{id}", h.getShow)
	route("POST /shows/{id}/reserve", h.reserve)
	route("GET /reservations/{id}", h.getReservation)
	route("POST /reservations/{id}/confirm", h.confirm)
	route("POST /reservations/{id}/cancel", h.cancel)
	return mux
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "seat-reservation",
		"endpoints": []string{
			"POST /auth/token", "POST /shows (admin)", "GET /shows/{id}", "POST /shows/{id}/reserve",
			"GET /reservations/{id}", "POST /reservations/{id}/confirm", "POST /reservations/{id}/cancel",
			"GET /healthz", "GET /readyz", "GET /metrics",
		},
	})
}
