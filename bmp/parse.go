package bmp

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// version is the BMP version this package reads (RFC 7854 section 4.1).
const version = 3

// openBMPMagic starts an OpenBMP binary frame (spec §5.4), the Kafka
// envelope a BMP message may still be wrapped in.
var openBMPMagic = []byte("OBMP")

// parseHeader reads the common header from the front of b, which holds at
// least commonHeaderLen bytes.
func parseHeader(b []byte) Header {
	return Header{Version: b[0], Length: binary.BigEndian.Uint32(b[1:]), Type: Type(b[5])}
}

// ParseMessage reads the one BMP message b holds, such as a Kafka frame's
// payload. len(b) must be the header's Length: shorter is ErrTruncated and
// longer is ErrMalformed. The message aliases b. Its errors are
// *MessageError with Offset 0.
func ParseMessage(b []byte) (Message, error) {
	fail := func(h Header, raw []byte, err error) (Message, error) {
		return nil, &MessageError{Offset: 0, Header: h, Raw: raw, Err: err}
	}
	if bytes.HasPrefix(b, openBMPMagic) {
		return fail(Header{}, b, fmt.Errorf("%w: an OpenBMP frame (OBMP header), not a BMP message; unwrap it with openbmp.Parse", ErrMalformed))
	}
	if len(b) < commonHeaderLen {
		return fail(Header{}, nil, ErrTruncated)
	}
	h := parseHeader(b)
	switch n := uint64(len(b)); {
	case h.Length < commonHeaderLen:
		return fail(h, b, fmt.Errorf("%w: length %d is shorter than the common header", ErrMalformed, h.Length))
	case n < uint64(h.Length):
		return fail(h, nil, ErrTruncated)
	case n > uint64(h.Length):
		return fail(h, b, fmt.Errorf("%w: %d bytes after the message's length %d", ErrMalformed, n-uint64(h.Length), h.Length))
	}
	m, err := decode(h, b[:len(b):len(b)])
	if err != nil {
		return fail(h, b, err)
	}
	return m, nil
}

// decode turns one whole framed message (raw: the common header and the
// rest, len(raw) == h.Length >= 6) into its typed form. Only fixed fields are
// read. A non-nil error wraps ErrMalformed.
func decode(h Header, raw []byte) (Message, error) {
	if h.Version != version {
		return nil, fmt.Errorf("%w: version %d", ErrMalformed, h.Version)
	}
	msg := message{hdr: h, raw: raw}
	switch h.Type {
	case TypeInitiation:
		return Initiation{message: msg, info: raw[commonHeaderLen:len(raw):len(raw)]}, nil
	case TypeTermination:
		return Termination{message: msg, info: raw[commonHeaderLen:len(raw):len(raw)]}, nil
	case TypeRouteMonitoring, TypeStatsReport, TypePeerDown, TypePeerUp, TypeRouteMirroring:
	default:
		return Other{msg}, nil
	}
	const off = commonHeaderLen + peerHeaderLen
	if len(raw) < off {
		return nil, short("per-peer header")
	}
	peer := parsePeerHeader(raw[commonHeaderLen:])
	rest := raw[off:len(raw):len(raw)] // every slice of it has cap == len
	switch h.Type {
	case TypeRouteMonitoring:
		return RouteMonitoring{message: msg, Peer: peer, Update: rest}, nil
	case TypeStatsReport:
		if len(rest) < 4 {
			return nil, short("statistics count")
		}
		return StatsReport{message: msg, Peer: peer, Count: binary.BigEndian.Uint32(rest), stats: rest[4:]}, nil
	case TypePeerDown:
		d := PeerDown{message: msg, Peer: peer}
		if len(rest) > 0 {
			d.Reason, d.Data = PeerDownReason(rest[0]), rest[1:]
		}
		return d, nil
	case TypePeerUp:
		return PeerUp{message: msg, Peer: peer, Data: rest}, nil
	}
	return RouteMirroring{message: msg, Peer: peer, tlvs: rest}, nil
}
