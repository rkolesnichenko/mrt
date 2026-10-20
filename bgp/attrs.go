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
//
// The attributes most paths carry are fields; the rest (NEXT_HOP excepted, which
// is held as its four bytes) live in a part allocated only when the path has
// one of them, and are read through methods. A Path is produced by decoding;
// the methods' values cannot be set from outside the package.
type Path struct {
	ASPath           ASPath // AS_PATH merged with AS4_PATH (RFC 6793 §4.2.3); see ReceivedASPath
	Communities      []Community
	LargeCommunities []LargeCommunity
	MPReach          *MPReach
	MED, LocalPref   uint32
	OnlyToCustomer   ASN // RFC 9234 OTC
	Origin           Origin

	// Presence flags, and ATOMIC_AGGREGATE (it has no value).
	HasOrigin, HasMED, HasLocalPref, AtomicAggregate, HasOnlyToCustomer bool

	nextHop    [4]byte
	hasNextHop bool
	rare       *pathRare // nil when the path has none of what it holds
}

// pathRare holds what few paths carry. Aggregator and ORIGINATOR_ID addresses
// are IPv4 on the wire and are held as their four bytes.
type pathRare struct {
	receivedASPath ASPath // AS_PATH as on the wire; set only when AS4_PATH was merged
	as4Path        ASPath
	extCommunities []ExtCommunity
	clusterList    []netip.Addr
	unknown        []Attribute
	mpUnreach      *MPUnreach
	aggregatorAS   ASN
	aggregatorAddr [4]byte
	originatorID   [4]byte
	merged         bool // receivedASPath is set
	hasAggregator  bool
	hasOriginator  bool
}

// rarePart returns p's rare part, allocating it on first use.
func (p *Path) rarePart() *pathRare {
	if p.rare == nil {
		p.rare = new(pathRare)
	}
	return p.rare
}

// addUnknown appends a to the attributes Unknown returns.
func (p *Path) addUnknown(a Attribute) {
	r := p.rarePart()
	r.unknown = append(r.unknown, a)
}

// NextHop returns the NEXT_HOP attribute; the zero Addr when it is absent or
// malformed. A next hop carried in MP_REACH_NLRI is MPReach's, not this.
func (p Path) NextHop() netip.Addr {
	if !p.hasNextHop {
		return netip.Addr{}
	}
	return netip.AddrFrom4(p.nextHop)
}

// ReceivedASPath returns AS_PATH as it was on the wire. It is ASPath itself
// (the same slices) unless AS4_PATH was merged into ASPath.
func (p Path) ReceivedASPath() ASPath {
	if p.rare != nil && p.rare.merged {
		return p.rare.receivedASPath
	}
	return p.ASPath
}

// AS4Path returns AS4_PATH as it was on the wire, confederation segments
// discarded; the zero ASPath when absent or malformed. It is returned whether
// or not the AS4 rule merged it.
func (p Path) AS4Path() ASPath {
	if p.rare == nil {
		return ASPath{}
	}
	return p.rare.as4Path
}

// Aggregator returns the aggregator after the AS4_AGGREGATOR rule, and whether
// there is one.
func (p Path) Aggregator() (Aggregator, bool) {
	if p.rare == nil || !p.rare.hasAggregator {
		return Aggregator{}, false
	}
	return Aggregator{p.rare.aggregatorAS, netip.AddrFrom4(p.rare.aggregatorAddr)}, true
}

// ExtCommunities returns the EXTENDED COMMUNITIES attribute; nil when absent.
func (p Path) ExtCommunities() []ExtCommunity {
	if p.rare == nil {
		return nil
	}
	return p.rare.extCommunities
}

// OriginatorID returns the ORIGINATOR_ID attribute; the zero Addr when absent
// or malformed.
func (p Path) OriginatorID() netip.Addr {
	if p.rare == nil || !p.rare.hasOriginator {
		return netip.Addr{}
	}
	return netip.AddrFrom4(p.rare.originatorID)
}

// ClusterList returns the CLUSTER_LIST attribute; nil when absent.
func (p Path) ClusterList() []netip.Addr {
	if p.rare == nil {
		return nil
	}
	return p.rare.clusterList
}

// MPUnreach returns the decoded MP_UNREACH_NLRI attribute; nil when absent or
// malformed.
func (p Path) MPUnreach() *MPUnreach {
	if p.rare == nil {
		return nil
	}
	return p.rare.mpUnreach
}

// Unknown returns every attribute not decoded into the rest of the Path, in
// order, raw: unknown codes, malformed attributes and later duplicates.
func (p Path) Unknown() []Attribute {
	if p.rare == nil {
		return nil
	}
	return p.rare.unknown
}

// Clone returns a deep copy of p that shares no memory with p or with the
// attribute block p was decoded from: every slice, MPReach, the rare part,
// MPUnreach and each Unknown()[i].Value is copied. A nil slice stays nil and an
// empty one stays empty.
func (p Path) Clone() Path {
	c := p
	c.ASPath = p.ASPath.clone()
	c.Communities = cloneSlice(p.Communities)
	c.LargeCommunities = cloneSlice(p.LargeCommunities)
	if p.MPReach != nil {
		r := *p.MPReach
		r.NLRI = cloneSlice(r.NLRI)
		r.RawNLRI = cloneSlice(r.RawNLRI)
		c.MPReach = &r
	}
	if p.rare != nil {
		c.rare = p.rare.clone()
	}
	return c
}

