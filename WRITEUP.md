# Write-up

## 1. The atomic decision

Every reserve is a single Postgres transaction at READ COMMITTED. The decision is made while holding row locks, so there is no window between "is it free?" and "take it".

```
BEGIN
  INSERT INTO idempotency_keys (show_id, user_id, key, request_hash) ... ON CONFLICT DO NOTHING
  SELECT pg_advisory_xact_lock(hashtext(show_id || ':' || user_id))
  SELECT label, status, hold_expires_at FROM seats
   WHERE show_id = $1 AND label = ANY($2) ORDER BY label FOR UPDATE
  -- any seat not free           -> record decline, COMMIT, 409 seat_taken
  SELECT count(*) FROM seats WHERE show_id = $1 AND held_by = $2 AND <active>
  -- count + requested > limit   -> record decline, COMMIT, 409 per_user_limit
  INSERT INTO reservations ...
  UPDATE seats SET status = 'held', reservation_id = $r, ...
   WHERE show_id = $1 AND label = ANY($2) AND (status = 'available' OR <hold lapsed>)
  -- rows affected must equal the number of seats requested, else ROLLBACK
  UPDATE idempotency_keys SET reservation_id = $r ...
COMMIT
```

**Why it is race-free.** Each seat is exactly one row, with primary key `(show_id, label)`, so a second copy of A12 cannot exist. When 500 transactions want A12, they all need that row's lock. The first one takes it and the other 499 queue behind it. When the winner commits, Postgres re-reads the row's newest version for each waiter, as READ COMMITTED does for `FOR UPDATE`. Each waiter therefore sees `held` and declines. The guarded `UPDATE` with its row-count check is a second, independent guard: a seat that is not free cannot match the `WHERE` clause. Check constraints in the schema also tie `status` to the ownership columns, so an available seat with an owner cannot be written.

**Per-user limit.** Seat row locks do not protect "how many seats does this user hold", because the user's other seats are different rows. Ten parallel requests from one user would each count 0 and each succeed. The fix is a transaction-scoped advisory lock on `(show, user)`. Reserves by the same user on the same show run one at a time, and different users never wait on each other. A hash collision between two users only adds a short wait, never a wrong answer. A counter row per (user, show) with a check constraint would also work. I rejected it because cancel, confirm and the sweeper would all have to maintain the counter, which is more places to get it wrong.

**Multi-seat requests and deadlock.** Seat labels are normalized (trimmed, upper-cased, de-duplicated) and sorted before the transaction starts. Every path that touches seats locks them in `(show_id, label)` order:

| Path | Lock order |
|---|---|
| reserve | idempotency row, then advisory (show, user), then seats by label |
| confirm, cancel | reservation row, then that reservation's seats by label |
| sweeper | lapsed reservation rows with `SKIP LOCKED`, then their seats by (show, label) |

Reserve never locks an existing reservation row, and seats are always taken in one global order, so no cycle can form. Deadlock (`40P01`) and serialization (`40001`) errors are retried up to three times as defence in depth. The tests run opposite-order overlapping requests (`[A1,A2,A3]` against `[A3,A2,A1]`) for 50 rounds, plus a mixed race of reserves, confirms, cancels and sweeps. Neither has deadlocked.

**Partial requests are all-or-nothing.** If any requested seat is not free, the whole request is declined and nothing changes. All requested rows are locked before any decision, so this holds under concurrency.

**The invariant.** Each seat row has exactly one status, so `available + held + confirmed == total` is true by construction. `GET /shows/{id}` derives both the per-seat list and the counts from one `SELECT`, which is one snapshot. The burst script samples it every 250 ms during a burst.

## 2. Idempotency

