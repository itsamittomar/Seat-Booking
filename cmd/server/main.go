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
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       60 * time.Second,
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
