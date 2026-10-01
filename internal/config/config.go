// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port          string
	DatabaseURL   string
	TokenSecret   string
	AdminToken    string
	DBMaxConns    int32
	SweepInterval time.Duration
	LogLevel      string
}

func Load() (Config, error) {
	cfg := Config{
		Port:          getenv("PORT", "8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		TokenSecret:   os.Getenv("TOKEN_SECRET"),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
		DBMaxConns:    20,
		SweepInterval: time.Second,
		LogLevel:      getenv("LOG_LEVEL", "info"),
	}
	for _, req := range []struct{ name, val string }{
		{"DATABASE_URL", cfg.DatabaseURL},
		{"TOKEN_SECRET", cfg.TokenSecret},
		{"ADMIN_TOKEN", cfg.AdminToken},
	} {
		if req.val == "" {
			return Config{}, fmt.Errorf("config: %s is required", req.name)
		}
	}
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: DB_MAX_CONNS must be a positive integer")
		}
		cfg.DBMaxConns = int32(n)
	}
	if v := os.Getenv("SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config: SWEEP_INTERVAL must be a positive duration")
		}
		cfg.SweepInterval = d
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
