package mrtdump

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// The line formats are bgpdump.c's (RIPE-NCC/bgpdump at caf2cb07):
//
//	B and A, -m: TYPE|time|B|peer|peerAS|prefix|[pathID|]path|origin|nexthop|localpref|med|communities|[large|]AG|aggregator|[unknown|]
//	B and A, -M: TYPE|time|B|peer|peerAS|prefix|[pathID|]path|origin
//	W:           TYPE|time|W|peer|peerAS|prefix[|pathID]
//	STATE:       TYPE|time|STATE|peer|peerAS|old|new
//
// where -l adds the large communities field and -u the unknown attributes.

var (
	v4Unicast = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}
	v6Unicast = bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}
)

// noNextHop is what bgpdump prints for a missing NEXT_HOP: its "unset"
// value, INADDR_NONE.
const noNextHop = "255.255.255.255"

// timeField is the record's timestamp as bgpdump prints it: Unix seconds
// (-m) or MM/DD/YY HH:MM:SS in UTC (-M), then .%06d microseconds for
// BGP4MP_ET. Both come from the header bytes as written, as bgpdump reads
// them.
func (d *dumper) timeField(b []byte, h mrt.Header, raw []byte) []byte {
	sec := binary.BigEndian.Uint32(raw)
	if d.opts.human {
		b = time.Unix(int64(sec), 0).UTC().AppendFormat(b, "01/02/06 15:04:05")
	} else {
		b = strconv.AppendUint(b, uint64(sec), 10)
	}
	if h.Type == mrt.TypeBGP4MPET {
		b = append(b, fmt.Sprintf(".%06d", binary.BigEndian.Uint32(raw[12:]))...)
	}
	return b
}

// start begins a line: TYPE|time|kind|.
func (d *dumper) start(typ string, rec mrt.Record, kind string) []byte {
	b := append(d.line[:0], typ...)
	b = append(b, '|')
	b = d.timeField(b, rec.Header(), rec.Raw())
	b = append(b, '|')
	b = append(b, kind...)
	return append(b, '|')
}

// end writes the line b and keeps its buffer.
func (d *dumper) end(b []byte) {
	b = append(b, '\n')
	d.out.Write(b)
	d.line = b
}

// fmtAddr formats a as bgpdump does: dotted quad, or fmtIPv6.
func fmtAddr(a netip.Addr) string {
	if a.Is4() {
		return a.String()
	}
	return fmtIPv6(a)
}

func appendPrefix(b []byte, p netip.Prefix) []byte {
	b = append(b, fmtAddr(p.Addr())...)
	b = append(b, '/')
	return strconv.AppendInt(b, int64(p.Bits()), 10)
}

func appendPeer(b []byte, addr netip.Addr, as bgp.ASN) []byte {
	b = append(b, fmtAddr(addr)...)
	b = append(b, '|')
	b = strconv.AppendUint(b, uint64(as), 10)
	return append(b, '|')
}

// fmtIPv6 is bgpdump's fmt_ipv6 (inet_ntop.c): v4-mapped addresses print as
// ::ffff:a.b.c.d and v4-compatible ones (::a.b.c.d other than :: and ::1) as
// ::a.b.c.d; otherwise the groups print in lower-case hex without leading
// zeros and the longest run of zero groups, even a single one, becomes "::"
// (the first of equal runs). Go's netip.Addr.String follows RFC 5952, which
// never compresses a single group.
func fmtIPv6(a netip.Addr) string {
	x := a.As16()
	var g [8]uint16
	for i := range g {
		g[i] = binary.BigEndian.Uint16(x[2*i:])
	}
	if g[0]|g[1]|g[2]|g[3]|g[4] == 0 && (g[5] == 0xffff || g[5] == 0 && g[6]|g[7] != 0 && !(g[6] == 0 && g[7] == 1)) {
		v4 := netip.AddrFrom4([4]byte(x[12:])).String()
		if g[5] == 0xffff {
			return "::ffff:" + v4
		}
		return "::" + v4
	}
	best, bestLen := -1, 0
	for i := 0; i < 8; {
		if g[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && g[j] == 0 {
			j++
		}
		if j-i > bestLen {
			best, bestLen = i, j-i
		}
		i = j
	}
	var b []byte
	for i := 0; i < 8; i++ {
		if i == best {
			if i == 0 {
				b = append(b, ':')
			}
			b = append(b, ':')
			i += bestLen - 1
			continue
		}
		b = strconv.AppendUint(b, uint64(g[i]), 16)
		if i != 7 {
			b = append(b, ':')
		}
	}
	return string(b)
}

// decodedByBgpdump are the attribute codes bgpdump decodes: every other
// attribute is "unknown" to it and printed by -u (extended communities and
// OTC among them).
var decodedByBgpdump = [256]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true,
	9: true, 10: true, 14: true, 15: true, 17: true, 18: true, 32: true}

