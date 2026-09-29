package store

import (
	"regexp"
	"sort"
	"testing"
	"time"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIDMonotonicAndWellFormed(t *testing.T) {
	ids := make([]string, 5000)
	for i := range ids {
		ids[i] = NewID()
		if !uuidRE.MatchString(ids[i]) {
			t.Fatalf("bad uuidv7 %q", ids[i])
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ids minted in sequence must sort in creation order")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}

func TestIDTime(t *testing.T) {
	before := time.Now().Add(-time.Millisecond)
	got, ok := IDTime(NewID())
	if !ok || got.Before(before) || got.After(time.Now().Add(time.Millisecond)) {
		t.Fatalf("IDTime = %v %v", got, ok)
	}
	for _, bad := range []string{"", "not-an-id", "0190f7c2-6a3e-4c1a-9b2e-2f6f9d1b4c55", "zz90f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"} {
		if _, ok := IDTime(bad); ok {
			t.Errorf("IDTime(%q) accepted", bad)
		}
	}
}
