package bgp

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"net/netip"
	"strconv"
)

// ExtCommunity is an extended community (RFC 4360), kept as its 8 raw bytes.
// The bytes stay the source of truth; the methods read the well-known layouts
// and report false for anything else.
type ExtCommunity [8]byte

// ExtCommunityAdmin is the administrator and assigned-number pair of a route
// target or route origin. AS is set for the two- and four-octet AS forms, Addr
// for the IPv4 form. Local is 4 bytes wide in the two-octet AS form, 2 bytes
// otherwise.
type ExtCommunityAdmin struct {
	AS    ASN
	Addr  netip.Addr
	Local uint32
}

// ValidationState is the BGP origin validation state of RFC 8097.
type ValidationState uint8

// Origin validation states.
const (
	ValidationValid    ValidationState = 0
	ValidationNotFound ValidationState = 1
	ValidationInvalid  ValidationState = 2
)

// Type returns the high-order type octet, transitive bit included.
func (c ExtCommunity) Type() uint8 { return c[0] }

// Subtype returns the second octet.
func (c ExtCommunity) Subtype() uint8 { return c[1] }

// Transitive reports whether the transitive-across-ASes bit (0x40 of the
// type) is clear.
func (c ExtCommunity) Transitive() bool { return c[0]&0x40 == 0 }

// admin decodes the AS-specific forms (types 0x00, 0x01, 0x02, either
// transitivity) of subtype sub.
func (c ExtCommunity) admin(sub uint8) (ExtCommunityAdmin, bool) {
	if c[1] != sub {
		return ExtCommunityAdmin{}, false
	}
	switch c[0] &^ 0x40 {
	case 0x00:
		return ExtCommunityAdmin{AS: ASN(binary.BigEndian.Uint16(c[2:])), Local: binary.BigEndian.Uint32(c[4:])}, true
	case 0x01:
		return ExtCommunityAdmin{Addr: netip.AddrFrom4([4]byte(c[2:6])), Local: uint32(binary.BigEndian.Uint16(c[6:]))}, true
	case 0x02:
		return ExtCommunityAdmin{AS: ASN(binary.BigEndian.Uint32(c[2:])), Local: uint32(binary.BigEndian.Uint16(c[6:]))}, true
	}
	return ExtCommunityAdmin{}, false
}

// RouteTarget decodes a route target (subtype 0x02 of the two-octet AS, IPv4
// and four-octet AS types, RFC 4360 and RFC 5668).
func (c ExtCommunity) RouteTarget() (ExtCommunityAdmin, bool) { return c.admin(0x02) }

// RouteOrigin decodes a route origin (subtype 0x03, same types as RouteTarget).
func (c ExtCommunity) RouteOrigin() (ExtCommunityAdmin, bool) { return c.admin(0x03) }

// Color returns the color value of a Color community (RFC 9012 §4.3).
func (c ExtCommunity) Color() (uint32, bool) {
	if c[0] != 0x03 || c[1] != 0x0B {
		return 0, false
	}
	return binary.BigEndian.Uint32(c[4:]), true
}

// Encapsulation returns the tunnel type of an Encapsulation community
// (RFC 9012 §4.1).
func (c ExtCommunity) Encapsulation() (uint16, bool) {
	if c[0] != 0x03 || c[1] != 0x0C {
		return 0, false
	}
	return binary.BigEndian.Uint16(c[6:]), true
}

// LinkBandwidth returns the AS and the bandwidth in bytes per second of a link
// bandwidth community (draft-ietf-idr-link-bandwidth).
func (c ExtCommunity) LinkBandwidth() (ASN, float32, bool) {
	if (c[0] != 0x00 && c[0] != 0x40) || c[1] != 0x04 {
		return 0, 0, false
	}
	return ASN(binary.BigEndian.Uint16(c[2:])), math.Float32frombits(binary.BigEndian.Uint32(c[4:])), true
}

// OriginValidation returns the state of an origin validation community
// (RFC 8097); a state above 2 is false.
func (c ExtCommunity) OriginValidation() (ValidationState, bool) {
	if c[0] != 0x43 || c[1] != 0x00 || c[7] > 2 {
		return 0, false
	}
	return ValidationState(c[7]), true
}

// String labels the community: rt:AS:local, ro:AS:local, rt:a.b.c.d:local,
// ro:a.b.c.d:local, color:N, encap:N, lbw:AS:F or ov:valid|not-found|invalid;
// anything else is 0x and the 8 bytes in hex. The transitive bit does not
// change a label.
func (c ExtCommunity) String() string {
	for _, k := range [...]struct {
		sub  uint8
		name string
	}{{0x02, "rt:"}, {0x03, "ro:"}} {
		if a, ok := c.admin(k.sub); ok {
			who := a.Addr.String()
			if !a.Addr.IsValid() {
				who = strconv.FormatUint(uint64(a.AS), 10)
			}
			return k.name + who + ":" + strconv.FormatUint(uint64(a.Local), 10)
		}
	}
	if v, ok := c.Color(); ok {
		return "color:" + strconv.FormatUint(uint64(v), 10)
	}
	if v, ok := c.Encapsulation(); ok {
		return "encap:" + strconv.FormatUint(uint64(v), 10)
	}
	if as, f, ok := c.LinkBandwidth(); ok {
		return "lbw:" + strconv.FormatUint(uint64(as), 10) + ":" + strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	if v, ok := c.OriginValidation(); ok {
		return "ov:" + [...]string{"valid", "not-found", "invalid"}[v]
	}
	return "0x" + hex.EncodeToString(c[:])
}
