package mrt

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt/bgp"
)

func peerTable(addrs ...string) []byte {
	body := cat([]byte{10, 0, 0, 1}, be16(0), be16(uint16(len(addrs))))
	for i, a := range addrs {
		ip := netip.MustParseAddr(a)
		typ := byte(0)
		if ip.Is6() {
			typ = 1
		}
		body = cat(body, []byte{typ}, []byte{1, 1, 1, byte(i + 1)}, ip.AsSlice(), be16(uint16(64500+i)))
	}
	return frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, body)
}

func ribEntry(peer uint16, attrs []byte) []byte {
	return cat(be16(peer), be32(ts), be16(uint16(len(attrs))), attrs)
}

func ribV4(entries ...[]byte) []byte {
	return frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0,
		cat(be32(1), []byte{8, 10}, be16(uint16(len(entries))), cat(entries...)))
}

func routesOf(t *testing.T, in []byte) (rs []Route, errs []error) {
	t.Helper()
	for r, err := range Routes(Records(bytes.NewReader(in))) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rs = append(rs, r)
	}
	return
}

var origin = []byte{0x40, 0x01, 0x01, 0x00} // ORIGIN IGP

func TestKindString(t *testing.T) {
	if KindRIB.String() != "B" || KindAnnounce.String() != "A" || KindWithdraw.String() != "W" || Kind(0).String() != "?" {
		t.Error("Kind.String")
	}
	// The zero Kind is no route: what an error result carries.
	if KindRIB != 1 || KindAnnounce != 2 || KindWithdraw != 3 {
		t.Errorf("Kind values %d %d %d, want 1 2 3", KindRIB, KindAnnounce, KindWithdraw)
	}
}

func TestRoutesRIB(t *testing.T) {
	in := cat(peerTable("192.0.2.1", "2001:db8::2"), ribV4(ribEntry(0, origin), ribEntry(1, origin)))
	rs, errs := routesOf(t, in)
	if len(errs) != 0 || len(rs) != 2 {
		t.Fatalf("%v %v", rs, errs)
	}
	if rs[0].Kind != KindRIB || rs[0].Peer.Addr != netip.MustParseAddr("192.0.2.1") || rs[1].Peer.Addr != netip.MustParseAddr("2001:db8::2") ||
		rs[1].Peer.AS != 64501 {
		t.Errorf("%+v %+v", rs[0], rs[1])
	}
	if rs[0].Prefix != netip.MustParsePrefix("10.0.0.0/8") || rs[0].Prefix != rs[1].Prefix ||
		rs[0].Family != (bgp.Family{AFI: 1, SAFI: 1}) || rs[0].HasPathID || rs[0].Time.Unix() != ts {
		t.Errorf("%+v", rs[0])
	}
	if rs[0].Path == nil || rs[0].Path == rs[1].Path {
		t.Error("paths must be set and distinct")
	}
}

func TestRoutesRIBAddPath(t *testing.T) {
	e := cat(be16(0), be32(ts), be32(42), be16(4), origin)
	in := cat(peerTable("192.0.2.1"), frame(ts, TypeTableDumpV2, SubRIBIPv4UnicastAddPath, 0,
		cat(be32(1), []byte{8, 10}, be16(1), e)))
	rs, errs := routesOf(t, in)
	if len(errs) != 0 || len(rs) != 1 || rs[0].PathID != 42 || !rs[0].HasPathID {
		t.Fatalf("%+v %v", rs, errs)
	}
}

func TestRoutesUsesLatestPeerTable(t *testing.T) { // Review Focus 1
	in := cat(peerTable("192.0.2.1"), ribV4(ribEntry(0, origin)),
		peerTable("198.51.100.1"), ribV4(ribEntry(0, origin)))
	rs, errs := routesOf(t, in)
	if len(errs) != 0 || len(rs) != 2 {
		t.Fatalf("%v %v", rs, errs)
	}
	if rs[0].Peer.Addr != netip.MustParseAddr("192.0.2.1") || rs[1].Peer.Addr != netip.MustParseAddr("198.51.100.1") {
		t.Errorf("%v %v", rs[0].Peer, rs[1].Peer)
	}
}

