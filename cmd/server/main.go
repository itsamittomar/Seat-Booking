package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bookingSystem/internal/auth"
	"bookingSystem/internal/booking"
	"bookingSystem/internal/config"
	"bookingSystem/internal/httpapi"
	"bookingSystem/internal/metrics"
	"bookingSystem/internal/store"
	"bookingSystem/internal/sweeper"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, 60*time.Second)
	if err != nil {
		logger.Error("startup: database", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		logger.Error("startup: migrate", "err", err)
		os.Exit(1)
	}

	m := metrics.New()
	svc := booking.NewService(db.Pool)
	handler := httpapi.NewHandler(httpapi.Deps{
		DB:      db,
		Service: svc,
		Auth:    auth.New(cfg.TokenSecret, cfg.AdminToken),
		Metrics: m,
		Logger:  logger,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Generous on purpose: in an on-sale burst a request may queue for a pool connection for a
		// long time on a small instance. A slow answer beats a connection reset the client cannot interpret.
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go sweeper.Run(ctx, svc, db.Pool, m, logger, cfg.SweepInterval)
	go func() {
		logger.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// healthcheck probes this process's readiness endpoint. The runtime image has no shell or curl,
// so container health checks call the binary itself: /server healthcheck.
func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
