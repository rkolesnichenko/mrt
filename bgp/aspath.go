package bgp

import "strconv"

// SegmentType is an AS_PATH segment type.
type SegmentType uint8

// AS_PATH segment types.
const (
	ASSet            SegmentType = 1
	ASSequence       SegmentType = 2
	ASConfedSequence SegmentType = 3
	ASConfedSet      SegmentType = 4
)

// Segment is one AS_PATH segment.
type Segment struct {
	Type SegmentType
	ASNs []ASN
}

// ASPath is a sequence of AS_PATH segments.
type ASPath struct{ Segments []Segment }

// Len is the path length of RFC 4271 §9.1.2.2: a sequence counts each ASN, a
// set counts 1, and confederation segments count 0.
func (p ASPath) Len() int {
	n := 0
	for _, s := range p.Segments {
		switch s.Type {
		case ASSequence:
			n += len(s.ASNs)
		case ASSet:
			n++
		}
	}
	return n
}

// Origin returns the last ASN of a final AS_SEQUENCE. It reports false when the
// path is empty or ends in any other kind of segment.
func (p ASPath) Origin() (ASN, bool) {
	if len(p.Segments) == 0 {
		return 0, false
	}
	last := p.Segments[len(p.Segments)-1]
	if last.Type != ASSequence || len(last.ASNs) == 0 {
		return 0, false
	}
	return last.ASNs[len(last.ASNs)-1], true
}

// String formats the path in bgpdump's notation. A sequence is space-separated
// ASNs, an AS_SET is "{a,b}", an AS_CONFED_SEQUENCE is "(a b)" and an
// AS_CONFED_SET is "[a,b]". Segments are joined by one space; an empty path is "".
func (p ASPath) String() string {
	var b []byte
	for i, s := range p.Segments {
		if i > 0 {
			b = append(b, ' ')
		}
		sep, open, closer := byte(' '), "", ""
		switch s.Type {
		case ASSet:
			sep, open, closer = ',', "{", "}"
		case ASConfedSequence:
			open, closer = "(", ")"
		case ASConfedSet:
			sep, open, closer = ',', "[", "]"
		}
		b = append(b, open...)
		for j, a := range s.ASNs {
			if j > 0 {
				b = append(b, sep)
			}
			b = strconv.AppendUint(b, uint64(a), 10)
		}
		b = append(b, closer...)
	}
	return string(b)
}
