package mrt

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt/bgp"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// readOne decodes the only record in in.
func readOne(t *testing.T, in []byte) Record {
	t.Helper()
	recs, errs := collect(t, in, Options{})
	if len(errs) != 0 || len(recs) != 1 {
		t.Fatalf("got %d records, errors %v", len(recs), errs)
	}
	return recs[0]
}

const ts = 1300475700

func TestPeerIndexTable(t *testing.T) {
	// RFC 6396 Appendix A, Figure 18.
	body := unhex(t, `c6 33 64 04  00 00  00 02
		02 c6 33 64 05  c6 33 64 05  00 01 00 05
		02 c0 00 02 21  c0 00 02 21  00 01 00 06`)
	rec := readOne(t, frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, body))
	p, ok := rec.(PeerIndexTable)
	if !ok {
		t.Fatalf("got %T", rec)
	}
	if p.CollectorID != netip.MustParseAddr("198.51.100.4") || p.ViewName != "" || len(p.Peers) != 2 {
		t.Fatalf("%+v", p)
	}
	want := []Peer{
		{netip.MustParseAddr("198.51.100.5"), netip.MustParseAddr("198.51.100.5"), 65541},
		{netip.MustParseAddr("192.0.2.33"), netip.MustParseAddr("192.0.2.33"), 65542},
	}
	for i := range want {
		if p.Peers[i] != want[i] {
			t.Errorf("peer %d = %+v, want %+v", i, p.Peers[i], want[i])
		}
	}
	if p.Header().Type != TypeTableDumpV2 || !bytes.Equal(p.Raw()[12:], body) {
		t.Error("header/raw")
	}

	// Peer types 0 (IPv4, 2-byte AS) and 3 (IPv6, 4-byte AS), with a view name.
	body = cat([]byte{10, 0, 0, 1}, be16(4), []byte("view"), be16(2),
		[]byte{0}, []byte{1, 1, 1, 1}, []byte{192, 0, 2, 9}, be16(64512),
		[]byte{3}, []byte{2, 2, 2, 2}, netip.MustParseAddr("2001:db8::1").AsSlice(), be32(4200000000))
	p = readOne(t, frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, body)).(PeerIndexTable)
	if p.ViewName != "view" || len(p.Peers) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Peers[0] != (Peer{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("192.0.2.9"), 64512}) {
		t.Errorf("peer0 %+v", p.Peers[0])
	}
	if p.Peers[1] != (Peer{netip.MustParseAddr("2.2.2.2"), netip.MustParseAddr("2001:db8::1"), 4200000000}) {
		t.Errorf("peer1 %+v", p.Peers[1])
	}

	// A count of 1000 peers in a short body is malformed.
	body = cat([]byte{10, 0, 0, 1}, be16(0), be16(1000), make([]byte, 30))
	_, errs := collect(t, frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, body), Options{})
	var re *RecordError
	if len(errs) != 1 || !errors.As(errs[0], &re) || !errors.Is(errs[0], ErrMalformed) {
		t.Fatalf("errs %v", errs)
	}
	// View name running past the record.
	body = cat([]byte{10, 0, 0, 1}, be16(50), []byte("short"))
	if _, errs = collect(t, frame(ts, TypeTableDumpV2, SubPeerIndexTable, 0, body), Options{}); len(errs) != 1 {
		t.Fatalf("errs %v", errs)
	}
}

const rfcRIBv6Attrs = `40 01 01 00 50 02 00 0e 02 03 00 00 fb f0 00 00
	fb ff 00 00 fb f6 80 0e 2b 00 02 01 20 20 01 0d
	b8 00 0d 00 ff 00 00 00 00 00 00 01 87 fe 80 00
	00 00 00 00 00 02 12 f2 ff fe 9f 1b 00 00 00 20
	20 01 0d b8`

