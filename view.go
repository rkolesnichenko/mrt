package mrt

// PeerType is a BMP peer type (RFC 7854 section 4.2, RFC 9069 for Loc-RIB).
// It is BMP-only: every MRT peer is the global instance, the zero value.
type PeerType uint8

// BMP peer types.
const (
	PeerGlobal PeerType = 0 // global instance peer
	PeerRD     PeerType = 1 // RD instance peer
	PeerLocal  PeerType = 2 // local instance peer
	PeerLocRIB PeerType = 3 // Loc-RIB instance peer
)

// String returns "global", "rd", "local" or "loc-rib", and "unknown" for any
// other value.
func (p PeerType) String() string {
	switch p {
	case PeerGlobal:
		return "global"
	case PeerRD:
		return "rd"
	case PeerLocal:
		return "local"
	case PeerLocRIB:
		return "loc-rib"
	}
	return "unknown"
}

// View says which RIB a BMP route is in. It is BMP-only and the zero View,
// "not stated", is what every MRT route carries: an MRT file does not say
// which RIB its routes were taken from. A BMP table's key is (router, peer,
// View), because the same peer has up to four Adj-RIB views at once.
type View uint8

// BMP views. The O and L flags are those of the BMP per-peer header
// (RFC 7854 section 4.2, RFC 8671 for Adj-RIB-Out).
const (
	ViewAdjRIBInPre   View = 1 // O = 0, L = 0
	ViewAdjRIBInPost  View = 2 // O = 0, L = 1
	ViewAdjRIBOutPre  View = 3 // O = 1, L = 0
	ViewAdjRIBOutPost View = 4 // O = 1, L = 1
	ViewLocRIB        View = 5 // peer type 3
)

// String returns "adj-rib-in-pre", "adj-rib-in-post", "adj-rib-out-pre",
// "adj-rib-out-post" or "loc-rib", and "unknown" for the zero View or any
// other value.
func (v View) String() string {
	switch v {
	case ViewAdjRIBInPre:
		return "adj-rib-in-pre"
	case ViewAdjRIBInPost:
		return "adj-rib-in-post"
	case ViewAdjRIBOutPre:
		return "adj-rib-out-pre"
	case ViewAdjRIBOutPost:
		return "adj-rib-out-post"
	case ViewLocRIB:
		return "loc-rib"
	}
	return "unknown"
}
