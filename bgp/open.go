package bgp

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
)

// Open is a parsed BGP OPEN message (RFC 4271 §4.2, RFC 9072).
type Open struct {
	Version      uint8
	MyAS         ASN // the 2-byte field; AS_TRANS when the speaker's AS needs 4 bytes
	HoldTime     uint16
	BGPID        netip.Addr
	Capabilities []Capability // every capability, in order, raw; Values alias the message
}

// CapabilityCode is a BGP capability code (RFC 5492).
type CapabilityCode uint8

// Capability codes this package reads.
const (
	CapMultiprotocol CapabilityCode = 1  // RFC 4760
	CapAS4           CapabilityCode = 65 // RFC 6793
	CapAddPath       CapabilityCode = 69 // RFC 7911
)

// Capability is one capability as sent.
type Capability struct {
	Code  CapabilityCode
	Value []byte
}

// AddPathFamily is one tuple of the ADD-PATH capability.
type AddPathFamily struct {
	Family Family
	Mode   uint8 // Send/Receive as on the wire: 1 receive, 2 send, 3 both
}

// Receive reports whether the speaker can receive path identifiers (Mode 1 or 3).
func (f AddPathFamily) Receive() bool { return f.Mode == 1 || f.Mode == 3 }

// Send reports whether the speaker can send path identifiers (Mode 2 or 3).
func (f AddPathFamily) Send() bool { return f.Mode == 2 || f.Mode == 3 }

const (
	openFixedLen = 29  // header, version, My AS, hold time, BGP ID, optional parameters length
	paramTypeCap = 2   // optional parameter type: capabilities (RFC 5492)
	paramTypeExt = 255 // RFC 9072: the extended form follows
)

// walkCaps calls fn for every capability in params, the optional parameters
// (base is the offset of params[0] in the message; ext says parameter lengths
// are 2 bytes). Parameters of types other than 2 are skipped. fn gets the
// capability's offset, code and value. It returns the offset and cause of the
// first field that runs past params, and ok false then.
func walkCaps(params []byte, base int, ext bool, fn func(off int, c Capability)) (off int, what string, ok bool) {
	hdr := 2
	if ext {
		hdr = 3
	}
	for p := 0; p < len(params); {
		if len(params)-p < hdr {
			return base + p, "optional parameter header runs past the optional parameters", false
		}
		typ := params[p]
		n := int(params[p+1])
		if ext {
			n = int(binary.BigEndian.Uint16(params[p+1:]))
		}
		if n > len(params)-p-hdr {
			return base + p, fmt.Sprintf("optional parameter of %d bytes runs past the optional parameters", n), false
		}
		val := params[p+hdr : p+hdr+n]
		if typ == paramTypeCap {
			for q := 0; q < len(val); {
				if len(val)-q < 2 {
					return base + p + hdr + q, "capability header runs past its parameter", false
				}
				l := int(val[q+1])
				if l > len(val)-q-2 {
					return base + p + hdr + q, fmt.Sprintf("capability of %d bytes runs past its parameter", l), false
				}
				fn(base+p+hdr+q, Capability{CapabilityCode(val[q]), val[q+2 : q+2+l]})
				q += 2 + l
			}
		}
		p += hdr + n
	}
	return 0, "", true
}

