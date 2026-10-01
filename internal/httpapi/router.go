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
	for _, rt := range h.routes() {
		mux.HandleFunc(rt.pattern, observe(h.metrics, h.logger, rt.pattern, rt.fn))
	}
	mux.Handle("GET /metrics", h.metrics.Handler())
	return mux
}

type route struct {
	pattern string
	fn      http.HandlerFunc
}

// routes is the single route table. TestSpecCoversEveryRoute keeps openapi.yaml in step with it.
func (h *handler) routes() []route {
	return []route{
		{"GET /{$}", h.index},
		{"GET /docs", h.docs},
		{"GET /openapi.yaml", h.openapi},
		{"GET /healthz", h.healthz},
		{"GET /readyz", h.readyz},
		{"POST /auth/token", h.mintToken},
		{"POST /shows", h.createShow},
		{"GET /shows/{id}", h.getShow},
		{"POST /shows/{id}/reserve", h.reserve},
		{"GET /reservations/{id}", h.getReservation},
		{"POST /reservations/{id}/confirm", h.confirm},
		{"POST /reservations/{id}/cancel", h.cancel},
	}
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "seat-reservation",
		"docs":    "/docs",
		"openapi": "/openapi.yaml",
		"endpoints": []string{
			"POST /auth/token", "POST /shows (admin)", "GET /shows/{id}", "POST /shows/{id}/reserve",
			"GET /reservations/{id}", "POST /reservations/{id}/confirm", "POST /reservations/{id}/cancel",
			"GET /healthz", "GET /readyz", "GET /metrics",
		},
	})
}
