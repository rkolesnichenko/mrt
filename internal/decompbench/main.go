// Command decompbench is a measurement tool, not a public API: the v0.3 gate
// (design §7). For each file it times Decompress and DecompressWith, first
// decompressing alone and then with Routes over the output, and checks that
// the two outputs are identical:
//
//	go run ./internal/decompbench -workers N FILE...
//
// It first prints its environment (Go version, GOMAXPROCS, NumCPU, memory
// limit). For each file it prints the decompress-only time and MB/s of both
// readers (MB of output, 10^6 bytes) and the speed-up, the SHA-256 of both
// outputs (hashed in passes of their own, not timed) and whether they are
// identical, and the Decompress+Routes and DecompressWith+Routes times and
// that speed-up. It exits 1 if a file fails to read, the outputs differ, or
// the routes differ in count.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/rkolesnichenko/mrt"
)

func main() {
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "DecompressWith's Workers")
	flag.Parse()
	if flag.NArg() == 0 || *workers < 1 {
		fmt.Fprintln(os.Stderr, "usage: decompbench -workers N FILE...")
		os.Exit(2)
	}
	fmt.Printf("go %s GOMAXPROCS %d NumCPU %d GOMEMLIMIT %d workers %d\n",
		runtime.Version(), runtime.GOMAXPROCS(0), runtime.NumCPU(), debug.SetMemoryLimit(-1), *workers)
	ok := true
	for _, p := range flag.Args() {
		if err := bench(p, *workers); err != nil {
			fmt.Printf("%s: FAIL: %v\n", filepath.Base(p), err)
			ok = false
		}
	}
	if !ok {
		os.Exit(1)
	}
}

// open returns a reader of the file at p: Decompress (workers 0) or
// DecompressWith, and a func that closes the file and cancels the Context.
func open(p string, workers int) (io.Reader, func(), error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := func() { cancel(); f.Close() }
	var r io.Reader
	if workers == 0 {
		r, err = mrt.Decompress(f)
	} else {
		r, err = mrt.DecompressWith(f, mrt.DecompressOptions{Workers: workers, Context: ctx})
	}
	if err != nil {
		done()
		return nil, nil, err
	}
	return r, done, nil
}

// copyTo reads the decompressed file into w, and returns the bytes and the
// time it took.
func copyTo(w io.Writer, p string, workers int) (int64, time.Duration, error) {
	start := time.Now()
	r, done, err := open(p, workers)
	if err != nil {
		return 0, 0, err
	}
	defer done()
	n, err := io.Copy(w, r)
	return n, time.Since(start), err
}

// routes reads the decompressed file through Routes, and returns the routes,
// the errors and the time it took.
func routes(p string, workers int) (n, errs int, d time.Duration, err error) {
	start := time.Now()
	r, done, err := open(p, workers)
	if err != nil {
		return 0, 0, 0, err
	}
	defer done()
	for _, err := range mrt.Routes(mrt.Records(r)) {
		if err != nil {
			errs++
			continue
		}
		n++
	}
	return n, errs, time.Since(start), nil
}

func bench(p string, workers int) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	// Read the file once, so that both readers find it in the page cache.
	if _, _, err := copyTo(io.Discard, p, 0); err != nil {
		return err
	}
	serialN, serialD, err := copyTo(io.Discard, p, 0)
	if err != nil {
		return fmt.Errorf("reading with Decompress: %v", err)
	}
	parN, parD, err := copyTo(io.Discard, p, workers)
	if err != nil {
		return fmt.Errorf("reading with DecompressWith: %v", err)
	}
	fmt.Printf("%s compressed %d out %d\n", st.Name(), st.Size(), serialN)
	fmt.Printf("  decompress only: Decompress %.2fs (%.1f MB/s) | DecompressWith %.2fs (%.1f MB/s) | speed-up %.2fx\n",
		serialD.Seconds(), mbps(serialN, serialD), parD.Seconds(), mbps(parN, parD), serialD.Seconds()/parD.Seconds())

	sums := [2]string{}
	for i, w := range []int{0, workers} {
		h := sha256.New()
		if _, _, err := copyTo(h, p, w); err != nil {
			return fmt.Errorf("hashing (workers %d): %v", w, err)
		}
		sums[i] = hex.EncodeToString(h.Sum(nil))
	}
	same := sums[0] == sums[1] && serialN == parN
	fmt.Printf("  sha256: Decompress %s | DecompressWith %s | identical %v\n", sums[0], sums[1], same)

	sr, se, sd, err := routes(p, 0)
	if err != nil {
		return err
	}
	pr, pe, pd, err := routes(p, workers)
	if err != nil {
		return err
	}
	fmt.Printf("  with Routes: Decompress+Routes %.2fs (%d routes, %d errors) | DecompressWith+Routes %.2fs (%d routes, %d errors) | speed-up %.2fx\n",
		sd.Seconds(), sr, se, pd.Seconds(), pr, pe, sd.Seconds()/pd.Seconds())
	switch {
	case !same:
		return fmt.Errorf("outputs differ")
	case sr != pr || se != pe:
		return fmt.Errorf("routes differ")
	}
	return nil
}

func mbps(n int64, d time.Duration) float64 { return float64(n) / 1e6 / d.Seconds() }
