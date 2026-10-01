.PHONY: build test test-db run burst burst-20k docker

BASE_URL ?= http://localhost:8080
ADMIN_TOKEN ?= dev-admin-token
TEST_DATABASE_URL ?= postgres://localhost:5432/booking_test?sslmode=disable
ARGS ?=

build:
	go build -o bin/server ./cmd/server
	go build -o bin/burst ./cmd/burst

test:
	go test ./...

test-db:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

run:
	go run ./cmd/server

# ~5.5k requests: 5 hot seats x 500 users, 3000 spread, limit and retry storms
burst:
	go run ./cmd/burst -base-url $(BASE_URL) -admin-token $(ADMIN_TOKEN) $(ARGS)

# ~20k requests: 5 hot seats x 2000 users, 10000 spread over 1000 seats
burst-20k:
	go run ./cmd/burst -base-url $(BASE_URL) -admin-token $(ADMIN_TOKEN) -seats 1000 -hot-users 2000 -spread 10000 -concurrency 1000 $(ARGS)

docker:
	docker compose up --build
