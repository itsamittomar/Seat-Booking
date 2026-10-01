package booking

import (
	"reflect"
	"testing"
)

func TestNormalizeSeats(t *testing.T) {
	got, err := NormalizeSeats([]string{"b2", " A1 ", "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"A1", "B2"}) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range [][]string{nil, {}, {""}, {"has space"}, {"x/y"}} {
		if _, err := NormalizeSeats(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "A1"
	}
	if _, err := NormalizeSeats(tooMany); err == nil {
		t.Error("expected error for >100 seats")
	}
}

func TestRequestHashStable(t *testing.T) {
	a := RequestHash("show", []string{"A1", "A2"})
	b := RequestHash("show", []string{"A1", "A2"})
	c := RequestHash("show", []string{"A1", "A3"})
	d := RequestHash("other", []string{"A1", "A2"})
	if a != b || a == c || a == d || len(a) != 64 {
		t.Fatalf("hash not stable/unique: %s %s %s %s", a, b, c, d)
	}
}
