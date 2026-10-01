package auth

import (
	"strings"
	"testing"
)

func TestSignVerify(t *testing.T) {
	a := New("secret", "admin-token")
	tok, err := a.Sign("user-1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := a.Verify(tok)
	if err != nil || uid != "user-1" {
		t.Fatalf("verify: %v %q", err, uid)
	}
	_, sig, _ := strings.Cut(tok, ".")
	for name, bad := range map[string]string{
		"tampered signature":       tok + "0",
		"signature for other user": "user-2." + sig,
		"no signature":             "user-1",
		"empty":                    "",
		"admin token as user":      "admin-token",
	} {
		if _, err := a.Verify(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := New("other", "x").Verify(tok); err == nil {
		t.Fatal("token from a different secret accepted")
	}
	for _, bad := range []string{"bad user!", "", strings.Repeat("a", 65), "a.b"} {
		if _, err := a.Sign(bad); err == nil {
			t.Errorf("invalid user id %q accepted", bad)
		}
	}
}

func TestIsAdmin(t *testing.T) {
	a := New("secret", "admin-token")
	if !a.IsAdmin("admin-token") || a.IsAdmin("nope") || a.IsAdmin("") {
		t.Fatal("admin check wrong")
	}
	if New("secret", "").IsAdmin("") {
		t.Fatal("empty admin token must never match")
	}
}
