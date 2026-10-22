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
// release gate (spec §7.6, Ruling R21): pass 5 met no error, yielded one
// route per RIB entry pass 3 decoded (so the file must be a RIB), and
// allocated less than pass 3. Exit status:
// 0 the gate holds, 1 it does not, 2 a bad command line or unreadable file.
//
// With -bmp it reads a BMP stream instead (spec §9.2, §9.6; bmp.go):
//
//	go run . -bmp FILE
//
// FILE is a .bmp file (one router's messages) or an .obmp file (OpenBMP
// frames, one session per router). The messages are framed once and held in
// memory; then, after a garbage collection each, mrt (bmp.Session.Routes over
// bmp.ParseMessage) and gobgp (ParseBMPMessageWithOptions, with the Peer Up
// state of gobgpSession) decode them, and the TotalAlloc and Mallocs each
// adds are printed per message. Then both sides' announcements and
// withdrawals are rendered as oracle.FromRoute lines and compared; the line
// counts and every mismatch are printed (divergences.md is not read). Exit
// status: 0 no mismatch, 1 a mismatch, 2 a bad command line or unreadable
// file.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gobgp "github.com/osrg/gobgp/v4/pkg/packet/mrt"
	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/internal/oracle"
)

func main() {
	switch {
	case len(os.Args) == 3 && os.Args[1] == "-bmp":
		os.Exit(runBMP(os.Args[2], os.Stdout))
	case len(os.Args) == 2 && os.Args[1] != "-bmp":
		os.Exit(run(os.Args[1], os.Stdout))
	}
	fmt.Fprintln(os.Stderr, "usage: compare FILE | compare -bmp FILE")
	os.Exit(2)
}

// walk is what one pass counted: a summary, the items the gate compares
// (gobgp's RIB entries, mrt's routes) and the errors met.
type walk struct {
	summary     string
	items, errs int
}

// result is one pass: what it allocated, how long it took, what it counted.
type result struct {
	walk
	name       string
	wall       time.Duration
	totalAlloc uint64
	mallocs    uint64
	err        error
}

// measure runs f after a garbage collection and returns the TotalAlloc and
// Mallocs it added and its wall time.
func measure(name string, f func() (walk, error)) result {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	wk, err := f()
	wall := time.Since(start)
	runtime.ReadMemStats(&after)
	return result{wk, name, wall, after.TotalAlloc - before.TotalAlloc, after.Mallocs - before.Mallocs, err}
}

// gate is the release gate (spec §7.6, Ruling R21): mrt's Routes walk met no
// error, yielded one route per RIB entry gobgp decoded, and allocated less.
func gate(gob, routes result) (pass bool, reason string) {
	switch {
	case routes.errs != 0:
		return false, fmt.Sprintf("mrt Routes yielded %d errors", routes.errs)
	case routes.items == 0 || routes.items != gob.items:
		return false, fmt.Sprintf("mrt Routes yielded %d routes, gobgp decoded %d RIB entries", routes.items, gob.items)
	case routes.totalAlloc >= gob.totalAlloc:
		return false, "mrt Routes allocates no less than gobgp"
	}
	return true, "mrt Routes allocates less than gobgp, one route per gobgp RIB entry, no errors"
}

