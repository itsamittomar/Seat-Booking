package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxIdempotencyKeyLen = 128

// Reserve performs the atomic decision for one request: an all-or-nothing hold
// on every requested seat, or a clean decline that changes nothing.
//
// Order inside the single transaction:
//  1. idempotency row keyed (show, user, key): insert, or lock the existing one, so a key acts at most once
//  2. advisory transaction lock on (show, user) so the per-user limit check is exact
//  3. seat rows locked FOR UPDATE in label order, so multi-seat requests cannot deadlock
//  4. limit check, then a guarded UPDATE whose row count must equal the request size
//
// Seat-taken and per-user-limit declines are committed together with the decline
// recorded on the idempotency row, so a retry replays the same answer.
func (s *Service) Reserve(ctx context.Context, in ReserveInput) (ReserveResult, error) {
	if in.UserID == "" {
		return ReserveResult{}, validation("user is required")
	}
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > maxIdempotencyKeyLen {
		return ReserveResult{}, validation("idempotency_key is required (max 128 chars)")
	}
	seats, err := NormalizeSeats(in.Seats)
	if err != nil {
		return ReserveResult{}, err
	}
	show, err := s.getShow(ctx, s.pool, in.ShowID)
	if err != nil {
		return ReserveResult{}, err
	}
	hash := RequestHash(show.ID, seats)

	var out ReserveResult
	err = s.withRetry(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.reserveTx(ctx, show, in.UserID, in.IdempotencyKey, hash, seats)
		return err
	})
	return out, err
}

