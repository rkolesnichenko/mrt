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

// TestAllocations is the allocation gate: allocation counts are
// deterministic, so a change that adds allocations to the decode path fails
// here, where a timing benchmark would only drift. Records allocates two
// objects per record (the record's bytes and the Record interface value);
// Routes and Routes with an Interner are held to ceilings about 5% above the
// counts measured with Go 1.23.0 and 1.27.3 on 64-bit. A change that lowers
// a count should lower its ceiling.
//
// The counts are for 64-bit platforms without the race detector, which
// inflates allocation.
func TestAllocations(t *testing.T) {
	if mrt.RaceEnabled {
		t.Skip("the race detector inflates allocation")
	}
	if strconv.IntSize != 64 {
		t.Skip("the ceilings are measured on 64-bit platforms")
	}
	// Measured on darwin/arm64 at release-0.4.1, the higher of the two
	// toolchains plus 5%.
	ceilings := map[string]struct{ routes, interner float64 }{
		"rv2-rib.20261015.1200":         {132957, 30790},
		"rv2-tabledump.20070601.0025":   {59043, 69686},
		"rv2-updates-as2.20110601.1200": {89160, 130178},
		"rv2-updates-et.20261015.1200":  {80948, 120934},
		"rv6-rib.20261015.1200":         {137643, 104275},
		"rv6-tabledump.20070601.0123":   {29483, 47003},
		"rv6-updates-et.20261015.1200":  {4029, 6463},
		"rvwide-rib.20261015.1200":      {56969, 24894},
	}
	paths, err := filepath.Glob("testdata/mrt/*.gz")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures in testdata/mrt: %v", err)
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".gz")
		t.Run(name, func(t *testing.T) {
			d := gunzip(t, p)
			records := 0
			for range mrt.Records(bytes.NewReader(d)) {
				records++
			}
			got := testing.AllocsPerRun(3, func() {
				for range mrt.Records(bytes.NewReader(d)) {
				}
			})
			// The constant covers the bufio.Reader and the iterator's state.
			if limit := float64(2*records + 16); got > limit {
				t.Errorf("Records: %.0f allocations for %d records, want at most %.0f (2 per record)", got, records, limit)
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
