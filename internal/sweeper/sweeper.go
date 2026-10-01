// Package sweeper runs the background loop that expires lapsed holds and refreshes gauges.
package sweeper

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bookingSystem/internal/booking"
	"bookingSystem/internal/metrics"
)

const batch = 500

// Run ticks every interval until ctx is done. Correctness never depends on it: the reserve
// path already treats lapsed holds as free. The sweeper keeps show state and gauges truthful.
func Run(ctx context.Context, svc *booking.Service, pool *pgxpool.Pool, m *metrics.Metrics, logger *slog.Logger, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		Tick(ctx, svc, pool, m, logger)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one sweep and gauge refresh.
func Tick(ctx context.Context, svc *booking.Service, pool *pgxpool.Pool, m *metrics.Metrics, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		n, err := svc.SweepExpired(ctx, batch)
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("sweep failed", "err", err)
			}
			break
		}
		if n > 0 {
			m.ReservationsReleased.WithLabelValues("expire").Add(float64(n))
			logger.Info("expired holds released", "count", n)
		}
		if n < batch {
			break
		}
	}
	counts, err := svc.SeatCounts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn("seat counts failed", "err", err)
		}
	} else {
		for showID, c := range counts {
			m.SeatsAvailable.WithLabelValues(showID).Set(float64(c.Available))
			m.SeatsHeld.WithLabelValues(showID).Set(float64(c.Held))
			m.SeatsConfirmed.WithLabelValues(showID).Set(float64(c.Confirmed))
		}
	}
	st := pool.Stat()
	m.DBPoolAcquired.Set(float64(st.AcquiredConns()))
	m.DBPoolIdle.Set(float64(st.IdleConns()))
	m.DBPoolWaiting.Set(float64(st.EmptyAcquireCount()))
}
