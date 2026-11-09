package mrt_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// TestAllocations is the allocation gate: allocation counts are nearly
// deterministic (with an Interner, map growth depends on the hash seed by a
// few allocations), so a change that adds allocations to the decode path
// fails here, where a timing benchmark would only drift. Records allocates
// two objects per record (the record's bytes and the Record interface
// value) and at most perError per error; Routes and Routes with an Interner
// are held to ceilings about 5% above the counts measured with Go 1.23.0 and
// 1.27.3 on darwin/arm64. A change that lowers a count should lower its
// ceiling; a new Go release that allocates more in the runtime or in fmt may
// need new ceilings, measured with both toolchains again.
//
// The counts are for 64-bit platforms without the race detector, which
// inflates allocation.
func TestAllocations(t *testing.T) {
	const perError = 8
	if mrt.RaceEnabled {
		t.Skip("the race detector inflates allocation")
	}
	if strconv.IntSize != 64 {
		t.Skip("the ceilings are measured on 64-bit platforms")
	}
	// Measured on darwin/arm64, the higher of the two toolchains plus 5%:
	// routes at release-0.4.2 (.data/allocs-v042.log), interner at
	// release-0.5.1 (.data/allocs-v051.log).
	ceilings := map[string]struct{ routes, interner float64 }{
		"rv2-rib.20261015.1200":         {99586, 18991},
		"rv2-tabledump.20070601.0025":   {48544, 48213},
		"rv2-updates-as2.20110601.1200": {71769, 79948},
		"rv2-updates-et.20261015.1200":  {64634, 72006},
		"rv6-rib.20261015.1200":         {111509, 59694},
		"rv6-tabledump.20070601.0123":   {24205, 30762},
		"rv6-updates-et.20261015.1200":  {3731, 4011},
		"rvwide-rib.20261015.1200":      {43571, 18501},
	}
	paths, err := filepath.Glob("testdata/mrt/*.gz")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures in testdata/mrt: %v", err)
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".gz")
		t.Run(name, func(t *testing.T) {
			d := gunzip(t, p)
			records, errs := 0, 0
			for _, err := range mrt.Records(bytes.NewReader(d)) {
				if err != nil {
					errs++
				} else {
					records++
				}
			}
			got := testing.AllocsPerRun(3, func() {
				for range mrt.Records(bytes.NewReader(d)) {
				}
			})
			// The constant covers the bufio.Reader and the iterator's state.
			if limit := float64(2*records + perError*errs + 16); got > limit {
				t.Errorf("Records: %.0f allocations for %d records and %d errors, want at most %.0f (2 per record, %d per error)", got, records, errs, limit, perError)
			}
			c, ok := ceilings[name]
			if !ok {
				t.Errorf("no Routes ceilings for %s: measure it and add one", name)
				return
			}
			if got := testing.AllocsPerRun(3, func() {
				for range mrt.Routes(mrt.Records(bytes.NewReader(d))) {
				}
			}); got > c.routes {
				t.Errorf("Routes: %.0f allocations, ceiling %.0f", got, c.routes)
			}
			if got := testing.AllocsPerRun(3, func() {
				for range mrt.RoutesWith(mrt.Records(bytes.NewReader(d)), mrt.RoutesOptions{Interner: bgp.NewInterner()}) {
				}
			}); got > c.interner {
				t.Errorf("Routes with an Interner: %.0f allocations, ceiling %.0f", got, c.interner)
			}
		})
	}
}

// gunzip returns the decompressed bytes of the gzip file at path.
func gunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	d, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