func TestRoutesRIBBeforePeerTable(t *testing.T) {
	in := cat(ribV4(ribEntry(0, origin), ribEntry(1, origin)), peerTable("192.0.2.1"),
		ribV4(ribEntry(0, origin), ribEntry(5, origin)))
	rs, errs := routesOf(t, in)
	if len(errs) != 3 || len(rs) != 1 {
		t.Fatalf("%d routes, errors %v", len(rs), errs)
	}
	for _, err := range errs {
		var re *RecordError
		if !errors.As(err, &re) || re.Offset != -1 || !errors.Is(err, ErrMalformed) || re.Header.Type != TypeTableDumpV2 || len(re.Raw) == 0 {
			t.Errorf("%#v", err)
		}
	}
}

func TestRoutesLegacyTableDump(t *testing.T) {
	attrs := unhex(t, "40 01 01 00  40 02 06 02 02 fd e8 fd e9  40 03 04 c0 00 02 01")
	body := cat(be16(0), be16(5), []byte{10, 0, 0, 0}, []byte{8, 1}, be32(ts),
		[]byte{192, 0, 2, 1}, be16(65000), be16(uint16(len(attrs))), attrs)
	rs, errs := routesOf(t, frame(ts, TypeTableDump, SubAFIIPv4, 0, body))
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("%v %v", rs, errs)
	}
	r := rs[0]
	if r.Kind != KindRIB || r.Family != (bgp.Family{AFI: 1, SAFI: 1}) || r.Peer.AS != 65000 ||
		r.Prefix != netip.MustParsePrefix("10.0.0.0/8") || r.Path == nil || r.Path.ASPath.String() != "65000 65001" {
		t.Errorf("%+v", r)
	}
}

// updateMsg builds a BGP UPDATE.
func updateMsg(withdrawn, attrs, nlri []byte) []byte {
	return cat(bytesOf(0xff, 16), be16(uint16(19+2+len(withdrawn)+2+len(attrs)+len(nlri))), []byte{2},
		be16(uint16(len(withdrawn))), withdrawn, be16(uint16(len(attrs))), attrs, nlri)
}

func mpAttr(code byte, afi uint16, safi byte, pre []byte, nlri []byte) []byte {
	v := cat(be16(afi), []byte{safi}, pre, nlri)
	return cat([]byte{0x80, code, byte(len(v))}, v)
}

func bgp4mp(sub Subtype, msg []byte) []byte {
	return frame(ts, TypeBGP4MP, sub, 0, cat(be32(64496), be32(64497), be16(0), be16(1),
		[]byte{192, 0, 2, 85}, []byte{198, 51, 100, 4}, msg))
}

