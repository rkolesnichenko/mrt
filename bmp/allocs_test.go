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
// interface value.
func TestAllocations(t *testing.T) {
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
			msgs := 0
			for range Messages(bytes.NewReader(d)) {
				msgs++
			}
			got := testing.AllocsPerRun(3, func() {
				for range Messages(bytes.NewReader(d)) {
				}
			})
			// The constant covers the bufio.Reader and the iterator's state.
			if limit := float64(2*msgs + 16); got > limit {
				t.Errorf("Messages: %.0f allocations for %d messages, want at most %.0f (2 per message)", got, msgs, limit)
			}
		})
	}
}
