package httpapi

import (
	"net/http"
	"time"

	"bookingSystem/internal/booking"
)

type createShowRequest struct {
	Name           string   `json:"name"`
	Seats          []string `json:"seats"`
	PricePaise     int64    `json:"price_paise"`
	PerUserLimit   int      `json:"per_user_limit"`
	HoldTTLSeconds int      `json:"hold_ttl_seconds"`
}

type showResponse struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	PricePaise     int64              `json:"price_paise"`
	PerUserLimit   int                `json:"per_user_limit"`
	HoldTTLSeconds int                `json:"hold_ttl_seconds"`
	CreatedAt      time.Time          `json:"created_at"`
	Counts         booking.Counts     `json:"counts"`
	Seats          []booking.SeatView `json:"seats"`
}

func toShowResponse(st booking.ShowState) showResponse {
	return showResponse{
		ID: st.Show.ID, Name: st.Show.Name, PricePaise: st.Show.PricePaise, PerUserLimit: st.Show.PerUserLimit,
		HoldTTLSeconds: int(st.Show.HoldTTL / time.Second), CreatedAt: st.Show.CreatedAt, Counts: st.Counts, Seats: st.Seats,
	}
}

func (h *handler) createShow(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var req createShowRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.HoldTTLSeconds < 0 || req.PerUserLimit < 0 {
		writeError(w, http.StatusUnprocessableEntity, booking.CodeValidation, "per_user_limit and hold_ttl_seconds must be positive when given")
		return
	}
	show, err := h.svc.CreateShow(r.Context(), booking.CreateShowInput{
		Name: req.Name, Seats: req.Seats, PricePaise: req.PricePaise, PerUserLimit: req.PerUserLimit,
		HoldTTL: time.Duration(req.HoldTTLSeconds) * time.Second,
	})
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	st, err := h.svc.GetShow(r.Context(), show.ID)
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toShowResponse(st))
}

func (h *handler) getShow(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.GetShow(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toShowResponse(st))
}
