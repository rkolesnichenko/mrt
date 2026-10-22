// Package bmpgen is a test-only BMP encoder. Writing BMP is out of scope for
// the library; tests use this package for the cases real feeds lack.
//
// Every builder encodes exactly what it is given and validates nothing, so
// tests can also build malformed data. Message builders return whole BMP
// messages, version 3, with the length counted.
package bmpgen

import (
	"encoding/binary"
	"net/netip"
	"slices"

	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
)

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// addr16 returns a as the 16-byte field of the per-peer header and of Peer Up:
// IPv4 in the last four bytes, IPv6 in all sixteen, zeros for an invalid address.
func addr16(a netip.Addr) []byte {
	b := make([]byte, 16)
	switch {
	case a.Is4():
		a4 := a.As4()
		copy(b[12:], a4[:])
	case a.Is6():
		a6 := a.As16()
		copy(b, a6[:])
	}
	return b
}

// Message returns a whole version 3 BMP message of type t.
func Message(t bmp.Type, body []byte) []byte {
	b := append([]byte{3}, u32(uint32(6+len(body)))...)
	b = append(b, byte(t))
	return append(b, body...)
}

// PeerHeader returns the 42-byte per-peer header. Flags are written as given;
// the address goes in the last four of sixteen bytes for IPv4, in all sixteen
// for IPv6, and as zeros when h.Addr is invalid. A zero Time is 0 and 0.
func PeerHeader(h bmp.PeerHeader) []byte {
	b := []byte{byte(h.Type), h.Flags}
	b = binary.BigEndian.AppendUint64(b, h.Distinguisher)
	b = append(b, addr16(h.Addr)...)
	b = append(b, u32(uint32(h.AS))...)
	if h.BGPID.Is4() {
		id := h.BGPID.As4()
		b = append(b, id[:]...)
	} else {
		b = append(b, 0, 0, 0, 0)
	}
	var sec, usec uint32
	if !h.Time.IsZero() {
		sec, usec = uint32(h.Time.Unix()), uint32(h.Time.Nanosecond()/1000)
	}
	b = append(b, u32(sec)...)
	return append(b, u32(usec)...)
}

// RouteMonitoring returns a Route Monitoring message carrying update, a whole
// BGP message.
func RouteMonitoring(h bmp.PeerHeader, update []byte) []byte {
	return Message(bmp.TypeRouteMonitoring, append(PeerHeader(h), update...))
}

// PeerUp returns a Peer Up Notification: the local address, the two ports, the
// sent and received OPEN messages, then the Information TLVs.
func PeerUp(h bmp.PeerHeader, local netip.Addr, lport, rport uint16, sent, recv []byte, tlvs ...[]byte) []byte {
	b := append(PeerHeader(h), addr16(local)...)
	b = append(b, u16(lport)...)
	b = append(b, u16(rport)...)
	b = append(b, sent...)
	b = append(b, recv...)
	b = append(b, slices.Concat(tlvs...)...)
	return Message(bmp.TypePeerUp, b)
}

// PeerDown returns a Peer Down Notification with a reason and its data.
func PeerDown(h bmp.PeerHeader, reason uint8, data []byte) []byte {
	return Message(bmp.TypePeerDown, slices.Concat(PeerHeader(h), []byte{reason}, data))
}

// Stats returns a Statistics Report that states count stats, followed by tlvs
// as given (count is not checked against them).
func Stats(h bmp.PeerHeader, count uint32, tlvs ...[]byte) []byte {
	return Message(bmp.TypeStatsReport, slices.Concat(PeerHeader(h), u32(count), slices.Concat(tlvs...)))
}

// Initiation returns an Initiation message made of tlvs.
func Initiation(tlvs ...[]byte) []byte {
	return Message(bmp.TypeInitiation, slices.Concat(tlvs...))
}

// Termination returns a Termination message made of tlvs.
func Termination(tlvs ...[]byte) []byte {
	return Message(bmp.TypeTermination, slices.Concat(tlvs...))
}

// Mirroring returns a Route Mirroring message made of tlvs.
func Mirroring(h bmp.PeerHeader, tlvs ...[]byte) []byte {
	return Message(bmp.TypeRouteMirroring, slices.Concat(PeerHeader(h), slices.Concat(tlvs...)))
}

// TLV returns one TLV: the type, the length of v, and v.
func TLV(typ uint16, v []byte) []byte {
	return slices.Concat(u16(typ), u16(uint16(len(v))), v)
}

// Capability returns one capability: code, length, value.
func Capability(code uint8, v []byte) []byte {
	return slices.Concat([]byte{code, byte(len(v))}, v)
}