func TestRIBIPv6Unicast(t *testing.T) {
	attrs := unhex(t, rfcRIBv6Attrs)
	if len(attrs) != 68 {
		t.Fatal(len(attrs))
	}
	body := cat(be32(42), []byte{32, 0x20, 0x01, 0x0d, 0xb8}, be16(1),
		be16(15), be32(ts), be16(68), attrs)
	if len(body) != 87 {
		t.Fatal(len(body))
	}
	rib, ok := readOne(t, frame(ts, TypeTableDumpV2, SubRIBIPv6Unicast, 0, body)).(RIB)
	if !ok {
		t.Fatal("not a RIB")
	}
	if rib.Sequence != 42 || rib.Family != (bgp.Family{AFI: 2, SAFI: 1}) ||
		rib.Prefix != netip.MustParsePrefix("2001:db8::/32") || rib.EntryCount() != 1 || rib.AddPath {
		t.Fatalf("%+v", rib)
	}
	n := 0
	for e, err := range rib.Entries() {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if e.PeerIndex != 15 || !e.Originated.Equal(time.Unix(ts, 0)) || e.Originated.Location() != time.UTC {
			t.Errorf("%+v", e)
		}
		path, _ := e.Attrs.Decode()
		if path.MPReach == nil || path.MPReach.Family != (bgp.Family{AFI: 2, SAFI: 1}) {
			t.Fatalf("MPReach %+v", path.MPReach)
		}
		// The RFC's bytes are the full RFC 4760 form (first byte 0), not the abbreviated one.
		if path.MPReach.Abbreviated || path.MPReach.LinkLocal != netip.MustParseAddr("fe80::212:f2ff:fe9f:1b00") {
			t.Errorf("MPReach %+v", *path.MPReach)
		}
		if path.MPReach.NextHop != netip.MustParseAddr("2001:db8:d:ff::187") {
			t.Errorf("next hop %v", path.MPReach.NextHop)
		}
	}
	if n != 1 {
		t.Errorf("entries %d", n)
	}
}

func TestRIBAbbreviated(t *testing.T) {
	// RFC 6396 section 4.3.4: next hop length and next hop only.
	nh := netip.MustParseAddr("2001:db8::1").AsSlice()
	mp := cat([]byte{0x80, 14, byte(1 + len(nh)), byte(len(nh))}, nh)
	attrs := cat([]byte{0x40, 1, 1, 0}, mp)
	body := cat(be32(1), []byte{32, 0x20, 0x01, 0x0d, 0xb8}, be16(1),
		be16(0), be32(ts), be16(uint16(len(attrs))), attrs)
	rib := readOne(t, frame(ts, TypeTableDumpV2, SubRIBIPv6Unicast, 0, body)).(RIB)
	for e, err := range rib.Entries() {
		if err != nil {
			t.Fatal(err)
		}
		path, _ := e.Attrs.Decode()
		if path.MPReach == nil || !path.MPReach.Abbreviated || path.MPReach.NextHop != netip.MustParseAddr("2001:db8::1") {
			t.Fatalf("%+v", path.MPReach)
		}
	}
}

