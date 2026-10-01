// Package booking holds the seat reservation domain: shows, holds, confirmations and their invariants.
package booking

import "time"

type SeatStatus string

const (
	SeatAvailable SeatStatus = "available"
	SeatHeld      SeatStatus = "held"
	SeatConfirmed SeatStatus = "confirmed"
)

type ReservationStatus string

const (
	ReservationHeld      ReservationStatus = "held"
	ReservationConfirmed ReservationStatus = "confirmed"
	ReservationCancelled ReservationStatus = "cancelled"
	ReservationExpired   ReservationStatus = "expired"
)

type Show struct {
	ID           string
	Name         string
	PricePaise   int64
	PerUserLimit int
	HoldTTL      time.Duration
	CreatedAt    time.Time
}

type SeatView struct {
	Label  string     `json:"label"`
	Status SeatStatus `json:"status"`
}

type Counts struct {
	Available int `json:"available"`
	Held      int `json:"held"`
	Confirmed int `json:"confirmed"`
	Total     int `json:"total"`
}

type ShowState struct {
	Show   Show
	Seats  []SeatView
	Counts Counts
}

type Reservation struct {
	ID          string
	ShowID      string
	UserID      string
	Status      ReservationStatus
	Seats       []string
	AmountPaise int64
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

type CreateShowInput struct {
	Name         string
	Seats        []string
	PricePaise   int64
	PerUserLimit int           // 0 means DefaultPerUserLimit
	HoldTTL      time.Duration // 0 means DefaultHoldTTL
}

type ReserveInput struct {
	ShowID         string
	UserID         string
	IdempotencyKey string
	Seats          []string
}

type ReserveResult struct {
	Reservation Reservation
	Replayed    bool // served from the idempotency store, nothing moved
}
