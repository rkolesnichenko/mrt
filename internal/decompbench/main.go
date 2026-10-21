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
//
//	go run ./internal/decompbench -blocks FILE...
//
// With -blocks it times nothing: it walks each file's streams and blocks with
// internal/bzip2 and prints the block count, the largest and the mean decoded
// block size in bytes, and last a line "cap: N", the smallest power of two
// that is at least twice the largest block over all files and at least 2 MiB
// (the v0.3.1 design §5.1 sizes the parallel reader's speculative cap from
// it). It exits 1 if a file fails to read.
//
//	go run ./internal/decompbench -mem [-crafted] -workers N FILE...
//
// With -mem it reads each file once with DecompressWith into io.Discard while
// a goroutine samples the runtime/metrics value /memory/classes/heap/objects:bytes
// every 2 ms. It prints the heap before the read (after a GC) as "baseline", the
// highest sample as "peak" and the growth over the baseline, in bytes. With
// -crafted it adds two inputs built in memory, the same as decompress_with_test.go
// builds them: "end-magic-flood" (BZh9 and then end magics, past the look-ahead
// of the given workers) and "sparse-block-magics" (32 MiB, a block magic and
// 200 KiB of zeros repeated). A read error on a crafted input is reported, not
// a failure. It exits 1 if a FILE fails to open. The peak is the largest of the
// samples, so a short peak between two samples can be missed. With the default
// GC the figure includes garbage not yet collected; the v0.3.1 gate runs it
// under GOGC=10, as an upper-bound estimate of live bytes
// (.data/v031-gate-mem.log).
package main

import (
	"bufio"
	"bytes"
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
	"runtime/metrics"
	"sync"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/internal/bzip2"
)

func main() {
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "DecompressWith's Workers")
	blocks := flag.Bool("blocks", false, "print the decoded block sizes of each file, and the speculative cap")
	memMode := flag.Bool("mem", false, "sample the heap while reading each file once with DecompressWith")
	crafted := flag.Bool("crafted", false, "with -mem: add the end-magic flood and sparse block magics")
	flag.Parse()
	if flag.NArg() == 0 || *workers < 1 {
		fmt.Fprintln(os.Stderr, "usage: decompbench -workers N FILE... | decompbench -blocks FILE... | decompbench -mem [-crafted] -workers N FILE...")
		os.Exit(2)
	}
	if *memMode {
		os.Exit(memRun(flag.Args(), *workers, *crafted))
	}
	if *blocks {
		os.Exit(blockSizes(flag.Args()))
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

// blockSizes prints each file's block count and largest and mean block size,
// and the cap for all of them (see the package comment). It returns the exit
// status.
func blockSizes(paths []string) int {
	status, largest := 0, 0
	for _, p := range paths {
		sizes, err := walkBlocks(p)
		if err != nil {
			fmt.Printf("%s: FAIL: %v\n", filepath.Base(p), err)
			status = 1
			continue
		}
		maxSize, sum := 0, 0
		for _, n := range sizes {
			maxSize = max(maxSize, n)
			sum += n
		}
		fmt.Printf("%s blocks %d largest %d mean %d\n", filepath.Base(p), len(sizes), maxSize, sum/max(len(sizes), 1))
		largest = max(largest, maxSize)
	}
	capBytes := 2 << 20
	for capBytes < 2*largest {
		capBytes <<= 1
	}
	fmt.Printf("cap: %d\n", capBytes)
	return status
}

// walkBlocks returns the decoded size of each block of the file at p, across
// all its streams.
func walkBlocks(p string) ([]int, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var d bzip2.Decoder
	br := bzip2.NewBitReader(bufio.NewReaderSize(f, 1<<20))
	level, err := d.Header(br)
	if err != nil {
		return nil, err
	}
	var sizes []int
	for level != 0 {
		var crc uint32
		for br.PeekMagic() == bzip2.BlockMagic {
			data, stored, err := d.Block(br, level)
			if err != nil {
				return nil, fmt.Errorf("block %d: %v", len(sizes), err)
			}
			sizes = append(sizes, len(data))
			crc = bzip2.FoldCRC(crc, stored)
		}
		if level, err = d.StreamEnd(br, crc); err != nil && err != io.EOF {
			return nil, err
		}
	}
	return sizes, nil
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

// memRun is the -mem mode (see the package comment). It returns the exit
// status.
func memRun(paths []string, workers int, crafted bool) int {
	fmt.Printf("go %s GOMAXPROCS %d NumCPU %d GOMEMLIMIT %d workers %d\n",
		runtime.Version(), runtime.GOMAXPROCS(0), runtime.NumCPU(), debug.SetMemoryLimit(-1), workers)
	status := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			fmt.Printf("%s: FAIL: %v\n", filepath.Base(p), err)
			status = 1
			continue
		}
		memOne(filepath.Base(p), f, workers)
		f.Close()
	}
	if crafted {
		memOne("end-magic-flood", bytes.NewReader(endMagicFlood(workers)), workers)
		memOne("sparse-block-magics", bytes.NewReader(sparseBlockMagics()), workers)
	}
	return status
}

// heapSample reads /memory/classes/heap/objects:bytes.
func heapSample(s []metrics.Sample) uint64 {
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// memOne reads in once with DecompressWith into io.Discard, sampling the heap
// every 2 ms, and prints the baseline and the peak.
func memOne(name string, in io.Reader, workers int) {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	runtime.GC()
	base := heapSample(s)
	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	go func() {
		defer close(done)
		sm := []metrics.Sample{{Name: s[0].Name}}
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for {
			v := heapSample(sm)
			mu.Lock()
			peak = max(peak, v)
			mu.Unlock()
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	var n int64
	r, err := mrt.DecompressWith(in, mrt.DecompressOptions{Workers: workers, Context: ctx})
	if err == nil {
		n, err = io.Copy(io.Discard, r)
	}
	cancel()
	took := time.Since(start)
	last := heapSample(s)
	mu.Lock()
	peak = max(peak, last)
	mu.Unlock()
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	res := "ok"
	if err != nil {
		res = "error: " + err.Error()
	}
	fmt.Printf("%s workers %d out %d took %.2fs baseline %d peak %d growth %d (%.1f MB) result %s\n",
		name, workers, n, took.Seconds(), base, peak, int64(peak)-int64(base), float64(int64(peak)-int64(base))/1e6, res)
}

// endMagicFlood is "BZh9" and then end magics, past the look-ahead of the
// given workers (the same as decompress_with_test.go's input).
func endMagicFlood(workers int) []byte {
	end := []byte{0x17, 0x72, 0x45, 0x38, 0x50, 0x90} // the end magic's 48 bits
	return append([]byte("BZh9"), bytes.Repeat(end, (8+2*workers+1)<<20/6)...)
}

// sparseBlockMagics is "BZh9" and then, to 32 MiB, a block magic followed by
// 200 KiB of zeros, repeated (the same as decompress_with_test.go's input).
func sparseBlockMagics() []byte {
	magic := []byte{0x31, 0x41, 0x59, 0x26, 0x53, 0x59} // the block magic's 48 bits
	unit := append(magic, make([]byte, 200<<10)...)
	in := []byte("BZh9")
	for len(in) < 32<<20 {
		in = append(in, unit...)
	}
	return in[:32<<20]
}
