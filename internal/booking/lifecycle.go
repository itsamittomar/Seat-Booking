package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Lock order, shared by every write path so no two transactions can wait on each other in a cycle:
//
//	reserve:              idempotency row -> advisory (show,user) -> seats by (show_id, label)
//	confirm/cancel:       reservation row -> seats by (show_id, label)
//	sweep:                reservation rows (SKIP LOCKED) -> seats by (show_id, label)
//
// Reserve never locks an existing reservation row, and seats are always taken in the same order.

// lockReservationSeats locks a reservation's seat rows in label order before they are modified.
func lockReservationSeats(ctx context.Context, tx pgx.Tx, r Reservation) error {
	_, err := tx.Exec(ctx,
		`SELECT 1 FROM seats WHERE show_id=$1 AND label = ANY($2) ORDER BY label FOR UPDATE`,
		r.ShowID, r.Seats)
	return err
}

// Confirm moves a live hold to confirmed. Owner-only. Repeat calls on a confirmed reservation are no-ops.
func (s *Service) Confirm(ctx context.Context, id, userID string) (Reservation, error) {
	var out Reservation
	err := s.withRetry(ctx, func(ctx context.Context) error {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		r, err := s.getReservation(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r.UserID != userID {
			return ErrForbidden
		}
		switch r.Status {
		case ReservationConfirmed:
			out = r
			return nil
		case ReservationHeld:
			if r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now()) {
				return invalidState("hold has expired")
			}
		default:
			return invalidState("reservation is " + string(r.Status))
		}
		if err := lockReservationSeats(ctx, tx, r); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE seats SET status='confirmed', hold_expires_at=NULL
			  WHERE show_id=$1 AND label = ANY($2) AND reservation_id=$3 AND status='held'`,
			r.ShowID, r.Seats, r.ID)
		if err != nil {
			return err
		}
		if int(tag.RowsAffected()) != len(r.Seats) {
			// The hold lost a seat (lapsed and re-booked). Roll back; never confirm a partial set.
			return invalidState("hold no longer owns all of its seats")
		}
		if _, err := tx.Exec(ctx, `UPDATE reservations SET status='confirmed', expires_at=NULL WHERE id=$1`, r.ID); err != nil {
			return err
		}
		r.Status, r.ExpiresAt = ReservationConfirmed, nil
		out = r
		return tx.Commit(ctx)
	})
	return out, err
}

// Cancel releases a held or confirmed reservation. Owner-only. Repeat calls are no-ops.
// The reservation_id guard means a stale cancel can never release a seat someone else now owns.
func (s *Service) Cancel(ctx context.Context, id, userID string) (Reservation, error) {
	var out Reservation
	err := s.withRetry(ctx, func(ctx context.Context) error {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		r, err := s.getReservation(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r.UserID != userID {
			return ErrForbidden
		}
		if r.Status == ReservationCancelled || r.Status == ReservationExpired {
			out = r
			return nil
		}
		if err := lockReservationSeats(ctx, tx, r); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE seats SET status='available', reservation_id=NULL, held_by=NULL, hold_expires_at=NULL
			  WHERE show_id=$1 AND label = ANY($2) AND reservation_id=$3`,
			r.ShowID, r.Seats, r.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservations SET status='cancelled', expires_at=NULL WHERE id=$1`, r.ID); err != nil {
			return err
		}
		r.Status, r.ExpiresAt = ReservationCancelled, nil
		out = r
		return tx.Commit(ctx)
	})
	return out, err
}

// SweepExpired marks up to limit lapsed holds as expired and frees the seats they still own.
// SKIP LOCKED lets several instances sweep concurrently and never blocks a confirm or cancel.
func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	n := 0
	err := s.withRetry(ctx, func(ctx context.Context) error {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx,
			`SELECT id::text FROM reservations
			  WHERE status='held' AND expires_at <= $1
			  ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED`, time.Now(), limit)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			n = 0
			return nil
		}
		if _, err := tx.Exec(ctx,
			`SELECT 1 FROM seats WHERE reservation_id = ANY($1::uuid[]) AND status='held'
			  ORDER BY show_id, label FOR UPDATE`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE seats SET status='available', reservation_id=NULL, held_by=NULL, hold_expires_at=NULL
			  WHERE reservation_id = ANY($1::uuid[]) AND status='held'`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservations SET status='expired' WHERE id = ANY($1::uuid[])`, ids); err != nil {
			return err
		}
		n = len(ids)
		return tx.Commit(ctx)
	})
	return n, err
}

// SeatCounts returns per-show seat counts, used to drive the seats_* gauges.
func (s *Service) SeatCounts(ctx context.Context) (map[string]Counts, error) {
	rows, err := s.pool.Query(ctx, `SELECT show_id::text, status::text, count(*) FROM seats GROUP BY show_id, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Counts{}
	for rows.Next() {
		var showID, status string
		var n int
		if err := rows.Scan(&showID, &status, &n); err != nil {
			return nil, err
		}
		c := out[showID]
		switch SeatStatus(status) {
		case SeatAvailable:
			c.Available += n
		case SeatHeld:
			c.Held += n
		case SeatConfirmed:
			c.Confirmed += n
		}
		c.Total += n
		out[showID] = c
	}
	return out, rows.Err()
}