- **Where the key lives.** The key lives in the `idempotency_keys` table in Postgres, with primary key `(show_id, user_id, key)`. The key is written in the same transaction as the reservation, so the key and its effect commit or roll back together. There is no separate cache that could disagree with the database.
- **How exactly-once is enforced.** The first statement of the transaction is `INSERT ... ON CONFLICT DO NOTHING`. If another request with the same key is in flight, this insert blocks on the unique index until that transaction ends. If it committed, the waiter finds the row, locks it, and returns the stored outcome. If it rolled back, for example on a database error, the waiter's insert succeeds and it does the work itself. A key therefore acts at most once and is never stuck. In testing, 50 parallel retries with one key produced 1 reservation and 49 replays.
- **Same key, different body.** The row stores `sha256(show_id | sorted seats)`. A different hash returns 409 `idempotency_mismatch`. Seat order, case and duplicates do not change the hash.
- **Replay semantics.** A replayed success returns 200 with the original body and `Idempotent-Replay: true`. It is not a 201, so each seat has exactly one 201 even when clients retry. Declines (`seat_taken`, `per_user_limit`) are stored too and replay as the same 409, so a retry always gets a consistent answer. Malformed requests (unknown seat, validation) roll back and are not stored.
- **A scope change found under load.** Keys started out scoped to `(user, key)` globally. The second 20k burst returned 2,500 `idempotency_mismatch` responses, because the first run had used the same user ids and keys against another show. A grader re-running a fixed script against a fresh show would hit the same thing. Migration 0002 re-scoped keys per show. The trade-off is that one key reused on two different shows creates two reservations. That is acceptable because the show is part of the URL being called.

## 3. Holds and expiry

- **Model.** A reserve creates a hold. The default TTL is 300 seconds, configurable per show. Only the owner can confirm it, which moves it to `confirmed`, or cancel it, which moves the seats back to `available`. Both calls are idempotent.
- **Expiry does not depend on a background job.** The reserve path treats a lapsed hold as free inside the same locked check, so a seat becomes re-bookable the moment its hold lapses. A sweeper runs every second, releases lapsed holds in batches of 500 with `FOR UPDATE SKIP LOCKED`, marks them `expired`, and refreshes the gauges. It keeps the reported state truthful but is not needed for correctness.
- **A release never resurrects someone else's seat.** Each seat row records the reservation that owns it. Every release (cancel, sweep) is guarded with `WHERE reservation_id = <this reservation>`. If a hold lapsed and the seat was re-booked or confirmed by someone else, the seat points at the new reservation and the stale release matches nothing. Confirm requires an unexpired hold whose seats all still point at it, and otherwise rolls back with 409 `invalid_state`. Tests cover a stale cancel and a stale sweep against a re-booked, confirmed seat.
- **Per-user limit after release.** The limit counts unlapsed held seats plus confirmed seats. Cancel and expiry free capacity immediately.
- **Clocks.** Expiry is written and compared using the application clock throughout. With several instances this relies on NTP; using the database clock is listed under next steps.

## 4. Consistency vs availability under a partition

The service chooses consistency. There is a single Postgres primary and no other source of truth, no local cache of seat state, and no fallback that answers without the database.

- If the app cannot reach Postgres, reserve, confirm and cancel return 503 `unavailable`. `/readyz` turns 503, the platform stops treating the instance as healthy, and nothing is ever reported as held or confirmed without a committed transaction. Selling a seat twice costs a refund and a customer's trust; refusing sales for a minute costs a retry.
- A partition mid-request leaves the client unsure whether it committed. The idempotency key makes the retry safe: it returns whatever actually happened.
- If Neon restarts its compute, in-flight transactions abort and roll back. Nothing is half-applied, and clients retry with the same key.
- All coordination (row locks, advisory locks, unique keys) is in Postgres, so running several app instances does not change any guarantee.

## 5. Observability: what pages at 2am

Page someone:

- **Any 5xx on reserve.** `http_requests_total{route="POST /shows/{id}/reserve",status=~"5.."}` above 0 for 2 minutes. By design the only 5xx is "database unreachable", so this means Postgres is down or a bug escaped.
- **Readiness failing.** `/readyz` down for more than 2 minutes, from the uptime monitor or the platform health check.
- **Inventory drift.** For any show, `seats_available + seats_held + seats_confirmed` differs from its seat count, or a seat points at a reservation that is not held or confirmed. This should be impossible; if it fires, stop sales for that show.
- **Saturation during an on-sale.** Reserve p99 above 2 seconds for 5 minutes while `db_pool_empty_acquire_total` climbs fast. Requests are queueing on the pool, and buyers will start timing out.

Ticket, not page:

- A spike in `reservations_declined_total{reason="seat_taken"}`. That is what an on-sale looks like.
- `reservations_released_total{cause="expire"}` running high, meaning buyers are not completing checkout. That is a product signal.
- Sustained `idempotency_mismatch`. Some client is reusing keys, which is a client bug.

**An incident this caught.** The first 20k burst against the live free instance returned 11,733 502s from Render's proxy, and the process restarted mid-burst. The cause was the readiness probe. It borrowed a connection from the same pool as the roughly 1,000 queued requests, waited past its 2-second timeout, and returned 503, so Render restarted a healthy instance at peak load. Correctness held: the database showed no double-sold seat and one reservation per key. Readiness now uses its own dedicated connection, so it measures whether Postgres is reachable, not how busy the service is. The rerun had zero 5xx, readiness answered within 0.8 seconds throughout, and the instance did not restart. The lesson is that a health check that degrades under load turns overload into an outage.

Every log line carries `request_id`, which is echoed in the `X-Request-ID` response header, so one buyer's complaint can be traced to one line.

## 6. AI usage

I used Claude Code throughout, and I am disclosing which parts were directed and which were decided.

**Directed by me:**

- I gave it the assignment and asked for a plan before any code.
- I reviewed and approved the design.
- I asked it to add a deployment plan to the design, since the assignment weights Deploy & Observe equally with correctness.
- I set the working rules: incremental commits under my name, no AI co-author lines, and planning documents kept out of the repo.

**Decided by the AI, and accepted by me:**

- It asked me which datastore and platform to use. I asked for its recommendation, it chose Postgres on Neon with Render, and I accepted.
- It proposed the time-boxed hold with explicit confirm, the all-or-nothing rule for partial requests, the advisory lock for the per-user limit, the global lock order, storing idempotency in the same transaction, and the metric set.

**Written by the AI:** the code, tests, burst script and first drafts of these documents.

**Caught along the way:**

- **Partial-hold commit.** The first plan's "should be unreachable" guard would have committed a decline after inserting a reservation, leaving an orphan reservation. It was changed to roll back.
- **Cancel and reserve could deadlock.** In the first plan, cancel updated seats in index order while reserve locked them in label order. All paths now share one lock order.
- **Idempotency scope.** Found by rerunning the 20k burst, as described in section 2.
- **Readiness under load.** Found by the live 20k burst, as described in section 5.
- **Two bugs in the load tool itself.** The seat-label generator broke past row Z, and `burst.sh` took a flag for the admin token.

## 7. What I would do next

- Payments: a hold creates a payment intent, and confirm happens on the payment webhook, with an outbox so confirmation and notification cannot diverge.
- Expire idempotency keys after 24 hours with a cleanup job.
- Use the database clock (`now()`) for hold expiry, so many instances agree without relying on NTP.
- Cache shows in process, since they are immutable after creation. That saves a round trip per reserve.
- Add a waiting room or per-show admission queue for very large on-sales, so the database does not spend work on requests that are certain to lose. Any "seat already taken" fast path would be a hint only; Postgres stays the authority.
- Replace the demo token endpoint with JWT verification against a real identity provider.
- Commit alert rules and a dashboard alongside the code.
- Run on a larger instance for real on-sales. The free instance drains about 150 reserves per second because it is CPU-bound; the design scales with CPU until Postgres becomes the limit.
