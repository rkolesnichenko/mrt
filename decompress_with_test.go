package mrt_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/internal/bzip2"
)

const benchBz2 = "testdata/bench/rv2-updates-et.20261015.1200.bz2"

// readOut reads r to its end or its first error, with io.ReadAll, through
// iotest.OneByteReader when oneByte is set.
func readOut(r io.Reader, oneByte bool) ([]byte, error) {
	if oneByte {
		r = iotest.OneByteReader(r)
	}
	return io.ReadAll(r)
}

// decompressed reads in through Decompress (workers 0) or DecompressWith.
func decompressed(in io.Reader, workers int, oneByte bool) ([]byte, error) {
	if workers == 0 {
		r, err := mrt.Decompress(in)
		if err != nil {
			return nil, err
		}
		return readOut(r, oneByte)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := mrt.DecompressWith(in, mrt.DecompressOptions{Workers: workers, Context: ctx})
	if err != nil {
		return nil, err
	}
	return readOut(r, oneByte)
}

// checkSame checks spec §6.4 for one read: the bytes and the failure of
// DecompressWith (got) against those of Decompress (want). Where Decompress
// fails with io.ErrUnexpectedEOF the bytes it returns with the error are
// decoded from bits past the input's end and their count depends on the
// caller's Read sizes (ledger ruling 9): both must fail, and agree on their
// common prefix. Everywhere else the bytes are equal.
func checkSame(t testing.TB, what string, got []byte, gotErr error, want []byte, wantErr error) {
	t.Helper()
	if errors.Is(wantErr, io.ErrUnexpectedEOF) {
		n := min(len(got), len(want))
		if gotErr == nil || !bytes.Equal(got[:n], want[:n]) {
			t.Errorf("%s: %d bytes, error %v; Decompress: %d bytes, error %v (common prefix equal: %v)",
				what, len(got), gotErr, len(want), wantErr, bytes.Equal(got[:n], want[:n]))
		}
		return
	}
	if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
		t.Errorf("%s: %d bytes, error %v; Decompress: %d bytes, error %v (bytes equal: %v)",
			what, len(got), gotErr, len(want), wantErr, bytes.Equal(got, want))
	}
}

// matchesDecompress checks §6.4 on in at workers 1, 2 and 8, reading with
// io.ReadAll and, when oneByte is set, through iotest.OneByteReader.
func matchesDecompress(t testing.TB, in []byte, oneByte bool) {
	t.Helper()
	modes := []bool{false}
	if oneByte {
		modes = append(modes, true)
	}
	for _, ob := range modes {
		want, wantErr := decompressed(bytes.NewReader(in), 0, ob)
		for _, w := range []int{1, 2, 8} {
			got, gotErr := decompressed(bytes.NewReader(in), w, ob)
			checkSame(t, fmt.Sprintf("workers %d, one byte per Read %v", w, ob), got, gotErr, want, wantErr)
		}
	}
}

// bzip2Inputs returns every testdata/bzip2 file and the bench file, by name.
func bzip2Inputs(t testing.TB) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob("testdata/bzip2/*.bz2")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no testdata/bzip2 files: %v", err)
	}
	out := make(map[string][]byte)
	for _, p := range append(paths, benchBz2) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = b
	}
	return out
}

// afterStreamEnd are bytes after a stream's end that compress/bzip2 checks, as
// in internal/bzip2's tests.
var afterStreamEnd = []string{"B", "BZ", "XY", "BX", "BZh", "BZx9", "BZh0", "BZh:", "BZh1", "BZh9", "BZh91AY&SY"}

// fixtureBytes returns every testdata/mrt and testdata/synthetic fixture,
// decompressed, by name.
func fixtureBytes(t testing.TB) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	for _, pat := range []string{"testdata/mrt/*", "testdata/synthetic/*.mrt"} {
		paths, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			r, err := mrt.Decompress(f)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(r)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			out[filepath.Base(p)] = b
		}
	}
	return out
}

// bzip2Command compresses b with the bzip2 command at the given level.
func bzip2Command(t testing.TB, level string, b []byte) []byte {
	t.Helper()
	cmd := exec.Command("bzip2", level, "-c")
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bzip2 %s: %v", level, err)
	}
	return out
}

// bitsAt returns the n bits of b at bit offset off.
func bitsAt(b []byte, off uint64, n int) uint64 {
	var v uint64
	for i := uint64(0); i < uint64(n); i++ {
		p := off + i
		v = v<<1 | uint64(b[p/8]>>(7-p%8)&1)
	}
	return v
}

