package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"bookingSystem/internal/metrics"
)

type ctxKey struct{}

// requestInfo is filled in by handlers and read back by the logging middleware.
type requestInfo struct {
	ID            string
	UserID        string
	DeclineReason string
}

func info(r *http.Request) *requestInfo {
	if ri, ok := r.Context().Value(ctxKey{}).(*requestInfo); ok {
		return ri
	}
	return &requestInfo{}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// observe wraps a route with a correlation id, panic recovery, one structured log line and
// request metrics. route is the mux pattern, which keeps metric label cardinality bounded.
func observe(m *metrics.Metrics, logger *slog.Logger, route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ri := &requestInfo{ID: r.Header.Get("X-Request-ID")}
		if ri.ID == "" || len(ri.ID) > 128 {
			ri.ID = newRequestID()
		}
		w.Header().Set("X-Request-ID", ri.ID)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri))
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if p := recover(); p != nil {
				logger.Error("panic", "request_id", ri.ID, "route", route, "panic", p)
				if !sw.wrote {
					writeError(sw, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			dur := time.Since(start)
			status := strconv.Itoa(sw.status)
			m.HTTPRequests.WithLabelValues(route, r.Method, status).Inc()
			m.HTTPDuration.WithLabelValues(route, r.Method, status).Observe(dur.Seconds())
			attrs := []any{"request_id", ri.ID, "method", r.Method, "route", route, "path", r.URL.Path,
				"status", sw.status, "duration_ms", dur.Milliseconds()}
			if ri.UserID != "" {
				attrs = append(attrs, "user_id", ri.UserID)
			}
			if ri.DeclineReason != "" {
				attrs = append(attrs, "decline_reason", ri.DeclineReason)
			}
			if r.Context().Err() != nil {
				attrs = append(attrs, "client_gone", true)
			}
			switch {
			case sw.status >= 500:
				logger.Error("request", attrs...)
			case route == "GET /healthz" || route == "GET /readyz":
				logger.Debug("request", attrs...)
			default:
				logger.Info("request", attrs...)
			}
		}()
		next(sw, r)
	}
}