// ParseOpen reads the OPEN message msg, marker and header included.
// A bad header or a message of another type gives an empty Open and one Error
// diagnostic (DiagMessageHeader, DiagNotOpen). Fields that run past the
// message give an Error (DiagOpenLength), or DiagMessageTruncated when the
// message is cut short of its header's length, and what parsed before them is
// kept. A capability with a wrong length for its code, an ADD-PATH tuple with
// a Send/Receive value outside 1-3 or a family listed twice, and an ADD-PATH
// capability after the first give a Warning (DiagCapabilityValue); the
// capability stays in Capabilities and the accessors skip or ignore it as
// described on them. Parameters other than capabilities are skipped.
// Capability values alias msg.
func ParseOpen(msg []byte) (Open, []Diagnostic) {
	var o Open
	hdr, err := ParseMessageHeader(msg)
	if err != nil {
		return o, []Diagnostic{{SeverityError, DiagMessageHeader, 0, 0, err.Error()}}
	}
	if hdr.Type != MessageOpen {
		return o, []Diagnostic{{SeverityError, DiagNotOpen, 18, 0,
			fmt.Sprintf("message type %d is not OPEN", hdr.Type)}}
	}
	var diags []Diagnostic
	cut := hdr.Length > len(msg)
	if hdr.Length < len(msg) {
		diags = append(diags, Diagnostic{SeverityWarning, DiagMessageHeader, 16, 0,
			fmt.Sprintf("%d bytes follow the message's own length of %d", len(msg)-hdr.Length, hdr.Length)})
		msg = msg[:hdr.Length]
	}
	short := func(off int, what string) []Diagnostic {
		if cut {
			return append(diags, Diagnostic{SeverityError, DiagMessageTruncated, len(msg), 0,
				fmt.Sprintf("message cut at %d of its %d bytes; read as far as whole fields go", len(msg), hdr.Length)})
		}
		return append(diags, Diagnostic{SeverityError, DiagOpenLength, off, 0, what})
	}
	if len(msg) < openFixedLen {
		return o, short(len(msg), fmt.Sprintf("OPEN of %d bytes is shorter than its fixed part of %d", len(msg), openFixedLen))
	}
	o.Version = msg[19]
	o.MyAS = ASN(binary.BigEndian.Uint16(msg[20:]))
	o.HoldTime = binary.BigEndian.Uint16(msg[22:])
	o.BGPID = netip.AddrFrom4([4]byte(msg[24:28]))

	// RFC 9072 §2: a non-zero length followed by type 255 is the extended form.
	optLen, start, ext := int(msg[28]), openFixedLen, false
	if optLen != 0 && len(msg) > openFixedLen && msg[openFixedLen] == paramTypeExt {
		ext = true
		if len(msg) < openFixedLen+3 {
			return o, short(len(msg), "extended optional parameters length runs past the message")
		}
		optLen, start = int(binary.BigEndian.Uint16(msg[openFixedLen+1:])), openFixedLen+3
	}
	avail := len(msg) - start
	declared, overran := optLen, optLen > avail
	if overran {
		diags = short(start, fmt.Sprintf("optional parameters of %d bytes run past the %d bytes present", optLen, avail))
		optLen = avail
	}
	params := msg[start : start+optLen]
	if declared < avail {
		diags = append(diags, Diagnostic{SeverityWarning, DiagOpenLength, start + declared, 0,
			fmt.Sprintf("%d bytes follow the optional parameters", avail-declared)})
	}

	// The first pass counts, so Capabilities is sized by bytes present and not by a length field.
	n := 0
	walkCaps(params, start, ext, func(int, Capability) { n++ })
	if n > 0 {
		o.Capabilities = make([]Capability, 0, n)
	}
	seenAddPath := false
	off, what, ok := walkCaps(params, start, ext, func(off int, c Capability) {
		o.Capabilities = append(o.Capabilities, c)
		if msg := capabilityProblem(c, &seenAddPath); msg != "" {
			diags = append(diags, Diagnostic{SeverityWarning, DiagCapabilityValue, off, 0, msg})
		}
	})
	if !ok && !overran { // a parameter cut by the overrun already drew its diagnostic
		diags = short(off, what)
	}
	if cut && !slices.ContainsFunc(diags, func(d Diagnostic) bool { return d.Code == DiagMessageTruncated }) {
		diags = short(len(msg), "") // cut between fields: still say so once
	}
	return o, diags
}

// capabilityProblem checks c against its code and returns what is wrong with
// it, or "". seenAddPath is set by the first ADD-PATH capability and read by
// the following ones.
func capabilityProblem(c Capability, seenAddPath *bool) string {
	switch c.Code {
	case CapMultiprotocol, CapAS4:
		if len(c.Value) != 4 {
			return fmt.Sprintf("capability %d has %d value bytes, want 4", c.Code, len(c.Value))
		}
	case CapAddPath:
		if *seenAddPath {
			return "second ADD-PATH capability ignored (RFC 7911 §4: one instance carries every family)"
		}
		*seenAddPath = true
		if len(c.Value)%4 != 0 {
			return fmt.Sprintf("ADD-PATH capability has %d value bytes, want a multiple of 4", len(c.Value))
		}
		_, ignored, dup := addPathTuples(c.Value)
		if ignored {
			return "ADD-PATH Send/Receive value outside 1-3: capability ignored (RFC 7911 §4)"
		}
		if dup {
			return "ADD-PATH family listed twice; the first tuple wins"
		}
	}
	return ""
}