func TestRoutesUpdate(t *testing.T) {
	v6a := cat([]byte{32, 0x20, 0x01, 0x0d, 0xb8})
	v6b := cat([]byte{48, 0x20, 0x01, 0x0d, 0xb8, 0, 1})
	nh := cat([]byte{16}, netip.MustParseAddr("2001:db8::1").AsSlice(), []byte{0})
	attrs := cat(origin, unhex(t, "40 03 04 c0 00 02 01"),
		mpAttr(15, 2, 1, nil, v6a), mpAttr(14, 2, 1, nh, v6b))
	msg := updateMsg([]byte{8, 10}, attrs, []byte{24, 192, 0, 2})
	rs, errs := routesOf(t, bgp4mp(SubMessageAS4, msg))
	if len(errs) != 0 || len(rs) != 4 {
		t.Fatalf("%v %v", rs, errs)
	}
	want := []struct {
		kind Kind
		fam  bgp.Family
		pfx  string
	}{
		{KindWithdraw, bgp.Family{AFI: 1, SAFI: 1}, "10.0.0.0/8"},
		{KindWithdraw, bgp.Family{AFI: 2, SAFI: 1}, "2001:db8::/32"},
		{KindAnnounce, bgp.Family{AFI: 1, SAFI: 1}, "192.0.2.0/24"},
		{KindAnnounce, bgp.Family{AFI: 2, SAFI: 1}, "2001:db8:1::/48"},
	}
	for i, w := range want {
		r := rs[i]
		if r.Kind != w.kind || r.Family != w.fam || r.Prefix != netip.MustParsePrefix(w.pfx) ||
			r.Peer != (Peer{Addr: netip.MustParseAddr("192.0.2.85"), AS: 64496}) || r.Time.Unix() != ts {
			t.Errorf("route %d: %+v", i, r)
		}
	}
	if rs[0].Path != nil || rs[1].Path != nil {
		t.Error("withdrawals must have no Path")
	}
	if rs[2].Path == nil || rs[2].Path != rs[3].Path {
		t.Error("announcements must share one Path")
	}
}

func TestRoutesUpdateAddPath(t *testing.T) {
	msg := updateMsg(nil, origin, cat(be32(7), []byte{24, 192, 0, 2}))
	rs, errs := routesOf(t, bgp4mp(SubMessageAS4AddPath, msg))
	if len(errs) != 0 || len(rs) != 1 || rs[0].PathID != 7 || !rs[0].HasPathID {
		t.Fatalf("%+v %v", rs, errs)
	}
}

func TestRoutesWithdrawOnly(t *testing.T) { // Review Focus 3
	attrs := mpAttr(15, 2, 1, nil, []byte{32, 0x20, 0x01, 0x0d, 0xb8})
	rs, errs := routesOf(t, bgp4mp(SubMessageAS4, updateMsg(nil, attrs, nil)))
	if len(errs) != 0 || len(rs) != 1 || rs[0].Kind != KindWithdraw {
		t.Fatalf("%+v %v", rs, errs)
	}
	if rs[0].Diagnostics != nil || rs[0].Path != nil {
		t.Errorf("diagnostics %v path %v", rs[0].Diagnostics, rs[0].Path)
	}
}

func TestRoutesSkips(t *testing.T) {
	sc := frame(ts, TypeBGP4MP, SubStateChangeAS4, 0, cat(be32(1), be32(2), be16(0), be16(1),
		[]byte{192, 0, 2, 1}, []byte{192, 0, 2, 2}, be16(5), be16(6)))
	keep := cat(bytesOf(0xff, 16), be16(19), []byte{4})
	local := bgp4mp(SubMessageAS4Local, updateMsg(nil, origin, []byte{24, 192, 0, 2}))
	attrs := mpAttr(14, 25, 128, cat([]byte{4}, []byte{1, 2, 3, 4}, []byte{0}), []byte{1, 2, 3})
	vpn := bgp4mp(SubMessageAS4, updateMsg(nil, attrs, nil))
	other := frame(ts, TypeTableDumpV2, SubGeoPeerTable, 0, []byte{9})
	in := cat(peerTable("192.0.2.1"), sc, bgp4mp(SubMessageAS4, keep), local, vpn, other)
	rs, errs := routesOf(t, in)
	if len(rs) != 0 || len(errs) != 0 {
		t.Fatalf("%v %v", rs, errs)
	}
}

func TestRoutesBadMessageType(t *testing.T) {
	bad := cat(bytesOf(0x00, 16), be16(19), []byte{2}) // bad marker
	rs, errs := routesOf(t, cat(bgp4mp(SubMessageAS4, bad), bgp4mp(SubMessageAS4, updateMsg(nil, origin, []byte{24, 192, 0, 2}))))
	if len(rs) != 1 || len(errs) != 1 {
		t.Fatalf("%v %v", rs, errs)
	}
	var re *RecordError
	if !errors.As(errs[0], &re) || re.Offset != -1 || !errors.Is(errs[0], ErrMalformed) || !errors.Is(errs[0], bgp.ErrMarker) {
		t.Errorf("%#v", errs[0])
	}
}

