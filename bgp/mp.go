package bgp

import (
	"fmt"
	"net/netip"
)

// MPReach is a decoded MP_REACH_NLRI attribute (RFC 4760).
type MPReach struct {
	Family      Family     // zero in the abbreviated form: the record supplies it
	NextHop     netip.Addr // global
	LinkLocal   netip.Addr // second IPv6 next hop, when present
	NLRI        []NLRI     // decoded for IPv4/IPv6 unicast and multicast only, outside a RIB entry
	RawNLRI     []byte     // other families outside a RIB entry: kept raw
	Abbreviated bool       // RFC 6396 §4.3.4 form
}

// MPUnreach is a decoded MP_UNREACH_NLRI attribute (RFC 4760).
type MPUnreach struct {
	Family    Family
	Withdrawn []NLRI
	RawNLRI   []byte
}

// setMPNextHop stores the next hop in v by its length (RFC 4760, RFC 2545,
// RFC 8950): 4 is IPv4, 16 IPv6, 32 IPv6 plus link-local. It reports whether
// the length was one of those.
func setMPNextHop(r *MPReach, v []byte) bool {
	switch len(v) {
	case 4:
		r.NextHop = netip.AddrFrom4([4]byte(v))
	case 16:
		r.NextHop = netip.AddrFrom16([16]byte(v))
	case 32:
		r.NextHop = netip.AddrFrom16([16]byte(v[:16]))
		r.LinkLocal = netip.AddrFrom16([16]byte(v[16:]))
	default:
		return false
	}
	return true
}

func rawOrNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b[:len(b):len(b)]
}

// decodeMPReach reads an MP_REACH_NLRI value. ok is false when the value is
// malformed and belongs in Unknown. Diagnostics carry Offset off and Attr.
func decodeMPReach(v []byte, ctx Context, off int) (r MPReach, ok bool, diags []Diagnostic) {
	diag := func(sev Severity, code, msg string) {
		diags = append(diags, Diagnostic{sev, code, off, AttrMPReach, msg})
	}
	if ctx.RIB && len(v) > 0 && v[0] != 0 { // RFC 6396 §4.3.4: next hop only
		nh := int(v[0])
		if len(v)-1 != nh {
			diag(SeverityWarning, "bgp/attr-length", "abbreviated MP_REACH_NLRI length does not match its next hop length")
			return r, false, diags
		}
		r.Abbreviated = true
		if !setMPNextHop(&r, v[1:]) { // a RIB's family is always IP
			diag(SeverityWarning, "bgp/attr-value", fmt.Sprintf("MP_REACH_NLRI next hop of %d bytes", nh))
		}
		return r, true, diags
	}
	if len(v) < 5 {
		diag(SeverityWarning, "bgp/attr-length", "MP_REACH_NLRI is shorter than its fixed fields")
		return r, false, diags
	}
	nh := int(v[3])
	if len(v)-4 < nh+1 {
		diag(SeverityWarning, "bgp/attr-length", "MP_REACH_NLRI next hop runs past the attribute")
		return r, false, diags
	}
	r.Family = Family{AFI(uint16(v[0])<<8 | uint16(v[1])), SAFI(v[2])}
	if !setMPNextHop(&r, v[4:4+nh]) && r.Family.HasPrefixes() {
		diag(SeverityWarning, "bgp/attr-value", fmt.Sprintf("MP_REACH_NLRI next hop of %d bytes", nh))
	}
	rest := v[4+nh+1:] // skip the reserved byte
	// The full form in a RIB entry (first byte zero, or the test above would
	// have taken it): the record carries the prefix, so the NLRI is neither
	// parsed nor kept (Ruling R20, amended 2026-10-20); the bytes stay in
	// Attrs.Raw(). Abbreviated=false records the form.
	switch {
	case ctx.RIB:
	case r.Family.HasPrefixes():
		var nd []Diagnostic
		r.NLRI, nd = parseNLRI(rest, r.Family.AFI, ctx.AddPath)
		for _, d := range nd {
			d.Offset, d.Attr = off, AttrMPReach
			diags = append(diags, d)
		}
	default:
		r.RawNLRI = rawOrNil(rest)
	}
	return r, true, diags
}

// decodeMPUnreach reads an MP_UNREACH_NLRI value, like decodeMPReach.
func decodeMPUnreach(v []byte, ctx Context, off int) (r MPUnreach, ok bool, diags []Diagnostic) {
	if len(v) < 3 {
		diags = append(diags, Diagnostic{SeverityWarning, "bgp/attr-length", off, AttrMPUnreach,
			"MP_UNREACH_NLRI is shorter than its fixed fields"})
		return r, false, diags
	}
	r.Family = Family{AFI(uint16(v[0])<<8 | uint16(v[1])), SAFI(v[2])}
	rest := v[3:]
	if r.Family.HasPrefixes() {
		var nd []Diagnostic
		r.Withdrawn, nd = parseNLRI(rest, r.Family.AFI, ctx.AddPath)
		for _, d := range nd {
			d.Offset, d.Attr = off, AttrMPUnreach
			diags = append(diags, d)
		}
	} else {
		r.RawNLRI = rawOrNil(rest)
	}
	return r, true, diags
}
