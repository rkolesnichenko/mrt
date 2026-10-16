package mrt

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt/bgp"
)

// appendixUpdate is the BGP UPDATE of RFC 6396 Appendix A, Figure 16 (4-byte
// AS_PATH 64496 64511 64502). The RFC prints the total path attribute length
// as 0x1f; the attributes it lists are 0x23 bytes and fill the message length
// 0x3e exactly, so 0x23 is used here.
const appendixUpdate = `ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff
	00 3e 02 00 00 00 23 40 01 01 02 40 02 0e 02 03
	00 00 fb f0 00 00 fb ff 00 00 fb f6 40 03 04 c6
	33 64 55 c0 08 04 fb f0 00 0e 18 cb 00 71`

func as4Body(t *testing.T, msg []byte) []byte {
	return cat(be32(64496), be32(64497), be16(0), be16(1),
		[]byte{192, 0, 2, 85}, []byte{198, 51, 100, 4}, msg)
}

func TestBGP4MPSubtypeFlags(t *testing.T) {
	tests := []struct {
		sub                 Subtype
		as4, local, addPath bool
	}{
		{SubMessage, false, false, false},
		{SubMessageAS4, true, false, false},
		{SubMessageLocal, false, true, false},
		{SubMessageAS4Local, true, true, false},
		{SubMessageAddPath, false, false, true},
		{SubMessageAS4AddPath, true, false, true},
		{SubMessageLocalAddPath, false, true, true},
		{SubMessageAS4LocalAddPath, true, true, true},
	}
	msg := unhex(t, appendixUpdate)
	for _, tc := range tests {
		w := 2
		if tc.as4 {
			w = 4
		}
		var body []byte
		if w == 4 {
			body = as4Body(t, msg)
		} else {
			body = cat(be16(64496), be16(64497), be16(7), be16(1),
				[]byte{192, 0, 2, 85}, []byte{198, 51, 100, 4}, msg)
		}
		m, ok := readOne(t, frame(ts, TypeBGP4MP, tc.sub, 0, body)).(BGP4MPMessage)
		if !ok {
			t.Fatalf("subtype %d: not a BGP4MPMessage", tc.sub)
		}
		if m.AS4 != tc.as4 || m.Local != tc.local || m.AddPath != tc.addPath {
			t.Errorf("subtype %d: flags %v %v %v", tc.sub, m.AS4, m.Local, m.AddPath)
		}
		if got, want := m.Context(), (bgp.Context{AS4: tc.as4, AddPath: tc.addPath}); got != want {
			t.Errorf("subtype %d: Context %+v, want %+v", tc.sub, got, want)
		}
		if m.PeerAS != 64496 || m.LocalAS != 64497 || m.PeerAddr != netip.MustParseAddr("192.0.2.85") ||
			m.LocalAddr != netip.MustParseAddr("198.51.100.4") || string(m.Message) != string(msg) {
			t.Errorf("subtype %d: %+v", tc.sub, m)
		}
	}
}

func TestBGP4MPMessageAS4Appendix(t *testing.T) {
	msg := unhex(t, appendixUpdate)
	rec := readOne(t, frame(ts, TypeBGP4MP, SubMessageAS4, 0, as4Body(t, msg)))
	m, ok := rec.(BGP4MPMessage)
	if !ok {
		t.Fatalf("got %T", rec)
	}
	if m.Header().Length != 82 || m.PeerAS != 64496 || m.LocalAS != 64497 || m.IfIndex != 0 ||
		m.PeerAddr != netip.MustParseAddr("192.0.2.85") || m.LocalAddr != netip.MustParseAddr("198.51.100.4") {
		t.Fatalf("%+v", m)
	}
	if mt, err := m.Type(); err != nil || mt != bgp.MessageUpdate {
		t.Fatalf("Type = %v, %v", mt, err)
	}
	u, diags := bgp.ParseUpdate(m.Message, m.Context())
	if len(diags) != 0 {
		t.Fatalf("diags %v", diags)
	}
	p, diags := u.Attrs.Decode()
	if len(diags) != 0 {
		t.Fatalf("diags %v", diags)
	}
	want := []bgp.ASN{64496, 64511, 64502}
	if len(p.ASPath.Segments) != 1 || p.ASPath.Segments[0].Type != bgp.ASSequence {
		t.Fatalf("path %+v", p.ASPath)
	}
	for i, a := range p.ASPath.Segments[0].ASNs {
		if a != want[i] {
			t.Fatalf("path %+v", p.ASPath)
		}
	}
}

