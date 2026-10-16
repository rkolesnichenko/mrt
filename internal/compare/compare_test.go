package main

import (
	"strings"
	"testing"
)

// TestGate: the release gate cannot pass vacuously (Ruling R21).
func TestGate(t *testing.T) {
	gob := result{walk: walk{items: 100}, totalAlloc: 1000}
	for _, tc := range []struct {
		name   string
		routes result
		pass   bool
		reason string
	}{
		{"less, clean, same count", result{walk: walk{items: 100}, totalAlloc: 999}, true, "allocates less"},
		{"equal allocation", result{walk: walk{items: 100}, totalAlloc: 1000}, false, "no less"},
		{"route errors", result{walk: walk{items: 100, errs: 1}, totalAlloc: 10}, false, "1 errors"},
		{"fewer routes than gobgp's entries", result{walk: walk{items: 99}, totalAlloc: 10}, false, "99 routes"},
		{"no routes at all", result{walk: walk{}, totalAlloc: 0}, false, "0 routes"},
	} {
		pass, reason := gate(gob, tc.routes)
		if pass != tc.pass || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: gate = %v %q, want %v and %q", tc.name, pass, reason, tc.pass, tc.reason)
		}
	}
}
