package httpapi

import (
	"net/http"
	"strings"

	"bookingSystem/internal/auth"
)

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// requireUser verifies the bearer token and returns its user id, writing 401 on failure.
func (h *handler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid, err := h.auth.Verify(bearer(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return "", false
	}
	info(r).UserID = uid
	return uid, true
}

func (h *handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !h.auth.IsAdmin(bearer(r)) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin token required")
		return false
	}
	info(r).UserID = "admin"
	return true
}

type tokenRequest struct {
	UserID string `json:"user_id"`
}

// mintToken is a deliberate demo shortcut so load testers can create many users.
// A real deployment would get identity from an identity provider instead.
func (h *handler) mintToken(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tok, err := h.auth.Sign(req.UserID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation", auth.ErrInvalidUserID.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user_id": req.UserID, "token": tok})
}
