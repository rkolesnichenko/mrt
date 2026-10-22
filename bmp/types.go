package bmp

import (
	"encoding/binary"
	"fmt"
	"iter"
	"net/netip"

	"github.com/rkolesnichenko/mrt/bgp"
)

// RouteMonitoring is a Route Monitoring message (RFC 7854 section 4.6): one
// BGP UPDATE about the peer. Update is not validated until MessageType or
// bgp.ParseUpdate looks at it.
type RouteMonitoring struct {
	message
	Peer   PeerHeader
	Update []byte // the BGP message, marker included; a slice of Raw, cap == len
}

// MessageType returns the BGP message type from Update's header. The error
// wraps bgp.ErrMarker or bgp.ErrShortMessage when the header is broken.
func (m RouteMonitoring) MessageType() (bgp.MessageType, error) {
	h, err := bgp.ParseMessageHeader(m.Update)
	if err != nil {
		return 0, err
	}
	return h.Type, nil
}

// StatsReport is a Statistics Report (RFC 7854 section 4.8).
type StatsReport struct {
	message
	Peer  PeerHeader
	Count uint32 // as stated
}

// Stats walks the statistics in wire order. A TLV that runs past the
// message ends the walk with one error. Otherwise every whole TLV is
// yielded, and when their number is not Count (more or fewer) one error
// follows the last. Count never sizes anything.
func (m StatsReport) Stats() iter.Seq2[Stat, error] {
	return func(yield func(Stat, error) bool) {
		var n uint64
		for t, err := range tlvs(m.raw[commonHeaderLen+peerHeaderLen+4:]) {
			if err != nil {
				yield(Stat{}, err)
				return
			}
			n++
			if !yield(Stat{Type: StatType(t.Type), Value: t.Value}, nil) {
				return
			}
		}
		if n != uint64(m.Count) {
			yield(Stat{}, fmt.Errorf("%w: statistics report states %d stats and holds %d", ErrMalformed, m.Count, n))
		}
	}
}

// PeerDownReason is a Peer Down reason code (RFC 7854 section 4.9, RFC 9069
// section 5.3).
type PeerDownReason uint8

// Peer Down reasons.
const (
	PeerDownLocalNotification  PeerDownReason = 1 // local close; a NOTIFICATION PDU follows
	PeerDownLocalFSMEvent      PeerDownReason = 2 // local close; a 2-byte FSM event code follows
	PeerDownRemoteNotification PeerDownReason = 3 // remote close; a NOTIFICATION PDU follows
	PeerDownRemoteNoData       PeerDownReason = 4 // remote close; no data
	PeerDownDeconfigured       PeerDownReason = 5 // the peer was de-configured
	PeerDownLocalTLV           PeerDownReason = 6 // Loc-RIB local close; TLVs follow
)

// PeerDown is a Peer Down Notification (RFC 7854 section 4.9). Only its
// per-peer header must read: a missing reason or damaged data is found by
// the accessors.
type PeerDown struct {
	message
	Peer   PeerHeader
	Reason PeerDownReason // 0 (reserved) when the message ends after the per-peer header
	Data   []byte         // everything after the reason; nil without one; a slice of Raw, cap == len
}

// Notification returns Data, the NOTIFICATION PDU, for reasons 1 and 3. It is
// not validated: bgp.ParseNotification reads it.
func (m PeerDown) Notification() ([]byte, bool) {
	if m.Reason != PeerDownLocalNotification && m.Reason != PeerDownRemoteNotification {
		return nil, false
	}
	return m.Data, true
}

// FSMEvent returns the FSM event code of reason 2 when Data is its 2 bytes
// (0 means none).
func (m PeerDown) FSMEvent() (uint16, bool) {
	if m.Reason != PeerDownLocalFSMEvent || len(m.Data) != 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(m.Data), true
}

// Info walks the TLVs of reason 6, and yields nothing for any other reason.
// A TLV that runs past the message ends the walk with one error.
func (m PeerDown) Info() iter.Seq2[TLV, error] {
	if m.Reason != PeerDownLocalTLV {
		return tlvs(nil)
	}
	return tlvs(m.Data)
}

// PeerUp is a Peer Up Notification (RFC 7854 section 4.10). Only its
// per-peer header must read; Body reads the rest.
type PeerUp struct {
	message
	Peer PeerHeader
	Data []byte // everything after the per-peer header; a slice of Raw, cap == len
}

// peerUpFixedLen is the local address and the two ports.
const peerUpFixedLen = 16 + 2 + 2