func TestBGP4MPMessage2ByteAS(t *testing.T) {
	attrs := unhex(t, `40 01 01 00  40 02 06 02 02 0d 1c 05 13  40 03 04 c0 00 02 01`)
	nlri := []byte{24, 203, 0, 113}
	msg := cat(bytesOf(0xff, 16), be16(uint16(19+2+2+len(attrs)+len(nlri))), []byte{2},
		be16(0), be16(uint16(len(attrs))), attrs, nlri)
	body := cat(be16(3356), be16(12654), be16(0), be16(1),
		[]byte{192, 0, 2, 1}, []byte{192, 0, 2, 2}, msg)
	m := readOne(t, frame(ts, TypeBGP4MP, SubMessage, 0, body)).(BGP4MPMessage)
	u, diags := bgp.ParseUpdate(m.Message, m.Context())
	if len(diags) != 0 {
		t.Fatalf("diags %v", diags)
	}
	p, diags := u.Attrs.Decode()
	if len(diags) != 0 {
		t.Fatalf("diags %v", diags)
	}
	s := p.ASPath.Segments
	if len(s) != 1 || s[0].Type != bgp.ASSequence || len(s[0].ASNs) != 2 || s[0].ASNs[0] != 3356 || s[0].ASNs[1] != 1299 {
		t.Fatalf("path %+v", p.ASPath)
	}
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestBGP4MPETTimestamp(t *testing.T) {
	body := as4Body(t, unhex(t, appendixUpdate))
	et := readOne(t, frame(ts, TypeBGP4MPET, SubMessageAS4, 123456, body)).(BGP4MPMessage)
	plain := readOne(t, frame(ts, TypeBGP4MP, SubMessageAS4, 0, body)).(BGP4MPMessage)
	if want := time.Unix(ts, 123456000).UTC(); !et.Header().Time.Equal(want) {
		t.Fatalf("time %v, want %v", et.Header().Time, want)
	}
	if et.PeerAS != plain.PeerAS || et.LocalAS != plain.LocalAS || et.PeerAddr != plain.PeerAddr ||
		et.LocalAddr != plain.LocalAddr || string(et.Message) != string(plain.Message) || et.AS4 != plain.AS4 {
		t.Fatalf("ET %+v differs from plain %+v", et, plain)
	}
}

func TestBGP4MPIPv6Peer(t *testing.T) {
	p6 := netip.MustParseAddr("2001:db8::1")
	l6 := netip.MustParseAddr("2001:db8::2")
	msg := unhex(t, appendixUpdate)
	body := cat(be32(64496), be32(64497), be16(3), be16(2), p6.AsSlice(), l6.AsSlice(), msg)
	m := readOne(t, frame(ts, TypeBGP4MP, SubMessageAS4, 0, body)).(BGP4MPMessage)
	if m.PeerAddr != p6 || m.LocalAddr != l6 || m.IfIndex != 3 || string(m.Message) != string(msg) {
		t.Fatalf("%+v", m)
	}
}

func TestBGP4MPBadMessageIsLazy(t *testing.T) {
	msg := unhex(t, appendixUpdate)
	msg[3] = 0 // break the marker
	recs, errs := collect(t, frame(ts, TypeBGP4MP, SubMessageAS4, 0, as4Body(t, msg)), Options{})
	if len(errs) != 0 || len(recs) != 1 {
		t.Fatalf("records %d, errors %v", len(recs), errs)
	}
	m := recs[0].(BGP4MPMessage)
	if _, err := m.Type(); !errors.Is(err, bgp.ErrMarker) {
		t.Fatalf("Type error %v, want ErrMarker", err)
	}
	m.Message = msg[:5]
	if _, err := m.Type(); !errors.Is(err, bgp.ErrShortMessage) {
		t.Fatalf("Type error %v, want ErrShortMessage", err)
	}
}

func TestBGP4MPStateChange(t *testing.T) {
	for _, tc := range []struct {
		sub Subtype
		as4 bool
		hdr []byte
	}{
		{SubStateChange, false, cat(be16(64496), be16(64497))},
		{SubStateChangeAS4, true, cat(be32(64496), be32(64497))},
	} {
		body := cat(tc.hdr, be16(9), be16(1), []byte{192, 0, 2, 85}, []byte{198, 51, 100, 4},
			be16(uint16(StateOpenConfirm)), be16(uint16(StateEstablished)))
		s, ok := readOne(t, frame(ts, TypeBGP4MP, tc.sub, 0, body)).(BGP4MPStateChange)
		if !ok {
			t.Fatalf("subtype %d: wrong type", tc.sub)
		}
		if s.AS4 != tc.as4 || s.PeerAS != 64496 || s.LocalAS != 64497 || s.IfIndex != 9 ||
			s.PeerAddr != netip.MustParseAddr("192.0.2.85") || s.Old != StateOpenConfirm || s.New != StateEstablished {
			t.Fatalf("subtype %d: %+v", tc.sub, s)
		}
	}
}

func TestBGP4MPOtherAndMalformed(t *testing.T) {
	for _, sub := range []Subtype{2, 3, 12, 99} {
		if _, ok := readOne(t, frame(ts, TypeBGP4MP, sub, 0, []byte{1, 2, 3})).(Other); !ok {
			t.Errorf("subtype %d should be Other", sub)
		}
	}
	good := as4Body(t, unhex(t, appendixUpdate))
	bad := [][]byte{
		good[:10],                               // short fixed fields
		cat(be32(1), be32(2), be16(0), be16(3)), // unknown address family
		cat(be32(1), be32(2), be16(0), be16(2), []byte{1, 2, 3, 4}), // v6 addresses cut short
	}
	for i, body := range bad {
		_, errs := collect(t, frame(ts, TypeBGP4MP, SubMessageAS4, 0, body), Options{})
		if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
			t.Errorf("case %d: errors %v", i, errs)
		}
	}
	// state change missing its states
	_, errs := collect(t, frame(ts, TypeBGP4MP, SubStateChangeAS4, 0, good[:20]), Options{})
	if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
		t.Errorf("state change: %v", errs)
	}
}
