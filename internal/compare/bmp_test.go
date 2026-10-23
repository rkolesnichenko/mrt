package main

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
	"github.com/rkolesnichenko/mrt/internal/oracle"
)

const bmpDivergencesFile = "../../testdata/bmp/divergences.md"

// bmpFixture is one file of testdata/bmp, named without its extension.
type bmpFixture struct{ name, path string }

func bmpFixtures(t *testing.T) []bmpFixture {
	t.Helper()
	var fs []bmpFixture
	for _, ext := range []string{".bmp", ".obmp"} {
		paths, err := filepath.Glob("../../testdata/bmp/*" + ext)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			fs = append(fs, bmpFixture{strings.TrimSuffix(filepath.Base(p), ext), p})
		}
	}
	if len(fs) == 0 {
		t.Fatal("no fixtures")
	}
	return fs
}

// fixtureLines is f's announcement and withdrawal lines from mrt and from
// gobgp, each sorted.
func fixtureLines(t *testing.T, f bmpFixture) (ours, theirs []string) {
	t.Helper()
	streams, err := readBMPStreams(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range streams {
		ours = append(ours, mrtBMPRoutes(s)...)
		g, err := gobgpBMPRoutes(s)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		theirs = append(theirs, g...)
	}
	return oracle.Sorted(ours), oracle.Sorted(theirs)
}

// TestGobgpBMPMatches: on every fixture of testdata/bmp, mrt's announcements
// and withdrawals equal gobgp's but for the lines of divergences.md's
// "## gobgp" section, which holds in both directions: every line that
// differs is documented, and every documented line still differs. A failure
// prints the undocumented lines as pair blocks.
func TestGobgpBMPMatches(t *testing.T) {
	d, err := oracle.ParseDivergences(bmpDivergencesFile, "bgpkit-parser", "gobgp")
	if err != nil {
		t.Fatal(err)
	}
	pairs := d["gobgp"]
	fixtures := bmpFixtures(t)
	names := map[string]bool{}
	for _, f := range fixtures {
		names[f.name] = true
	}
	for _, p := range pairs {
		if !names[p.Fixture] {
			t.Errorf("%s: no fixture named %q", p.Entry, p.Fixture)
		}
	}
	oursName, theirsName := oracle.Labels("gobgp")
	total := 0
	for _, f := range fixtures {
		ours, theirs := fixtureLines(t, f)
		total += len(ours)
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
	if total == 0 {
		t.Error("the fixtures gave no mrt line at all")
	}
}

// TestGobgpLineFormat: a route with every field of the normalized line gives
// the same line from gobgp as oracle.FromRoute gives from mrt, so that
// gobgpLine and renderUpdate cannot drift from internal/oracle on the fields
// no fixture carries.
func TestGobgpLineFormat(t *testing.T) {
	h := bmp.PeerHeader{Type: mrt.PeerGlobal, Addr: netip.MustParseAddr("192.0.2.1"), AS: 4200000001,
		BGPID: netip.MustParseAddr("192.0.2.1"), Time: time.Unix(1700000000, 0)}
	nh4, nh6 := netip.MustParseAddr("192.0.2.254"), netip.MustParseAddr("2001:db8::fe")
	attrs := mrtgen.Attrs(
		mrtgen.Origin(bgp.OriginEGP),
		mrtgen.ASPath(true, mrtgen.Seq(64501, 4200000002), mrtgen.Set(64510, 64511),
			bgp.Segment{Type: bgp.ASConfedSequence, ASNs: []bgp.ASN{64600, 64601}},
			bgp.Segment{Type: bgp.ASConfedSet, ASNs: []bgp.ASN{64602}}),
		mrtgen.NextHop(nh4),
		mrtgen.MED(10),
		mrtgen.LocalPref(200),
		mrtgen.Raw(mrtgen.FlagTransitive, 6, nil), // ATOMIC_AGGREGATE
		mrtgen.AggregatorAttr(true, 4200000003, netip.MustParseAddr("192.0.2.9")),
		mrtgen.Communities(bgp.Community(64501<<16|1), bgp.Community(0xffffff01)),
		mrtgen.ExtCommunities(bgp.ExtCommunity{0x00, 0x02, 0xfb, 0xf5, 0, 0, 0, 7}, bgp.ExtCommunity{0x43, 0, 0, 0, 0, 0, 0, 2}),
		mrtgen.Large(bgp.LargeCommunity{Global: 4200000001, Local1: 1, Local2: 2}),
		mrtgen.MPReach(bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}, []netip.Addr{nh6},
			[]bgp.NLRI{{Prefix: netip.MustParsePrefix("2001:db8:1::/48")}}, false),
	)
	update := mrtgen.Update([]bgp.NLRI{{Prefix: netip.MustParsePrefix("203.0.113.0/24")}},
		[]bgp.NLRI{{Prefix: netip.MustParsePrefix("198.51.100.0/24")}}, attrs, false)
	stream := [][]byte{bmpgen.RouteMonitoring(h, update)}

	ours := mrtBMPRoutes(stream)
	theirs, err := gobgpBMPRoutes(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ours, theirs) {
		t.Fatalf("lines differ:\n mrt:   %q\n gobgp: %q", ours, theirs)
	}
	if len(ours) != 3 {
		t.Fatalf("got %d lines, want 3 (one withdrawal, two announcements): %q", len(ours), ours)
	}
	all := strings.Join(ours, "\n")
	for _, field := range []string{`"kind":"W"`, `"as_path":"64501 4200000002 {64510,64511} (64600 64601) [64602]"`,
		`"origin":"EGP"`, `"next_hop":"192.0.2.254"`, `"next_hop":"2001:db8::fe"`, `"med":10`, `"local_pref":200`,
		`"communities":["64501:1","65535:65281"]`, `"ext_communities":["rt:64501:7","ov:invalid"]`,
		`"large_communities":["4200000001:1:2"]`, `"aggregator":"4200000003 192.0.2.9"`, `"atomic":true`} {
		if !strings.Contains(all, field) {
			t.Errorf("no line has %s:\n%s", field, all)
		}
	}
}

// TestRunBMP: the -bmp mode exits 0 on a fixture where both sides agree, 1
// on one with a documented divergence, and prints both line counts.
func TestRunBMP(t *testing.T) {
	for _, tc := range []struct {
		file   string
		status int
		want   string
	}{
		{"bmp-addpath.bmp", 0, "mrt lines 12, gobgp lines 12\nno mismatch"},
		{"obmp-v6-router.obmp", 0, "no mismatch"},
		{"bmp-as2.bmp", 1, "MISMATCH: 1 mrt and 1 gobgp lines differ"},
	} {
		var out strings.Builder
		if got := runBMP("../../testdata/bmp/"+tc.file, &out); got != tc.status || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: status %d, want %d and %q in:\n%s", tc.file, got, tc.status, tc.want, out.String())
		}
	}
}
