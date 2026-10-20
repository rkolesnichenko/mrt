package bgp

import (
	"net/netip"
	"slices"
	"testing"
)

var (
	v6global = netip.MustParseAddr("2001:db8::1")
	v6ll     = netip.MustParseAddr("fe80::1")
)

func b16(a netip.Addr) []byte { x := a.As16(); return x[:] }

func decodeMP(t *testing.T, ctx Context, code byte, v ...byte) (Path, []Diagnostic) {
	t.Helper()
	return NewAttrs(attr(0x80, code, v...), ctx).Decode()
}

func TestMPReachFull(t *testing.T) {
	v := concat([]byte{0, 2, 1, 32}, b16(v6global), b16(v6ll), []byte{0, 48, 0x20, 1, 0x0d, 0xb8, 0, 1})
	p, d := decodeMP(t, Context{}, 14, v...)
	r := p.MPReach
	if len(d) != 0 || r == nil || r.Abbreviated || r.Family != (Family{2, 1}) ||
		r.NextHop != v6global || r.LinkLocal != v6ll ||
		len(r.NLRI) != 1 || r.NLRI[0].Prefix != netip.MustParsePrefix("2001:db8:1::/48") || len(p.Unknown()) != 0 {
		t.Fatalf("%+v %v", r, d)
	}
	if p.NextHop().IsValid() {
		t.Fatal("Path.NextHop must stay the NEXT_HOP attribute only")
	}
}

func TestMPReachAbbreviatedInRIB(t *testing.T) {
	p, d := decodeMP(t, Context{RIB: true}, 14, append([]byte{16}, b16(v6global)...)...)
	r := p.MPReach
	if len(d) != 0 || r == nil || !r.Abbreviated || r.NextHop != v6global || r.NLRI != nil || r.Family != (Family{}) {
		t.Fatalf("%+v %v", r, d)
	}
	// The full form in a RIB entry (route-views6 writes it on every entry,
	// Ruling R20, amended 2026-10-20): no diagnostic, family and next hops
	// decoded, the NLRI not kept (the record supplies the prefix).
	nlri := []byte{32, 0x20, 1, 0x0d, 0xb8}
	v := concat([]byte{0, 2, 1, 32}, b16(v6global), b16(v6ll), []byte{0}, nlri)
	p, d = decodeMP(t, Context{RIB: true}, 14, v...)
	r = p.MPReach
	if len(d) != 0 || r == nil || r.Abbreviated || r.Family != (Family{2, 1}) || r.NextHop != v6global || r.LinkLocal != v6ll ||
		r.NLRI != nil || r.RawNLRI != nil || len(p.Unknown()) != 0 {
		t.Fatalf("%+v %v", r, d)
	}
	// ... also with add-path, and when the NLRI would not parse: it is not read.
	for _, ctx := range []Context{{RIB: true, AddPath: true}, {RIB: true, AS4: true}} {
		v = concat([]byte{0, 2, 1, 16}, b16(v6global), []byte{0, 200, 1})
		p, d = decodeMP(t, ctx, 14, v...)
		if r = p.MPReach; len(d) != 0 || r == nil || r.NLRI != nil || r.RawNLRI != nil {
			t.Fatalf("%+v: %+v %v", ctx, r, d)
		}
	}
	// Outside a RIB entry the same bytes are parsed as before: a /200 is
	// reported.
	p, d = decodeMP(t, Context{}, 14, v...)
	if r = p.MPReach; r == nil || len(d) == 0 || r.RawNLRI != nil {
		t.Fatalf("not RIB: %+v %v", r, d)
	}
	// without RIB the same non-zero first byte is not abbreviated: AFI 0x1002 here
	p, d = decodeMP(t, Context{}, 14, append([]byte{16}, b16(v6global)...)...)
	if p.MPReach != nil || len(p.Unknown()) != 1 || len(d) != 1 || d[0].Code != "bgp/attr-length" {
		t.Fatalf("%+v %v", p, d)
	}
	// abbreviated with trailing bytes: wrong length
	p, d = decodeMP(t, Context{RIB: true}, 14, append(append([]byte{16}, b16(v6global)...), 0)...)
	if p.MPReach != nil || len(p.Unknown()) != 1 || d[0].Code != "bgp/attr-length" {
		t.Fatalf("%+v %v", p, d)
	}
}

func TestMPReachIPv4WithIPv6NextHop(t *testing.T) { // RFC 8950
	v := concat([]byte{0, 1, 1, 16}, b16(v6global), []byte{0, 0x18, 0xC0, 0, 2})
	p, d := decodeMP(t, Context{}, 14, v...)
	r := p.MPReach
	if len(d) != 0 || r == nil || r.NextHop != v6global || len(r.NLRI) != 1 || r.NLRI[0].Prefix != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatalf("%+v %v", r, d)
	}
}

func TestMPReachNextHopLengths(t *testing.T) {
	// IPv4 next hop (4) for AFI 1
	p, d := decodeMP(t, Context{}, 14, 0, 1, 1, 4, 192, 0, 2, 1, 0)
	if len(d) != 0 || p.MPReach.NextHop != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("%+v %v", p.MPReach, d)
	}
	// 12 bytes on IPv4 unicast: Warning, next hop zero, still decoded
	p, d = decodeMP(t, Context{}, 14, append([]byte{0, 1, 1, 12}, make([]byte, 13)...)...)
	if p.MPReach == nil || p.MPReach.NextHop.IsValid() || len(d) != 1 || d[0].Code != "bgp/attr-value" || d[0].Severity != SeverityWarning {
		t.Fatalf("%+v %v", p.MPReach, d)
	}
	// 24 bytes on a VPN family: no diagnostic
	p, d = decodeMP(t, Context{}, 14, append([]byte{0, 1, 128, 24}, make([]byte, 25)...)...)
	if p.MPReach == nil || len(d) != 0 || p.MPReach.NextHop.IsValid() {
		t.Fatalf("%+v %v", p.MPReach, d)
	}
}