func TestRoutesPassesRecordErrors(t *testing.T) {
	in := cat(frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, []byte{1, 2}), // malformed
		peerTable("192.0.2.1"), ribV4(ribEntry(0, origin)))
	rs, errs := routesOf(t, in)
	var re *RecordError
	if len(rs) != 1 || len(errs) != 1 || !errors.As(errs[0], &re) || re.Offset != 0 {
		t.Fatalf("%v %v", rs, errs)
	}
}

func TestRoutesPartialRIB(t *testing.T) {
	good := cat(be16(0), be32(ts), be16(4), origin)
	bad := cat(be16(0), be32(ts), be16(200), origin)
	body := cat(be32(1), []byte{8, 10}, be16(3), good, bad, good)
	in := cat(peerTable("192.0.2.1"), frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, body))
	var kinds []string
	for r, err := range Routes(Records(bytes.NewReader(in))) {
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("%v", err)
			}
			kinds = append(kinds, "err")
			continue
		}
		kinds = append(kinds, r.Kind.String())
	}
	if len(kinds) != 2 || kinds[0] != "B" || kinds[1] != "err" {
		t.Errorf("%v", kinds)
	}
}

func TestRoutesStopsWhenYieldFalse(t *testing.T) {
	msg := updateMsg([]byte{8, 10, 8, 11}, origin, []byte{24, 192, 0, 2})
	tests := []struct {
		name   string
		in     []byte
		pulled int // records read from the source when the consumer stops at the first result
	}{
		{"update", cat(bgp4mp(SubMessageAS4, msg), bgp4mp(SubMessageAS4, msg)), 1},
		{"rib", cat(peerTable("192.0.2.1"), ribV4(ribEntry(0, origin), ribEntry(0, origin)), ribV4(ribEntry(0, origin))), 2},
		{"error", cat(ribV4(ribEntry(0, origin)), ribV4(ribEntry(0, origin))), 1},
	}
	for _, tc := range tests {
		pulled, results := 0, 0
		src := func(yield func(Record, error) bool) {
			for r, err := range Records(bytes.NewReader(tc.in)) {
				pulled++
				if !yield(r, err) {
					return
				}
			}
		}
		for range Routes(src) {
			results++
			break
		}
		if results != 1 || pulled != tc.pulled {
			t.Errorf("%s: results %d, records pulled %d, want %d", tc.name, results, pulled, tc.pulled)
		}
	}
}

func TestRoutesBrokenPeerTableClearsOld(t *testing.T) {
	broken := frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, []byte{1, 2})
	in := cat(peerTable("192.0.2.1"), ribV4(ribEntry(0, origin)), broken, ribV4(ribEntry(0, origin)))
	rs, errs := routesOf(t, in)
	if len(rs) != 1 || len(errs) != 2 {
		t.Fatalf("%d routes, errors %v", len(rs), errs)
	}
	var re *RecordError
	if !errors.As(errs[1], &re) || re.Offset != -1 || !errors.Is(errs[1], ErrMalformed) {
		t.Errorf("second error %#v, want the R12 error", errs[1])
	}
}

// wantRecordError checks the R12 shape of a Routes error for record rec:
// Offset -1, the record's header and bytes, ErrMalformed, and text containing
// want.
func wantRecordError(t *testing.T, err error, rec []byte, want string) {
	t.Helper()
	var re *RecordError
	if !errors.As(err, &re) {
		t.Fatalf("%#v is not a *RecordError", err)
	}
	if re.Offset != -1 || !bytes.Equal(re.Raw, rec) || re.Header.Length != uint32(len(rec)-12) || !errors.Is(err, ErrMalformed) ||
		!strings.Contains(err.Error(), want) {
		t.Errorf("%v: offset %d, header %+v, raw %d bytes; want offset -1, the record's header and %d bytes, ErrMalformed, %q",
			err, re.Offset, re.Header, len(re.Raw), len(rec), want)
	}
}