func TestRIBFamiliesAndMask(t *testing.T) {
	cases := []struct {
		sub  Subtype
		fam  bgp.Family
		plen byte
		pfx  []byte
		want string
	}{
		{SubRIBIPv4Unicast, bgp.Family{AFI: 1, SAFI: 1}, 20, []byte{10, 1, 0xff}, "10.1.240.0/20"},
		{SubRIBIPv4Multicast, bgp.Family{AFI: 1, SAFI: 2}, 0, nil, "0.0.0.0/0"},
		{SubRIBIPv6Multicast, bgp.Family{AFI: 2, SAFI: 2}, 8, []byte{0xff}, "ff00::/8"},
		{SubRIBIPv4UnicastAddPath, bgp.Family{AFI: 1, SAFI: 1}, 32, []byte{1, 2, 3, 4}, "1.2.3.4/32"},
		{SubRIBIPv6UnicastAddPath, bgp.Family{AFI: 2, SAFI: 1}, 128, netip.IPv6Unspecified().AsSlice(), "::/128"},
	}
	for _, c := range cases {
		body := cat(be32(7), []byte{c.plen}, c.pfx, be16(0))
		rib := readOne(t, frame(ts, TypeTableDumpV2, c.sub, 0, body)).(RIB)
		if rib.Family != c.fam || rib.Prefix != netip.MustParsePrefix(c.want) || rib.Sequence != 7 ||
			rib.AddPath != (c.sub >= 8) {
			t.Errorf("sub %d: %+v", c.sub, rib)
		}
		for range rib.Entries() {
			t.Error("entry from an empty RIB")
		}
	}
	// Over-long prefix length, and prefix bytes missing.
	for _, body := range [][]byte{
		cat(be32(7), []byte{33, 1, 2, 3, 4, 5}, be16(0)),
		cat(be32(7), []byte{24, 1, 2}),
		cat(be32(7)),
	} {
		_, errs := collect(t, frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, body), Options{})
		if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
			t.Errorf("errs %v", errs)
		}
	}
}

func TestRIBAddPath(t *testing.T) {
	attrs := unhex(t, "40 01 01 00")
	entry := cat(be16(3), be32(ts), be32(42), be16(uint16(len(attrs))), attrs)
	body := cat(be32(9), []byte{24, 192, 0, 2}, be16(2), entry, entry)
	rib := readOne(t, frame(ts, TypeTableDumpV2, SubRIBIPv4UnicastAddPath, 0, body)).(RIB)
	n := 0
	for e, err := range rib.Entries() {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if e.PeerIndex != 3 || e.PathID != 42 || len(e.Attrs.Raw()) != 4 {
			t.Errorf("%+v", e)
		}
	}
	if n != 2 {
		t.Errorf("entries %d", n)
	}
}

func TestRIBGeneric(t *testing.T) {
	attrs := unhex(t, "40 01 01 00")
	entry := cat(be16(1), be32(ts), be16(4), attrs)

	// AFI 1 SAFI 1, one NLRI 192.0.2.0/24.
	body := cat(be32(5), be16(1), []byte{1}, unhex(t, "18 C0 00 02"), be16(1), entry)
	rib := readOne(t, frame(ts, TypeTableDumpV2, SubRIBGeneric, 0, body)).(RIB)
	if rib.Family != (bgp.Family{AFI: 1, SAFI: 1}) || rib.Prefix != netip.MustParsePrefix("192.0.2.0/24") ||
		rib.Sequence != 5 || rib.AddPath || rib.EntryCount() != 1 || !bytes.Equal(rib.RawNLRI, unhex(t, "18 C0 00 02")) {
		t.Fatalf("%+v", rib)
	}
	n := 0
	for e, err := range rib.Entries() {
		if err != nil || e.PathID != 0 {
			t.Fatal(e, err)
		}
		n++
	}
	if n != 1 {
		t.Errorf("entries %d", n)
	}

	// SAFI 128: not plain prefixes; every byte after the SAFI is RawNLRI.
	rest := unhex(t, "00 01 02 03 04 05")
	body = cat(be32(6), be16(1), []byte{128}, rest)
	rib = readOne(t, frame(ts, TypeTableDumpV2, SubRIBGeneric, 0, body)).(RIB)
	if rib.Family != (bgp.Family{AFI: 1, SAFI: 128}) || rib.Prefix.IsValid() || !bytes.Equal(rib.RawNLRI, rest) || rib.EntryCount() != 0 {
		t.Fatalf("%+v", rib)
	}
	for range rib.Entries() {
		t.Error("entry")
	}

	// RIB_GENERIC_ADDPATH: path ID in the NLRI, none in the entries.
	body = cat(be32(8), be16(1), []byte{1}, unhex(t, "00 00 00 05 18 C0 00 02"), be16(1), entry)
	rib = readOne(t, frame(ts, TypeTableDumpV2, SubRIBGenericAddPath, 0, body)).(RIB)
	if !rib.AddPath || rib.PathID != 5 || rib.Prefix != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatalf("%+v", rib)
	}
	n = 0
	for e, err := range rib.Entries() {
		if err != nil || e.PeerIndex != 1 || e.PathID != 0 || len(e.Attrs.Raw()) != 4 {
			t.Fatal(e, err)
		}
		n++
	}
	if n != 1 {
		t.Errorf("entries %d", n)
	}

	// Malformed NLRI: length over the family's width; truncated.
	for _, nlri := range [][]byte{{33, 1, 2, 3, 4, 5}, {24, 1}, {}} {
		body = cat(be32(5), be16(1), []byte{1}, nlri)
		_, errs := collect(t, frame(ts, TypeTableDumpV2, SubRIBGeneric, 0, body), Options{})
		if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
			t.Errorf("%x: errs %v", nlri, errs)
		}
	}
}

