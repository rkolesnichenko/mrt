package bgp

import (
	"errors"
	"iter"
	"net/netip"
)

// errTruncated reports attribute framing that runs past the end of the block.
var errTruncated = errors.New("bgp: truncated path attribute")

const flagExtendedLength = 0x10

// Attrs is an undecoded block of path attributes and the context to read it with.
type Attrs struct {
	raw []byte
	ctx Context
}

// NewAttrs returns the attribute block raw, to be read with ctx. It keeps raw, not a copy.
func NewAttrs(raw []byte, ctx Context) Attrs { return Attrs{raw: raw, ctx: ctx} }

// Raw returns the undecoded block.
func (a Attrs) Raw() []byte { return a.raw }

// nextAttr reads the attribute framed at raw[off:]. It returns the attribute
// and the offset just past it. Value slices into raw.
func nextAttr(raw []byte, off int) (Attribute, int, error) {
	rest := raw[off:]
	if len(rest) < 2 {
		return Attribute{}, off, errTruncated
	}
	hdr, n := 3, 0
	if rest[0]&flagExtendedLength != 0 {
		hdr = 4
		if len(rest) < hdr {
			return Attribute{}, off, errTruncated
		}
		n = int(rest[2])<<8 | int(rest[3])
	} else {
		if len(rest) < hdr {
			return Attribute{}, off, errTruncated
		}
		n = int(rest[2])
	}
	if len(rest)-hdr < n {
		return Attribute{}, off, errTruncated
	}
	return Attribute{Flags: rest[0], Code: AttrCode(rest[1]), Value: rest[hdr : hdr+n : hdr+n]}, off + hdr + n, nil
}

// All walks the raw attributes in wire order. When the framing breaks it
// yields one (Attribute{}, err) and stops. Attribute.Value slices into the block.
func (a Attrs) All() iter.Seq2[Attribute, error] {
	return func(yield func(Attribute, error) bool) {
		for off := 0; off < len(a.raw); {
			at, next, err := nextAttr(a.raw, off)
			if err != nil {
				yield(Attribute{}, err)
				return
			}
			if !yield(at, nil) {
				return
			}
			off = next
		}
	}
}

// Path is a decoded attribute block. Presence of an optional scalar is its Has* flag.
type Path struct {
	Origin            Origin
	HasOrigin         bool
	ASPath            ASPath // AS_PATH merged with AS4_PATH (RFC 6793 §4.2.3)
	ReceivedASPath    ASPath // AS_PATH as it was on the wire
	AS4Path           ASPath // AS4_PATH as it was on the wire, if any
	NextHop           netip.Addr
	MED               uint32
	HasMED            bool
	LocalPref         uint32
	HasLocalPref      bool
	AtomicAggregate   bool
	Aggregator        Aggregator // after the AS4_AGGREGATOR rule
	HasAggregator     bool
	Communities       []Community
	ExtCommunities    []ExtCommunity
	LargeCommunities  []LargeCommunity
	OnlyToCustomer    ASN // RFC 9234 OTC
	HasOnlyToCustomer bool
	OriginatorID      netip.Addr
	ClusterList       []netip.Addr
	MPReach           *MPReach
	MPUnreach         *MPUnreach
	Unknown           []Attribute // every attribute not decoded above, in order, raw
}

// Decode reads the block into a Path. A malformed attribute goes raw to
// Unknown with a diagnostic and decoding continues. Broken framing puts the
// rest of the block, header included, in one Unknown entry (Flags and Code
// zero) and stops. Diagnostic.Offset is the attribute's offset in the block.
func (a Attrs) Decode() (Path, []Diagnostic) {
	var (
		p     Path
		diags []Diagnostic
		seen  [256]bool
		as4   as4State
	)
	for off := 0; off < len(a.raw); {
		at, next, err := nextAttr(a.raw, off)
		if err != nil {
			rest := a.raw[off:]
			var code AttrCode
			if len(rest) >= 2 {
				code = AttrCode(rest[1])
			}
			p.Unknown = append(p.Unknown, Attribute{Value: rest[:len(rest):len(rest)]})
			diags = append(diags, Diagnostic{SeverityError, "bgp/attr-truncated", off, code,
				"path attribute framing runs past the end of the block"})
			break
		}
		if seen[at.Code] {
			p.Unknown = append(p.Unknown, at)
			diags = append(diags, Diagnostic{SeverityWarning, "bgp/attr-duplicate", off, at.Code,
				"duplicate path attribute ignored"})
			off = next
			continue
		}
		seen[at.Code] = true // the first copy wins even when it is malformed (RFC 7606 §3.g)
		d, ok := a.decodeAttr(&p, &as4, at, off)
		if !ok {
			p.Unknown = append(p.Unknown, at)
		}
		if d != nil {
			diags = append(diags, *d)
		}
		off = next
	}
	diags = append(diags, applyAS4(&p, &as4, a.ctx)...)
	return p, diags
}

