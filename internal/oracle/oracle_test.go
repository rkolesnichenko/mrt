package oracle

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

// Every JSON shape bgpkit-parser 0.22.0 writes for an elem: plain and
// add-path prefixes, the simplified and the verbose AS path, well-known,
// custom, extended and large communities, an aggregator, a withdrawal.
const sample = `{"aggr_asn":null,"aggr_ip":null,"as_path":[64500,64501],"atomic":false,"communities":null,"deprecated":null,"local_pref":0,"med":0,"next_hop":"192.0.2.10","only_to_customer":null,"origin":"IGP","origin_asns":[64501],"peer_asn":64500,"peer_bgp_id":null,"peer_ip":"192.0.2.10","prefix":"203.0.113.0/24","timestamp":1700000000.0,"type":"ANNOUNCE","unknown":null}
{"aggr_asn":4200000001,"aggr_ip":"192.0.2.77","as_path":[64500,[64502,64503],64501],"atomic":true,"communities":[{"Custom":[65000,1]},"NoExport","NoAdvertise","NoExportSubConfed",{"TransitiveTwoOctetAs":{"global_admin":64500,"local_admin":[0,0,0,100],"subtype":2}},{"global_admin":64500,"local_data":[1,2]}],"deprecated":null,"local_pref":100,"med":5,"next_hop":"2001:0db8:0:0::1","only_to_customer":64500,"origin":"INCOMPLETE","origin_asns":[64501],"peer_asn":4200000001,"peer_bgp_id":"192.0.2.11","peer_ip":"2001:db8::11","prefix":{"path_id":7,"prefix":"2001:db8:200::/48"},"timestamp":1700000002.123456,"type":"ANNOUNCE","unknown":null}
{"aggr_asn":null,"aggr_ip":null,"as_path":[{"ty":"AS_CONFED_SEQUENCE","values":[65001,65002]},{"ty":"AS_CONFED_SET","values":[65003]},{"ty":"AS_SEQUENCE","values":[64500]},{"ty":"AS_SEQUENCE","values":[64501]}],"atomic":false,"communities":null,"deprecated":null,"local_pref":0,"med":0,"next_hop":"192.0.2.10","only_to_customer":null,"origin":"EGP","origin_asns":[64501],"peer_asn":64500,"peer_bgp_id":null,"peer_ip":"192.0.2.10","prefix":"198.51.100.0/24","timestamp":1700000003.0,"type":"ANNOUNCE","unknown":null}
{"aggr_asn":null,"aggr_ip":null,"as_path":null,"atomic":false,"communities":null,"deprecated":null,"local_pref":null,"med":null,"next_hop":null,"only_to_customer":null,"origin":null,"origin_asns":null,"peer_asn":64500,"peer_bgp_id":null,"peer_ip":"192.0.2.10","prefix":{"path_id":1,"prefix":"203.0.113.0/24"},"timestamp":1700000004.0,"type":"WITHDRAW","unknown":null}
`

var sampleLines = []string{
	`{"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","as_path":"(65001 65002) [65003] 64500 ; 64501","origin":"EGP","next_hop":"192.0.2.10"}`,
	`{"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"203.0.113.0/24","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}`,
	`{"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:200::/48","path_id":7,"as_path":"64500 {64502,64503} 64501","origin":"INCOMPLETE","next_hop":"2001:db8::1","med":5,"local_pref":100,"communities":["65000:1","65535:65281","65535:65282","65535:65283"],"large_communities":["64500:1:2"],"otc":64500,"aggregator":"4200000001 192.0.2.77","atomic":true}`,
	`{"kind":"W","peer":"192.0.2.10","peer_as":64500,"prefix":"203.0.113.0/24","path_id":1}`,
}