func TestRIBEntriesPartial(t *testing.T) {
	attrs := unhex(t, "40 01 01 00")
	good := cat(be16(1), be32(ts), be16(4), attrs)
	bad := cat(be16(2), be32(ts), be16(200), attrs) // attribute length runs past the record
	body := cat(be32(1), []byte{8, 10}, be16(3), good, bad, good)
	in := frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, body)
	rib := readOne(t, in).(RIB)
	var got []uint16
	var errs []error
	for pass := 0; pass < 2; pass++ { // re-iterable
		got, errs = nil, nil
		for e, err := range rib.Entries() {
			if err != nil {
				errs = append(errs, err)
				continue
			}
			got = append(got, e.PeerIndex)
		}
		if len(got) != 1 || got[0] != 1 || len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
			t.Fatalf("pass %d: %v %v", pass, got, errs)
		}
	}
	// Entry header cut short, and a count above the entries present.
	for _, b := range [][]byte{
		cat(be32(1), []byte{8, 10}, be16(1), good[:5]),
		cat(be32(1), []byte{8, 10}, be16(2), good),
	} {
		rib = readOne(t, frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, b)).(RIB)
		var n, ne int
		for _, err := range rib.Entries() {
			if err != nil {
				ne++
			} else {
				n++
			}
		}
		if ne != 1 || n > 1 {
			t.Errorf("n=%d errors=%d", n, ne)
		}
	}
	// Trailing bytes after EntryCount entries are ignored.
	body = cat(be32(1), []byte{8, 10}, be16(1), good, []byte{9, 9, 9})
	rib = readOne(t, frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, body)).(RIB)
	n := 0
	for _, err := range rib.Entries() {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1 {
		t.Errorf("entries %d", n)
	}
}

func TestEntriesStopEarly(t *testing.T) {
	good := cat(be16(1), be32(ts), be16(0))
	body := cat(be32(1), []byte{8, 10}, be16(2), good, good)
	rib := readOne(t, frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, body)).(RIB)
	n := 0
	for range rib.Entries() {
		n++
		break
	}
	if n != 1 {
		t.Error(n)
	}
}

