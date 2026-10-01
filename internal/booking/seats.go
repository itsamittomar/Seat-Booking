package booking

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

var seatLabelRe = regexp.MustCompile(`^[A-Z0-9-]{1,16}$`)

const maxSeatsPerRequest = 100

func normalizeLabel(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// NormalizeSeats upper-cases, trims, de-duplicates and sorts seat labels.
// Sorting is what gives every transaction the same lock order, which rules out deadlocks.
func NormalizeSeats(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, validation("seats must be a non-empty list")
	}
	if len(in) > maxSeatsPerRequest {
		return nil, validation("at most 100 seats per request")
	}
	set := make(map[string]struct{}, len(in))
	for _, raw := range in {
		s := normalizeLabel(raw)
		if !seatLabelRe.MatchString(s) {
			return nil, validation("invalid seat label: " + raw)
		}
		set[s] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// RequestHash fingerprints what a reserve request asks for. It is stored with the
// idempotency key so a reused key with a different body can be detected.
func RequestHash(showID string, sortedSeats []string) string {
	h := sha256.Sum256([]byte(showID + "|" + strings.Join(sortedSeats, ",")))
	return hex.EncodeToString(h[:])
}