// decodeAttr stores at into p, or into st for what the AS4 rule decides later.
// ok is false when at was not decoded and belongs in Unknown; the diagnostic,
// if any, explains why (or notes a decoded oddity).
func (a Attrs) decodeAttr(p *Path, st *as4State, at Attribute, off int) (d *Diagnostic, ok bool) {
	diag := func(sev Severity, code, msg string) *Diagnostic {
		return &Diagnostic{sev, code, off, at.Code, msg}
	}
	badLen := func(msg string) (*Diagnostic, bool) { return diag(SeverityWarning, "bgp/attr-length", msg), false }
	v := at.Value
	switch at.Code {
	case AttrOrigin:
		if len(v) != 1 {
			return badLen("ORIGIN is not 1 byte")
		}
		if v[0] > 2 {
			return diag(SeverityWarning, "bgp/attr-value", "ORIGIN value above 2"), false
		}
		p.Origin, p.HasOrigin = Origin(v[0]), true
	case AttrASPath:
		width := 2
		if a.ctx.AS4 {
			width = 4
		}
		asp, good := parseASPath(v, width)
		if !good {
			return diag(SeverityWarning, "bgp/aspath-width", "AS_PATH does not parse at the record's AS width"), false
		}
		p.ReceivedASPath = asp // ASPath is set from it after the AS4 rule
	case AttrAS4Path:
		asp, good := parseASPath(v, 4)
		if !good || len(v) == 0 { // RFC 6793 §6: too short to carry an AS is malformed too
			return diag(SeverityWarning, "bgp/attr-value", "AS4_PATH is malformed"), false
		}
		// RFC 6793 §6: confederation segments must not be in AS4_PATH; they
		// are discarded and the rest of the attribute is used.
		kept := asp.Segments[:0]
		for _, s := range asp.Segments {
			if s.Type != ASConfedSequence && s.Type != ASConfedSet {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			return diag(SeverityWarning, "bgp/attr-value", "AS4_PATH holds only confederation segments, all discarded"), false
		}
		p.AS4Path, st.hasAS4Path, st.as4PathOff = ASPath{kept}, true, off
		if len(kept) < len(asp.Segments) {
			return diag(SeverityWarning, "bgp/attr-value", "AS4_PATH confederation segments discarded"), true
		}
	case AttrAggregator:
		switch {
		case !a.ctx.AS4 && len(v) == 6:
			st.agg = Aggregator{ASN(v[0])<<8 | ASN(v[1]), netip.AddrFrom4([4]byte(v[2:]))}
		case a.ctx.AS4 && len(v) == 8:
			st.agg = Aggregator{ASN(be32(v)), netip.AddrFrom4([4]byte(v[4:]))}
		case a.ctx.AS4:
			return badLen("AGGREGATOR is not 8 bytes in a 4-byte AS context")
		default:
			return badLen("AGGREGATOR is not 6 bytes in a 2-byte AS context")
		}
		st.hasAgg = true
	case AttrAS4Aggregator:
		if len(v) != 8 {
			return badLen("AS4_AGGREGATOR is not 8 bytes")
		}
		st.as4Agg = Aggregator{ASN(be32(v)), netip.AddrFrom4([4]byte(v[4:]))}
		st.hasAS4Agg, st.as4AggOff = true, off
	case AttrNextHop:
		if len(v) != 4 {
			return badLen("NEXT_HOP is not 4 bytes")
		}
		p.NextHop = netip.AddrFrom4([4]byte(v))
	case AttrMED:
		if len(v) != 4 {
			return badLen("MULTI_EXIT_DISC is not 4 bytes")
		}
		p.MED, p.HasMED = be32(v), true
	case AttrLocalPref:
		if len(v) != 4 {
			return badLen("LOCAL_PREF is not 4 bytes")
		}
		p.LocalPref, p.HasLocalPref = be32(v), true
	case AttrAtomicAggregate:
		if len(v) != 0 {
			return badLen("ATOMIC_AGGREGATE is not empty")
		}
		p.AtomicAggregate = true
	case AttrCommunities:
		if len(v)%4 != 0 {
			return badLen("COMMUNITIES is not a multiple of 4 bytes")
		}
		p.Communities = make([]Community, len(v)/4)
		for i := range p.Communities {
			p.Communities[i] = Community(be32(v[4*i:]))
		}
	case AttrOriginatorID:
		if len(v) != 4 {
			return badLen("ORIGINATOR_ID is not 4 bytes")
		}
		p.OriginatorID = netip.AddrFrom4([4]byte(v))
	case AttrClusterList:
		if len(v)%4 != 0 {
			return badLen("CLUSTER_LIST is not a multiple of 4 bytes")
		}
		p.ClusterList = make([]netip.Addr, len(v)/4)
		for i := range p.ClusterList {
			p.ClusterList[i] = netip.AddrFrom4([4]byte(v[4*i:]))
		}
	case AttrExtCommunities:
		if len(v)%8 != 0 {
			return badLen("EXTENDED COMMUNITIES is not a multiple of 8 bytes")
		}
		p.ExtCommunities = make([]ExtCommunity, len(v)/8)
		for i := range p.ExtCommunities {
			copy(p.ExtCommunities[i][:], v[8*i:])
		}
	case AttrLargeCommunity:
		if len(v)%12 != 0 {
			return badLen("LARGE_COMMUNITY is not a multiple of 12 bytes")
		}
		p.LargeCommunities = make([]LargeCommunity, len(v)/12)
		for i := range p.LargeCommunities {
			w := v[12*i:]
			p.LargeCommunities[i] = LargeCommunity{be32(w), be32(w[4:]), be32(w[8:])}
		}
	case AttrOnlyToCustomer:
		if len(v) != 4 {
			return badLen("OTC is not 4 bytes")
		}
		p.OnlyToCustomer, p.HasOnlyToCustomer = ASN(be32(v)), true
	default:
		// Unknown, and (until decoded) MP_REACH_NLRI and MP_UNREACH_NLRI, stay raw.
		return nil, false
	}
	return nil, true
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