func (s *Service) reserveTx(ctx context.Context, show Show, userID, key, hash string, seats []string) (ReserveResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReserveResult{}, err
	}
	defer tx.Rollback(ctx)

	// 1. Idempotency. If a concurrent request with the same key is in flight, this INSERT
	// blocks on the primary key until that transaction ends, then reports a conflict.
	tag, err := tx.Exec(ctx,
		`INSERT INTO idempotency_keys (show_id, user_id, key, request_hash) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		show.ID, userID, key, hash)
	if err != nil {
		return ReserveResult{}, err
	}
	if tag.RowsAffected() == 0 {
		var storedHash string
		var reservationID, declineCode, declineMsg *string
		err := tx.QueryRow(ctx,
			`SELECT request_hash, reservation_id::text, decline_code, decline_message
			   FROM idempotency_keys WHERE show_id=$1 AND user_id=$2 AND key=$3 FOR UPDATE`,
			show.ID, userID, key).Scan(&storedHash, &reservationID, &declineCode, &declineMsg)
		if err != nil {
			return ReserveResult{}, err
		}
		if storedHash != hash {
			return ReserveResult{}, errIdempotencyMismatch
		}
		switch {
		case reservationID != nil:
			r, err := s.getReservation(ctx, tx, *reservationID, false)
			if err != nil {
				return ReserveResult{}, err
			}
			return ReserveResult{Reservation: r, Replayed: true}, nil
		case declineCode != nil:
			return ReserveResult{}, &DeclineError{Code: *declineCode, Message: *declineMsg, Replayed: true}
		}
		// A row with no outcome cannot be committed by this code; if one exists, fall through and decide now.
	}

	// 2. Serialize this user's reserves on this show. Hash collisions only add waiting, never wrong answers.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text || ':' || $2::text))`, show.ID, userID); err != nil {
		return ReserveResult{}, err
	}

	// 3. Lock the requested seats in label order. Under READ COMMITTED a waiter re-reads the
	// row after the holder commits, so the 499 losers of a hot seat see it as held.
	now := time.Now()
	rows, err := tx.Query(ctx,
		`SELECT label, status::text, hold_expires_at FROM seats
		  WHERE show_id=$1 AND label = ANY($2) ORDER BY label FOR UPDATE`,
		show.ID, seats)
	if err != nil {
		return ReserveResult{}, err
	}
	var unavailable []string
	found := 0
	for rows.Next() {
		var label, status string
		var expires *time.Time
		if err := rows.Scan(&label, &status, &expires); err != nil {
			rows.Close()
			return ReserveResult{}, err
		}
		found++
		if !seatFree(SeatStatus(status), expires, now) {
			unavailable = append(unavailable, label)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReserveResult{}, err
	}
	if found != len(seats) {
		// Not recorded on the idempotency row (rolled back): the request was malformed, not decided.
		return ReserveResult{}, notFound("one or more seats")
	}
	if len(unavailable) > 0 {
		return commitDecline(ctx, tx, show.ID, userID, key, seatTaken(unavailable))
	}

	// 4. Per-user limit over seats this user actively holds or owns on this show.
	var active int
	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM seats
		  WHERE show_id=$1 AND held_by=$2 AND status <> 'available'
		    AND NOT (status = 'held' AND hold_expires_at <= $3)`,
		show.ID, userID, now).Scan(&active)
	if err != nil {
		return ReserveResult{}, err
	}
	if active+len(seats) > show.PerUserLimit {
		return commitDecline(ctx, tx, show.ID, userID, key, perUserLimit(show.PerUserLimit))
	}

	// 5. Write.
	expiresAt := now.Add(show.HoldTTL)
	amount := show.PricePaise * int64(len(seats))
	var r Reservation
	var status string
	err = tx.QueryRow(ctx,
		`INSERT INTO reservations (show_id, user_id, status, seats, amount_paise, expires_at)
		 VALUES ($1,$2,'held',$3,$4,$5)
		 RETURNING id, show_id, user_id, status::text, seats, amount_paise, expires_at, created_at`,
		show.ID, userID, seats, amount, expiresAt,
	).Scan(&r.ID, &r.ShowID, &r.UserID, &status, &r.Seats, &r.AmountPaise, &r.ExpiresAt, &r.CreatedAt)
	if err != nil {
		return ReserveResult{}, err
	}
	r.Status = ReservationStatus(status)
	tag, err = tx.Exec(ctx,
		`UPDATE seats SET status='held', reservation_id=$3, held_by=$4, hold_expires_at=$5
		  WHERE show_id=$1 AND label = ANY($2)
		    AND (status='available' OR (status='held' AND hold_expires_at <= $6))`,
		show.ID, seats, r.ID, userID, expiresAt, now)
	if err != nil {
		return ReserveResult{}, err
	}
	if int(tag.RowsAffected()) != len(seats) {
		// Unreachable while the row locks above are held. If it ever happens, roll everything
		// back (the deferred Rollback) rather than commit a partial hold.
		return ReserveResult{}, seatTaken(seats)
	}
	if _, err := tx.Exec(ctx, `UPDATE idempotency_keys SET reservation_id=$4 WHERE show_id=$1 AND user_id=$2 AND key=$3`,
		show.ID, userID, key, r.ID); err != nil {
		return ReserveResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReserveResult{}, err
	}
	return ReserveResult{Reservation: r}, nil
}

// seatFree reports whether a seat can be taken: available, or held with a lapsed hold.
// Treating lapsed holds as free here means correctness never depends on the sweeper's timing.
func seatFree(status SeatStatus, holdExpiresAt *time.Time, now time.Time) bool {
	switch status {
	case SeatAvailable:
		return true
	case SeatHeld:
		return holdExpiresAt != nil && !holdExpiresAt.After(now)
	default:
		return false
	}
}

// commitDecline records the decline on the idempotency row and commits, so a retry replays it.
func commitDecline(ctx context.Context, tx pgx.Tx, showID, userID, key string, d *DeclineError) (ReserveResult, error) {
	if _, err := tx.Exec(ctx,
		`UPDATE idempotency_keys SET decline_code=$4, decline_message=$5 WHERE show_id=$1 AND user_id=$2 AND key=$3`,
		showID, userID, key, d.Code, d.Message); err != nil {
		return ReserveResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReserveResult{}, err
	}
	return ReserveResult{}, d
}

// withRetry re-runs fn on serialization failures or deadlocks (40001, 40P01), up to 3 attempts.
// With a single global lock order these should not occur; this is defence in depth.
func (s *Service) withRetry(ctx context.Context, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = fn(ctx)
		if err == nil || !isRetryable(err) {
			return err
		}
	}
	return fmt.Errorf("transaction failed after retries: %w", err)
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}