func run(path string, w io.Writer) int {
	compressed, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compare: %v\n", err)
		return 2
	}
	decompress := func() (io.Reader, error) { return mrt.Decompress(bytes.NewReader(compressed)) }

	var size int64
	decomp := measure("decompress only", func() (walk, error) {
		r, err := decompress()
		if err != nil {
			return walk{}, err
		}
		size, err = io.Copy(io.Discard, r)
		return walk{summary: fmt.Sprintf("%d bytes out", size)}, err
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
		measure("gobgp ParseHeader+ParseBody", func() (walk, error) { return walkGobgp(bytes.NewReader(data)) }),
		measure("mrt Records", func() (walk, error) { return walkRecords(bytes.NewReader(data)) }),
		measure("mrt Routes (decode)", func() (walk, error) { return walkRoutes(bytes.NewReader(data)) }),
		measure("mrt Routes from the file", func() (walk, error) {
			r, err := decompress()
			if err != nil {
				return walk{}, err
			}
			return walkRoutes(r)
		}),
	}
	data = nil

	fmt.Fprintf(w, "file %s\ncompressed %d bytes, decompressed %d bytes; %s, GOMAXPROCS %d\n\n",
		path, len(compressed), size, runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Fprintf(w, "%-30s %12s %16s %12s  %s\n", "pass", "wall", "TotalAlloc", "Mallocs", "counted")
	for _, r := range results {
		counts := r.summary
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
	fmt.Fprintf(w, "gobgp RIB entries %d, mrt routes %d, mrt route errors %d\n", gob.items, routes.items, routes.errs)
	pass, reason := gate(gob, routes)
	if !pass {
		fmt.Fprintf(w, "release gate: FAIL (%s)\n", reason)
		return 1
	}
	fmt.Fprintf(w, "release gate: PASS (%s)\n", reason)
	return 0
}

// walkGobgp is gobgp's own loop (cmd/gobgp/mrt.go, `gobgp mrt inject`).
func walkGobgp(r io.Reader) (walk, error) {
	var records, ribs, entries, attrs, parseErrors int
	for {
		buf := make([]byte, gobgp.MRT_COMMON_HEADER_LEN)
		if _, err := io.ReadFull(r, buf); err == io.EOF {
			break
		} else if err != nil {
			return walk{}, err
		}
		h, err := gobgp.ParseHeader(buf)
		if err != nil {
			return walk{}, err
		}
		buf = make([]byte, h.Len)
		if _, err := io.ReadFull(r, buf); err != nil {
			return walk{}, err
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
	return walk{fmt.Sprintf("%d records, %d RIBs, %d entries, %d attributes, %d parse errors", records, ribs, entries, attrs, parseErrors),
		entries, parseErrors}, nil
}

// walkRecords reads every record and nothing more.
func walkRecords(r io.Reader) (walk, error) {
	var records, recordErrors int
	for _, err := range mrt.Records(r) {
		records++
		if err != nil {
			var re *mrt.RecordError
			if !errors.As(err, &re) {
				return walk{}, err
			}
			recordErrors++
		}
	}
	return walk{fmt.Sprintf("%d records, %d record errors", records, recordErrors), records, recordErrors}, nil
}

// walkRoutes yields every route, its path decoded.
func walkRoutes(r io.Reader) (walk, error) {
	var routes, withPath, paths, routeErrors, asns int
	var last *bgp.Path
	for rt, err := range mrt.Routes(mrt.Records(r)) {
		if err != nil {
			var re *mrt.RecordError
			if !errors.As(err, &re) {
				return walk{}, err
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
	return walk{fmt.Sprintf("%d routes (%d with a path), %d paths, %d errors, AS path length sum %d", routes, withPath, paths, routeErrors, asns),
		routes, routeErrors}, nil
}

// runBMP is the -bmp mode: it measures both decoders over the messages of
// path, then compares their routes line by line.
func runBMP(path string, w io.Writer) int {
	streams, err := readBMPStreams(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compare: %v\n", err)
		return 2
	}
	msgs := 0
	for _, s := range streams {
		msgs += len(s)
	}
	if msgs == 0 {
		fmt.Fprintf(os.Stderr, "compare: %s: no BMP message\n", path)
		return 2
	}
	pass := func(name string, f func([][][]byte) (int, int)) result {
		return measure(name, func() (walk, error) {
			routes, errs := f(streams)
			return walk{fmt.Sprintf("%d routes, %d errors", routes, errs), routes, errs}, nil
		})
	}
	results := []result{
		pass("mrt bmp.Session.Routes", walkMrtBMP),
		pass("gobgp ParseBMPMessage", walkGobgpBMP),
	}
	var ours, theirs []string
	for _, s := range streams {
		ours = append(ours, mrtBMPRoutes(s)...)
		g, err := gobgpBMPRoutes(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "compare: %s: gobgp: %v\n", path, err)
			return 2
		}
		theirs = append(theirs, g...)
	}
	ours, theirs = oracle.Sorted(ours), oracle.Sorted(theirs)

	fmt.Fprintf(w, "file %s\n%d messages, %d router streams; %s, GOMAXPROCS %d\n\n", path, msgs, len(streams), runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Fprintf(w, "%-30s %12s %14s %12s %10s %10s  %s\n", "pass", "wall", "TotalAlloc", "Mallocs", "B/msg", "allocs/msg", "counted")
	for _, r := range results {
		fmt.Fprintf(w, "%-30s %12s %14d %12d %10.1f %10.2f  %s\n", r.name, r.wall.Round(time.Microsecond), r.totalAlloc, r.mallocs,
			float64(r.totalAlloc)/float64(msgs), float64(r.mallocs)/float64(msgs), r.summary)
	}
	fmt.Fprintf(w, "\nmrt lines %d, gobgp lines %d\n", len(ours), len(theirs))
	onlyOurs, onlyTheirs := oracle.Diff(ours, theirs)
	if len(onlyOurs)+len(onlyTheirs) > 0 {
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".bmp"), ".obmp")
		fmt.Fprintf(w, "MISMATCH: %d mrt and %d gobgp lines differ:\n%s", len(onlyOurs), len(onlyTheirs),
			oracle.PairBlocks(name, "mrt", "gobgp", onlyOurs, onlyTheirs, oracle.LineKey))
		return 1
	}
	fmt.Fprintln(w, "no mismatch")
	return 0
}
