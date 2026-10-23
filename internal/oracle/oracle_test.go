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
// custom, extended (and IPv6 address specific ones, which are skipped) and
// large communities, an aggregator, a withdrawal.
const sample = `{"aggr_asn":null,"aggr_ip":null,"as_path":[64500,64501],"atomic":false,"communities":null,"deprecated":null,"local_pref":0,"med":0,"next_hop":"192.0.2.10","only_to_customer":null,"origin":"IGP","origin_asns":[64501],"peer_asn":64500,"peer_bgp_id":null,"peer_ip":"192.0.2.10","prefix":"203.0.113.0/24","timestamp":1700000000.0,"type":"ANNOUNCE","unknown":null}
{"aggr_asn":4200000001,"aggr_ip":"192.0.2.77","as_path":[64500,[64502,64503],64501],"atomic":true,"communities":[{"Custom":[65000,1]},"NoExport","NoAdvertise","NoExportSubConfed",{"TransitiveTwoOctetAs":{"global_admin":64500,"local_admin":[0,0,0,100],"subtype":2}},{"global_admin":64500,"local_data":[1,2]},{"TransitiveIpv4Addr":{"global_admin":"192.0.2.1","local_admin":[0,7],"subtype":3}},{"community_type":"TransitiveTwoOctetAs","global_admin":"2001:db8::1","local_admin":[0,5],"subtype":2},{"Raw":[26,112,1,0,0,0,64,129]}],"deprecated":null,"local_pref":100,"med":5,"next_hop":"2001:0db8:0:0::1","only_to_customer":64500,"origin":"INCOMPLETE","origin_asns":[64501],"peer_asn":4200000001,"peer_bgp_id":"192.0.2.11","peer_ip":"2001:db8::11","prefix":{"path_id":7,"prefix":"2001:db8:200::/48"},"timestamp":1700000002.123456,"type":"ANNOUNCE","unknown":null}
{"aggr_asn":null,"aggr_ip":null,"as_path":[{"ty":"AS_CONFED_SEQUENCE","values":[65001,65002]},{"ty":"AS_CONFED_SET","values":[65003]},{"ty":"AS_SEQUENCE","values":[64500]},{"ty":"AS_SEQUENCE","values":[64501]}],"atomic":false,"communities":null,"deprecated":null,"local_pref":0,"med":0,"next_hop":"192.0.2.10","only_to_customer":null,"origin":"EGP","origin_asns":[64501],"peer_asn":64500,"peer_bgp_id":null,"peer_ip":"192.0.2.10","prefix":"198.51.100.0/24","timestamp":1700000003.0,"type":"ANNOUNCE","unknown":null}
{"aggr_asn":null,"aggr_ip":null,"as_path":null,"atomic":false,"communities":null,"deprecated":null,"local_pref":null,"med":null,"next_hop":null,"only_to_customer":null,"origin":null,"origin_asns":null,"peer_asn":64500,"peer_bgp_id":null,"peer_ip":"192.0.2.10","prefix":{"path_id":1,"prefix":"203.0.113.0/24"},"timestamp":1700000004.0,"type":"WITHDRAW","unknown":null}
`

