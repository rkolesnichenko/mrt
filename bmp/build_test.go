package bmp

import "encoding/binary"

// Small byte builders for the in-package tests.

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// bmpMsg builds a version 3 message of type typ: the common header, with the
// length counted, then body.
func bmpMsg(typ byte, body ...[]byte) []byte {
	b := cat(body...)
	h := make([]byte, 6, 6+len(b))
	h[0] = 3
	binary.BigEndian.PutUint32(h[1:], uint32(6+len(b)))
	h[5] = typ
	return append(h, b...)
}

// testPeer is a per-peer header's fields as on the wire.
type testPeer struct {
	typ, flags byte
	dist       uint64
	addr       [16]byte
	as         uint32
	bgpID      [4]byte
	sec, usec  uint32
}

// defaultPeer is a global instance IPv4 peer 192.0.2.9, AS 64500, BGP ID
// 192.0.2.9, at 1700000000.000250.
func defaultPeer() testPeer {
	p := testPeer{as: 64500, bgpID: [4]byte{192, 0, 2, 9}, sec: 1700000000, usec: 250}
	copy(p.addr[12:], []byte{192, 0, 2, 9})
	return p
}

func (p testPeer) bytes() []byte {
	b := make([]byte, 42)
	b[0], b[1] = p.typ, p.flags
	binary.BigEndian.PutUint64(b[2:], p.dist)
	copy(b[10:26], p.addr[:])
	binary.BigEndian.PutUint32(b[26:], p.as)
	copy(b[30:34], p.bgpID[:])
	binary.BigEndian.PutUint32(b[34:], p.sec)
	binary.BigEndian.PutUint32(b[38:], p.usec)
	return b
}

// tlv builds a BMP TLV: 2-byte type, 2-byte length, value.
func tlv(typ uint16, val ...byte) []byte {
	b := make([]byte, 4, 4+len(val))
	binary.BigEndian.PutUint16(b, typ)
	binary.BigEndian.PutUint16(b[2:], uint16(len(val)))
	return append(b, val...)
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// bgpMsg builds a BGP message of type typ: marker, length, type, body.
func bgpMsg(typ byte, body ...byte) []byte {
	n := 19 + len(body)
	m := make([]byte, 16, n)
	for i := range m {
		m[i] = 0xFF
	}
	return append(append(m, byte(n>>8), byte(n), typ), body...)
}

// openMsg builds an OPEN with My AS 23456, hold time 90, BGP ID 192.0.2.1 and
// the given already-framed optional parameters (bgp's open_test.go openMsg).
func openMsg(params ...[]byte) []byte {
	p := cat(params...)
	body := []byte{4, 0x5b, 0xa0, 0, 90, 192, 0, 2, 1, byte(len(p))}
	return bgpMsg(1, append(body, p...)...)
}

// capParam wraps capabilities in a type 2 optional parameter.
func capParam(caps ...[]byte) []byte {
	v := cat(caps...)
	return append([]byte{2, byte(len(v))}, v...)
}

func capTLV(code byte, val ...byte) []byte { return append([]byte{code, byte(len(val))}, val...) }

var (
	capMP  = capTLV(1, 0, 1, 0, 1)
	capMP6 = capTLV(1, 0, 2, 0, 1)
	capAS4 = capTLV(65, 0xfa, 0x56, 0xea, 0x00) // 4200000000
)

// emptyUpdate is an UPDATE with no withdrawals, attributes or NLRI (End-of-RIB).
var emptyUpdate = bgpMsg(2, 0, 0, 0, 0)
