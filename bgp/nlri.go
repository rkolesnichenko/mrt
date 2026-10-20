package bgp

import (
	"fmt"
	"net/netip"
)

// ParseNLRI reads a run of prefixes of family fam from b. With addPath every
// prefix is preceded by a 4-byte path identifier. A prefix length above the
// family's width, or bytes that run out, is an Error diagnostic
// (DiagNLRILength) that stops the parse; the prefixes before it are kept.
// Bits past a prefix's length are cleared, with an Info diagnostic
// (DiagPrefixBits). Diagnostic.Offset is the offset in b. A family without
// plain prefixes (Family.HasPrefixes false) is refused, even for empty b, with
// one Error diagnostic (DiagNLRIFamily) and nil NLRI.
func ParseNLRI(b []byte, fam Family, addPath bool) ([]NLRI, []Diagnostic) {
	if !fam.HasPrefixes() {
		return nil, []Diagnostic{{SeverityError, DiagNLRIFamily, 0, 0,
			fmt.Sprintf("NLRI of AFI %d SAFI %d are not plain prefixes", fam.AFI, fam.SAFI)}}
	}
	return parseNLRI(b, fam.AFI, addPath)
}

func parseNLRI(b []byte, afi AFI, addPath bool) ([]NLRI, []Diagnostic) {
	var maxBits int
	switch afi {
	case AFIIPv4:
		maxBits = 32
	case AFIIPv6:
		maxBits = 128
	default:
		if len(b) == 0 {
			return nil, nil
		}
		return nil, []Diagnostic{{SeverityError, DiagNLRILength, 0, 0,
			fmt.Sprintf("NLRI of AFI %d cannot be parsed", afi)}}
	}
	var (
		out   []NLRI
		diags []Diagnostic
	)
	fail := func(off int, msg string) {
		diags = append(diags, Diagnostic{SeverityError, DiagNLRILength, off, 0, msg})
	}
	for off := 0; off < len(b); {
		start := off
		var n NLRI
		if addPath {
			if len(b)-off < 5 {
				fail(start, "NLRI runs out before its path identifier and prefix length")
				break
			}
			n.PathID, n.HasPathID = be32(b[off:]), true
			off += 4
		}
		plen := int(b[off])
		off++
		if plen > maxBits {
			fail(start, fmt.Sprintf("prefix length %d exceeds %d", plen, maxBits))
			break
		}
		nb := (plen + 7) / 8
		if len(b)-off < nb {
			fail(start, "NLRI runs out inside a prefix")
			break
		}
		var raw [16]byte
		copy(raw[:], b[off:off+nb])
		off += nb
		var addr netip.Addr
		if afi == AFIIPv4 {
			addr = netip.AddrFrom4([4]byte(raw[:4]))
		} else {
			addr = netip.AddrFrom16(raw)
		}
		pfx := netip.PrefixFrom(addr, plen)
		if m := pfx.Masked(); m != pfx {
			diags = append(diags, Diagnostic{SeverityInfo, DiagPrefixBits, start, 0,
				"prefix bits past the length were set and are cleared"})
			pfx = m
		}
		n.Prefix = pfx
		out = append(out, n)
	}
	return out, diags
}
