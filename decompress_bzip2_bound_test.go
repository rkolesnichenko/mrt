package mrt

import (
	"math"
	"math/big"
	"testing"
)

// TestWindowBoundSaturates: windowBound is the exact bound,
// 2 × (maxSegment + Workers × 2 MiB + chainSlack) + fillSize, wherever that
// fits in an int, and math.MaxInt beyond, never less: a bound that wrapped
// negative made grow add one read's room at a time, recopying the window on
// every read. On 32-bit platforms the bound passes math.MaxInt from 508
// workers; on 64-bit ones only for absurd counts, which the test also checks.
func TestWindowBoundSaturates(t *testing.T) {
	exact := func(w int) *big.Int {
		b := big.NewInt(int64(w))
		b.Lsh(b, 21)
		b.Add(b, big.NewInt(maxSegment+chainSlack))
		b.Mul(b, big.NewInt(2))
		return b.Add(b, big.NewInt(fillSize))
	}
	limit := big.NewInt(math.MaxInt)
	prev := 0
	for _, w := range []int{0, 1, 14, 507, 508, 509, 1 << 20, math.MaxInt >> 22, math.MaxInt>>22 + 1, math.MaxInt / 4, math.MaxInt / 2, math.MaxInt} {
		want := exact(w)
		if want.Cmp(limit) > 0 {
			want = limit
		}
		got := windowBound(w)
		if big.NewInt(int64(got)).Cmp(want) != 0 {
			t.Errorf("windowBound(%d) = %d, want %v", w, got, want)
		}
		if got < prev {
			t.Errorf("windowBound(%d) = %d, less than %d for fewer workers", w, got, prev)
		}
		prev = got
	}
}

// TestGrowDoublesAtManyWorkers: with many workers, a full window of 1 MiB
// still doubles when it grows. With the bound wrapped negative, grow added
// only one read's room (on 32-bit platforms from 508 workers).
func TestGrowDoublesAtManyWorkers(t *testing.T) {
	for _, w := range []int{14, 507, 508, 1 << 20, math.MaxInt>>22 + 1, math.MaxInt} {
		z := &parallelBzip2{workers: w, win: make([]byte, 1<<20)}
		z.grow()
		if cap(z.win) < 2<<20 {
			t.Errorf("workers %d: a full 1 MiB window grew to %d, want at least %d", w, cap(z.win), 2<<20)
		}
	}
}
