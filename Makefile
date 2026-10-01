.PHONY: build test test-db run burst docker

BASE_URL ?= http://localhost:8080
ADMIN_TOKEN ?= dev-admin-token
TEST_DATABASE_URL ?= postgres://localhost:5432/booking_test?sslmode=disable

build:
	go build -o bin/server ./cmd/server

test:
	go test ./...

test-db:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

run:
	go run ./cmd/server

burst:
	go run ./cmd/burst -base-url $(BASE_URL) -admin-token $(ADMIN_TOKEN)

docker:
	docker compose up --build