// CapMP is the Multiprotocol capability (RFC 4760).
func CapMP(f bgp.Family) []byte {
	return Capability(1, slices.Concat(u16(uint16(f.AFI)), []byte{0, byte(f.SAFI)}))
}

// CapAS4 is the 4-octet AS capability (RFC 6793).
func CapAS4(as bgp.ASN) []byte { return Capability(65, u32(uint32(as))) }

// CapAddPath is the ADD-PATH capability (RFC 7911), one tuple per family.
func CapAddPath(tuples ...bgp.AddPathFamily) []byte {
	var v []byte
	for _, t := range tuples {
		v = append(v, u16(uint16(t.Family.AFI))...)
		v = append(v, byte(t.Family.SAFI), t.Mode)
	}
	return Capability(69, v)
}

// openFixed returns the OPEN fields up to the optional parameters length:
// the marker, a zero length, type 1, version 4, My AS, hold time 90 and the
// BGP identifier. My AS is AS_TRANS when as does not fit in two bytes.
func openFixed(as bgp.ASN, id netip.Addr) []byte {
	my := uint16(bgp.ASTrans)
	if as <= 0xFFFF {
		my = uint16(as)
	}
	b := slices.Repeat([]byte{0xFF}, 16)
	b = append(b, 0, 0, 1, 4)
	b = append(b, u16(my)...)
	b = append(b, u16(90)...)
	id4 := id.As4()
	return append(b, id4[:]...)
}

// finish sets the length of the OPEN message in b.
func finish(b []byte) []byte {
	binary.BigEndian.PutUint16(b[16:], uint16(len(b)))
	return b
}

// Open returns an OPEN message (version 4, hold time 90) with each capability
// in an optional parameter of its own (type 2).
func Open(as bgp.ASN, id netip.Addr, caps ...[]byte) []byte {
	var params []byte
	for _, c := range caps {
		params = append(params, 2, byte(len(c)))
		params = append(params, c...)
	}
	b := append(openFixed(as, id), byte(len(params)))
	return finish(append(b, params...))
}

// OpenExtended is Open in the RFC 9072 form: the non-extended length is 255,
// then type 255 and a 2-byte length, and each parameter has a 2-byte length.
func OpenExtended(as bgp.ASN, id netip.Addr, caps ...[]byte) []byte {
	var params []byte
	for _, c := range caps {
		params = append(params, 2)
		params = append(params, u16(uint16(len(c)))...)
		params = append(params, c...)
	}
	b := append(openFixed(as, id), 255, 255)
	b = append(b, u16(uint16(len(params)))...)
	return finish(append(b, params...))
}

// OBMP wraps bmpMsg in an OBMP v1.7 envelope. It computes Length and
// MessageLen itself. Zero Major and Minor mean 1.7, a zero ObjectType 12 and a
// zero RowCount 1. Flags always carry the router bit and the IPv6 bit of
// RouterIP; any other bit of h.Flags is kept. An IPv4 router IP goes in the
// first 4 bytes of its field.
func OBMP(h openbmp.Header, bmpMsg []byte) []byte {
	if h.Major == 0 && h.Minor == 0 {
		h.Major, h.Minor = 1, 7
	}
	if h.ObjectType == 0 {
		h.ObjectType = openbmp.ObjectBMPRaw
	}
	if h.RowCount == 0 {
		h.RowCount = 1
	}
	flags := h.Flags&^(openbmp.FlagRouter|openbmp.FlagIPv6) | openbmp.FlagRouter
	ip := make([]byte, 16)
	switch {
	case h.RouterIP.Is4():
		a := h.RouterIP.As4()
		copy(ip, a[:])
	case h.RouterIP.IsValid():
		a := h.RouterIP.As16()
		copy(ip, a[:])
		flags |= openbmp.FlagIPv6
	}
	var sec, usec uint32
	if !h.Time.IsZero() {
		sec, usec = uint32(h.Time.Unix()), uint32(h.Time.Nanosecond()/1000)
	}
	b := []byte("OBMP")
	b = append(b, h.Major, h.Minor, 0, 0, 0, 0, 0, 0, flags, h.ObjectType)
	b = append(b, u32(sec)...)
	b = append(b, u32(usec)...)
	b = append(b, h.CollectorHash[:]...)
	b = append(b, u16(uint16(len(h.CollectorAdminID)))...)
	b = append(b, h.CollectorAdminID...)
	b = append(b, h.RouterHash[:]...)
	b = append(b, ip...)
	b = append(b, u16(uint16(len(h.RouterGroup)))...)
	b = append(b, h.RouterGroup...)
	b = append(b, u32(h.RowCount)...)
	binary.BigEndian.PutUint16(b[6:], uint16(len(b)))
	binary.BigEndian.PutUint32(b[8:], uint32(len(bmpMsg)))
	return append(b, bmpMsg...)
}