// attrFields are the fields of B and A lines that come from the attributes.
type attrFields struct {
	path, origin   string
	nextHop        string // from NEXT_HOP
	localPref, med uint32
	communities    string
	large          string
	atomic         bool
	aggregator     string
	unknown        string // -u field, without its trailing |
}

// fields computes the attribute fields of path p, decoded from attrs.
func (d *dumper) fields(attrs bgp.Attrs, p *bgp.Path) attrFields {
	f := attrFields{
		path:      p.ASPath.String(),
		origin:    "INCOMPLETE", // bgpdump's word for anything but 0 and 1, missing included
		nextHop:   noNextHop,
		localPref: p.LocalPref, // zero unless present
		med:       p.MED,
		atomic:    p.AtomicAggregate,
	}
	if p.HasOrigin && p.Origin == bgp.OriginIGP {
		f.origin = "IGP"
	} else if p.HasOrigin && p.Origin == bgp.OriginEGP {
		f.origin = "EGP"
	}
	if p.NextHop.IsValid() {
		f.nextHop = p.NextHop.String()
	}
	var b []byte
	for i, c := range p.Communities {
		if i > 0 {
			b = append(b, ' ')
		}
		switch c {
		case 0xFFFFFF01:
			b = append(b, "no-export"...)
		case 0xFFFFFF02:
			b = append(b, "no-advertise"...)
		case 0xFFFFFF03:
			b = append(b, "local-AS"...)
		default:
			b = append(b, c.String()...)
		}
	}
	f.communities = string(b)
	if d.opts.large {
		b = b[:0]
		for i, c := range p.LargeCommunities {
			if i > 0 {
				b = append(b, ' ')
			}
			b = append(b, c.String()...)
		}
		f.large = string(b)
	}
	if p.HasAggregator {
		f.aggregator = strconv.FormatUint(uint64(p.Aggregator.AS), 10) + " " + p.Aggregator.Addr.String()
	}
	// The raw walk: AS_PATH present but unparseable prints as bgpdump's
	// "! Error !", ATOMIC_AGGREGATE is AG whatever its length, and -u lists
	// what bgpdump does not decode, in wire order.
	b = b[:0]
	asPathSeen := false
	for a, err := range attrs.All() {
		if err != nil {
			break // bgpdump skips a truncated attribute too
		}
		switch {
		case a.Code == bgp.AttrASPath && !asPathSeen:
			asPathSeen = true
			if len(a.Value) > 0 && len(p.ReceivedASPath.Segments) == 0 {
				f.path = "! Error !"
			}
		case a.Code == bgp.AttrAtomicAggregate:
			f.atomic = true
		case !decodedByBgpdump[a.Code] && d.opts.unknown:
			if len(b) > 0 {
				b = append(b, ' ')
			}
			b = fmt.Appendf(b, "%02x:%02x:", uint8(a.Code), a.Flags)
			b = hex.AppendEncode(b, a.Value)
		}
	}
	f.unknown = string(b)
	return f
}

// announce writes one B or A line. pathID < 0 means no path ID field.
func (d *dumper) announce(typ string, rec mrt.Record, kind string, peer netip.Addr, peerAS bgp.ASN,
	prefix netip.Prefix, pathID int64, f *attrFields, nextHop string) {
	b := d.start(typ, rec, kind)
	b = appendPeer(b, peer, peerAS)
	b = appendPrefix(b, prefix)
	b = append(b, '|')
	if pathID >= 0 {
		b = strconv.AppendInt(b, pathID, 10)
		b = append(b, '|')
	}
	b = append(b, f.path...)
	b = append(b, '|')
	b = append(b, f.origin...)
	if d.opts.human {
		d.end(b)
		return
	}
	b = append(b, '|')
	b = append(b, nextHop...)
	b = append(b, '|')
	b = strconv.AppendUint(b, uint64(f.localPref), 10)
	b = append(b, '|')
	b = strconv.AppendUint(b, uint64(f.med), 10)
	b = append(b, '|')
	b = append(b, f.communities...)
	b = append(b, '|')
	if d.opts.large {
		b = append(b, f.large...)
		b = append(b, '|')
	}
	if f.atomic {
		b = append(b, "AG|"...)
	} else {
		b = append(b, "NAG|"...)
	}
	b = append(b, f.aggregator...)
	b = append(b, '|')
	if d.opts.unknown {
		b = append(b, f.unknown...)
		b = append(b, '|')
	}
	d.end(b)
}

// ribNextHop is the next hop of a RIB entry as bgpdump picks it: an
// MP_REACH_NLRI next hop (the abbreviated form, or the full form for IPv6
// unicast or, outside legacy TABLE_DUMP, IPv4 unicast), else NEXT_HOP.
func ribNextHop(p *bgp.Path, f *attrFields, legacy bool) string {
	if mp := p.MPReach; mp != nil && mp.NextHop.IsValid() &&
		(mp.Abbreviated || mp.Family == v6Unicast || !legacy && mp.Family == v4Unicast) {
		return fmtAddr(mp.NextHop)
	}
	return f.nextHop
}