func TestLegacyTableDump(t *testing.T) {
	// AS_PATH with 2-byte ASNs: SEQ[65000 65001].
	attrs := unhex(t, "40 01 01 00  40 02 06 02 02 fd e8 fd e9  40 03 04 c0 00 02 01")
	body := cat(be16(0), be16(5), []byte{10, 0, 0, 0}, []byte{8, 1}, be32(ts),
		[]byte{192, 0, 2, 1}, be16(65000), be16(uint16(len(attrs))), attrs)
	td, ok := readOne(t, frame(ts, TypeTableDump, SubAFIIPv4, 0, body)).(TableDump)
	if !ok {
		t.Fatal("not a TableDump")
	}
	if td.View != 0 || td.Sequence != 5 || td.Prefix != netip.MustParsePrefix("10.0.0.0/8") || td.Status != 1 ||
		!td.Originated.Equal(time.Unix(ts, 0)) ||
		td.Peer != (Peer{Addr: netip.MustParseAddr("192.0.2.1"), AS: 65000}) {
		t.Fatalf("%+v", td)
	}
	path, diags := td.Attrs.Decode()
	if len(diags) != 0 || path.ASPath.String() != "65000 65001" {
		t.Fatalf("%q %v", path.ASPath.String(), diags)
	}
	if len(path.ASPath.Segments) != 1 || path.ASPath.Segments[0].Type != bgp.ASSequence {
		t.Errorf("%+v", path.ASPath)
	}

	// IPv6, with the prefix masked.
	p6 := netip.MustParseAddr("2001:db8:ffff::").AsSlice()
	body = cat(be16(1), be16(6), p6, []byte{32, 1}, be32(ts),
		netip.MustParseAddr("2001:db8::2").AsSlice(), be16(65001), be16(0))
	td = readOne(t, frame(ts, TypeTableDump, SubAFIIPv6, 0, body)).(TableDump)
	if td.Prefix != netip.MustParsePrefix("2001:db8::/32") || td.View != 1 ||
		td.Peer.Addr != netip.MustParseAddr("2001:db8::2") || td.Peer.AS != 65001 {
		t.Fatalf("%+v", td)
	}

	// Any shortfall, an unknown family, or a prefix length too large.
	full := cat(be16(0), be16(5), []byte{10, 0, 0, 0}, []byte{8, 1}, be32(ts),
		[]byte{192, 0, 2, 1}, be16(65000), be16(4), attrs[:4])
	for i := 0; i < len(full); i++ {
		_, errs := collect(t, frame(ts, TypeTableDump, SubAFIIPv4, 0, full[:i]), Options{})
		if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
			t.Fatalf("cut at %d: %v", i, errs)
		}
	}
	if recs, errs := collect(t, frame(ts, TypeTableDump, SubAFIIPv4, 0, full), Options{}); len(errs) != 0 || len(recs) != 1 {
		t.Fatal(errs)
	}
	bad := bytes.Clone(full)
	bad[8] = 33
	if _, errs := collect(t, frame(ts, TypeTableDump, SubAFIIPv4, 0, bad), Options{}); len(errs) != 1 {
		t.Errorf("prefix length 33: %v", errs)
	}
	if recs, errs := collect(t, frame(ts, TypeTableDump, 7, 0, full), Options{}); len(errs) != 0 {
		t.Error(errs)
	} else if _, ok := recs[0].(Other); !ok {
		t.Errorf("%T", recs[0])
	}
}

func TestOtherTableDumpV2Subtypes(t *testing.T) {
	for _, sub := range []Subtype{SubGeoPeerTable, 0, 13, 99} {
		rec := readOne(t, frame(ts, TypeTableDumpV2, sub, 0, []byte{1, 2, 3}))
		if _, ok := rec.(Other); !ok {
			t.Errorf("subtype %d: %T", sub, rec)
		}
	}
}

func TestMalformedRecordKeepsReading(t *testing.T) {
	bad := frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, []byte{0, 0, 0, 1, 40, 1})
	good := frame(ts, TypeTableDumpV2, SubRIBIPv4Unicast, 0, cat(be32(2), []byte{8, 10}, be16(0)))
	in := cat(bad, good)
	var recs []Record
	var errs []error
	for rec, err := range Records(bytes.NewReader(in)) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs = append(recs, rec)
	}
	if len(errs) != 1 || len(recs) != 1 {
		t.Fatalf("%d records, errors %v", len(recs), errs)
	}
	var re *RecordError
	if !errors.As(errs[0], &re) || !errors.Is(errs[0], ErrMalformed) {
		t.Fatalf("%T %v", errs[0], errs[0])
	}
	if !bytes.Equal(re.Raw, bad) || re.Offset != 0 || re.Header.Type != TypeTableDumpV2 {
		t.Errorf("%+v", re)
	}
	if rib := recs[0].(RIB); rib.Sequence != 2 {
		t.Errorf("%+v", rib)
	}
}