// In a RIB entry the family is always IP, so an abbreviated next hop of a
// length other than 4, 16 or 32 is a Warning; the attribute stays decoded.
func TestMPReachAbbreviatedNextHopLength(t *testing.T) {
	for _, n := range []int{4, 16, 32} {
		_, d := decodeMP(t, Context{RIB: true}, 14, append([]byte{byte(n)}, make([]byte, n)...)...)
		if len(d) != 0 {
			t.Errorf("%d bytes: %v", n, d)
		}
	}
	p, d := decodeMP(t, Context{RIB: true}, 14, append([]byte{12}, make([]byte, 12)...)...)
	want := []Diagnostic{{SeverityWarning, "bgp/attr-value", 0, AttrMPReach, "MP_REACH_NLRI next hop of 12 bytes"}}
	if r := p.MPReach; r == nil || !r.Abbreviated || r.NextHop.IsValid() || len(p.Unknown()) != 0 || !slices.Equal(d, want) {
		t.Fatalf("%+v %v, want %v", r, d, want)
	}
}

func TestMPReachMalformed(t *testing.T) {
	for _, v := range [][]byte{
		{},
		{0, 2, 1, 0},           // no reserved byte
		{0, 2, 1, 16, 1, 2, 3}, // next hop runs past
		{0, 2, 1, 255, 0},      // huge next hop length
	} {
		p, d := decodeMP(t, Context{}, 14, v...)
		if p.MPReach != nil || len(p.Unknown()) != 1 || len(d) != 1 || d[0].Code != "bgp/attr-length" || d[0].Attr != AttrMPReach {
			t.Errorf("%x: %+v %v", v, p, d)
		}
	}
}

func TestMPReachAddPath(t *testing.T) {
	v := concat([]byte{0, 1, 1, 4, 192, 0, 2, 1, 0}, []byte{0, 0, 0, 9, 0x18, 10, 0, 0})
	p, d := decodeMP(t, Context{AddPath: true}, 14, v...)
	if len(d) != 0 || len(p.MPReach.NLRI) != 1 || p.MPReach.NLRI[0].PathID != 9 || !p.MPReach.NLRI[0].HasPathID {
		t.Fatalf("%+v %v", p.MPReach, d)
	}
}

func TestMPReachNLRIErrorKeepsPrefixes(t *testing.T) {
	v := concat([]byte{0, 1, 1, 4, 192, 0, 2, 1, 0}, []byte{8, 10, 40})
	p, d := decodeMP(t, Context{}, 14, v...)
	if p.MPReach == nil || len(p.MPReach.NLRI) != 1 || len(d) != 1 || d[0].Code != "bgp/nlri-length" || d[0].Attr != AttrMPReach {
		t.Fatalf("%+v %v", p.MPReach, d)
	}
}

func TestMPUnknownFamilyKeptRaw(t *testing.T) {
	p, d := decodeMP(t, Context{}, 14, 0, 1, 128, 4, 1, 2, 3, 4, 0, 9, 9)
	r := p.MPReach
	if len(d) != 0 || r == nil || r.NLRI != nil || string(r.RawNLRI) != "\x09\x09" {
		t.Fatalf("%+v %v", r, d)
	}
	p, d = decodeMP(t, Context{}, 15, 0, 1, 128, 7, 7)
	if len(d) != 0 || p.MPUnreach() == nil || p.MPUnreach().Withdrawn != nil || string(p.MPUnreach().RawNLRI) != "\x07\x07" {
		t.Fatalf("%+v %v", p.MPUnreach(), d)
	}
}

func TestMPUnreach(t *testing.T) {
	p, d := decodeMP(t, Context{}, 15, 0, 2, 1, 32, 0x20, 1, 0x0d, 0xb8)
	u := p.MPUnreach()
	if len(d) != 0 || u == nil || u.Family != (Family{2, 1}) || len(u.Withdrawn) != 1 ||
		u.Withdrawn[0].Prefix != netip.MustParsePrefix("2001:db8::/32") {
		t.Fatalf("%+v %v", u, d)
	}
	// End-of-RIB form: family only
	p, d = decodeMP(t, Context{}, 15, 0, 2, 1)
	if len(d) != 0 || p.MPUnreach() == nil || len(p.MPUnreach().Withdrawn) != 0 {
		t.Fatalf("%+v %v", p.MPUnreach(), d)
	}
	p, d = decodeMP(t, Context{}, 15, 0, 2)
	if p.MPUnreach() != nil || len(p.Unknown()) != 1 || d[0].Code != "bgp/attr-length" {
		t.Fatalf("%+v %v", p, d)
	}
}

func TestMPDuplicate(t *testing.T) {
	one := attr(0x80, 15, 0, 2, 1)
	p, d := NewAttrs(concat(one, one), Context{}).Decode()
	if p.MPUnreach() == nil || len(p.Unknown()) != 1 || len(d) != 1 || d[0].Code != "bgp/attr-duplicate" {
		t.Fatalf("%+v %v", p, d)
	}
}
