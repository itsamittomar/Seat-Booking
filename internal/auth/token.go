// Package auth issues and verifies bearer tokens. Identity always comes from a verified
// token, never from a request body.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

var userIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrInvalidUserID = errors.New("user_id must match [A-Za-z0-9_-]{1,64}")
)

type Auth struct {
	secret []byte
	admin  []byte
}

func New(secret, adminToken string) *Auth {
	return &Auth{secret: []byte(secret), admin: []byte(adminToken)}
}

// Sign returns "<user_id>.<hex HMAC-SHA256(user_id)>".
func (a *Auth) Sign(userID string) (string, error) {
	if !userIDRe.MatchString(userID) {
		return "", ErrInvalidUserID
	}
	return userID + "." + a.mac(userID), nil
}

// Verify returns the user id embedded in a valid token.
func (a *Auth) Verify(token string) (string, error) {
	userID, sig, ok := strings.Cut(token, ".")
	if !ok || !userIDRe.MatchString(userID) {
		return "", ErrInvalidToken
	}
	if !hmac.Equal([]byte(sig), []byte(a.mac(userID))) {
		return "", ErrInvalidToken
	}
	return userID, nil
}

// IsAdmin compares in constant time. An unset admin token never matches.
func (a *Auth) IsAdmin(token string) bool {
	return len(a.admin) > 0 && subtle.ConstantTimeCompare([]byte(token), a.admin) == 1
}

func (a *Auth) mac(userID string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(userID))
	return hex.EncodeToString(m.Sum(nil))
}