var sampleLines = []string{
	`{"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","as_path":"(65001 65002) [65003] 64500 ; 64501","origin":"EGP","next_hop":"192.0.2.10"}`,
	`{"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"203.0.113.0/24","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}`,
	`{"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:200::/48","path_id":7,"as_path":"64500 {64502,64503} 64501","origin":"INCOMPLETE","next_hop":"2001:db8::1","med":5,"local_pref":100,"communities":["65000:1","65535:65281","65535:65282","65535:65283"],"ext_communities":["rt:64500:100","ro:192.0.2.1:7","0x1a70010000004081"],"large_communities":["64500:1:2"],"otc":64500,"aggregator":"4200000001 192.0.2.77","atomic":true}`,
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

// extElem is a bgpkit-parser elem with the given communities.
func extElem(comms string) string {
	return `{"type":"ANNOUNCE","peer_ip":"192.0.2.1","peer_asn":1,"prefix":"203.0.113.0/24","as_path":[1],"communities":[` + comms + `]}`
}

// TestNormalizeBgpkitExtCommunities: each extended community shape
// bgpkit-parser 0.22.0 writes (every object below is its output for the
// bytes beside it), normalized from bgpkit's fields alone. The bytes' own
// String must give the same label, so the two sides of the comparison agree
// by construction where both decode correctly.
func TestNormalizeBgpkitExtCommunities(t *testing.T) {
	for _, tc := range []struct {
		json  string
		bytes bgp.ExtCommunity
		want  string
	}{
		// The brief's vectors, from rv2-updates-et.20261015.1200.
		{`{"TransitiveTwoOctetAs":{"global_admin":7578,"local_admin":[0,0,0,10],"subtype":2}}`,
			bgp.ExtCommunity{0x00, 0x02, 0x1d, 0x9a, 0, 0, 0, 10}, "rt:7578:10"},
		{`{"TransitiveFourOctetAs":{"global_admin":265126,"local_admin":[0,10],"subtype":2}}`,
			bgp.ExtCommunity{0x02, 0x02, 0x00, 0x04, 0x0b, 0xa6, 0, 10}, "rt:265126:10"},
		{`{"TransitiveOpaque":{"subtype":11,"value":[0,0,0,2,146,81]}}`,
			bgp.ExtCommunity{0x03, 0x0b, 0, 0, 0, 2, 146, 81}, "color:168529"},
		{`{"Raw":[26,112,1,0,0,0,64,129]}`,
			bgp.ExtCommunity{26, 112, 1, 0, 0, 0, 64, 129}, "0x1a70010000004081"},
		// 862 of rv2-updates-et's: a subtype with no label, rebuilt byte for byte.
		{`{"TransitiveTwoOctetAs":{"global_admin":100,"local_admin":[0,0,0,1],"subtype":67}}`,
			bgp.ExtCommunity{0x00, 0x43, 0, 100, 0, 0, 0, 1}, "0x0043006400000001"},
		{`{"TransitiveTwoOctetAs":{"global_admin":7578,"local_admin":[0,1,0,2],"subtype":3}}`,
			bgp.ExtCommunity{0x00, 0x03, 0x1d, 0x9a, 0, 1, 0, 2}, "ro:7578:65538"},
		{`{"TransitiveFourOctetAs":{"global_admin":265126,"local_admin":[1,2],"subtype":3}}`,
			bgp.ExtCommunity{0x02, 0x03, 0x00, 0x04, 0x0b, 0xa6, 1, 2}, "ro:265126:258"},
		{`{"TransitiveFourOctetAs":{"global_admin":265126,"local_admin":[1,2],"subtype":9}}`,
			bgp.ExtCommunity{0x02, 0x09, 0x00, 0x04, 0x0b, 0xa6, 1, 2}, "0x020900040ba60102"},
		// From otc-large and a probe file.
		{`{"TransitiveIpv4Addr":{"global_admin":"192.0.2.1","local_admin":[0,7],"subtype":3}}`,
			bgp.ExtCommunity{0x01, 0x03, 192, 0, 2, 1, 0, 7}, "ro:192.0.2.1:7"},
		{`{"TransitiveIpv4Addr":{"global_admin":"192.0.2.1","local_admin":[0,9],"subtype":2}}`,
			bgp.ExtCommunity{0x01, 0x02, 192, 0, 2, 1, 0, 9}, "rt:192.0.2.1:9"},
		{`{"TransitiveIpv4Addr":{"global_admin":"192.0.2.1","local_admin":[0,9],"subtype":5}}`,
			bgp.ExtCommunity{0x01, 0x05, 192, 0, 2, 1, 0, 9}, "0x0105c00002010009"},
		// The transitive bit does not change a label.
		{`{"NonTransitiveTwoOctetAs":{"global_admin":64500,"local_admin":[0,0,0,1],"subtype":2}}`,
			bgp.ExtCommunity{0x40, 0x02, 0xfb, 0xf4, 0, 0, 0, 1}, "rt:64500:1"},
		{`{"NonTransitiveIpv4Addr":{"global_admin":"192.0.2.1","local_admin":[0,9],"subtype":3}}`,
			bgp.ExtCommunity{0x41, 0x03, 192, 0, 2, 1, 0, 9}, "ro:192.0.2.1:9"},
		{`{"NonTransitiveFourOctetAs":{"global_admin":309414,"local_admin":[0,10],"subtype":2}}`,
			bgp.ExtCommunity{0x42, 0x02, 0x00, 0x04, 0xb8, 0xa6, 0, 10}, "rt:309414:10"},
		{`{"LinkBandwidth":{"bandwidth":1250000000.0,"global_admin":64500,"transitive":true}}`,
			bgp.ExtCommunity{0x00, 0x04, 0xfb, 0xf4, 0x4e, 0x95, 0x02, 0xf9}, "lbw:64500:1.25e+09"},
		{`{"LinkBandwidth":{"bandwidth":1250000000.0,"global_admin":64500,"transitive":false}}`,
			bgp.ExtCommunity{0x40, 0x04, 0xfb, 0xf4, 0x4e, 0x95, 0x02, 0xf9}, "lbw:64500:1.25e+09"},
		{`{"TransitiveOpaque":{"subtype":12,"value":[0,0,0,0,0,8]}}`,
			bgp.ExtCommunity{0x03, 0x0c, 0, 0, 0, 0, 0, 8}, "encap:8"},
		// A label would hide the non-zero bytes: hex.
		{`{"TransitiveOpaque":{"subtype":12,"value":[0,0,0,1,0,8]}}`,
			bgp.ExtCommunity{0x03, 0x0c, 0, 0, 0, 1, 0, 8}, "0x030c000000010008"},
		{`{"TransitiveOpaque":{"subtype":11,"value":[64,0,0,0,0,7]}}`,
			bgp.ExtCommunity{0x03, 0x0b, 0x40, 0, 0, 0, 0, 7}, "0x030b400000000007"},
		{`{"TransitiveOpaque":{"subtype":6,"value":[1,2,3,4,5,6]}}`,
			bgp.ExtCommunity{0x03, 0x06, 1, 2, 3, 4, 5, 6}, "0x0306010203040506"},
		{`{"NonTransitiveOpaque":{"subtype":0,"value":[0,0,0,0,0,2]}}`,
			bgp.ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 2}, "ov:invalid"},
		{`{"NonTransitiveOpaque":{"subtype":0,"value":[0,0,0,0,0,0]}}`,
			bgp.ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 0}, "ov:valid"},
		{`{"NonTransitiveOpaque":{"subtype":0,"value":[0,0,0,0,0,1]}}`,
			bgp.ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 1}, "ov:not-found"},
		{`{"NonTransitiveOpaque":{"subtype":0,"value":[0,0,0,0,0,3]}}`,
			bgp.ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 3}, "0x4300000000000003"},
		{`{"NonTransitiveOpaque":{"subtype":0,"value":[0,0,0,0,1,2]}}`,
			bgp.ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 1, 2}, "0x4300000000000102"},
		// RFC 8955's redirect (0x80) is Raw; bgpkit's FlowSpecRedirect is 0x40 subtype 8.
		{`{"Raw":[128,8,251,244,0,0,0,1]}`,
			bgp.ExtCommunity{0x80, 0x08, 0xfb, 0xf4, 0, 0, 0, 1}, "0x8008fbf400000001"},
		{`{"FlowSpecRedirect":{"global_admin":64500,"local_admin":[0,0,0,1],"subtype":8}}`,
			bgp.ExtCommunity{0x40, 0x08, 0xfb, 0xf4, 0, 0, 0, 1}, "0x4008fbf400000001"},
	} {
		if got := tc.bytes.String(); got != tc.want {
			t.Errorf("% x: String %q, want %q", tc.bytes[:], got, tc.want)
		}
		got, err := NormalizeBgpkit([]byte(extElem(tc.json)))
		if err != nil {
			t.Errorf("%s: %v", tc.json, err)
			continue
		}
		want := `{"kind":"A","peer":"192.0.2.1","peer_as":1,"prefix":"203.0.113.0/24","as_path":"1","ext_communities":["` + tc.want + `"]}`
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s:\n got %v\nwant %s", tc.json, got, want)
		}
	}

	// Wire order, and an IPv6 address specific community (attribute 25,
	// which mrt does not put in ExtCommunities) is skipped.
	got, err := NormalizeBgpkit([]byte(extElem(`{"Raw":[128,8,251,244,0,0,0,1]},` +
		`{"community_type":"TransitiveTwoOctetAs","global_admin":"2001:db8::1","local_admin":[0,5],"subtype":2},` +
		`{"Custom":[65000,1]},{"TransitiveOpaque":{"subtype":12,"value":[0,0,0,0,0,8]}}`)))
	want := `{"kind":"A","peer":"192.0.2.1","peer_as":1,"prefix":"203.0.113.0/24","as_path":"1","communities":["65000:1"],"ext_communities":["0x8008fbf400000001","encap:8"]}`
	if err != nil || len(got) != 1 || got[0] != want {
		t.Errorf("order:\n got %v, %v\nwant %s", got, err, want)
	}
}

