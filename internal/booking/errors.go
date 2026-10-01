package booking

import (
	"errors"
	"fmt"
	"strings"
)

// Decline codes are stable strings shared by the HTTP layer, metrics and logs.
const (
	CodeSeatTaken           = "seat_taken"
	CodePerUserLimit        = "per_user_limit"
	CodeIdempotencyMismatch = "idempotency_mismatch"
	CodeInvalidState        = "invalid_state"
	CodeNotFound            = "not_found"
	CodeValidation          = "validation"
	CodeForbidden           = "forbidden"
)

// DeclineError is a domain outcome (4xx), never a server failure.
type DeclineError struct {
	Code     string
	Message  string
	Replayed bool // true when served from the idempotency store
}

func (e *DeclineError) Error() string { return e.Code + ": " + e.Message }

func seatTaken(seats []string) *DeclineError {
	return &DeclineError{Code: CodeSeatTaken, Message: "seats not available: " + strings.Join(seats, ",")}
}

func perUserLimit(limit int) *DeclineError {
	return &DeclineError{Code: CodePerUserLimit, Message: fmt.Sprintf("per-user limit of %d seats for this show would be exceeded", limit)}
}

func notFound(what string) *DeclineError {
	return &DeclineError{Code: CodeNotFound, Message: what + " not found"}
}

func validation(msg string) *DeclineError {
	return &DeclineError{Code: CodeValidation, Message: msg}
}

func invalidState(msg string) *DeclineError {
	return &DeclineError{Code: CodeInvalidState, Message: msg}
}

// ErrForbidden is returned when a user acts on a reservation they do not own.
var ErrForbidden = &DeclineError{Code: CodeForbidden, Message: "reservation belongs to another user"}

var errIdempotencyMismatch = &DeclineError{Code: CodeIdempotencyMismatch, Message: "idempotency key was already used with a different request"}

// AsDecline unwraps a DeclineError if err is one.
func AsDecline(err error) (*DeclineError, bool) {
	var d *DeclineError
	if errors.As(err, &d) {
		return d, true
	}
	return nil, false
}
