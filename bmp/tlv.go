package bmp

import (
	"encoding/binary"
	"iter"

	"github.com/rkolesnichenko/mrt/bgp"
)

// TLV is one Information TLV (2-byte type, 2-byte length, value) of an
// Initiation, Termination, Peer Up, Peer Down or Route Mirroring message.
type TLV struct {
	Type  uint16
	Value []byte // a slice of Raw, cap == len
}

// TLV types, one set per registry (IANA bmp-parameters). Strings stay bytes.
const (
	// Initiation (RFC 7854 section 4.4).
	TLVInitString   uint16 = 0
	TLVInitSysDescr uint16 = 1
	TLVInitSysName  uint16 = 2

	// Termination (RFC 7854 section 4.5).
	TLVTermString uint16 = 0
	TLVTermReason uint16 = 1 // a 2-byte TerminationReason

	// Route Mirroring (RFC 7854 section 4.7).
	TLVMirrorBGPMessage  uint16 = 0 // a whole BGP PDU; it comes last
	TLVMirrorInformation uint16 = 1 // a 2-byte Mirror information code

	// Peer Up (RFC 9736); Peer Down reason 6 uses TLVPeerUpTableName
	// (RFC 9069 section 5.3).
	TLVPeerUpString     uint16 = 0
	TLVPeerUpTableName  uint16 = 3 // VRF/Table Name, 1-255 bytes
	TLVPeerUpAdminLabel uint16 = 4 // RFC 8671 section 6.3.1
)

// Route Mirroring information codes, the value of a TLVMirrorInformation TLV.
const (
	MirrorErroredPDU   uint16 = 0
	MirrorMessagesLost uint16 = 1
)

// nextTLV reads the TLV at the front of b and returns the rest.
func nextTLV(b []byte) (TLV, []byte, error) {
	if len(b) < 4 {
		return TLV{}, nil, short("TLV header")
	}
	n := 4 + int(binary.BigEndian.Uint16(b[2:]))
	if len(b) < n {
		return TLV{}, nil, short("TLV")
	}
	return TLV{Type: binary.BigEndian.Uint16(b), Value: b[4:n:n]}, b[n:], nil
}

// tlvs walks the TLVs of b in order. When one runs past b it yields one
// (TLV{}, err) wrapping ErrMalformed and stops.
func tlvs(b []byte) iter.Seq2[TLV, error] {
	return func(yield func(TLV, error) bool) {
		for len(b) > 0 {
			t, rest, err := nextTLV(b)
			if err != nil {
				yield(TLV{}, err)
				return
			}
			if !yield(t, nil) {
				return
			}
			b = rest
		}
	}
}

// StatType is a Statistics Report stat type (RFC 7854 section 4.8, RFC 9972).
type StatType uint16

