package bmp_test

// The differential against bgpkit-parser 0.22.0 (spec §9.2). Every fixture in
// testdata/bmp (internal/bmpgen) has a golden in testdata/golden written by
// the pinned harness internal/oracle/bgpkit-bmp:
//
//	NAME.bgpkit-bmp.json  bgpkit-bmp FILE, normalized and sorted by
//	                      internal/oracle
//
// TestBMPMatchesBgpkit needs no oracle installed. TestBMPMatchesBgpkitLive
// runs the harness when it is on PATH (and fails without it under
// MRT_ORACLES_REQUIRED=1, as CI sets); MRT_GOLDEN_UPDATE=1 makes it rewrite
// the goldens instead (review the diff). Every difference is listed in
// testdata/bmp/divergences.md, which TestBMPDivergencesDocumented holds in
// both directions.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
	"github.com/rkolesnichenko/mrt/internal/oracle"
)

const (
	bmpGoldenDir       = "../testdata/golden"
	bmpGoldenExt       = ".bgpkit-bmp.json"
	bmpDivergencesFile = "../testdata/bmp/divergences.md"
)

// goldenFixture is one BMP or OpenBMP file the goldens cover.
type goldenFixture struct {
	name string // the file name without .bmp or .obmp
	path string
}

func (f goldenFixture) golden() string { return filepath.Join(bmpGoldenDir, f.name+bmpGoldenExt) }

func goldenFixtures(t *testing.T) []goldenFixture {
	t.Helper()
	var fs []goldenFixture
	for _, ext := range []string{".bmp", ".obmp"} {
		paths, err := filepath.Glob("../testdata/bmp/*" + ext)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			fs = append(fs, goldenFixture{strings.TrimSuffix(filepath.Base(p), ext), p})
		}
	}
	if len(fs) == 0 {
		t.Fatal("no fixtures")
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if seen[f.name] {
			t.Fatalf("two fixtures are named %s", f.name)
		}
		seen[f.name] = true
	}
	return fs
}

var (
	routeLinesMu    sync.Mutex
	routeLinesCache = map[string][]string{}
)

// bmpRouteLines is f's announcements and withdrawals, normalized and sorted.
// A .bmp file is read by RoutesWith over Messages; an .obmp file frame by
// frame, through openbmp.Parse, ParseMessage and one Session per router
// (Header.RouterHash). Errors yield no line.
func bmpRouteLines(t *testing.T, f goldenFixture) []string {
	t.Helper()
	routeLinesMu.Lock()
	defer routeLinesMu.Unlock()
	if ls, ok := routeLinesCache[f.path]; ok {
		return ls
	}
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var ls []string
	keep := func(r mrt.Route, err error) {
		if err == nil && (r.Kind == mrt.KindAnnounce || r.Kind == mrt.KindWithdraw) {
			ls = append(ls, oracle.FromRoute(r))
		}
	}
	if strings.HasSuffix(f.path, ".obmp") {
		sessions := map[[16]byte]*bmp.Session{}
		for frame, err := range openbmp.Frames(file) {
			if err != nil {
				continue
			}
			h, msg, err := openbmp.Parse(frame)
			if err != nil {
				continue
			}
			s := sessions[h.RouterHash]
			if s == nil {
				s = bmp.NewSession(bmp.RoutesOptions{Router: h.RouterIP})
				sessions[h.RouterHash] = s
			}
			for r, err := range s.Routes(bmp.ParseMessage(msg)) {
				keep(r, err)
			}
		}
	} else {
		for r, err := range bmp.RoutesWith(bmp.Messages(file), bmp.RoutesOptions{}) {
			keep(r, err)
		}
	}
	ls = oracle.Sorted(ls)
	routeLinesCache[f.path] = ls
	return ls
}

func readBMPGolden(t *testing.T, f goldenFixture) []string {
	t.Helper()
	b, err := oracle.ReadGolden(f.golden())
	if err != nil {
		t.Fatalf("%v (MRT_GOLDEN_UPDATE=1 go test -run TestBMPMatchesBgpkitLive ./bmp writes the goldens; it needs bgpkit-bmp)", err)
	}
	return oracle.Lines(b)
}

// bgpkitPairs are the pairs of divergences.md's "## bgpkit-parser" section.
// The "## gobgp" section is read too, so that its pairs are not refused as
// outside a named section; internal/compare's TestGobgpBMPMatches checks it.
func bgpkitPairs(t *testing.T) []oracle.Pair {
	t.Helper()
	d, err := oracle.ParseDivergences(bmpDivergencesFile, "bgpkit-parser", "gobgp")
	if err != nil {
		t.Fatal(err)
	}
	return d["bgpkit-parser"]
}

