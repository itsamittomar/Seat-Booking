package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("TOKEN_SECRET", "s")
	t.Setenv("ADMIN_TOKEN", "a")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.DBMaxConns != 20 || cfg.SweepInterval != time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TOKEN_SECRET", "s")
	t.Setenv("ADMIN_TOKEN", "a")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing DATABASE_URL")
	}
}

func TestLoadRejectsBadNumbers(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("TOKEN_SECRET", "s")
	t.Setenv("ADMIN_TOKEN", "a")
	t.Setenv("DB_MAX_CONNS", "zero")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad DB_MAX_CONNS")
	}
}