// Statistics types. The 32-bit counters are read by Stat.Counter, the 64-bit
// gauges by Stat.Gauge and the per-AFI/SAFI gauges by Stat.FamilyGauge.
// Types 24 and 25 were temporary registrations, expired 2026-10-18, and are
// treated as unregistered.
const (
	StatRejectedPrefixes        StatType = 0  // counter: prefixes rejected by inbound policy
	StatDuplicatePrefixes       StatType = 1  // counter: (known) duplicate prefix advertisements
	StatDuplicateWithdraws      StatType = 2  // counter: (known) duplicate withdraws
	StatClusterListLoop         StatType = 3  // counter: updates invalidated by a CLUSTER_LIST loop
	StatASPathLoop              StatType = 4  // counter: updates invalidated by an AS_PATH loop
	StatOriginatorIDLoop        StatType = 5  // counter: updates invalidated by ORIGINATOR_ID
	StatConfedLoop              StatType = 6  // counter: updates invalidated by an AS_CONFED loop
	StatAdjRIBIn                StatType = 7  // gauge: routes in Adj-RIBs-In
	StatLocRIB                  StatType = 8  // gauge: routes in Loc-RIB
	StatFamilyAdjRIBIn          StatType = 9  // per-AFI/SAFI gauge: routes in Adj-RIB-In
	StatFamilyLocRIB            StatType = 10 // per-AFI/SAFI gauge: routes in Loc-RIB
	StatTreatAsWithdrawUpdates  StatType = 11 // counter: updates subjected to treat-as-withdraw
	StatTreatAsWithdrawPrefixes StatType = 12 // counter: prefixes subjected to treat-as-withdraw
	StatDuplicateUpdates        StatType = 13 // counter: duplicate update messages
	StatAdjRIBOutPre            StatType = 14 // gauge: routes in pre-policy Adj-RIB-Out
	StatAdjRIBOutPost           StatType = 15 // gauge: routes in post-policy Adj-RIB-Out
	StatFamilyAdjRIBOutPre      StatType = 16 // per-AFI/SAFI gauge: routes in pre-policy Adj-RIB-Out
	StatFamilyAdjRIBOutPost     StatType = 17 // per-AFI/SAFI gauge: routes in post-policy Adj-RIB-Out
	StatAdjRIBInPre             StatType = 18 // gauge: routes in pre-policy Adj-RIB-In
	StatFamilyAdjRIBInPre       StatType = 19 // per-AFI/SAFI gauge: routes in pre-policy Adj-RIB-In
	StatAdjRIBInPost            StatType = 20 // gauge: routes in post-policy Adj-RIB-In
	StatFamilyAdjRIBInPost      StatType = 21 // per-AFI/SAFI gauge: routes in post-policy Adj-RIB-In
	StatFamilyInboundRejected   StatType = 22 // per-AFI/SAFI gauge: pre-policy Adj-RIB-In routes rejected by inbound policy
	StatFamilyInboundAccepted   StatType = 23 // per-AFI/SAFI gauge: post-policy Adj-RIB-In routes accepted by inbound policy
	StatFamilyDampened          StatType = 26 // per-AFI/SAFI gauge: routes suppressed by damping
	StatFamilyStaleGR           StatType = 27 // per-AFI/SAFI gauge: routes stale by Graceful Restart
	StatFamilyStaleLLGR         StatType = 28 // per-AFI/SAFI gauge: routes stale by Long-Lived Graceful Restart
	StatThresholdLeft           StatType = 29 // gauge: routes left before the received-route threshold
	StatFamilyThresholdLeft     StatType = 30 // per-AFI/SAFI gauge: routes left before the received-route threshold
	StatLicenseLeft             StatType = 31 // gauge: routes left before a license threshold
	StatFamilyLicenseLeft       StatType = 32 // per-AFI/SAFI gauge: routes left before a license threshold
	StatASPathTooLongIn         StatType = 33 // gauge: pre-policy Adj-RIB-In routes rejected for AS_PATH length
	StatFamilyASPathTooLongIn   StatType = 34 // per-AFI/SAFI gauge: pre-policy Adj-RIB-In routes rejected for AS_PATH length
	StatFamilyRPKIInvalidIn     StatType = 35 // per-AFI/SAFI gauge: post-policy Adj-RIB-In routes RPKI-invalid
	StatFamilyRPKIValidIn       StatType = 36 // per-AFI/SAFI gauge: post-policy Adj-RIB-In routes RPKI-valid
	StatFamilyRPKINotFoundIn    StatType = 37 // per-AFI/SAFI gauge: post-policy Adj-RIB-In routes RPKI NotFound
	StatFamilyOutboundRejected  StatType = 38 // per-AFI/SAFI gauge: pre-policy Adj-RIB-Out routes rejected by outbound policy
	StatASPathTooLongOut        StatType = 39 // gauge: pre-policy Adj-RIB-Out routes filtered for AS_PATH length
	StatFamilyASPathTooLongOut  StatType = 40 // per-AFI/SAFI gauge: pre-policy Adj-RIB-Out routes filtered for AS_PATH length
	StatFamilyRPKIInvalidOut    StatType = 41 // per-AFI/SAFI gauge: post-policy Adj-RIB-Out routes RPKI-invalid
	StatFamilyRPKIValidOut      StatType = 42 // per-AFI/SAFI gauge: post-policy Adj-RIB-Out routes RPKI-valid
	StatFamilyRPKINotFoundOut   StatType = 43 // per-AFI/SAFI gauge: post-policy Adj-RIB-Out routes RPKI NotFound
)