func TestBMPMatchesBgpkit(t *testing.T) {
	pairs := bgpkitPairs(t)
	for _, f := range goldenFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			ours, theirs := oracle.Documented(pairs, f.name)
			got := oracle.Without(bmpRouteLines(t, f), ours)
			want := oracle.Without(readBMPGolden(t, f), theirs)
			if !slices.Equal(got, want) {
				t.Errorf("Routes differs from bgpkit-bmp's golden at %s\n(TestBMPDivergencesDocumented lists the lines)",
					oracle.FirstDifference(got, want))
			}
		})
	}
}

// TestBMPDivergencesDocumented holds divergences.md to the goldens in both
// directions: every line on which mrt and bgpkit-parser differ is documented,
// and every documented line still differs. A failure prints the undocumented
// lines as pair blocks.
func TestBMPDivergencesDocumented(t *testing.T) {
	pairs := bgpkitPairs(t)
	names := map[string]bool{}
	for _, f := range goldenFixtures(t) {
		names[f.name] = true
	}
	for _, p := range pairs {
		if !names[p.Fixture] {
			t.Errorf("%s: no fixture named %q", p.Entry, p.Fixture)
		}
	}
	oursName, theirsName := oracle.Labels("bgpkit-parser")
	for _, f := range goldenFixtures(t) {
		ours, theirs := bmpRouteLines(t, f), readBMPGolden(t, f)
		docOurs, docTheirs := oracle.Documented(pairs, f.name)
		for _, l := range oracle.StillDiffers(ours, theirs, docOurs) {
			t.Errorf("%s: the %s line is documented more often than it differs: %s", f.name, oursName, l)
		}
		for _, l := range oracle.StillDiffers(theirs, ours, docTheirs) {
			t.Errorf("%s: the %s line is documented more often than it differs: %s", f.name, theirsName, l)
		}
		onlyOurs, onlyTheirs := oracle.Diff(ours, theirs)
		onlyOurs, onlyTheirs = oracle.Without(onlyOurs, docOurs), oracle.Without(onlyTheirs, docTheirs)
		if len(onlyOurs)+len(onlyTheirs) > 0 {
			t.Errorf("%s: %d %s and %d %s lines differ and are not in %s:\n%s", f.name,
				len(onlyOurs), oursName, len(onlyTheirs), theirsName, bmpDivergencesFile,
				oracle.PairBlocks(f.name, oursName, theirsName, onlyOurs, onlyTheirs, oracle.LineKey))
		}
	}
}

func TestBMPMatchesBgpkitLive(t *testing.T) {
	update := os.Getenv("MRT_GOLDEN_UPDATE") == "1"
	bin, err := exec.LookPath("bgpkit-bmp")
	if err != nil {
		if update || os.Getenv("MRT_ORACLES_REQUIRED") == "1" {
			t.Fatal("MRT_GOLDEN_UPDATE=1 and MRT_ORACLES_REQUIRED=1 need bgpkit-bmp on PATH (scripts/build-oracles.sh)")
		}
		t.Skip("bgpkit-bmp is not on PATH (scripts/build-oracles.sh installs it)")
	}
	for _, f := range goldenFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			out, err := oracle.BgpkitBMP(bin, f.path)
			if err != nil {
				t.Fatal(err)
			}
			ls, err := oracle.NormalizeBgpkit(out)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			for _, l := range oracle.Sorted(ls) {
				b.WriteString(l)
				b.WriteByte('\n')
			}
			if update {
				if err := oracle.WriteGolden(f.golden(), b.Bytes()); err != nil {
					t.Fatal(err)
				}
				return
			}
			golden, err := oracle.ReadGolden(f.golden())
			if err != nil {
				t.Fatalf("%v (MRT_GOLDEN_UPDATE=1 writes it)", err)
			}
			if !bytes.Equal(b.Bytes(), golden) {
				t.Errorf("bgpkit-bmp's output differs from %s at %s\n(MRT_GOLDEN_UPDATE=1 rewrites it; review the diff)",
					f.golden(), oracle.FirstDifference(oracle.Lines(golden), oracle.Lines(b.Bytes())))
			}
		})
	}
}

// TestBMPFixturesListed: every fixture has a golden, and every BMP golden
// belongs to a fixture.
func TestBMPFixturesListed(t *testing.T) {
	names := map[string]bool{}
	for _, f := range goldenFixtures(t) {
		names[f.name] = true
		if _, err := oracle.ReadGolden(f.golden()); err != nil {
			t.Errorf("%s: %v", f.name, err)
		}
	}
	goldens, err := os.ReadDir(bmpGoldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range goldens {
		n, ok := strings.CutSuffix(strings.TrimSuffix(g.Name(), ".gz"), bmpGoldenExt)
		if ok && !names[n] {
			t.Errorf("a golden with no fixture: %s", g.Name())
		}
	}
}
