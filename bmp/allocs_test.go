package bmp

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rkolesnichenko/mrt"
)

// TestAllocations is the bmp allocation gate (see the root TestAllocations):
// Messages allocates two objects per message, its bytes and the Message
// interface value, and at most perError per error (on the fixtures an error
// costs 2 to 4).
func TestAllocations(t *testing.T) {
	const perError = 8
	if raceEnabled {
		t.Skip("the race detector inflates allocation")
	}
	if strconv.IntSize != 64 {
		t.Skip("the counts are measured on 64-bit platforms")
	}
	paths, err := filepath.Glob("../testdata/bmp/*.bmp")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no .bmp fixtures: %v", err)
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			d, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			msgs, errs := 0, 0
			for _, err := range Messages(bytes.NewReader(d)) {
				if err != nil {
					errs++
				} else {
					msgs++
				}
			}
			got := testing.AllocsPerRun(3, func() {
				for range Messages(bytes.NewReader(d)) {
				}
			})
			// An error costs its formatting (perError); the constant covers
			// the bufio.Reader and the iterator's state.
			if limit := float64(2*msgs + perError*errs + 16); got > limit {
				t.Errorf("Messages: %.0f allocations for %d messages and %d errors, want at most %.0f (2 per message, %d per error)", got, msgs, errs, limit, perError)
			}
		})
	}
}

// TestSessionRoutesAllocations: Session.Routes of a message that yields
// nothing is one allocation, the returned sequence; what it holds of the
// message is inside it. The sequence is called with a yield made once: a
// range loop over it adds its own body's closure, which is the caller's.
func TestSessionRoutesAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector inflates allocation")
	}
	m, err := ParseMessage([]byte{3, 0, 0, 0, 6, byte(TypeTermination)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(RoutesOptions{PeerStates: true})
	yield := func(mrt.Route, error) bool { return true }
	if got := testing.AllocsPerRun(100, func() { s.Routes(m, nil)(yield) }); got != 1 {
		t.Errorf("Session.Routes of a Termination: %v allocations, want 1", got)
	}
}

// TestSessionEachAllocations: Session.Each of a message that yields nothing
// allocates nothing, where Session.Routes allocates its sequence. The yield
// is a closure that captures a variable and is made on every call, as a
// caller's would be: it stays on the stack only while Each does not let it
// escape.
func TestSessionEachAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector inflates allocation")
	}
	m, err := ParseMessage([]byte{3, 0, 0, 0, 6, byte(TypeTermination)})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(RoutesOptions{PeerStates: true})
	n := 0
	if got := testing.AllocsPerRun(100, func() {
		s.Each(m, nil, func(mrt.Route, error) bool { n++; return true })
	}); got != 0 {
		t.Errorf("Session.Each of a Termination: %v allocations, want 0", got)
	}
}
