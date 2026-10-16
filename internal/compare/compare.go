// Command compare measures what reading one MRT file costs with gobgp's
// pkg/packet/mrt and with mrt (spec §7.6), in one process so that every pass
// runs under the same conditions:
//
//	go run . FILE
//
// The file is read into memory, then:
//
//  1. decompress only: mrt.Decompress copied to io.Discard (the bzip2 or
//     gzip cost, and the decompressed size);
//  2. the decompressed bytes are held in memory (not measured);
//  3. gobgp: the loop of gobgp's own `gobgp mrt inject` (cmd/gobgp/mrt.go):
//     a 12-byte header buffer, mrt.ParseHeader, a body buffer of the
//     header's length, mrt.ParseBody, which decodes every RIB entry and path
//     attribute;
//  4. mrt Records: mrt.Records alone (record framing and fixed fields);
//  5. mrt Routes: mrt.Routes over mrt.Records, every path decoded;
//  6. mrt Routes from the compressed file: 5 with 1's decompression in front.
//
// Passes 3-5 read the decompressed bytes from memory. Each pass reports its
// wall time and the runtime.MemStats TotalAlloc and Mallocs it added. The
// release gate (spec §7.6): pass 5 allocates less than pass 3. Exit status:
// 0 the gate holds, 1 it does not, 2 a bad command line or unreadable file.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	gobgp "github.com/osrg/gobgp/v4/pkg/packet/mrt"
	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: compare FILE")
		os.Exit(2)
	}
	os.Exit(run(os.Args[1], os.Stdout))
}

// result is one pass: what it allocated, how long it took, what it counted.
type result struct {
	name       string
	wall       time.Duration
	totalAlloc uint64
	mallocs    uint64
	counts     string
	err        error
}

// measure runs f after a garbage collection and returns the TotalAlloc and
// Mallocs it added and its wall time.
func measure(name string, f func() (string, error)) result {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	counts, err := f()
	wall := time.Since(start)
	runtime.ReadMemStats(&after)
	return result{name, wall, after.TotalAlloc - before.TotalAlloc, after.Mallocs - before.Mallocs, counts, err}
}

func run(path string, w io.Writer) int {
	compressed, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compare: %v\n", err)
		return 2
	}
	decompress := func() (io.Reader, error) { return mrt.Decompress(bytes.NewReader(compressed)) }

	var size int64
	decomp := measure("decompress only", func() (string, error) {
		r, err := decompress()
		if err != nil {
			return "", err
		}
		size, err = io.Copy(io.Discard, r)
		return fmt.Sprintf("%d bytes out", size), err
	})
	if decomp.err != nil {
		fmt.Fprintf(os.Stderr, "compare: %s: %v\n", path, decomp.err)
		return 2
	}
	r, _ := decompress()
	data, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compare: %s: %v\n", path, err)
		return 2
	}

	results := []result{
		decomp,
		measure("gobgp ParseHeader+ParseBody", func() (string, error) { return walkGobgp(bytes.NewReader(data)) }),
		measure("mrt Records", func() (string, error) { return walkRecords(bytes.NewReader(data)) }),
		measure("mrt Routes (decode)", func() (string, error) { return walkRoutes(bytes.NewReader(data)) }),
		measure("mrt Routes from the file", func() (string, error) {
			r, err := decompress()
			if err != nil {
				return "", err
			}
			return walkRoutes(r)
		}),
	}
	data = nil

	fmt.Fprintf(w, "file %s\ncompressed %d bytes, decompressed %d bytes; %s, GOMAXPROCS %d\n\n",
		path, len(compressed), size, runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Fprintf(w, "%-30s %12s %16s %12s  %s\n", "pass", "wall", "TotalAlloc", "Mallocs", "counted")
	for _, r := range results {
		counts := r.counts
		if r.err != nil {
			counts += " ERROR: " + r.err.Error()
		}
		fmt.Fprintf(w, "%-30s %12s %16d %12d  %s\n", r.name, r.wall.Round(time.Millisecond), r.totalAlloc, r.mallocs, counts)
	}
	gob, records, routes, full := results[1], results[2], results[3], results[4]
	fmt.Fprintf(w, "\nmrt Routes TotalAlloc / gobgp TotalAlloc: %.3f\n", float64(routes.totalAlloc)/float64(gob.totalAlloc))
	fmt.Fprintf(w, "mrt Records bytes allocated per decompressed byte: %.3f (aim <= 1.25)\n", float64(records.totalAlloc)/float64(size))
	fmt.Fprintf(w, "decompression share of the full Routes pass: %s / %s = %.1f%%\n",
		decomp.wall.Round(time.Millisecond), full.wall.Round(time.Millisecond), 100*decomp.wall.Seconds()/full.wall.Seconds())
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(w, "FAIL: %s: %v\n", r.name, r.err)
			return 1
		}
	}
	if routes.totalAlloc >= gob.totalAlloc {
		fmt.Fprintln(w, "release gate: FAIL (mrt Routes allocates no less than gobgp)")
		return 1
	}
	fmt.Fprintln(w, "release gate: PASS (mrt Routes allocates less than gobgp)")
	return 0
}

// walkGobgp is gobgp's own loop (cmd/gobgp/mrt.go, `gobgp mrt inject`).
func walkGobgp(r io.Reader) (string, error) {
	var records, ribs, entries, attrs, parseErrors int
	for {
		buf := make([]byte, gobgp.MRT_COMMON_HEADER_LEN)
		if _, err := io.ReadFull(r, buf); err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
		h, err := gobgp.ParseHeader(buf)
		if err != nil {
			return "", err
		}
		buf = make([]byte, h.Len)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		records++
		msg, err := gobgp.ParseBody(buf, h)
		if err != nil {
			parseErrors++
			continue
		}
		if rib, ok := msg.Body.(*gobgp.Rib); ok {
			ribs++
			entries += len(rib.Entries)
			for _, e := range rib.Entries {
				attrs += len(e.PathAttributes)
			}
		}
	}
	return fmt.Sprintf("%d records, %d RIBs, %d entries, %d attributes, %d parse errors", records, ribs, entries, attrs, parseErrors), nil
}

// walkRecords reads every record and nothing more.
func walkRecords(r io.Reader) (string, error) {
	var records, recordErrors int
	for _, err := range mrt.Records(r) {
		records++
		if err != nil {
			var re *mrt.RecordError
			if !errors.As(err, &re) {
				return "", err
			}
			recordErrors++
		}
	}
	return fmt.Sprintf("%d records, %d record errors", records, recordErrors), nil
}

// walkRoutes yields every route, its path decoded.
func walkRoutes(r io.Reader) (string, error) {
	var routes, withPath, paths, routeErrors, asns int
	var last *bgp.Path
	for rt, err := range mrt.Routes(mrt.Records(r)) {
		if err != nil {
			var re *mrt.RecordError
			if !errors.As(err, &re) {
				return "", err
			}
			routeErrors++
			continue
		}
		routes++
		if rt.Path != nil {
			withPath++
			if rt.Path != last { // one path per RIB entry or UPDATE
				paths++
				asns += rt.Path.ASPath.Len()
				last = rt.Path
			}
		}
	}
	return fmt.Sprintf("%d routes (%d with a path), %d paths, %d errors, AS path length sum %d", routes, withPath, paths, routeErrors, asns), nil
}
