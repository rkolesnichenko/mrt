package mrt

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/rkolesnichenko/mrt/bgp"
)

// State is a BGP finite state machine state (RFC 4271 section 8.2.2), as
// carried by BGP4MP state change records.
type State uint16

// BGP states.
const (
	StateIdle        State = 1
	StateConnect     State = 2
	StateActive      State = 3
	StateOpenSent    State = 4
	StateOpenConfirm State = 5
	StateEstablished State = 6
)

// BGP4MPMessage is a BGP4MP or BGP4MP_ET message record (RFC 6396 section
// 4.4.2 and later, RFC 8050): one BGP message seen on a session. Only the
// fixed fields are read; Message is not validated until Type or
// bgp.ParseUpdate looks at it.
type BGP4MPMessage struct {
	record
	PeerAS, LocalAS     bgp.ASN
	IfIndex             uint16
	PeerAddr, LocalAddr netip.Addr
	AS4, Local, AddPath bool   // from the subtype
	Message             []byte // whole BGP message, marker included; a slice of Raw
}

// BGP4MPStateChange is a BGP4MP or BGP4MP_ET state change record (RFC 6396
// section 4.4.1).
type BGP4MPStateChange struct {
	record
	PeerAS, LocalAS     bgp.ASN
	IfIndex             uint16
	PeerAddr, LocalAddr netip.Addr
	AS4                 bool
	Old, New            State
}

// Context is what bgp.ParseUpdate needs to read Message: the AS width and
// add-path format come from the subtype, never from the bytes.
func (m BGP4MPMessage) Context() bgp.Context {
	return bgp.Context{AS4: m.AS4, AddPath: m.AddPath}
}

// Type returns the BGP message type from Message's header. The error wraps
// bgp.ErrMarker or bgp.ErrShortMessage when the header is broken. A message
// cut short of its header's length still has a type (bgp.ParseUpdate reads
// what is there and reports the cut).
func (m BGP4MPMessage) Type() (bgp.MessageType, error) {
	h, err := bgp.ParseMessageHeader(m.Message)
	if err != nil {
		return 0, err
	}
	return h.Type, nil
}

// bgp4mpHeader reads the fields common to message and state change records
// from b (the record body), returning the rest.
func bgp4mpHeader(b []byte, as4 bool) (peerAS, localAS bgp.ASN, ifIndex uint16, peer, local netip.Addr, rest []byte, err error) {
	w := 2
	if as4 {
		w = 4
	}
	if len(b) < 2*w+4 {
		err = short("BGP4MP header")
		return
	}
	if as4 {
		peerAS, localAS = bgp.ASN(binary.BigEndian.Uint32(b)), bgp.ASN(binary.BigEndian.Uint32(b[4:]))
	} else {
		peerAS, localAS = bgp.ASN(binary.BigEndian.Uint16(b)), bgp.ASN(binary.BigEndian.Uint16(b[2:]))
	}
	b = b[2*w:]
	ifIndex = binary.BigEndian.Uint16(b)
	afi := binary.BigEndian.Uint16(b[2:])
	b = b[4:]
	if afi != 1 && afi != 2 {
		err = fmt.Errorf("%w: BGP4MP address family %d", ErrMalformed, afi)
		return
	}
	var ok bool
	if peer, b, ok = addrOf(b, afi == 2); !ok {
		err = short("BGP4MP peer address")
		return
	}
	if local, b, ok = addrOf(b, afi == 2); !ok {
		err = short("BGP4MP local address")
		return
	}
	rest = b
	return
}

func decodeBGP4MP(h Header, raw []byte) (Record, error) {
	hlen := 12
	if h.Type.isET() {
		hlen = 16
	}
	b := raw[hlen:]
	rec := record{hdr: h, raw: raw}
	switch h.Subtype {
	case SubStateChange, SubStateChangeAS4:
		as4 := h.Subtype == SubStateChangeAS4
		s := BGP4MPStateChange{record: rec, AS4: as4}
		var rest []byte
		var err error
		s.PeerAS, s.LocalAS, s.IfIndex, s.PeerAddr, s.LocalAddr, rest, err = bgp4mpHeader(b, as4)
		if err != nil {
			return nil, err
		}
		if len(rest) < 4 {
			return nil, short("BGP4MP state change states")
		}
		s.Old, s.New = State(binary.BigEndian.Uint16(rest)), State(binary.BigEndian.Uint16(rest[2:]))
		return s, nil
	case SubMessage, SubMessageAS4, SubMessageLocal, SubMessageAS4Local,
		SubMessageAddPath, SubMessageAS4AddPath, SubMessageLocalAddPath, SubMessageAS4LocalAddPath:
		s := h.Subtype
		m := BGP4MPMessage{record: rec}
		m.AS4 = s == SubMessageAS4 || s == SubMessageAS4Local || s == SubMessageAS4AddPath || s == SubMessageAS4LocalAddPath
		m.Local = s == SubMessageLocal || s == SubMessageAS4Local || s == SubMessageLocalAddPath || s == SubMessageAS4LocalAddPath
		m.AddPath = s >= SubMessageAddPath
		var err error
		m.PeerAS, m.LocalAS, m.IfIndex, m.PeerAddr, m.LocalAddr, m.Message, err = bgp4mpHeader(b, m.AS4)
		if err != nil {
			return nil, err
		}
		m.Message = m.Message[:len(m.Message):len(m.Message)]
		return m, nil
	}
	return Other{rec}, nil
}
