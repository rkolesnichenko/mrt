package mrt

import "time"

// Type is an MRT record type code (RFC 6396 section 4, IANA registry).
type Type uint16

// Subtype is an MRT record subtype code. Its meaning depends on the Type:
// the TABLE_DUMP and BGP4MP values overlap.
type Subtype uint16

// MRT record types.
const (
	TypeOSPFv2      Type = 11
	TypeTableDump   Type = 12
	TypeTableDumpV2 Type = 13
	TypeBGP4MP      Type = 16
	TypeBGP4MPET    Type = 17
	TypeISIS        Type = 32
	TypeISISET      Type = 33
	TypeOSPFv3      Type = 48
	TypeOSPFv3ET    Type = 49
)

// TABLE_DUMP subtypes (the address family).
const (
	SubAFIIPv4 Subtype = 1
	SubAFIIPv6 Subtype = 2
)

// TABLE_DUMP_V2 subtypes.
const (
	SubPeerIndexTable          Subtype = 1
	SubRIBIPv4Unicast          Subtype = 2
	SubRIBIPv4Multicast        Subtype = 3
	SubRIBIPv6Unicast          Subtype = 4
	SubRIBIPv6Multicast        Subtype = 5
	SubRIBGeneric              Subtype = 6
	SubGeoPeerTable            Subtype = 7
	SubRIBIPv4UnicastAddPath   Subtype = 8
	SubRIBIPv4MulticastAddPath Subtype = 9
	SubRIBIPv6UnicastAddPath   Subtype = 10
	SubRIBIPv6MulticastAddPath Subtype = 11
	SubRIBGenericAddPath       Subtype = 12
)

// BGP4MP subtypes.
const (
	SubStateChange            Subtype = 0
	SubMessage                Subtype = 1
	SubMessageAS4             Subtype = 4
	SubStateChangeAS4         Subtype = 5
	SubMessageLocal           Subtype = 6
	SubMessageAS4Local        Subtype = 7
	SubMessageAddPath         Subtype = 8
	SubMessageAS4AddPath      Subtype = 9
	SubMessageLocalAddPath    Subtype = 10
	SubMessageAS4LocalAddPath Subtype = 11
)

// Header is the MRT common header.
type Header struct {
	Time    time.Time // seconds, plus microseconds for *_ET types
	Type    Type
	Subtype Subtype
	Length  uint32 // as on the wire; for *_ET it includes the 4 microsecond bytes
}

// isET reports whether t carries the extended (microsecond) timestamp.
func (t Type) isET() bool {
	return t == TypeBGP4MPET || t == TypeISISET || t == TypeOSPFv3ET
}