func TestNormalizeBgpkit(t *testing.T) {
	got, err := NormalizeBgpkit([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	got = Sorted(got)
	if !slices.Equal(got, sampleLines) {
		t.Errorf("normalized:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(sampleLines, "\n"))
	}
}

func TestNormalizeBgpkitRefusesGarbage(t *testing.T) {
	for _, in := range []string{
		`{"type":"ANNOUNCE","peer_ip":"x","peer_asn":1,"prefix":"203.0.113.0/24"}`,
		`{"type":"ANNOUNCE","peer_ip":"192.0.2.1","peer_asn":1,"prefix":"203.0.113.0/24","as_path":[{"ty":"AS_NOPE","values":[1]}]}`,
		`{"type":"ANNOUNCE","peer_ip":"192.0.2.1","peer_asn":1,"prefix":"203.0.113.0/24","communities":[7]}`,
		`{"type":"NOPE","peer_ip":"192.0.2.1","peer_asn":1,"prefix":"203.0.113.0/24"}`,
		`not json`,
	} {
		if _, err := NormalizeBgpkit([]byte(in)); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func TestFromRoute(t *testing.T) {
	a := netip.MustParseAddr
	// A Path is produced by decoding, so each one here is decoded from a block.
	decode := func(ctx bgp.Context, attrs ...[]byte) *bgp.Path {
		t.Helper()
		p, diags := bgp.NewAttrs(mrtgen.Attrs(attrs...), ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags %v", diags)
		}
		return &p
	}
	v6 := bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}
	peer := mrt.Peer{Addr: a("2001:db8::11"), AS: 4200000001}
	path := decode(bgp.Context{AS4: true},
		mrtgen.Origin(bgp.OriginIncomplete),
		mrtgen.ASPath(true, mrtgen.Seq(64500), mrtgen.Set(64502, 64503), mrtgen.Seq(64501)),
		mrtgen.NextHop(a("192.0.2.99")), // not this route's: it came in MP_REACH_NLRI
		mrtgen.MED(5), mrtgen.LocalPref(100),
		mrtgen.Raw(mrtgen.FlagTransitive, 6, nil), // ATOMIC_AGGREGATE
		mrtgen.AggregatorAttr(true, 4200000001, a("192.0.2.77")),
		mrtgen.Communities(65000<<16|1, 0xFFFFFF01, 0xFFFFFF02, 0xFFFFFF03),
		mrtgen.ExtCommunities(bgp.ExtCommunity{0, 2, 0xfb, 0xf4, 0, 0, 0, 100}),
		mrtgen.Large(bgp.LargeCommunity{Global: 64500, Local1: 1, Local2: 2}),
		mrtgen.OTC(64500),
		mrtgen.MPReach(v6, []netip.Addr{a("2001:db8::1")}, nil, false),
	)
	r := mrt.Route{Kind: mrt.KindAnnounce, Peer: peer, Family: path.MPReach.Family,
		Prefix: netip.MustParsePrefix("2001:db8:200::/48"), PathID: 7, HasPathID: true, Path: path}
	if got := FromRoute(r); got != sampleLines[2] {
		t.Errorf("announce:\n got %s\nwant %s", got, sampleLines[2])
	}

	w := mrt.Route{Kind: mrt.KindWithdraw, Peer: mrt.Peer{Addr: a("192.0.2.10"), AS: 64500},
		Family: bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast},
		Prefix: netip.MustParsePrefix("203.0.113.0/24"), PathID: 1, HasPathID: true}
	if got := FromRoute(w); got != sampleLines[3] {
		t.Errorf("withdraw:\n got %s\nwant %s", got, sampleLines[3])
	}

	// A RIB entry is "A", its next hop is MP_REACH_NLRI's when it has one,
	// and absent attributes leave their fields out.
	v4 := bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}
	rib := mrt.Route{Kind: mrt.KindRIB, Peer: mrt.Peer{Addr: a("192.0.2.10"), AS: 64500}, Family: v4,
		Prefix: netip.MustParsePrefix("198.51.100.0/24"),
		Path:   decode(bgp.Context{AS4: true, RIB: true}, mrtgen.NextHop(a("192.0.2.10")), mrtgen.MPReachAbbrev(a("2001:db8::1")))}
	want := `{"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","next_hop":"2001:db8::1"}`
	if got := FromRoute(rib); got != want {
		t.Errorf("rib:\n got %s\nwant %s", got, want)
	}
	// An IPv4 announcement with an MP_REACH_NLRI of another family keeps NEXT_HOP.
	ann := mrt.Route{Kind: mrt.KindAnnounce, Peer: rib.Peer, Family: v4, Prefix: rib.Prefix,
		Path: decode(bgp.Context{AS4: true}, mrtgen.NextHop(a("192.0.2.10")), mrtgen.MPReach(v6, []netip.Addr{a("2001:db8::1")}, nil, false))}
	want = `{"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","next_hop":"192.0.2.10"}`
	if got := FromRoute(ann); got != want {
		t.Errorf("v4 announce:\n got %s\nwant %s", got, want)
	}
}

func TestGoldenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	small := []byte("one line\n")
	big := bytes.Repeat([]byte("a line of a large golden\n"), 4000)
	for _, tc := range []struct {
		data   []byte
		gzname bool
	}{{small, false}, {big, true}, {small, false}} {
		p := filepath.Join(dir, "x.bgpdump")
		if err := WriteGolden(p, tc.data); err != nil {
			t.Fatal(err)
		}
		_, errPlain := os.Stat(p)
		_, errGz := os.Stat(p + ".gz")
		if (errGz == nil) != tc.gzname || (errPlain == nil) == tc.gzname {
			t.Errorf("%d bytes: plain %v, gz %v; want only the gzip one: %v", len(tc.data), errPlain, errGz, tc.gzname)
		}
		got, err := ReadGolden(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, tc.data) {
			t.Errorf("%d bytes: read back %d bytes", len(tc.data), len(got))
		}
		if tc.gzname {
			b, _ := os.ReadFile(p + ".gz")
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			if zr.ModTime.Unix() > 0 || zr.Name != "" {
				t.Errorf("gzip header carries ModTime %v / Name %q", zr.ModTime, zr.Name)
			}
		}
	}
	if _, err := ReadGolden(filepath.Join(dir, "missing")); err == nil {
		t.Error("ReadGolden of a missing golden: no error")
	}
}

func TestRunMissingBinary(t *testing.T) {
	if _, _, err := Bgpdump(filepath.Join(t.TempDir(), "no-such-bgpdump"), "x"); err == nil {
		t.Error("Bgpdump with a missing binary: no error")
	}
	if _, err := Bgpkit(filepath.Join(t.TempDir(), "no-such-bgpkit"), "x"); err == nil {
		t.Error("Bgpkit with a missing binary: no error")
	}
}
