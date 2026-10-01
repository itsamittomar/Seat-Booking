package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMigrateIsIdempotent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url, 4, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate run %d: %v", i, err)
		}
	}
	var n int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("want at least 1 applied migration, got %d", n)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOpenGivesUpWhenUnreachable(t *testing.T) {
	_, err := Open(context.Background(), "postgres://nobody@127.0.0.1:1/x?sslmode=disable&connect_timeout=1", 2, time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
}