func (r *pathRare) clone() *pathRare {
	c := *r
	c.receivedASPath = r.receivedASPath.clone()
	c.as4Path = r.as4Path.clone()
	c.extCommunities = cloneSlice(r.extCommunities)
	c.clusterList = cloneSlice(r.clusterList)
	if r.mpUnreach != nil {
		u := *r.mpUnreach
		u.Withdrawn = cloneSlice(u.Withdrawn)
		u.RawNLRI = cloneSlice(u.RawNLRI)
		c.mpUnreach = &u
	}
	if r.unknown != nil {
		c.unknown = make([]Attribute, len(r.unknown))
		for i, a := range r.unknown {
			a.Value = cloneSlice(a.Value)
			c.unknown[i] = a
		}
	}
	return &c
}

// cloneSlice copies s, keeping nil as nil and empty as empty non-nil.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append(make([]T, 0, len(s)), s...)
}

func (p ASPath) clone() ASPath {
	if p.Segments == nil {
		return p
	}
	segs := make([]Segment, len(p.Segments))
	for i, s := range p.Segments {
		s.ASNs = cloneSlice(s.ASNs)
		segs[i] = s
	}
	return ASPath{Segments: segs}
}

// Decode reads the block into a Path. A malformed attribute goes raw to
// Unknown with a diagnostic and decoding continues. Broken framing puts the
// rest of the block, header included, in one Unknown entry (Flags and Code
// zero) and stops. Diagnostic.Offset is the attribute's offset in the block.
// Path.Unknown()[i].Value, MPReach.RawNLRI (outside a RIB entry) and MPUnreach().RawNLRI alias the block.
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
			p.addUnknown(Attribute{Value: rest[:len(rest):len(rest)]})
			diags = append(diags, Diagnostic{SeverityError, DiagAttrTruncated, off, code,
				"path attribute framing runs past the end of the block"})
			break
		}
		if seen[at.Code] {
			p.addUnknown(at)
			diags = append(diags, Diagnostic{SeverityWarning, DiagAttrDuplicate, off, at.Code,
				"duplicate path attribute ignored"})
			off = next
			continue
		}
		seen[at.Code] = true // the first copy wins even when it is malformed (RFC 7606 §3.g)
		var ds []Diagnostic
		var ok bool
		switch at.Code {
		case AttrMPReach:
			var r MPReach
			r, ok, ds = decodeMPReach(at.Value, a.ctx, off)
			if ok {
				p.MPReach = &r
			}
		case AttrMPUnreach:
			var r MPUnreach
			r, ok, ds = decodeMPUnreach(at.Value, a.ctx, off)
			if ok {
				p.rarePart().mpUnreach = &r
			}
		default:
			var d *Diagnostic
			d, ok = a.decodeAttr(&p, &as4, at, off)
			if d != nil {
				ds = []Diagnostic{*d}
			}
		}
		if !ok {
			p.addUnknown(at)
		}
		diags = append(diags, ds...)
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
	badLen := func(msg string) (*Diagnostic, bool) { return diag(SeverityWarning, DiagAttrLength, msg), false }
	v := at.Value
	switch at.Code {
	case AttrOrigin:
		if len(v) != 1 {
			return badLen("ORIGIN is not 1 byte")
		}
		if v[0] > 2 {
			return diag(SeverityWarning, DiagAttrValue, "ORIGIN value above 2"), false
		}
		p.Origin, p.HasOrigin = Origin(v[0]), true
	case AttrASPath:
		width := 2
		if a.ctx.AS4 {
			width = 4
		}
		asp, good := parseASPath(v, width)
		if !good {
			return diag(SeverityWarning, DiagASPathWidth, "AS_PATH does not parse at the record's AS width"), false
		}
		p.ASPath = asp // the AS4 rule may replace it with the merged path
	case AttrAS4Path:
		asp, good := parseASPath(v, 4)
		if !good || len(v) == 0 { // RFC 6793 §6: too short to carry an AS is malformed too
			return diag(SeverityWarning, DiagAttrValue, "AS4_PATH is malformed"), false
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
			return diag(SeverityWarning, DiagAttrValue, "AS4_PATH holds only confederation segments, all discarded"), false
		}
		p.rarePart().as4Path, st.hasAS4Path, st.as4PathOff = ASPath{kept}, true, off
		if len(kept) < len(asp.Segments) {
			return diag(SeverityWarning, DiagAttrValue, "AS4_PATH confederation segments discarded"), true
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
		p.nextHop, p.hasNextHop = [4]byte(v), true
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
		r := p.rarePart()
		r.originatorID, r.hasOriginator = [4]byte(v), true
	case AttrClusterList:
		if len(v)%4 != 0 {
			return badLen("CLUSTER_LIST is not a multiple of 4 bytes")
		}
		cl := make([]netip.Addr, len(v)/4)
		for i := range cl {
			cl[i] = netip.AddrFrom4([4]byte(v[4*i:]))
		}
		p.rarePart().clusterList = cl
	case AttrExtCommunities:
		if len(v)%8 != 0 {
			return badLen("EXTENDED COMMUNITIES is not a multiple of 8 bytes")
		}
		ext := make([]ExtCommunity, len(v)/8)
		for i := range ext {
			copy(ext[i][:], v[8*i:])
		}
		p.rarePart().extCommunities = ext
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
		// Unknown attributes stay raw (MP_REACH_NLRI and MP_UNREACH_NLRI are decoded in Decode).
		return nil, false
	}
	return nil, true
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
