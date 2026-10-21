package mrt_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/internal/bzcraft"
)

// TestBoundTerms is the v0.3.2 design §5.2 gate on the terms of
// DecompressWith's bound that the parallel bzip2 reader can count, through its
// test hooks. It runs only with MRT_BOUNDGATE=1, alone (it holds hundreds of
// MB): zeros-9 and the bzcraft inputs that load the terms (skipped without the
// bzip2 command), each read whole into io.Discard at Workers 4 and 14. It logs
// one row per input, Workers and term: the term's bound, its peak and the
// share of the bound reached. It fails if a peak is over its bound:
//
//   - segment copies (Held): 32 MiB, segmentBudget;
//   - results sent and not received (Results): 2 × Workers × SpecCap;
//   - the window (Window): WindowBound, what fill and compact allow,
//     2 × (8 MiB + 2 × Workers MiB + 16) + 64 KiB. The row also gives
//     2 × (8 MiB + 2 × Workers MiB), the v0.3.1 design's figure, which a
//     read of r can pass by up to 64 KiB + 32 bytes.
//
// The heap terms the reader cannot count (each worker's decode, the chain's
// serial decode) are decompbench -mem's (v0.3.2 design §5.2).
func TestBoundTerms(t *testing.T) {
	if os.Getenv("MRT_BOUNDGATE") != "1" {
		t.Skip("MRT_BOUNDGATE=1 runs the bound gate")
	}
	inputs := []struct {
		name  string
		build func() ([]byte, error)
	}{
		{"zeros-9", func() ([]byte, error) { return os.ReadFile("testdata/bzip2/zeros/zeros-9.bz2") }},
		{"results-load", bzcraft.ResultsLoad},
		{"segment-load", bzcraft.SegmentLoad},
		{"under-cap-blocks", bzcraft.UnderCapBlocks},
	}
	for _, c := range inputs {
		t.Run(c.name, func(t *testing.T) {
			in := craftedInput(t, c.build)
			for _, w := range []int{4, 14} {
				boundTerms(t, c.name, in, w)
			}
		})
	}
}

// boundTerms reads in at the given Workers and checks each counted term's peak
// against its bound.
func boundTerms(t *testing.T, name string, in []byte, workers int) {
	var mu sync.Mutex // Results is called from any goroutine
	var segPeak, resPeak int64
	var winPeak int
	hooks := mrt.Bzip2Hooks{
		Held: func(segBytes int64, _ int) {
			mu.Lock()
			defer mu.Unlock()
			segPeak = max(segPeak, segBytes)
		},
		Results: func(held int64) {
			mu.Lock()
			defer mu.Unlock()
			resPeak = max(resPeak, held)
		},
		Window: func(n int) {
			mu.Lock()
			defer mu.Unlock()
			winPeak = max(winPeak, n)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := mrt.DecompressWithBzip2Hooks(bytes.NewReader(in), mrt.DecompressOptions{Workers: workers, Context: ctx}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatalf("workers %d: %d bytes, error %v", workers, n, err)
	}
	mu.Lock() // EOF stopped the workers
	defer mu.Unlock()
	lead := 8<<20 + workers<<21
	for _, row := range []struct {
		term        string
		bound, peak int64
		note        string
	}{
		{"segments", 32 << 20, segPeak, ""},
		{"results", int64(2 * workers * mrt.SpecCap), resPeak, ""},
		{"window", int64(mrt.WindowBound(workers)), int64(winPeak), "2L " + strconv.Itoa(2*lead)},
	} {
		t.Logf("%-16s W%-2d out %10d  %-8s bound %10d peak %10d %5.1f%% %s",
			name, workers, n, row.term, row.bound, row.peak, 100*float64(row.peak)/float64(row.bound), row.note)
		if row.peak > row.bound {
			t.Errorf("%s at workers %d: %s peak %d over the bound %d", name, workers, row.term, row.peak, row.bound)
		}
	}
}