// blockMagics returns the bit offsets of every block magic and end magic in
// b, found at every bit offset.
func blockMagics(b []byte) (blocks, ends []uint64) {
	const mask = 1<<48 - 1
	var v uint64
	for p := uint64(0); p < uint64(len(b))*8; p++ {
		v = (v<<1 | uint64(b[p/8]>>(7-p%8)&1)) & mask
		if p < 47 {
			continue
		}
		switch v {
		case bzip2.BlockMagic:
			blocks = append(blocks, p-47)
		case bzip2.EndMagic:
			ends = append(ends, p-47)
		}
	}
	return blocks, ends
}

// bitWriter appends bits, most significant first.
type bitWriter struct {
	b    []byte
	acc  uint64
	nacc uint
}

func (w *bitWriter) write(v uint64, n uint) {
	w.acc = w.acc<<n | v&(1<<n-1)
	w.nacc += n
	for w.nacc >= 8 {
		w.nacc -= 8
		w.b = append(w.b, byte(w.acc>>w.nacc))
	}
}

// benchPadBytes is how far paddedBench moves the bench file's blocks after
// the first: past 8 MiB, in whole 0xBBBB words.
const benchPadBytes = 8<<20 + 2

// paddedBench is the bench file with its first block padded past 8 MiB, as
// internal/bzip2's TestBlockSpanning8MiB builds it: no-op code-length deltas
// ("1 0" then "1 1") after the first Huffman table's base length. A
// speculative decode of that block runs out of its 8 MiB segment, so the
// chain decodes it itself (the chain-miss path).
var paddedBench = sync.OnceValues(func() ([]byte, error) {
	in, err := os.ReadFile(benchBz2)
	if err != nil {
		return nil, err
	}
	br := bzip2.NewBitReader(bytes.NewReader(in))
	br.ReadBits64(32)
	if br.ReadBits64(48) != bzip2.BlockMagic {
		return nil, errors.New("no block magic at bit 32")
	}
	br.ReadBits64(32 + 1 + 24) // CRC, randomized, origPtr
	used := br.ReadBits(16)
	for i := 0; i < 16; i++ {
		if used&(1<<(15-i)) != 0 {
			br.ReadBits(16)
		}
	}
	br.ReadBits(3) // number of trees
	for n := br.ReadBits(15); n > 0; n-- {
		for br.ReadBit() {
		}
	}
	if base := br.ReadBits(5); base < 1 || base > 19 {
		return nil, fmt.Errorf("first table's base length %d; the pairs need 1 to 19", base)
	}
	at := br.Offset()
	const padBytes = benchPadBytes
	w := &bitWriter{b: make([]byte, 0, len(in)+padBytes+8)}
	for p := uint64(0); p < at; p++ {
		w.write(bitsAt(in, p, 1), 1)
	}
	for i := 0; i < padBytes/2; i++ {
		w.write(0xBBBB, 16) // four +1 -1 pairs
	}
	p := at
	for ; p%8 != 0; p++ {
		w.write(bitsAt(in, p, 1), 1)
	}
	return append(w.b, in[p/8:]...), nil
})

// TestDecompressWithMatchesDecompress is spec §6.4 on bzip2: the testdata
// files and the bench file, whole, cut, with flipped bits and with bytes
// after their end, as internal/bzip2's tests read them; every fixture
// compressed with bzip2 -1 and -9; and the padded >8 MiB block. Small inputs
// and every whole input are also read one byte per Read; the cuts and flips
// of the bench file only with io.ReadAll.
//
// Under -race, which makes decoding about ten times slower, the bench file is
// cut and flipped at 10 offsets each, not 50, with no bytes after its end
// (the small files have them all), the fixtures are read only with
// io.ReadAll, and the padded block is left to TestDecompressWithSpeculates.
func TestDecompressWithMatchesDecompress(t *testing.T) {
	t.Parallel()
	full := !mrt.RaceEnabled
	type input struct {
		in      []byte
		oneByte bool
	}
	cases := map[string]input{}
	for name, in := range bzip2Inputs(t) {
		small := len(in) <= 4096
		cuts, flips := 50, 50
		switch {
		case small:
			cuts = len(in)
		case !full:
			cuts, flips = 10, 10
		}
		cases[name+"/whole"] = input{in, true}
		for i := 0; i < cuts; i++ {
			cut := i * len(in) / cuts
			cases[fmt.Sprintf("%s/cut-%d", name, cut)] = input{in[:cut], small}
		}
		for i := 0; i < flips; i++ {
			bit := i * len(in) * 8 / flips
			flipped := bytes.Clone(in)
			flipped[bit/8] ^= 0x80 >> (bit % 8)
			cases[fmt.Sprintf("%s/flip-bit-%d", name, bit)] = input{flipped, small}
		}
		if small || full {
			for _, tail := range afterStreamEnd {
				cases[fmt.Sprintf("%s/then-%q", name, tail)] = input{append(bytes.Clone(in), tail...), small}
			}
		}
	}
	if full {
		padded, err := paddedBench()
		if err != nil {
			t.Fatal(err)
		}
		cases["bench-padded-past-8MiB/whole"] = input{padded, false}
	}
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Logf("bzip2 not on PATH: fixtures not compressed (%v)", err)
	} else {
		for name, b := range fixtureBytes(t) {
			for _, level := range []string{"-1", "-9"} {
				cases[fmt.Sprintf("fixture/%s/bzip2%s", name, level)] = input{bzip2Command(t, level, b), full}
			}
		}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			matchesDecompress(t, c.in, c.oneByte)
		})
	}
}