// An UPDATE that yields no route but has a Warning or Error diagnostic is a
// RecordError, not silence; one with no such diagnostic stays silent.
func TestRoutesMalformedUpdateWithoutRoutes(t *testing.T) {
	// Withdrawn-routes length 65520 in a message whose header length is right.
	longWithdrawn := cat(bytesOf(0xff, 16), be16(21), []byte{2}, be16(65520))
	tests := []struct {
		name, msg string
		upd       []byte
	}{
		{"withdrawn length", "bgp/update-length: withdrawn routes run past the message", longWithdrawn},
		{"/33 NLRI", "bgp/nlri-length: prefix length 33 exceeds 32", updateMsg(nil, origin, []byte{33, 10, 0, 0, 0, 0})},
		{"malformed MP_REACH", "bgp/attr-length: MP_REACH_NLRI next hop runs past the attribute",
			updateMsg(nil, cat(origin, mpAttr(14, 2, 1, []byte{16, 1, 2}, nil)), nil)},
	}
	for _, tc := range tests {
		rec := bgp4mp(SubMessageAS4, tc.upd)
		rs, errs := routesOf(t, rec)
		if len(rs) != 0 || len(errs) != 1 {
			t.Errorf("%s: %d routes, errors %v; want one error", tc.name, len(rs), errs)
			continue
		}
		wantRecordError(t, errs[0], rec, tc.msg)
	}
	// End-of-RIB, an empty UPDATE, stays silent.
	rs, errs := routesOf(t, bgp4mp(SubMessageAS4, updateMsg(nil, nil, nil)))
	if len(rs) != 0 || len(errs) != 0 {
		t.Errorf("empty UPDATE: %v %v", rs, errs)
	}
}

// An UPDATE that yields routes carries its diagnostics on them, and the
// attribute diagnostics are rebased onto the BGP message.
func TestRoutesUpdateDiagnostics(t *testing.T) {
	// 2 withdrawn bytes; a duplicate ORIGIN at offset 4 of the attribute
	// block, which starts at 19+2+2+2 = 25 in the message; a /33 after a /24,
	// at 25+8+4 = 37.
	msg := updateMsg([]byte{8, 10}, cat(origin, origin), []byte{24, 192, 0, 2, 33, 1, 2, 3, 4, 5})
	rs, errs := routesOf(t, bgp4mp(SubMessageAS4, msg))
	if len(errs) != 0 || len(rs) != 2 {
		t.Fatalf("%v %v", rs, errs)
	}
	want := []bgp.Diagnostic{
		{Severity: bgp.SeverityError, Code: "bgp/nlri-length", Offset: 37, Message: "prefix length 33 exceeds 32"},
		{Severity: bgp.SeverityWarning, Code: "bgp/attr-duplicate", Offset: 29, Attr: bgp.AttrOrigin, Message: "duplicate path attribute ignored"},
	}
	for i, r := range rs {
		if !slices.Equal(r.Diagnostics, want) {
			t.Errorf("route %d (%s): diagnostics %+v, want %+v", i, r.Kind, r.Diagnostics, want)
		}
	}
}

// A RIB entry that runs past its record is a RecordError of that record.
func TestRoutesBrokenRIBEntryIsRecordError(t *testing.T) {
	bad := cat(be16(0), be32(ts), be16(200), origin)
	rib := frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, cat(be32(1), []byte{8, 10}, be16(1), bad))
	rs, errs := routesOf(t, cat(peerTable("192.0.2.1"), rib))
	if len(rs) != 0 || len(errs) != 1 {
		t.Fatalf("%v %v", rs, errs)
	}
	wantRecordError(t, errs[0], rib, "RIB entry 1 attributes (200 bytes) run past the record")
}
