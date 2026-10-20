package bgp

// as4State is what Decode gathers for the RFC 6793 §4.2.3 rule, which needs
// AS_PATH, AS4_PATH, AGGREGATOR and AS4_AGGREGATOR together. A has* flag is
// set only when that attribute decoded; a malformed one already has its own
// diagnostic and takes no part.
type as4State struct {
	agg, as4Agg           Aggregator
	hasAgg, hasAS4Agg     bool
	hasAS4Path            bool
	as4PathOff, as4AggOff int // offsets of AS4_PATH and AS4_AGGREGATOR in the block
}

// parseASPath reads AS_PATH segments of width-byte ASNs (2 or 4). Each segment
// is type(1) count(1) then count ASNs. The value must be consumed exactly; a
// segment type outside 1–4, a count of 0, or bytes missing or left over makes
// it malformed (ok false). An empty value is the empty path.
func parseASPath(v []byte, width int) (ASPath, bool) {
	var segs []Segment
	for off := 0; off < len(v); {
		if len(v)-off < 2 {
			return ASPath{}, false
		}
		typ, n := SegmentType(v[off]), int(v[off+1])
		off += 2
		if typ < ASSet || typ > ASConfedSet || n == 0 || len(v)-off < n*width {
			return ASPath{}, false
		}
		asns := make([]ASN, n)
		for i := range asns {
			if width == 2 {
				asns[i] = ASN(v[off])<<8 | ASN(v[off+1])
			} else {
				asns[i] = ASN(be32(v[off:]))
			}
			off += width
		}
		segs = append(segs, Segment{typ, asns})
	}
	return ASPath{segs}, true
}

// mergeAS4 builds the AS path from AS_PATH and AS4_PATH by RFC 6793 §4.2.3.
// ok is false when AS_PATH counts fewer ASes than AS4_PATH, which is then to be
// ignored. Otherwise leading ASes and segments of AS_PATH are taken until the
// count reaches AS_PATH.Len() − AS4_PATH.Len(): an AS_SEQUENCE may be split, an
// AS_SET counts 1 and is taken whole, and a confederation segment counts 0 and
// is taken when it leads or follows a segment taken whole. AS4_PATH's segments
// follow; a taken AS_SEQUENCE and AS4_PATH's leading AS_SEQUENCE are joined
// into one. The result shares no memory with either input.
func mergeAS4(asPath, as4Path ASPath) (ASPath, bool) {
	need := asPath.Len() - as4Path.Len()
	if need < 0 {
		return asPath, false
	}
	var out []Segment
	count := 0
	for _, s := range asPath.Segments {
		if s.Type == ASConfedSequence || s.Type == ASConfedSet {
			out = append(out, Segment{s.Type, append([]ASN(nil), s.ASNs...)})
			continue
		}
		if count >= need {
			break
		}
		if s.Type == ASSet {
			out = append(out, Segment{s.Type, append([]ASN(nil), s.ASNs...)})
			count++
			continue
		}
		take := min(len(s.ASNs), need-count)
		out = append(out, Segment{s.Type, append([]ASN(nil), s.ASNs[:take]...)})
		count += take
		if take < len(s.ASNs) {
			break // the rest of this sequence is not taken, so nothing after it is adjacent
		}
	}
	for i, s := range as4Path.Segments {
		if i == 0 && len(out) > 0 && out[len(out)-1].Type == ASSequence && s.Type == ASSequence {
			last := &out[len(out)-1]
			last.ASNs = append(last.ASNs, s.ASNs...)
			continue
		}
		out = append(out, Segment{s.Type, append([]ASN(nil), s.ASNs...)})
	}
	return ASPath{out}, true
}

// applyAS4 runs the RFC 6793 §4.2.3 rule over what Decode gathered, setting
// the aggregator and, when it merges AS4_PATH, p.ASPath (keeping the received
// AS_PATH in the rare part). With ctx.AS4, or when AGGREGATOR and
// AS4_AGGREGATOR both decoded and AGGREGATOR's AS is not AS_TRANS, AGGREGATOR
// is the aggregator and the AS4 attributes are ignored. Otherwise the
// aggregator is AS4_AGGREGATOR when it decoded (else AGGREGATOR) and AS4_PATH
// is merged into AS_PATH. It returns an Info diagnostic when it ignored a
// decoded AS4_PATH or AS4_AGGREGATOR, naming AS4_PATH when that was present.
func applyAS4(p *Path, st *as4State, ctx Context) []Diagnostic {
	ignored := func(msg string) []Diagnostic {
		d := Diagnostic{SeverityInfo, DiagAS4PathIgnored, st.as4PathOff, AttrAS4Path, msg}
		if !st.hasAS4Path {
			d.Offset, d.Attr = st.as4AggOff, AttrAS4Aggregator
		}
		return []Diagnostic{d}
	}
	if ctx.AS4 || (st.hasAgg && st.hasAS4Agg && st.agg.AS != ASTrans) {
		if st.hasAgg {
			p.setAggregator(st.agg)
		}
		switch {
		case !st.hasAS4Path && !st.hasAS4Agg:
			return nil
		case ctx.AS4:
			return ignored("AS4_PATH/AS4_AGGREGATOR ignored: AS_PATH already carries 4-byte ASNs")
		default:
			return ignored("AS4_PATH/AS4_AGGREGATOR ignored: AGGREGATOR's AS is not AS_TRANS")
		}
	}
	switch {
	case st.hasAS4Agg:
		p.setAggregator(st.as4Agg)
	case st.hasAgg:
		p.setAggregator(st.agg)
	}
	if !st.hasAS4Path {
		return nil
	}
	r := p.rare // non-nil: AS4_PATH decoded into it
	merged, ok := mergeAS4(p.ASPath, r.as4Path)
	if !ok {
		return ignored("AS4_PATH ignored: it counts more ASes than AS_PATH")
	}
	r.receivedASPath, r.merged = p.ASPath, true
	p.ASPath = merged
	return nil
}

// setAggregator stores a, whose address is IPv4 (it was decoded from 4 bytes).
func (p *Path) setAggregator(a Aggregator) {
	r := p.rarePart()
	r.aggregatorAS, r.aggregatorAddr, r.hasAggregator = a.AS, a.Addr.As4(), true
}