// rib prints a TABLE_DUMP_V2 RIB record, one B line per entry.
func (d *dumper) rib(r mrt.RIB, table mrt.PeerIndexTable) {
	if !r.Prefix.IsValid() {
		return // RIB_GENERIC of a family without prefixes
	}
	typ := "TABLE_DUMP2"
	if r.AddPath {
		typ = "TABLE_DUMP2_AP"
	}
	n := 0
	for e, err := range r.Entries() {
		n++
		if err != nil {
			d.malformed("%v", err)
			continue
		}
		if int(e.PeerIndex) >= len(table.Peers) {
			d.malformed("RIB entry %d: peer index %d is not in the peer index table (%d peers)", n, e.PeerIndex, len(table.Peers))
			continue
		}
		p, diags := e.Attrs.Decode()
		d.diagnostics(fmt.Sprintf("RIB entry %d: ", n), "entry's path attributes", diags)
		pathID := int64(-1)
		if r.AddPath {
			pathID = int64(e.PathID)
			if r.Header().Subtype == mrt.SubRIBGenericAddPath {
				pathID = int64(r.PathID)
			}
		}
		peer := table.Peers[e.PeerIndex]
		f := d.fields(e.Attrs, &p)
		d.announce(typ, r, "B", peer.Addr, peer.AS, r.Prefix, pathID, &f, ribNextHop(&p, &f, false))
	}
}

// tableDump prints a legacy TABLE_DUMP record.
func (d *dumper) tableDump(r mrt.TableDump) {
	p, diags := r.Attrs.Decode()
	d.diagnostics("", "path attributes", diags)
	f := d.fields(r.Attrs, &p)
	d.announce("TABLE_DUMP", r, "B", r.Peer.Addr, r.Peer.AS, r.Prefix, -1, &f, ribNextHop(&p, &f, true))
}

// message prints the withdrawals, then the announcements, of a BGP4MP UPDATE.
// Other BGP messages print nothing.
func (d *dumper) message(m mrt.BGP4MPMessage) {
	t, err := m.Type()
	if err != nil {
		d.malformed("%v", err)
		return
	}
	if t != bgp.MessageUpdate {
		return
	}
	u, d1 := bgp.ParseUpdate(m.Message, m.Context())
	d.diagnostics("", "BGP message", d1)
	p, d2 := u.Attrs.Decode()
	d.diagnostics("", "path attributes", d2)

	typ := "BGP4MP"
	if m.Header().Type == mrt.TypeBGP4MPET {
		typ = "BGP4MP_ET"
	}
	if m.Local {
		typ += "_LOCAL"
	}
	if m.AddPath {
		typ += "_AP"
	}
	withdraw := func(ns []bgp.NLRI) {
		for _, n := range ns {
			b := d.start(typ, m, "W")
			b = appendPeer(b, m.PeerAddr, m.PeerAS)
			b = appendPrefix(b, n.Prefix)
			if m.AddPath {
				b = append(b, '|')
				b = strconv.AppendUint(b, uint64(n.PathID), 10)
			}
			d.end(b)
		}
	}
	withdraw(u.Withdrawn)
	if mp := p.MPUnreach; mp != nil && mp.Family.HasPrefixes() {
		withdraw(mp.Withdrawn)
	}
	mp := p.MPReach
	if len(u.Announced) == 0 && (mp == nil || len(mp.NLRI) == 0) {
		return
	}
	f := d.fields(u.Attrs, &p)
	announce := func(ns []bgp.NLRI, nextHop string) {
		for _, n := range ns {
			pathID := int64(-1)
			if m.AddPath {
				pathID = int64(n.PathID)
			}
			d.announce(typ, m, "A", m.PeerAddr, m.PeerAS, n.Prefix, pathID, &f, nextHop)
		}
	}
	announce(u.Announced, f.nextHop)
	if mp != nil && mp.Family.HasPrefixes() {
		nh := "" // a next hop of a length bgpdump does not know
		if mp.NextHop.IsValid() {
			nh = fmtAddr(mp.NextHop)
		}
		announce(mp.NLRI, nh)
	}
}

// stateChange prints a BGP4MP state change: TYPE|time|STATE|peer|AS|old|new.
func (d *dumper) stateChange(s mrt.BGP4MPStateChange) {
	typ := "BGP4MP"
	if s.Header().Type == mrt.TypeBGP4MPET {
		typ = "BGP4MP_ET"
	}
	b := d.start(typ, s, "STATE")
	b = appendPeer(b, s.PeerAddr, s.PeerAS)
	b = strconv.AppendUint(b, uint64(s.Old), 10)
	b = append(b, '|')
	b = strconv.AppendUint(b, uint64(s.New), 10)
	d.end(b)
}
