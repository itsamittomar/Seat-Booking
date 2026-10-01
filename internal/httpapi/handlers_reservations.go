package httpapi

import (
	"net/http"
	"time"

	"bookingSystem/internal/booking"
)

type reserveRequest struct {
	Seats          []string `json:"seats"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type reservationResponse struct {
	ReservationID string     `json:"reservation_id"`
	ShowID        string     `json:"show_id"`
	UserID        string     `json:"user_id"`
	Seats         []string   `json:"seats"`
	AmountPaise   int64      `json:"amount_paise"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func toReservationResponse(r booking.Reservation) reservationResponse {
	return reservationResponse{ReservationID: r.ID, ShowID: r.ShowID, UserID: r.UserID, Seats: r.Seats,
		AmountPaise: r.AmountPaise, Status: string(r.Status), ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}
}

// reserve: 201 for a new hold, 200 + Idempotent-Replay for a retried key, 409/404/422 for declines.
func (h *handler) reserve(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var req reserveRequest
	if !decodeJSON(w, r, &req) {
		h.metrics.ReservationsDeclined.WithLabelValues(booking.CodeValidation).Inc()
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = req.IdempotencyKey
	}
	res, err := h.svc.Reserve(r.Context(), booking.ReserveInput{ShowID: r.PathValue("id"), UserID: uid, IdempotencyKey: key, Seats: req.Seats})
	if err != nil {
		if d, isDecline := booking.AsDecline(err); isDecline {
			reason := d.Code
			if d.Replayed {
				reason = "idempotent_replay"
			}
			h.metrics.ReservationsDeclined.WithLabelValues(reason).Inc()
		}
		h.writeDomainError(w, r, err)
		return
	}
	if res.Replayed {
		h.metrics.ReservationsDeclined.WithLabelValues("idempotent_replay").Inc()
		info(r).DeclineReason = "idempotent_replay"
		w.Header().Set("Idempotent-Replay", "true")
		writeJSON(w, http.StatusOK, toReservationResponse(res.Reservation))
		return
	}
	h.metrics.ReservationsHeld.Inc()
	writeJSON(w, http.StatusCreated, toReservationResponse(res.Reservation))
}

func (h *handler) getReservation(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	res, err := h.svc.GetReservation(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReservationResponse(res))
}

func (h *handler) confirm(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Confirm(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	h.metrics.ReservationsConfirmed.Inc()
	writeJSON(w, http.StatusOK, toReservationResponse(res))
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Cancel(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	h.metrics.ReservationsReleased.WithLabelValues("cancel").Inc()
	writeJSON(w, http.StatusOK, toReservationResponse(res))
}