// failAfter returns the bytes of r and then err, once n bytes are read.
type failAfter struct {
	r   io.Reader
	n   int
	err error
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, f.err
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

// TestDecompressWithInputReaders reads the bench file through input readers
// that behave oddly: one byte per Read, data returned with io.EOF, and an
// error 60% of the way in. DecompressWith gives the bytes and the failure
// Decompress gives on the same reader.
func TestDecompressWithInputReaders(t *testing.T) {
	t.Parallel()
	in, err := os.ReadFile(benchBz2)
	if err != nil {
		t.Fatal(err)
	}
	errInput := errors.New("input failed")
	readers := map[string]func() io.Reader{
		"OneByteReader": func() io.Reader { return iotest.OneByteReader(bytes.NewReader(in)) },
		"DataErrReader": func() io.Reader { return iotest.DataErrReader(bytes.NewReader(in)) },
		"fails-at-60%": func() io.Reader {
			return &failAfter{r: bytes.NewReader(in), n: len(in) * 6 / 10, err: errInput}
		},
	}
	for name, mk := range readers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want, wantErr := decompressed(mk(), 0, false)
			for _, w := range []int{1, 2, 8} {
				got, gotErr := decompressed(mk(), w, false)
				checkSame(t, fmt.Sprintf("workers %d", w), got, gotErr, want, wantErr)
				if errors.Is(gotErr, errInput) != errors.Is(wantErr, errInput) {
					t.Errorf("workers %d: error %v; Decompress: %v", w, gotErr, wantErr)
				}
			}
		})
	}
}

// chainLog records what the test hooks report: the candidates discarded and
// the blocks the chain took, speculatively or not.
type chainLog struct {
	mu          sync.Mutex
	discarded   []uint64
	speculative []uint64
	serial      []uint64
}

func (l *chainLog) discard(bit uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.discarded = append(l.discarded, bit)
}

func (l *chainLog) took(bit uint64, speculative bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if speculative {
		l.speculative = append(l.speculative, bit)
	} else {
		l.serial = append(l.serial, bit)
	}
}

// readWithHooks reads in through DecompressWith with the test hooks, adding
// the candidates in extra (sorted).
func readWithHooks(t *testing.T, in []byte, workers int, extra []uint64) ([]byte, *chainLog) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &chainLog{}
	add := func(lo, hi uint64) []uint64 {
		i, _ := slices.BinarySearch(extra, lo)
		j, _ := slices.BinarySearch(extra, hi)
		return extra[i:j]
	}
	r, err := mrt.DecompressWithHooks(bytes.NewReader(in), mrt.DecompressOptions{Workers: workers, Context: ctx},
		add, log.discard, log.took)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out, log
}