// addPathTuples reads the tuples of one ADD-PATH value whose length is a
// multiple of 4. A family listed again is dropped (dup), and ignored says
// some tuple, kept or dropped, has a Mode outside 1-3.
func addPathTuples(v []byte) (tuples []AddPathFamily, ignored, dup bool) {
	if len(v) < 4 {
		return nil, false, false
	}
	tuples = make([]AddPathFamily, 0, len(v)/4)
	for ; len(v) >= 4; v = v[4:] {
		t := AddPathFamily{Family{AFI(binary.BigEndian.Uint16(v)), SAFI(v[2])}, v[3]}
		if t.Mode < 1 || t.Mode > 3 {
			ignored = true
		}
		if slices.ContainsFunc(tuples, func(u AddPathFamily) bool { return u.Family == t.Family }) {
			dup = true
			continue
		}
		tuples = append(tuples, t)
	}
	return tuples, ignored, dup
}

// firstValid returns the first capability with the given code and a value of
// n bytes; later ones are skipped.
func (o Open) firstValid(code CapabilityCode, n int) (Capability, bool) {
	for _, c := range o.Capabilities {
		if c.Code == code && len(c.Value) == n {
			return c, true
		}
	}
	return Capability{}, false
}

// AS returns the AS of capability 65, else MyAS. A capability 65 with a value
// of the wrong length is skipped.
func (o Open) AS() ASN {
	if c, ok := o.firstValid(CapAS4, 4); ok {
		return ASN(binary.BigEndian.Uint32(c.Value))
	}
	return o.MyAS
}

// AS4 reports whether the speaker sent a valid capability 65.
func (o Open) AS4() bool {
	_, ok := o.firstValid(CapAS4, 4)
	return ok
}

// Multiprotocol returns the families of the capability 1 entries, in order.
// An entry with a value of the wrong length is skipped.
func (o Open) Multiprotocol() []Family {
	var out []Family
	for _, c := range o.Capabilities {
		if c.Code == CapMultiprotocol && len(c.Value) == 4 {
			out = append(out, Family{AFI(binary.BigEndian.Uint16(c.Value)), SAFI(c.Value[3])})
		}
	}
	return out
}

// addPath returns the tuples of the first ADD-PATH capability, and whether
// RFC 7911 §4 says to ignore the capability (a Mode outside 1-3). A first
// instance of the wrong length gives none, and later instances are never used.
func (o Open) addPath() (tuples []AddPathFamily, ignored bool) {
	for _, c := range o.Capabilities {
		if c.Code == CapAddPath {
			if len(c.Value)%4 != 0 {
				return nil, false
			}
			tuples, ignored, _ = addPathTuples(c.Value)
			return tuples, ignored
		}
	}
	return nil, false
}

// AddPath returns the tuples of the first ADD-PATH capability with their Mode
// as sent, also those AddPathFamilies ignores because a Mode is outside 1-3
// (Loc-RIB needs only the families). A family listed twice keeps its first
// tuple. A capability of the wrong length, and every ADD-PATH capability after
// the first, give none.
func (o Open) AddPath() []AddPathFamily {
	t, _ := o.addPath()
	return t
}

// AddPathFamilies is the set of families for which sender sends path
// identifiers to receiver (RFC 7911 §5): sender's Mode is 2 or 3 and
// receiver's is 1 or 3. A speaker with a Mode outside 1-3 in its ADD-PATH
// capability is treated as not having sent it (RFC 7911 §4).
func AddPathFamilies(sender, receiver Open) FamilySet {
	var set FamilySet
	st, sIgnored := sender.addPath()
	rt, rIgnored := receiver.addPath()
	if sIgnored || rIgnored {
		return set
	}
	for _, s := range st {
		if !s.Send() {
			continue
		}
		if slices.ContainsFunc(rt, func(r AddPathFamily) bool { return r.Family == s.Family && r.Receive() }) {
			set = set.With(s.Family)
		}
	}
	return set
}