// Body reads Data. Its error wraps ErrMalformed when the local address and
// ports do not fit or an OPEN does not frame: a broken BGP header, or a
// length past the data. The Information TLVs are not read: damage there
// shows in Info, never here.
func (m PeerUp) Body() (PeerUpBody, error) {
	b := m.Data
	if len(b) < peerUpFixedLen {
		return PeerUpBody{}, short("Peer Up local address and ports")
	}
	body := PeerUpBody{
		LocalAddr:  m.Peer.addrOf(b[:16]),
		LocalPort:  binary.BigEndian.Uint16(b[16:]),
		RemotePort: binary.BigEndian.Uint16(b[18:]),
	}
	var err error
	b = b[peerUpFixedLen:]
	if body.SentOpen, b, err = nextOpen(b, "sent"); err != nil {
		return PeerUpBody{}, err
	}
	if body.ReceivedOpen, b, err = nextOpen(b, "received"); err != nil {
		return PeerUpBody{}, err
	}
	body.info = b
	return body, nil
}

// nextOpen frames the BGP message at the front of b by its own header.
func nextOpen(b []byte, which string) (open, rest []byte, err error) {
	h, err := bgp.ParseMessageHeader(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: Peer Up %s OPEN: %w", ErrMalformed, which, err)
	}
	if h.Length > len(b) {
		return nil, nil, fmt.Errorf("%w: Peer Up %s OPEN of %d bytes runs past the message", ErrMalformed, which, h.Length)
	}
	return b[:h.Length:h.Length], b[h.Length:], nil
}

// PeerUpBody is what follows a Peer Up's per-peer header.
type PeerUpBody struct {
	LocalAddr              netip.Addr // by the V flag, as the peer address; zero for Loc-RIB and unknown peer types
	LocalPort, RemotePort  uint16
	SentOpen, ReceivedOpen []byte // whole OPEN messages, slices of Raw with cap == len
	info                   []byte // the Information TLVs
}

// Info walks the Information TLVs (RFC 9736). A TLV that runs past the
// message ends the walk with one error.
func (b PeerUpBody) Info() iter.Seq2[TLV, error] { return tlvs(b.info) }

// TableName returns the first VRF/Table Name TLV's value, converted without
// validating UTF-8. It is false when there is none before the TLVs end or
// break.
func (b PeerUpBody) TableName() (string, bool) {
	for t, err := range b.Info() {
		if err != nil {
			break
		}
		if t.Type == TLVPeerUpTableName {
			return string(t.Value), true
		}
	}
	return "", false
}

// Initiation is an Initiation message (RFC 7854 section 4.3): TLVs only.
type Initiation struct{ message }

// Info walks the TLVs. A TLV that runs past the message ends the walk with
// one error.
func (m Initiation) Info() iter.Seq2[TLV, error] { return tlvs(m.raw[commonHeaderLen:]) }

// TerminationReason is a Termination reason code (RFC 7854 section 4.5).
type TerminationReason uint16

// Termination reasons.
const (
	TerminationAdminClosed       TerminationReason = 0 // administratively closed
	TerminationUnspecified       TerminationReason = 1 // unspecified reason
	TerminationOutOfResources    TerminationReason = 2 // out of resources
	TerminationRedundant         TerminationReason = 3 // redundant connection
	TerminationPermanentlyClosed TerminationReason = 4 // permanently administratively closed
)

// Termination is a Termination message (RFC 7854 section 4.5): TLVs only.
type Termination struct{ message }

// Info walks the TLVs. A TLV that runs past the message ends the walk with
// one error.
func (m Termination) Info() iter.Seq2[TLV, error] { return tlvs(m.raw[commonHeaderLen:]) }

// Reason returns the reason of the first Reason TLV. It is false when there
// is none before the TLVs end or break, or when that TLV's value is not
// 2 bytes.
func (m Termination) Reason() (TerminationReason, bool) {
	for t, err := range m.Info() {
		if err != nil {
			break
		}
		if t.Type == TLVTermReason {
			if len(t.Value) != 2 {
				return 0, false
			}
			return TerminationReason(binary.BigEndian.Uint16(t.Value)), true
		}
	}
	return 0, false
}

// RouteMirroring is a Route Mirroring message (RFC 7854 section 4.7).
type RouteMirroring struct {
	message
	Peer PeerHeader
}

// TLVs walks the TLVs: Information codes and the mirrored BGP message. A TLV
// that runs past the message ends the walk with one error.
func (m RouteMirroring) TLVs() iter.Seq2[TLV, error] {
	return tlvs(m.raw[commonHeaderLen+peerHeaderLen:])
}

// Other is every message whose type BMP v3 does not define (7-255). It is
// kept raw and is never an error (RFC 7854 section 4.1: "MUST ignore").
type Other struct{ message }
