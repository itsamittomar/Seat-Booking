package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"bookingSystem/internal/booking"
)

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

// statusClientClosed is nginx's convention for "client went away"; it keeps those out of 5xx.
const statusClientClosed = 499

// writeDomainError maps a service error to a response. Domain declines are always 4xx.
// Only genuine infrastructure failure (database unreachable) becomes 503.
func (h *handler) writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	if d, ok := booking.AsDecline(err); ok {
		status := http.StatusConflict
		switch d.Code {
		case booking.CodeNotFound:
			status = http.StatusNotFound
		case booking.CodeValidation:
			status = http.StatusUnprocessableEntity
		case booking.CodeForbidden:
			status = http.StatusForbidden
		}
		info(r).DeclineReason = d.Code
		if d.Replayed {
			w.Header().Set("Idempotent-Replay", "true")
		}
		writeError(w, status, d.Code, d.Message)
		return
	}
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		info(r).DeclineReason = "client_gone"
		w.WriteHeader(statusClientClosed)
		return
	}
	h.logger.Error("internal error", "request_id", info(r).ID, "err", err)
	writeError(w, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
}

// decodeJSON reads the body into v. Unknown fields are accepted and ignored on purpose: a
// spoofed "user_id" in a body must be harmless, not an error, because identity comes from the token.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusUnprocessableEntity, booking.CodeValidation, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