func TestNormalizeBgpkitRefusesExtCommunities(t *testing.T) {
	for _, c := range []string{
		// bgpkit-parser writes a NaN or infinite bandwidth as null: the bits are lost.
		`{"LinkBandwidth":{"bandwidth":null,"global_admin":64500,"transitive":false}}`,
		// Shapes that drop bytes, or that no fixture has needed.
		`{"FlowSpecTrafficRate":{"as_number":64500,"rate_bytes_per_sec":1250000000.0}}`,
		`{"FlowSpecTrafficAction":{"as_number":64500,"sample":true,"terminal":true}}`,
		`{"FlowSpecTrafficMarking":{"as_number":64500,"dscp":9}}`,
		`{"SomethingNew":{"subtype":1}}`,
		`{"TransitiveTwoOctetAs":{"global_admin":7578,"local_admin":[0,0,10],"subtype":2}}`,
		`{"TransitiveTwoOctetAs":{"global_admin":70000,"local_admin":[0,0,0,10],"subtype":2}}`,
		`{"TransitiveFourOctetAs":{"global_admin":265126,"local_admin":[0,256],"subtype":2}}`,
		`{"TransitiveIpv4Addr":{"global_admin":"2001:db8::1","local_admin":[0,7],"subtype":3}}`,
		`{"TransitiveOpaque":{"subtype":11,"value":[0,0,2,146,81]}}`,
		`{"TransitiveOpaque":{"subtype":256,"value":[0,0,0,2,146,81]}}`,
		`{"Raw":[26,112,1,0,0,0,64]}`,
		`{"Raw":[26,112,1,0,0,0,64,129],"Custom":[1,2]}`,
		`{"nope":1,"other":2}`,
	} {
		if _, err := NormalizeBgpkit([]byte(extElem(c))); err == nil {
			t.Errorf("%s: no error", c)
		}
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
		mrtgen.ExtCommunities(bgp.ExtCommunity{0, 2, 0xfb, 0xf4, 0, 0, 0, 100},
			bgp.ExtCommunity{1, 3, 192, 0, 2, 1, 0, 7}, bgp.ExtCommunity{0x1a, 0x70, 1, 0, 0, 0, 0x40, 0x81}),
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
	if _, err := BgpkitBMP(filepath.Join(t.TempDir(), "no-such-bgpkit-bmp"), "x"); err == nil {
		t.Error("BgpkitBMP with a missing binary: no error")
	}
}
