package bgp

import "sync"

// Interner decodes attribute blocks into paths that share memory: equal RIB
// blocks give one *Path, and paths that differ share their equal AS paths and
// community lists. Every path it returns is a Clone, so none holds the bytes
// it was decoded from, and each deep-equals what Attrs.Decode gives for the
// same block. It is safe for concurrent use and grows until it is dropped.
//
// Returned paths and diagnostics may be shared with other callers: treat them
// as read-only.
type Interner struct {
	mu sync.Mutex

	// The whole-path index, RIB blocks only. clean holds blocks decoded with
	// no diagnostic, keyed by stripKey (the RIB-entry MP_REACH NLRI, which
	// decoding does not read, removed); info holds blocks whose diagnostics
	// are all Info, keyed on their exact bytes, so that the diagnostics'
	// offsets are the block's own. Both are nil until first used.
	clean map[string]*Path
	info  map[string]infoPath

	// Canonical components, shared by every path decoded through in.
	asPaths map[string][]Segment
	comms   map[string][]Community
	larges  map[string][]LargeCommunity
	exts    map[string][]ExtCommunity

	key, tmp []byte // key builders, used under mu
}

// infoPath is an interned path whose diagnostics are all Info.
type infoPath struct {
	p     *Path
	diags []Diagnostic
}

// NewInterner returns an empty Interner.
func NewInterner() *Interner { return new(Interner) }

// Decode decodes a like a.Decode, through in. For a RIB-entry block (Context
// RIB) with no Warning or Error diagnostic it returns the path and diagnostics
// already held for an equal block in the same Context, or holds this one's:
// blocks without diagnostics are equal when they differ only in a full-form
// MP_REACH_NLRI's NLRI, its length field and its flags byte, which decoding a
// RIB entry does not read; blocks with Info diagnostics only when their bytes
// are equal. Any other block (an UPDATE's, or one with a Warning or Error) is
// decoded afresh on every call. Either way the path's AS paths (merged,
// received and AS4) and community, extended community and large community
// lists share their arrays with equal ones decoded before.
//
// The result is a pointer because it is shared: (*p, diags) deep-equals
// a.Decode(), and neither aliases a.Raw().
func (in *Interner) Decode(a Attrs) (*Path, []Diagnostic) {
	if a.ctx.RIB {
		in.mu.Lock()
		p, diags, ok := in.lookup(a)
		in.mu.Unlock()
		if ok {
			return p, diags
		}
	}
	p, diags := a.Decode()
	c := p.Clone()
	whole := a.ctx.RIB
	for _, d := range diags {
		whole = whole && d.Severity == SeverityInfo
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	if whole { // another goroutine may have held an equal block meanwhile
		if diags == nil {
			in.key, _ = appendKey(in.key[:0], a, true)
			if q, ok := in.clean[string(in.key)]; ok {
				return q, nil
			}
		} else {
			in.key, _ = appendKey(in.key[:0], a, false)
			if e, ok := in.info[string(in.key)]; ok {
				return e.p, e.diags
			}
		}
	}
	in.shareParts(&c)
	if whole {
		if in.clean == nil {
			in.clean, in.info = make(map[string]*Path), make(map[string]infoPath)
		}
		if diags == nil {
			in.clean[string(in.key)] = &c
		} else {
			diags = diags[:len(diags):len(diags)] // shared: an append must not write into it
			in.info[string(in.key)] = infoPath{&c, diags}
		}
	}
	return &c, diags
}

// lookup returns the held path for a RIB block. in.mu must be held. It builds
// the keys in in.key, so a hit allocates nothing.
func (in *Interner) lookup(a Attrs) (*Path, []Diagnostic, bool) {
	var stripped bool
	in.key, stripped = appendKey(in.key[:0], a, true)
	if p, ok := in.clean[string(in.key)]; ok {
		return p, nil, true
	}
	if stripped {
		in.key, _ = appendKey(in.key[:0], a, false)
	}
	if e, ok := in.info[string(in.key)]; ok {
		return e.p, e.diags, true
	}
	return nil, nil, false
}

// Len returns the number of whole paths in the index: distinct RIB blocks
// held since NewInterner or the last DropPathIndex.
func (in *Interner) Len() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.clean) + len(in.info)
}