// statShape is the registered form of a stat type's value.
type statShape uint8

const (
	statUnregistered statShape = iota
	statCounter                // 4 bytes
	statGauge                  // 8 bytes
	statFamilyGauge            // 2-byte AFI, 1-byte SAFI, 8-byte gauge
)

// statShapes is the registry (spec §5.1 and §10.4) by stat type.
var statShapes = [...]statShape{
	0: statCounter, 1: statCounter, 2: statCounter, 3: statCounter, 4: statCounter, 5: statCounter, 6: statCounter,
	7: statGauge, 8: statGauge,
	9: statFamilyGauge, 10: statFamilyGauge,
	11: statCounter, 12: statCounter, 13: statCounter,
	14: statGauge, 15: statGauge,
	16: statFamilyGauge, 17: statFamilyGauge,
	18: statGauge, 19: statFamilyGauge, 20: statGauge,
	21: statFamilyGauge, 22: statFamilyGauge, 23: statFamilyGauge,
	24: statUnregistered, 25: statUnregistered,
	26: statFamilyGauge, 27: statFamilyGauge, 28: statFamilyGauge,
	29: statGauge, 30: statFamilyGauge, 31: statGauge, 32: statFamilyGauge, 33: statGauge,
	34: statFamilyGauge, 35: statFamilyGauge, 36: statFamilyGauge, 37: statFamilyGauge, 38: statFamilyGauge,
	39: statGauge,
	40: statFamilyGauge, 41: statFamilyGauge, 42: statFamilyGauge, 43: statFamilyGauge,
}

// shapeOf returns t's registered shape when v has its registered length.
func shapeOf(t StatType, v []byte) statShape {
	if int(t) >= len(statShapes) {
		return statUnregistered
	}
	switch s := statShapes[t]; {
	case s == statCounter && len(v) == 4, s == statGauge && len(v) == 8, s == statFamilyGauge && len(v) == 11:
		return s
	}
	return statUnregistered
}

// Stat is one statistic of a Statistics Report. Its accessors answer ok only
// for a registered type with its registered length; anything else stays
// bytes (RFC 7854 section 4.8: unexpected data MUST be ignored).
type Stat struct {
	Type  StatType
	Value []byte // as on the wire; a slice of Raw, cap == len
}

// Counter returns the value of a registered 32-bit counter with 4 bytes.
func (s Stat) Counter() (uint32, bool) {
	if shapeOf(s.Type, s.Value) != statCounter {
		return 0, false
	}
	return binary.BigEndian.Uint32(s.Value), true
}

// Gauge returns the value of a registered 64-bit gauge with 8 bytes.
func (s Stat) Gauge() (uint64, bool) {
	if shapeOf(s.Type, s.Value) != statGauge {
		return 0, false
	}
	return binary.BigEndian.Uint64(s.Value), true
}

// FamilyGauge returns the family and value of a registered per-AFI/SAFI
// gauge with 11 bytes.
func (s Stat) FamilyGauge() (bgp.Family, uint64, bool) {
	if shapeOf(s.Type, s.Value) != statFamilyGauge {
		return bgp.Family{}, 0, false
	}
	f := bgp.Family{AFI: bgp.AFI(binary.BigEndian.Uint16(s.Value)), SAFI: bgp.SAFI(s.Value[2])}
	return f, binary.BigEndian.Uint64(s.Value[3:]), true
}
