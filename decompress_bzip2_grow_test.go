package mrt

import "testing"

// TestGrowLeavesRoomForRead: after grow, the window has room for one read of
// fillSize bytes, whatever its length, also with its capacity at windowBound
// and a length the bound leaves no room for (which the derivation says fill
// never meets: grow then goes past the bound rather than fail the read).
func TestGrowLeavesRoomForRead(t *testing.T) {
	for _, workers := range []int{1, 4, 14} {
		bound := windowBound(workers)
		for _, c := range []struct{ len, cap int }{
			{0, 0}, {1, fillSize}, {fillSize, fillSize}, {fillSize - 1, fillSize},
			{bound / 2, bound / 2}, {bound - fillSize, bound}, {bound - fillSize + 1, bound}, {bound, bound},
		} {
			z := &parallelBzip2{workers: workers, win: make([]byte, c.len, c.cap)}
			z.grow()
			if room := cap(z.win) - len(z.win); room < fillSize || len(z.win) != c.len {
				t.Errorf("workers %d, len %d, cap %d: after grow len %d, cap %d (room %d), want len %d and room %d",
					workers, c.len, c.cap, len(z.win), cap(z.win), room, c.len, fillSize)
			}
			if c.len+fillSize <= bound && cap(z.win) > bound {
				t.Errorf("workers %d, len %d, cap %d: cap %d over the bound %d", workers, c.len, c.cap, cap(z.win), bound)
			}
		}
	}
}
