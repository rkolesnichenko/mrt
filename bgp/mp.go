package bgp

import "net/netip"

// MPReach is a decoded MP_REACH_NLRI attribute (RFC 4760).
type MPReach struct {
	Family      Family
	NextHop     netip.Addr // global
	LinkLocal   netip.Addr // second IPv6 next hop, when present
	NLRI        []NLRI     // decoded for IPv4/IPv6 unicast and multicast only
	RawNLRI     []byte     // other families: kept raw
	Abbreviated bool       // RFC 6396 §4.3.4 form
}

// MPUnreach is a decoded MP_UNREACH_NLRI attribute (RFC 4760).
type MPUnreach struct {
	Family    Family
	Withdrawn []NLRI
	RawNLRI   []byte
}
