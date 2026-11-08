package bmp

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
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