// TestDecompressWithSpeculates checks that the chain takes the workers'
// results: every block of the bench file is taken speculatively, and the
// padded file's first block, whose end is past its 8 MiB segment, is decoded
// by the chain itself (the chain-miss path), the others speculatively. The
// padding changes no byte of the output (internal/bzip2's
// TestBlockSpanning8MiB; TestDecompressWithMatchesDecompress compares the
// padded file with Decompress too, without -race).
func TestDecompressWithSpeculates(t *testing.T) {
	t.Parallel()
	in, err := os.ReadFile(benchBz2)
	if err != nil {
		t.Fatal(err)
	}
	want, err := decompressed(bytes.NewReader(in), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _ := blockMagics(in)
	padded, err := paddedBench()
	if err != nil {
		t.Fatal(err)
	}
	var paddedBlocks []uint64
	for _, b := range blocks[1:] {
		paddedBlocks = append(paddedBlocks, b+8*benchPadBytes)
	}
	for _, c := range []struct {
		name                string
		in                  []byte
		speculative, serial []uint64
	}{
		{"bench", in, blocks, nil},
		{"padded", padded, paddedBlocks, blocks[:1]},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, log := readWithHooks(t, c.in, 2, nil)
			if !bytes.Equal(got, want) {
				t.Fatalf("%d bytes, not the %d Decompress gives", len(got), len(want))
			}
			if !slices.Equal(log.speculative, c.speculative) || !slices.Equal(log.serial, c.serial) {
				t.Errorf("speculative blocks at %v, serial at %v; want speculative %v, serial %v",
					log.speculative, log.serial, c.speculative, c.serial)
			}
		})
	}
}

// TestDecompressWithFalseCandidates adds 100 false candidates per block
// through the test hook. The output is unchanged, every false candidate is
// discarded, and the chain takes only the true blocks.
func TestDecompressWithFalseCandidates(t *testing.T) {
	t.Parallel()
	all := bzip2Inputs(t)
	for _, name := range []string{"level1.bz2", filepath.Base(benchBz2)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := all[name]
			want, err := decompressed(bytes.NewReader(in), 0, false)
			if err != nil {
				t.Fatal(err)
			}
			blocks, ends := blockMagics(in)
			if len(ends) != 1 {
				t.Fatalf("%d end magics, want 1", len(ends))
			}
			bounds := append(slices.Clone(blocks), ends[0])
			var extra []uint64
			for i, b := range blocks {
				span := bounds[i+1] - b
				for k := uint64(1); k <= 100; k++ {
					extra = append(extra, b+k*span/101)
				}
			}
			for _, w := range []int{1, 2, 8} {
				got, log := readWithHooks(t, in, w, extra)
				if !bytes.Equal(got, want) {
					t.Errorf("workers %d: %d bytes, not the %d Decompress gives", w, len(got), len(want))
				}
				discarded := slices.Clone(log.discarded)
				slices.Sort(discarded)
				if !slices.Equal(discarded, extra) {
					t.Errorf("workers %d: %d candidates discarded, want the %d added; not discarded: %v",
						w, len(discarded), len(extra), notIn(extra, discarded))
				}
				took := append(slices.Clone(log.speculative), log.serial...)
				slices.Sort(took)
				if !slices.Equal(took, blocks) {
					t.Errorf("workers %d: chain took blocks at %v, want %v", w, took, blocks)
				}
			}
		})
	}
}

// goroutineBaseline is the goroutine count once the runtime has settled
// (ledger ruling 3: the tests that use it are not parallel).
func goroutineBaseline() int {
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	return runtime.NumGoroutine()
}

