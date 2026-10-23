package bmp

import (
	"encoding/binary"
	"net/netip"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// Peer flags for peer types 0-2 (RFC 7854 section 4.2, RFC 8671 section 4).
const (
	flagV = 0x80 // the peer address is IPv6
	flagL = 0x40 // post-policy
	flagA = 0x20 // legacy 2-byte AS_PATH
	flagO = 0x10 // Adj-RIB-Out
)

// flagF is the Loc-RIB peer flag (RFC 9069 section 4.2): the Loc-RIB is filtered.
const flagF = 0x80

// peerHeaderLen is the length of the per-peer header.
const peerHeaderLen = 42

// PeerHeader is the per-peer header (RFC 7854 section 4.2) every message
// but Initiation and Termination carries.
type PeerHeader struct {
	Type          mrt.PeerType
	Flags         uint8 // as on the wire
	Distinguisher uint64
	Addr          netip.Addr // types 0-2: IPv4 or IPv6 by the V flag; zero for Loc-RIB and unknown types
	AS            bgp.ASN
	BGPID         netip.Addr
	Time          time.Time // UTC; zero when both timestamp fields are 0 ("unavailable")
}

// parsePeerHeader reads the per-peer header from the front of b, which holds
// at least peerHeaderLen bytes.
func parsePeerHeader(b []byte) PeerHeader {
	h := PeerHeader{
		Type:          mrt.PeerType(b[0]),
		Flags:         b[1],
		Distinguisher: binary.BigEndian.Uint64(b[2:]),
		AS:            bgp.ASN(binary.BigEndian.Uint32(b[26:])),
		BGPID:         netip.AddrFrom4([4]byte(b[30:34])),
	}
	h.Addr = h.addrOf(b[10:26])
	if sec, usec := binary.BigEndian.Uint32(b[34:]), binary.BigEndian.Uint32(b[38:]); sec != 0 || usec != 0 {
		h.Time = time.Unix(int64(sec), int64(usec)*1000).UTC()
	}
	return h
}

// addrOf reads a 16-byte address field the way the peer address is read:
// IPv6 when V is set, otherwise IPv4 in the last 4 bytes, for types 0-2;
// the zero Addr for Loc-RIB and unknown types.
func (h PeerHeader) addrOf(b []byte) netip.Addr {
	if h.Type > mrt.PeerLocal {
		return netip.Addr{}
	}
	if h.Flags&flagV != 0 {
		return netip.AddrFrom16([16]byte(b[:16]))
	}
	return netip.AddrFrom4([4]byte(b[12:16]))
}

// View returns the RIB the message is about: from the O and L flags for
// types 0-2, ViewLocRIB for Loc-RIB, and zero for unknown types.
func (h PeerHeader) View() mrt.View {
	switch {
	case h.Type == mrt.PeerLocRIB:
		return mrt.ViewLocRIB
	case h.Type > mrt.PeerLocRIB:
		return 0
	}
	switch o, l := h.Flags&flagO != 0, h.Flags&flagL != 0; {
	case !o && !l:
		return mrt.ViewAdjRIBInPre
	case !o && l:
		return mrt.ViewAdjRIBInPost
	case o && !l:
		return mrt.ViewAdjRIBOutPre
	}
	return mrt.ViewAdjRIBOutPost
}

// AS4 reports whether AS numbers in the message's BGP PDUs are 4 bytes wide,
// and whether that is known: !A for types 0-2, true for Loc-RIB (RFC 9069
// section 5.4.1), unknown for other types.
func (h PeerHeader) AS4() (as4, known bool) {
	switch {
	case h.Type == mrt.PeerLocRIB:
		return true, true
	case h.Type > mrt.PeerLocRIB:
		return false, false
	}
	return h.Flags&flagA == 0, true
}

// Filtered reports the Loc-RIB F flag: the Loc-RIB is filtered. It is false
// for every other peer type.
func (h PeerHeader) Filtered() bool {
	return h.Type == mrt.PeerLocRIB && h.Flags&flagF != 0
}

// Peer returns the header's peer as an mrt.Peer.
func (h PeerHeader) Peer() mrt.Peer {
	return mrt.Peer{BGPID: h.BGPID, Addr: h.Addr, AS: h.AS, Type: h.Type, Distinguisher: h.Distinguisher}
}
