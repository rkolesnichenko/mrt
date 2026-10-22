package bmp

import (
	"net/netip"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// peerOf parses a Route Monitoring with p's per-peer header and returns the
// header as read.
func peerOf(t *testing.T, p testPeer) PeerHeader {
	t.Helper()
	m, err := ParseMessage(bmpMsg(0, p.bytes(), emptyUpdate))
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	return m.(RouteMonitoring).Peer
}

func TestPeerHeaderFlags(t *testing.T) {
	var addr [16]byte
	copy(addr[:], []byte{0x20, 0x01, 0x0d, 0xb8})
	copy(addr[12:], []byte{192, 0, 2, 9})
	v4, v6 := netip.MustParseAddr("192.0.2.9"), netip.AddrFrom16(addr)
	views := map[[2]bool]mrt.View{ // by (O, L)
		{false, false}: mrt.ViewAdjRIBInPre,
		{false, true}:  mrt.ViewAdjRIBInPost,
		{true, false}:  mrt.ViewAdjRIBOutPre,
		{true, true}:   mrt.ViewAdjRIBOutPost,
	}
	for typ := byte(0); typ <= 2; typ++ {
		for combo := 0; combo < 16; combo++ {
			v, l, a, o := combo&8 != 0, combo&4 != 0, combo&2 != 0, combo&1 != 0
			var flags byte
			for _, f := range []struct {
				set bool
				bit byte
			}{{v, 0x80}, {l, 0x40}, {a, 0x20}, {o, 0x10}} {
				if f.set {
					flags |= f.bit
				}
			}
			for _, extra := range []byte{0, 0x08, 0x07, 0x0F} { // bit 4 (deprecated F) and the reserved bits
				p := defaultPeer()
				p.typ, p.flags, p.addr, p.dist = typ, flags|extra, addr, 0x0102030405060708
				h := peerOf(t, p)
				wantAddr := v4
				if v {
					wantAddr = v6
				}
				if h.Type != mrt.PeerType(typ) || h.Flags != flags|extra || h.Addr != wantAddr {
					t.Errorf("type %d flags %#x: Type %d Flags %#x Addr %v, want Addr %v", typ, flags|extra, h.Type, h.Flags, h.Addr, wantAddr)
				}
				if got := h.View(); got != views[[2]bool{o, l}] {
					t.Errorf("type %d flags %#x: View %v, want %v", typ, flags|extra, got, views[[2]bool{o, l}])
				}
				if as4, known := h.AS4(); as4 != !a || !known {
					t.Errorf("type %d flags %#x: AS4 (%v, %v), want (%v, true)", typ, flags|extra, as4, known, !a)
				}
				if h.Filtered() {
					t.Errorf("type %d flags %#x: Filtered for a non-Loc-RIB peer", typ, flags|extra)
				}
				want := mrt.Peer{BGPID: netip.MustParseAddr("192.0.2.9"), Addr: wantAddr, AS: 64500,
					Type: mrt.PeerType(typ), Distinguisher: 0x0102030405060708}
				if got := h.Peer(); got != want {
					t.Errorf("type %d flags %#x: Peer %+v, want %+v", typ, flags|extra, got, want)
				}
			}
		}
	}
}

func TestPeerHeaderLocRIB(t *testing.T) {
	for _, flags := range []byte{0x80, 0xA0, 0x00, 0x20} {
		p := defaultPeer()
		p.typ, p.flags = 3, flags
		for i := range p.addr {
			p.addr[i] = 0xAB // non-zero bytes where Loc-RIB has no address
		}
		h := peerOf(t, p)
		if got := h.Filtered(); got != (flags&0x80 != 0) {
			t.Errorf("flags %#x: Filtered %v", flags, got)
		}
		if h.Addr != (netip.Addr{}) {
			t.Errorf("flags %#x: Addr %v, want the zero Addr", flags, h.Addr)
		}
		if got := h.View(); got != mrt.ViewLocRIB {
			t.Errorf("flags %#x: View %v, want loc-rib", flags, got)
		}
		if as4, known := h.AS4(); !as4 || !known {
			t.Errorf("flags %#x: AS4 (%v, %v), want (true, true)", flags, as4, known)
		}
		if got := h.Peer(); got.Type != mrt.PeerLocRIB || got.Addr != (netip.Addr{}) || got.AS != 64500 {
			t.Errorf("flags %#x: Peer %+v", flags, got)
		}
	}
}

func TestPeerHeaderUnknownType(t *testing.T) {
	p := defaultPeer()
	p.typ, p.flags = 7, 0xF0
	h := peerOf(t, p)
	if h.Type != 7 || h.View() != 0 || h.Addr != (netip.Addr{}) || h.Filtered() {
		t.Errorf("Type %d View %v Addr %v Filtered %v", h.Type, h.View(), h.Addr, h.Filtered())
	}
	if as4, known := h.AS4(); as4 || known {
		t.Errorf("AS4 (%v, %v), want (false, false)", as4, known)
	}
	if h.AS != bgp.ASN(64500) || h.BGPID != netip.MustParseAddr("192.0.2.9") {
		t.Errorf("AS %d BGPID %v", h.AS, h.BGPID)
	}
}

func TestPeerHeaderTime(t *testing.T) {
	p := defaultPeer()
	p.sec, p.usec = 0, 0
	if h := peerOf(t, p); !h.Time.IsZero() {
		t.Errorf("0/0: Time %v, want zero", h.Time)
	}
	p.sec, p.usec = 1700000000, 250000
	h := peerOf(t, p)
	want := time.Date(2023, 11, 14, 22, 13, 20, 250_000_000, time.UTC)
	if !h.Time.Equal(want) || h.Time.Location() != time.UTC {
		t.Errorf("Time %v (%v), want %v UTC", h.Time, h.Time.Location(), want)
	}
}
