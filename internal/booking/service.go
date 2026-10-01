package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultPerUserLimit = 4
	DefaultHoldTTL      = 5 * time.Minute
	maxSeatsPerShow     = 10000
)

// Service is the system of record. Every state change is one Postgres transaction.
type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (s *Service) CreateShow(ctx context.Context, in CreateShowInput) (Show, error) {
	name := in.Name
	if len(name) == 0 || len(name) > 200 {
		return Show{}, validation("name is required (max 200 chars)")
	}
	if in.PricePaise < 0 {
		return Show{}, validation("price_paise must be >= 0")
	}
	if len(in.Seats) == 0 {
		return Show{}, validation("seats must be a non-empty list")
	}
	if len(in.Seats) > maxSeatsPerShow {
		return Show{}, validation("at most 10000 seats per show")
	}
	seen := make(map[string]struct{}, len(in.Seats))
	labels := make([]string, 0, len(in.Seats))
	for _, raw := range in.Seats {
		l := normalizeLabel(raw)
		if !seatLabelRe.MatchString(l) {
			return Show{}, validation("invalid seat label: " + raw)
		}
		if _, dup := seen[l]; dup {
			return Show{}, validation("duplicate seat label: " + l)
		}
		seen[l] = struct{}{}
		labels = append(labels, l)
	}
	limit := in.PerUserLimit
	if limit == 0 {
		limit = DefaultPerUserLimit
	}
	if limit < 0 {
		return Show{}, validation("per_user_limit must be > 0")
	}
	ttl := in.HoldTTL
	if ttl == 0 {
		ttl = DefaultHoldTTL
	}
	if ttl < time.Second {
		return Show{}, validation("hold_ttl_seconds must be >= 1")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Show{}, err
	}
	defer tx.Rollback(ctx)
	var show Show
	var ttlSeconds int
	err = tx.QueryRow(ctx,
		`INSERT INTO shows (name, price_paise, per_user_limit, hold_ttl_seconds) VALUES ($1,$2,$3,$4)
		 RETURNING id, name, price_paise, per_user_limit, hold_ttl_seconds, created_at`,
		name, in.PricePaise, limit, int(ttl.Seconds()),
	).Scan(&show.ID, &show.Name, &show.PricePaise, &show.PerUserLimit, &ttlSeconds, &show.CreatedAt)
	if err != nil {
		return Show{}, err
	}
	show.HoldTTL = time.Duration(ttlSeconds) * time.Second
	rows := make([][]any, len(labels))
	for i, l := range labels {
		rows[i] = []any{show.ID, l}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"seats"}, []string{"show_id", "label"}, pgx.CopyFromRows(rows)); err != nil {
		return Show{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Show{}, err
	}
	return show, nil
}

func (s *Service) getShow(ctx context.Context, q querier, id string) (Show, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Show{}, notFound("show")
	}
	var show Show
	var ttlSeconds int
	err := q.QueryRow(ctx, `SELECT id, name, price_paise, per_user_limit, hold_ttl_seconds, created_at FROM shows WHERE id=$1`, id).
		Scan(&show.ID, &show.Name, &show.PricePaise, &show.PerUserLimit, &ttlSeconds, &show.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Show{}, notFound("show")
	}
	if err != nil {
		return Show{}, err
	}
	show.HoldTTL = time.Duration(ttlSeconds) * time.Second
	return show, nil
}

// GetShow derives seats and counts from a single statement, so the counts are one
// consistent snapshot and available+held+confirmed == total by construction.
func (s *Service) GetShow(ctx context.Context, id string) (ShowState, error) {
	show, err := s.getShow(ctx, s.pool, id)
	if err != nil {
		return ShowState{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT label, status::text FROM seats WHERE show_id=$1 ORDER BY label`, show.ID)
	if err != nil {
		return ShowState{}, err
	}
	defer rows.Close()
	st := ShowState{Show: show, Seats: []SeatView{}}
	for rows.Next() {
		var v SeatView
		var status string
		if err := rows.Scan(&v.Label, &status); err != nil {
			return ShowState{}, err
		}
		v.Status = SeatStatus(status)
		st.Seats = append(st.Seats, v)
		switch v.Status {
		case SeatAvailable:
			st.Counts.Available++
		case SeatHeld:
			st.Counts.Held++
		case SeatConfirmed:
			st.Counts.Confirmed++
		}
		st.Counts.Total++
	}
	return st, rows.Err()
}

// GetReservation returns a reservation owned by userID.
func (s *Service) GetReservation(ctx context.Context, id, userID string) (Reservation, error) {
	r, err := s.getReservation(ctx, s.pool, id, false)
	if err != nil {
		return Reservation{}, err
	}
	if r.UserID != userID {
		return Reservation{}, ErrForbidden
	}
	return r, nil
}

func (s *Service) getReservation(ctx context.Context, q querier, id string, forUpdate bool) (Reservation, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Reservation{}, notFound("reservation")
	}
	sql := `SELECT id, show_id, user_id, status::text, seats, amount_paise, expires_at, created_at FROM reservations WHERE id=$1`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var r Reservation
	var status string
	err := q.QueryRow(ctx, sql, id).Scan(&r.ID, &r.ShowID, &r.UserID, &status, &r.Seats, &r.AmountPaise, &r.ExpiresAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, notFound("reservation")
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("get reservation: %w", err)
	}
	r.Status = ReservationStatus(status)
	return r, nil
}
