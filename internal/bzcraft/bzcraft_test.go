package bzcraft

import (
	"bytes"
	"compress/bzip2"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"
)

// built returns the input build makes, skipping the test without bzip2.
func built(t *testing.T, build func() ([]byte, error)) []byte {
	t.Helper()
	start := time.Now()
	in, err := build()
	if errors.Is(err, ErrNoBzip2) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("built in %v", time.Since(start))
	return in
}

// TestBuilt checks the three bzip2-built inputs: each decodes through
// compress/bzip2 to its plaintext size, is under 15 MB, and has the blocks
// its doc states. The blocks of runs decode to just under SpecCap: at least
// 2,000,000 bytes, but for each stream's last.
func TestBuilt(t *testing.T) {
	const minRunBlock = 2_000_000
	for _, c := range []struct {
		name  string
		build func() ([]byte, error)
		size  int
		check func(t *testing.T, in []byte, starts, ends []uint64, sizes []int)
	}{
		{"UnderCapBlocks", UnderCapBlocks, underCapSize, func(t *testing.T, _ []byte, _, _ []uint64, sizes []int) {
			if len(sizes) < 2*14+1 {
				t.Errorf("%d blocks, want at least 29, for 2 × 14 workers' jobs behind the first", len(sizes))
			}
			runBlocks(t, sizes, minRunBlock)
		}},
		{"ResultsLoad", ResultsLoad, resultsLoadSize, func(t *testing.T, _ []byte, _, _ []uint64, sizes []int) {
			if len(sizes) < 1+2*14 || sizes[0] != zeroBytes {
				t.Fatalf("%d blocks, the first of %d bytes; want %d zero bytes then at least 28 blocks",
					len(sizes), sizes[0], zeroBytes)
			}
			runBlocks(t, sizes[1:], minRunBlock)
		}},
		{"SegmentLoad", SegmentLoad, segmentLoadSize, func(t *testing.T, in []byte, starts, ends []uint64, sizes []int) {
			if len(sizes) != 2 {
				t.Fatalf("%d blocks, want 2", len(sizes))
			}
			runBlocks(t, sizes, minRunBlock)
			for i := range starts {
				if n := (ends[i] - starts[i]) / 8; n < segmentBlockBytes-16 || n >= maxSegment {
					t.Errorf("block %d: %d compressed bytes, want about %d, under %d", i, n, segmentBlockBytes, maxSegment)
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := built(t, c.build)
			n := int64(c.size)
			if !raceEnabled { // the decode is 214 MB in all, 57 s under -race
				var err error
				n, err = io.Copy(io.Discard, bzip2.NewReader(bytes.NewReader(in)))
				if err != nil || n != int64(c.size) {
					t.Fatalf("compress/bzip2: %d bytes, error %v; want %d", n, err, c.size)
				}
			}
			if len(in) >= 15_000_000 {
				t.Errorf("%d bytes, want under 15 MB", len(in))
			}
			starts, ends, sizes, err := Blocks(in)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%d compressed bytes, %d blocks of %d to %d bytes, plaintext %d bytes",
				len(in), len(sizes), slices.Min(sizes), slices.Max(sizes), n)
			c.check(t, in, starts, ends, sizes)
		})
	}
}

// runBlocks checks that every block is at most SpecCap and, but for the last,
// at least min bytes.
func runBlocks(t *testing.T, sizes []int, min int) {
	t.Helper()
	for i, n := range sizes {
		if n > SpecCap || (n < min && i < len(sizes)-1) {
			t.Errorf("block %d of %d: %d bytes, want %d to %d", i, len(sizes), n, min, SpecCap)
		}
	}
}

// TestRepeated pads level1.bz2's one block to 64 KiB, three times: it decodes
// through compress/bzip2 to three copies of level1's plaintext, each copy is
// 64 KiB, or one byte less, and each has its block where level1 has it.
func TestRepeated(t *testing.T) {
	stream, err := os.ReadFile("../../testdata/bzip2/level1.bz2")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(stream)))
	if err != nil {
		t.Fatal(err)
	}
	const unit = 64 << 10
	in, err := Repeated(stream, unit, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(in)))
	if err != nil || !bytes.Equal(got, bytes.Repeat(plain, 3)) {
		t.Fatalf("%d bytes, error %v; want 3 copies of level1's %d", len(got), err, len(plain))
	}
	if n := len(in) / 3; len(in)%3 != 0 || n < unit-1 || n > unit {
		t.Errorf("%d bytes, want 3 copies of %d or %d", len(in), unit-1, unit)
	}
	starts, _, _, err := Blocks(in)
	if err != nil || len(starts) != 3 {
		t.Fatalf("blocks at %v, error %v; want 3", starts, err)
	}
	for i, s := range starts {
		if want := uint64(i*len(in)/3*8 + 32); s != want {
			t.Errorf("block %d at bit %d, want %d", i, s, want)
		}
	}
	if _, err := Repeated(append(bytes.Clone(stream), stream...), unit, 1); err == nil {
		t.Error("two streams: no error")
	}
}

// TestInsertPad compares insertPad with a bit-by-bit insertion, at every bit
// of a byte.
func TestInsertPad(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	in := make([]byte, 64)
	for i := range in {
		in[i] = byte(rng.IntN(256))
	}
	for at := uint64(16); at < 24; at++ {
		for _, pad := range []int{2, 4, 10} {
			var want bitWriter
			for p := uint64(0); p < at; p++ {
				want.write(in[p/8]>>(7-p%8)&1, 1)
			}
			for i := 0; i < pad; i++ {
				want.write(0xBB, 8)
			}
			for p := at; p < uint64(len(in))*8; p++ {
				want.write(in[p/8]>>(7-p%8)&1, 1)
			}
			if got := insertPad(in, at, pad); !bytes.Equal(got, want.b) {
				t.Errorf("at bit %d, %d bytes: got %x, want %x", at, pad, got, want.b)
			}
		}
	}
}

// bitWriter appends bits, most significant first.
type bitWriter struct {
	b    []byte
	acc  uint
	nacc uint
}

func (w *bitWriter) write(v byte, n uint) {
	w.acc = w.acc<<n | uint(v)&(1<<n-1)
	w.nacc += n
	for w.nacc >= 8 {
		w.nacc -= 8
		w.b = append(w.b, byte(w.acc>>w.nacc))
	}
}

// TestCrafted checks the sizes of the inputs that only look like bzip2.
func TestCrafted(t *testing.T) {
	if n := len(EndMagicFlood(4)); n != 4+(8+2*4+1)<<20/6*6 {
		t.Errorf("EndMagicFlood(4): %d bytes", n)
	}
	if n := len(SparseBlockMagics()); n != 32<<20 {
		t.Errorf("SparseBlockMagics: %d bytes", n)
	}
}
