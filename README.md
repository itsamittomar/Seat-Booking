# Seat Reservation at Scale

A JSON HTTP service that sells assigned seats for a show and stays correct when thousands of buyers hit the same seats in the same second. It never sells a seat twice, never lets a user exceed the per-show limit, and never acts twice on a retried request.

Go 1.26, Postgres, one transaction per decision. The design write-up is in [WRITEUP.md](WRITEUP.md).

| | |
|---|---|
| Live URL | `LIVE_URL` (filled in after deploy, see [Deploy](#deploy)) |
| Metrics | `LIVE_URL/metrics` |
| Health | `LIVE_URL/healthz` (liveness), `LIVE_URL/readyz` (readiness, checks Postgres) |
| Logs | Render log stream, see [Observability](#observability) |

## Quick start

```bash
docker compose up --build          # app on :8080 + Postgres 16, same image as production
./burst.sh http://localhost:8080 dev-admin-token
```

The burst prints the outcome distribution, runs the correctness checks, and ends with `RESULT: PASS` or `RESULT: FAIL` (non-zero exit).

## Burst script

One command reproduces the on-sale stampede against any deployment:

```bash
./burst.sh <BASE_URL> <ADMIN_TOKEN>                 # ~5.5k requests
make burst-20k BASE_URL=<BASE_URL> ADMIN_TOKEN=...  # ~20k requests
./burst.sh <BASE_URL> <ADMIN_TOKEN> -hot-users 3000 -spread 20000 -concurrency 1500
```

It creates a fresh show, mints user tokens, then releases every request at once in shuffled order:

- **Hot-seat storm.** 5 hot seats (A12 to A16), each raced by `-hot-users` distinct users (default 500).
- **Per-user limit storm.** One user fires 10 parallel single-seat reserves on a limit-4 show.
- **Idempotency storm.** One user sends 50 parallel retries with the same key, then the same key with a different seat.
- **Wide spread.** `-spread` users (default 3000) each request one of the remaining seats, several per seat.

Then it confirms the hot-seat winners, tries to cancel one as another user, and sends a reserve with a spoofed `user_id` in the body. It checks:

- exactly one 201 per hot seat and 409 `seat_taken` for every other racer
- no seat won by two requests anywhere in the burst
- the limit user ends with exactly 4 holds
- 50 retries produce 1 reservation and 49 replays, and the different-seat retry gets 409 `idempotency_mismatch`
- zero 5xx and zero transport errors
- `available + held + confirmed == total` in every sample taken during the burst, and after it
- held + confirmed equals the number of seats won
- a stranger's cancel gets 403, and the spoofed body acts only as the token's user

Finally it compares the server's `/metrics` deltas with what the client counted. Mismatches there are warnings, because other traffic may share the server.

Sample run on a laptop against the compose stack (20,060 requests, 1,000 in flight):

```
=== 20060 reserve requests in 3.315s (6052 req/s)  p50 162ms  p95 196ms  p99 207ms  max 229ms ===
  outcome                          total     hot  spread   limit    idem
  200 replay                          49       0       0       0      49
  201 held                           994       5     984       4       1
  409 per_user_limit                   6       0       0       6       0
  409 seat_taken                   19011    9995    9016       0       0
  ...
  [PASS] zero 5xx (got 0)
  [PASS] reconciliation: available + held + confirmed == total
RESULT: PASS
```

## API

All bodies are JSON. Money is integer paise. Errors look like `{"error": "<code>", "message": "..."}`.

| Method and path | Auth | Success |
|---|---|---|
| `POST /auth/token` | none | 200 `{user_id, token}` |
| `POST /shows` | admin | 201 show with every seat `available` |
| `GET /shows/{id}` | none | 200 per-seat status and `counts` |
| `POST /shows/{id}/reserve` | user | 201 new hold, 200 idempotent replay |
| `GET /reservations/{id}` | owner | 200 |
| `POST /reservations/{id}/confirm` | owner | 200 `status: confirmed` |
| `POST /reservations/{id}/cancel` | owner | 200 `status: cancelled` |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | none | |

`POST /auth/token` is a deliberate demo shortcut so load testers can create many users. Tokens are `<user_id>.<HMAC-SHA256>` signed with `TOKEN_SECRET`. Identity comes only from the token; a `user_id` field in a request body is ignored.

### Walkthrough

```bash
B=http://localhost:8080; ADMIN=dev-admin-token

SHOW=$(curl -s -X POST $B/shows -H "Authorization: Bearer $ADMIN" \
  -d '{"name":"friday-night","seats":["A1","A2","A12","A13"],"price_paise":25000}' | jq -r .id)

TOKEN=$(curl -s -X POST $B/auth/token -d '{"user_id":"alice"}' | jq -r .token)

curl -s -X POST $B/shows/$SHOW/reserve -H "Authorization: Bearer $TOKEN" \
  -d '{"seats":["A12"],"idempotency_key":"order-1"}'
# 201 {"reservation_id":"…","show_id":"…","user_id":"alice","seats":["A12"],
#      "amount_paise":25000,"status":"held","expires_at":"…","created_at":"…"}

curl -s -X POST $B/reservations/<reservation_id>/confirm -H "Authorization: Bearer $TOKEN"
curl -s $B/shows/$SHOW | jq .counts
# {"available":3,"held":0,"confirmed":1,"total":4}
```

`POST /shows` also accepts `per_user_limit` (default 4) and `hold_ttl_seconds` (default 300).

### Reserve behaviour

- **All-or-nothing.** If any requested seat is unavailable, nothing is held and the response is 409 `seat_taken`, listing the unavailable seats. This holds under concurrency because every seat in the request is row-locked in one transaction.
- **Holds.** A reserve creates a hold that lapses after `hold_ttl_seconds` unless the owner confirms it. A lapsed hold is free for anyone to take, even before the sweeper has released it.
- **Idempotency.** The key comes from the `Idempotency-Key` header or `idempotency_key` in the body; the header wins. Keys are scoped to (show, user, key). A retry returns the original response with `Idempotent-Replay: true`: 200 for a hold, or the same 409 for a decline. The same key with different seats is 409 `idempotency_mismatch`.

| Status | `error` | When |
|---|---|---|
| 409 | `seat_taken` | a requested seat is held or confirmed by someone |
| 409 | `per_user_limit` | the user's active seats plus this request would exceed the limit |
| 409 | `idempotency_mismatch` | the key was already used on this show with different seats |
| 409 | `invalid_state` | confirm on a lapsed, cancelled or expired reservation |
| 404 | `not_found` | unknown show, seat or reservation |
| 422 | `validation` | malformed body, empty seat list, missing idempotency key |
| 401 / 403 | `unauthorized` / `forbidden` | bad token / not the owner |
| 503 | `unavailable` | database unreachable (the only 5xx) |

## Observability

**Metrics** at `/metrics` (Prometheus text format):

| Metric | Type | Meaning |
|---|---|---|
| `reservations_held_total` | counter | reserves that created a hold (each 201) |
| `reservations_confirmed_total` | counter | holds confirmed |
| `reservations_declined_total{reason}` | counter | `seat_taken`, `per_user_limit`, `idempotent_replay`, `idempotency_mismatch`, `not_found`, `validation` |
| `reservations_released_total{cause}` | counter | `cancel`, `expire` |
| `seats_available`, `seats_held`, `seats_confirmed` `{show_id}` | gauge | recomputed from Postgres every second |
| `http_requests_total`, `http_request_duration_seconds` `{route,method,status}` | counter, histogram | per route |
| `db_pool_acquired_conns`, `db_pool_idle_conns`, `db_pool_empty_acquire_total` | gauge | connection pool pressure |

The seat gauges for a show always equal `GET /shows/{id}` counts at the last refresh. The burst script prints that comparison.

**Logs** are one JSON line per request on stdout:

```json
{"time":"…","level":"INFO","msg":"request","request_id":"1617d673b29d8f00ad1522ac","method":"POST",
 "route":"POST /shows/{id}/reserve","path":"/shows/…/reserve","status":409,"duration_ms":6,
 "user_id":"u42","decline_reason":"seat_taken"}
```

The request id is taken from an incoming `X-Request-ID` header or generated, and is echoed back in the response header. On Render, the live log stream is under the service's Logs tab. A recording of the logs during a burst is linked here: `LOG_RECORDING_URL`.

## Deploy

Production runs the same Docker image on Render, backed by Neon Postgres. Render's free Postgres is deleted after 30 days, so it is not used.

1. **Neon.** Create a project in AWS `us-west-2` (Oregon). Copy the direct connection string, not the pooled one; it must end in `sslmode=require`.
2. **Render.** New, then Blueprint, then select this repo. `render.yaml` defines a Docker web service in Oregon with `/readyz` as the health check. Set `DATABASE_URL` to the Neon string and `ADMIN_TOKEN` to a long random value (`openssl rand -hex 24`). `TOKEN_SECRET` is generated by Render.
3. **Deploy.** Every push to `main` builds and deploys. Render switches traffic only after `/readyz` returns 200. Migrations run at startup.
4. **Keep it warm.** Render's free tier sleeps after 15 idle minutes, and Neon suspends after 5. A free uptime monitor (UptimeRobot or cron-job.org) hitting `/readyz` every 5 minutes avoids cold starts. If the service is cold anyway, the first request waits roughly 30 to 60 seconds while it boots, and the burst script waits on `/readyz` before firing.

### Post-deploy checks

```bash
URL=https://<service>.onrender.com
curl -s $URL/healthz; curl -s $URL/readyz           # 200, 200
./burst.sh $URL $ADMIN_TOKEN                        # RESULT: PASS
make burst-20k BASE_URL=$URL ADMIN_TOKEN=$ADMIN_TOKEN
```

To check that readiness fails closed, suspend the Neon compute from its console. `/readyz` turns 503 within 2 seconds while `/healthz` stays 200, and both recover once Neon resumes. Locally, run `docker compose stop db` to see the same thing.

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | required | Postgres connection string |
| `TOKEN_SECRET` | required | HMAC key for user tokens |
| `ADMIN_TOKEN` | required | bearer token for `POST /shows` |
| `PORT` | `8080` | |
| `DB_MAX_CONNS` | `20` | pool size |
| `SWEEP_INTERVAL` | `1s` | expiry sweep and gauge refresh |
| `LOG_LEVEL` | `info` | `debug` also logs health checks |

## Development

```bash
make test                 # unit tests; DB tests skip without a database
make test-db              # all tests with -race against TEST_DATABASE_URL
                          # default: postgres://localhost:5432/booking_test?sslmode=disable
```

The integration tests run against real Postgres and cover:

- a 200-way race on one seat
- 10 parallel reserves on limit 4
- 20 parallel same-key retries
- multi-seat requests overlapping in opposite order
- stale cancel and stale sweep on a re-booked seat
- a mixed race of reserve, confirm, cancel and sweep

The invariant is checked after each test.

```
cmd/server        wiring, graceful shutdown, container health probe
cmd/burst         load generator and verifier
internal/booking  reserve / confirm / cancel / sweep transactions
internal/store    pgx pool with startup retry, embedded SQL migrations
internal/httpapi  routes, auth, error mapping, request logging, metrics middleware
internal/auth     HMAC tokens
internal/metrics  Prometheus collectors
internal/sweeper  expiry loop and gauge refresh
```
