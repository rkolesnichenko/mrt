package bgp

import (
	"net/netip"
	"strconv"
)

// ASN is an autonomous system number.
type ASN uint32

// ASTrans is AS_TRANS (RFC 6793), the 2-byte stand-in for a 4-byte ASN.
const ASTrans ASN = 23456

// AFI is an address family identifier.
type AFI uint16

// Address family identifiers.
const (
	AFIIPv4 AFI = 1
	AFIIPv6 AFI = 2
)

// SAFI is a subsequent address family identifier.
type SAFI uint8

// Subsequent address family identifiers.
const (
	SAFIUnicast   SAFI = 1
	SAFIMulticast SAFI = 2
)

// Family is an AFI/SAFI pair.
type Family struct {
	AFI  AFI
	SAFI SAFI
}

// HasPrefixes reports whether NLRI of f are plain prefixes this package
// decodes: AFI 1 or 2 with SAFI 1 (unicast) or 2 (multicast).
func (f Family) HasPrefixes() bool {
	return (f.AFI == AFIIPv4 || f.AFI == AFIIPv6) && (f.SAFI == SAFIUnicast || f.SAFI == SAFIMulticast)
}

// FamilySet is a set of the families whose NLRI are prefixes
// (Family.HasPrefixes): AFI 1 and 2 with SAFI 1 and 2.
type FamilySet uint8

// familyBit returns f's bit, or 0 for a family without prefixes.
func familyBit(f Family) FamilySet {
	if !f.HasPrefixes() {
		return 0
	}
	return 1 << ((f.AFI-1)*2 + AFI(f.SAFI-1))
}

// Has reports whether f is in s. A family without prefixes never is.
func (s FamilySet) Has(f Family) bool { return s&familyBit(f) != 0 }

// With returns s with f added. For a family without prefixes it returns s.
func (s FamilySet) With(f Family) FamilySet { return s | familyBit(f) }

// Context says how to read bytes that do not describe themselves.
type Context struct {
	AS4             bool      // AS_PATH and AGGREGATOR carry 4-byte ASNs
	AddPath         bool      // every NLRI is preceded by a 4-byte path identifier
	AddPathFamilies FamilySet // NLRI of these families are preceded by one (BMP: from Peer Up)
	RIB             bool      // MRT RIB entry: MP_REACH_NLRI may be abbreviated (RFC 6396 §4.3.4)
}

// addPath reports whether NLRI of family f carry path identifiers.
func (c Context) addPath(f Family) bool { return c.AddPath || c.AddPathFamilies.Has(f) }

// MessageType is a BGP message type.
type MessageType uint8

// BGP message types.
const (
	MessageOpen         MessageType = 1
	MessageUpdate       MessageType = 2
	MessageNotification MessageType = 3
	MessageKeepalive    MessageType = 4
	MessageRouteRefresh MessageType = 5
)

// NLRI is one announced or withdrawn prefix, with its ADD-PATH identifier if any.
type NLRI struct {
	Prefix    netip.Prefix
	PathID    uint32
	HasPathID bool
}

// AttrCode is a path attribute type code.
type AttrCode uint8

// Path attribute type codes.
const (
	AttrOrigin          AttrCode = 1
	AttrASPath          AttrCode = 2
	AttrNextHop         AttrCode = 3
	AttrMED             AttrCode = 4
	AttrLocalPref       AttrCode = 5
	AttrAtomicAggregate AttrCode = 6
	AttrAggregator      AttrCode = 7
	AttrCommunities     AttrCode = 8
	AttrOriginatorID    AttrCode = 9
	AttrClusterList     AttrCode = 10
	AttrMPReach         AttrCode = 14
	AttrMPUnreach       AttrCode = 15
	AttrExtCommunities  AttrCode = 16
	AttrAS4Path         AttrCode = 17
	AttrAS4Aggregator   AttrCode = 18
	AttrLargeCommunity  AttrCode = 32
	AttrOnlyToCustomer  AttrCode = 35
)

// Attribute is one raw path attribute.
type Attribute struct {
	Flags uint8
	Code  AttrCode
	Value []byte
}

// Origin is the ORIGIN attribute value.
type Origin uint8

// ORIGIN attribute values.
const (
	OriginIGP        Origin = 0
	OriginEGP        Origin = 1
	OriginIncomplete Origin = 2
)

// Aggregator is the AGGREGATOR attribute: the aggregating AS and router.
type Aggregator struct {
	AS   ASN
	Addr netip.Addr
}

// Community is a standard community (RFC 1997).
type Community uint32

// String formats c as "high:low", for example "65000:1".
func (c Community) String() string {
	return strconv.FormatUint(uint64(c>>16), 10) + ":" + strconv.FormatUint(uint64(c&0xffff), 10)
}

// LargeCommunity is a large community (RFC 8092).
type LargeCommunity struct{ Global, Local1, Local2 uint32 }

// String formats l as "global:local1:local2".
func (l LargeCommunity) String() string {
	return strconv.FormatUint(uint64(l.Global), 10) + ":" +
		strconv.FormatUint(uint64(l.Local1), 10) + ":" +
		strconv.FormatUint(uint64(l.Local2), 10)
}