// DropPathIndex forgets the whole-path index, freeing it; Len becomes 0. Paths
// already returned are unaffected, components are still shared, and later RIB
// blocks build a new index.
func (in *Interner) DropPathIndex() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.clean, in.info = nil, nil
}

// mpReachKeyFlags (optional, extended length) are the flags of a stripped
// MP_REACH_NLRI in a key.
const mpReachKeyFlags = 0x80 | flagExtendedLength

// appendKey appends the key of a to dst: one byte of Context flags, then the
// block. With strip, a full-form MP_REACH_NLRI in a RIB entry (accepted by
// decodeMPReach as such: first byte zero and at least its fixed fields and
// next hop present) is written without its NLRI, with flags mpReachKeyFlags
// and a two-byte length: decoding reads neither its NLRI nor its flags.
// stripped reports whether that changed the key. Broken framing ends the walk
// with the rest of the block appended as it is.
func appendKey(dst []byte, a Attrs, strip bool) (_ []byte, stripped bool) {
	var flags byte
	if a.ctx.AS4 {
		flags |= 1
	}
	if a.ctx.AddPath {
		flags |= 2
	}
	if a.ctx.RIB {
		flags |= 4
	}
	dst = append(dst, flags)
	raw := a.raw
	if !strip || !a.ctx.RIB {
		return append(dst, raw...), false
	}
	for off := 0; off < len(raw); {
		at, next, err := nextAttr(raw, off)
		if err != nil {
			return append(dst, raw[off:]...), stripped
		}
		v := at.Value
		if at.Code == AttrMPReach && len(v) >= 5 && v[0] == 0 && len(v) >= 5+int(v[3]) {
			keep := v[:5+int(v[3])] // AFI, SAFI, next hop length, next hop, reserved
			dst = append(dst, mpReachKeyFlags, byte(AttrMPReach), byte(len(keep)>>8), byte(len(keep)))
			dst = append(dst, keep...)
			stripped = stripped || len(keep) != len(v) || raw[off] != mpReachKeyFlags
		} else {
			dst = append(dst, raw[off:next]...)
		}
		off = next
	}
	return dst, stripped
}

// shareParts replaces c's AS paths and community lists with equal ones held
// before, or holds c's. c is a Clone owned by the caller. in.mu must be held.
func (in *Interner) shareParts(c *Path) {
	if in.asPaths == nil {
		in.asPaths = make(map[string][]Segment)
		in.comms = make(map[string][]Community)
		in.larges = make(map[string][]LargeCommunity)
		in.exts = make(map[string][]ExtCommunity)
	}
	c.ASPath.Segments = share(in, in.asPaths, c.ASPath.Segments, appendSegment)
	c.Communities = share(in, in.comms, c.Communities, appendCommunity)
	c.LargeCommunities = share(in, in.larges, c.LargeCommunities, appendLarge)
	if r := c.rare; r != nil {
		if r.merged {
			r.receivedASPath.Segments = share(in, in.asPaths, r.receivedASPath.Segments, appendSegment)
		}
		r.as4Path.Segments = share(in, in.asPaths, r.as4Path.Segments, appendSegment)
		r.extCommunities = share(in, in.exts, r.extCommunities, appendExt)
	}
}

// share returns the slice held in m equal to s, holding s when there is none.
// An empty slice is returned as it is (nil stays nil, empty stays empty).
// Equality is by enc, which must encode a slice injectively. in.mu must be held.
func share[T any](in *Interner, m map[string][]T, s []T, enc func([]byte, T) []byte) []T {
	if len(s) == 0 {
		return s
	}
	in.tmp = in.tmp[:0]
	for _, v := range s {
		in.tmp = enc(in.tmp, v)
	}
	if q, ok := m[string(in.tmp)]; ok {
		return q
	}
	m[string(in.tmp)] = s
	return s
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendSegment(b []byte, s Segment) []byte {
	b = append(b, byte(s.Type))
	b = appendU32(b, uint32(len(s.ASNs)))
	for _, n := range s.ASNs {
		b = appendU32(b, uint32(n))
	}
	return b
}

func appendCommunity(b []byte, c Community) []byte { return appendU32(b, uint32(c)) }

func appendLarge(b []byte, l LargeCommunity) []byte {
	return appendU32(appendU32(appendU32(b, l.Global), l.Local1), l.Local2)
}

func appendExt(b []byte, e ExtCommunity) []byte { return append(b, e[:]...) }