// waitGoroutines polls every 10 ms, for up to 2 s, until the goroutine count
// is back to base, and returns the last count.
func waitGoroutines(base int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 200 && n != base; i++ {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// Not parallel: it counts goroutines.
func TestDecompressWithOptions(t *testing.T) {
	bz, err := os.ReadFile("testdata/bzip2/level1.bz2")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/synthetic/state-changes.mrt")
	if err != nil {
		t.Fatal(err)
	}
	var gzbuf bytes.Buffer
	zw := gzip.NewWriter(&gzbuf)
	zw.Write(raw)
	zw.Close()

	ctx := context.Background()
	for name, in := range map[string][]byte{"bzip2": bz, "raw": raw} {
		for _, opts := range []mrt.DecompressOptions{{Workers: 1}, {Workers: -1}, {Workers: -1, Context: ctx}} {
			if r, err := mrt.DecompressWith(bytes.NewReader(in), opts); err == nil || r != nil {
				t.Errorf("%s, Workers %d, Context %v: reader %v, error %v; want an error", name, opts.Workers, opts.Context, r, err)
			}
		}
	}

	// Workers 0 is Decompress: the same bytes and the same failures.
	inputs := map[string][]byte{
		"bzip2": bz, "raw": raw, "gzip": gzbuf.Bytes(), "bad-gzip-header": []byte("\x1f\x8b\x00"),
		"short": []byte("BZ"), "empty": nil, "bzip2-cut": bz[:100],
	}
	for name, in := range inputs {
		for _, opts := range []mrt.DecompressOptions{{}, {Context: ctx}} {
			want, wantErr := decompressed(bytes.NewReader(in), 0, false)
			r, err := mrt.DecompressWith(bytes.NewReader(in), opts)
			var got []byte
			if err == nil {
				got, err = io.ReadAll(r)
			}
			if !bytes.Equal(got, want) || (err == nil) != (wantErr == nil) {
				t.Errorf("%s, Workers 0: %d bytes, error %v; Decompress: %d bytes, error %v", name, len(got), err, len(want), wantErr)
			}
		}
	}

	// Uncompressed input with Workers > 0 starts no goroutine.
	base := goroutineBaseline()
	r, err := mrt.DecompressWith(bytes.NewReader(raw), mrt.DecompressOptions{Workers: 4, Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("raw with Workers 4: %d bytes, error %v", len(got), err)
	}
	if n := runtime.NumGoroutine(); n != base {
		t.Errorf("raw with Workers 4: %d goroutines, %d before", n, base)
	}
}

// TestDecompressWithStopsEarly reads 10 records, stops and cancels: Read
// then returns context.Canceled, and every goroutine ends. Not parallel: it
// counts goroutines.
func TestDecompressWithStopsEarly(t *testing.T) {
	in, err := os.ReadFile(benchBz2)
	if err != nil {
		t.Fatal(err)
	}
	base := goroutineBaseline()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := mrt.DecompressWith(bytes.NewReader(in), mrt.DecompressOptions{Workers: 4, Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, err := range mrt.Records(r) {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == 10 {
			break
		}
	}
	if n != 10 {
		t.Fatalf("%d records, want 10", n)
	}
	if g := runtime.NumGoroutine(); g <= base {
		t.Errorf("%d goroutines while reading, %d before: no worker running", g, base)
	}
	cancel()
	if k, err := r.Read(make([]byte, 100)); k != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("Read after cancel: %d bytes, error %v; want context.Canceled", k, err)
	}
	if g := waitGoroutines(base); g != base {
		t.Errorf("%d goroutines 2 s after the cancel, %d before", g, base)
	}
}

// TestDecompressWithNoGoroutineAfterEOF: the Read that returns EOF or an
// error has waited for every worker, so no goroutine is left, with no wait.
// Not parallel: it counts goroutines.
func TestDecompressWithNoGoroutineAfterEOF(t *testing.T) {
	in, err := os.ReadFile(benchBz2)
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]byte{"whole": in, "cut": in[:len(in)/2]} {
		base := goroutineBaseline()
		ctx, cancel := context.WithCancel(context.Background())
		r, err := mrt.DecompressWith(bytes.NewReader(in), mrt.DecompressOptions{Workers: 4, Context: ctx})
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(r)
		n := runtime.NumGoroutine()
		cancel()
		if (err != nil) != (name == "cut") {
			t.Errorf("%s: error %v", name, err)
		}
		if n != base {
			t.Errorf("%s: %d goroutines right after the read returned, %d before", name, n, base)
		}
	}
}

// TestDecompressWithPipe feeds the bench file through an io.Pipe, 512 bytes
// per write with a 1 ms pause: the output is unchanged, and no goroutine is
// left blocked in a read of the pipe. Not parallel: it counts goroutines.
func TestDecompressWithPipe(t *testing.T) {
	in, err := os.ReadFile(benchBz2)
	if err != nil {
		t.Fatal(err)
	}
	want, err := decompressed(bytes.NewReader(in), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	base := goroutineBaseline()
	pr, pw := io.Pipe()
	wrote := make(chan error, 1)
	go func() {
		for b := in; len(b) > 0; {
			n := min(512, len(b))
			if _, err := pw.Write(b[:n]); err != nil {
				wrote <- err
				return
			}
			b = b[n:]
			time.Sleep(time.Millisecond)
		}
		wrote <- pw.Close()
	}()
	got, err := decompressed(pr, 4, false)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%d bytes, error %v; want the %d bytes Decompress gives", len(got), err, len(want))
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if g := waitGoroutines(base); g != base {
		t.Errorf("%d goroutines after the read, %d before", g, base)
	}
	buf := make([]byte, 1<<20)
	if stacks := string(buf[:runtime.Stack(buf, true)]); strings.Contains(stacks, "io.(*pipe).read") {
		t.Errorf("a goroutine is still reading the pipe:\n%s", stacks)
	}
}

// notIn returns the elements of a that are not in b.
func notIn(a, b []uint64) (out []uint64) {
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}
