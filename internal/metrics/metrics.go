// Package metrics defines the Prometheus collectors exposed on /metrics.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DeclineReasons are pre-registered so every series exists (at 0) before the first burst.
var DeclineReasons = []string{"seat_taken", "per_user_limit", "idempotent_replay", "idempotency_mismatch", "not_found", "validation"}

type Metrics struct {
	Registry *prometheus.Registry

	ReservationsHeld      prometheus.Counter
	ReservationsConfirmed prometheus.Counter
	ReservationsDeclined  *prometheus.CounterVec // reason
	ReservationsReleased  *prometheus.CounterVec // cause: cancel | expire

	SeatsAvailable *prometheus.GaugeVec // show_id
	SeatsHeld      *prometheus.GaugeVec
	SeatsConfirmed *prometheus.GaugeVec

	HTTPRequests *prometheus.CounterVec   // route, method, status
	HTTPDuration *prometheus.HistogramVec // route, method, status

	DBPoolAcquired prometheus.Gauge
	DBPoolIdle     prometheus.Gauge
	DBPoolWaiting  prometheus.Gauge
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := &Metrics{
		Registry: reg,
		ReservationsHeld: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reservations_held_total", Help: "Reserve requests that created a new hold (HTTP 201)."}),
		ReservationsConfirmed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reservations_confirmed_total", Help: "Holds moved to confirmed."}),
		ReservationsDeclined: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reservations_declined_total", Help: "Reserve requests that did not create a hold, by reason."}, []string{"reason"}),
		ReservationsReleased: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reservations_released_total", Help: "Reservations whose seats were released, by cause."}, []string{"cause"}),
		SeatsAvailable: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seats_available", Help: "Available seats per show (refreshed from the database every sweep)."}, []string{"show_id"}),
		SeatsHeld: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seats_held", Help: "Held seats per show."}, []string{"show_id"}),
		SeatsConfirmed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "seats_confirmed", Help: "Confirmed seats per show."}, []string{"show_id"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests by route, method and status."}, []string{"route", "method", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}}, []string{"route", "method", "status"}),
		DBPoolAcquired: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "db_pool_acquired_conns", Help: "Connections currently checked out of the pool."}),
		DBPoolIdle: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "db_pool_idle_conns", Help: "Idle connections in the pool."}),
		DBPoolWaiting: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "db_pool_empty_acquire_total", Help: "Cumulative acquires that had to wait for a free connection."}),
	}
	reg.MustRegister(m.ReservationsHeld, m.ReservationsConfirmed, m.ReservationsDeclined, m.ReservationsReleased,
		m.SeatsAvailable, m.SeatsHeld, m.SeatsConfirmed, m.HTTPRequests, m.HTTPDuration,
		m.DBPoolAcquired, m.DBPoolIdle, m.DBPoolWaiting)
	for _, r := range DeclineReasons {
		m.ReservationsDeclined.WithLabelValues(r)
	}
	m.ReservationsReleased.WithLabelValues("cancel")
	m.ReservationsReleased.WithLabelValues("expire")
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
