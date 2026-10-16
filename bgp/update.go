package bgp

import (
	"errors"
	"fmt"
)

// Sentinel errors from ParseMessageHeader.
var (
	ErrShortMessage = errors.New("bgp: message shorter than its header")
	ErrMarker       = errors.New("bgp: bad marker")
)

const headerLen = 19

// MessageHeader is the fixed header of a BGP message.
type MessageHeader struct {
	Type   MessageType
	Length int // from the header, marker and header included
}

// ParseMessageHeader reads the 19-byte header of msg. It errors with
// ErrShortMessage when msg is shorter than a header or the header's length
// field is less than 19, and with ErrMarker when the marker is not 16 bytes
// of 0xFF. A length field beyond the bytes present is not an error: the
// message was cut short (as old collectors wrote messages longer than their
// buffer), and ParseUpdate reads what is there.
func ParseMessageHeader(msg []byte) (MessageHeader, error) {
	if len(msg) < headerLen {
		return MessageHeader{}, fmt.Errorf("%w: %d bytes", ErrShortMessage, len(msg))
	}
	for _, c := range msg[:16] {
		if c != 0xFF {
			return MessageHeader{}, ErrMarker
		}
	}
	n := int(msg[16])<<8 | int(msg[17])
	if n < headerLen {
		return MessageHeader{}, fmt.Errorf("%w: header length %d", ErrShortMessage, n)
	}
	return MessageHeader{Type: MessageType(msg[18]), Length: n}, nil
}

// Update is the parsed framing of a BGP UPDATE message.
type Update struct {
	Withdrawn []NLRI // IPv4 unicast, from the Withdrawn Routes field
	Attrs     Attrs
	Announced []NLRI // IPv4 unicast, from the NLRI field
}

// ParseUpdate reads the UPDATE message msg, marker and header included.
// A bad header or a message of another type gives an empty Update and one
// diagnostic. Fields that run past the message give an Error diagnostic
// ("bgp/update-length") and what parsed before them is kept. A message cut
// short of its header's length is read as far as its bytes go, whole
// withdrawn prefixes, whole attributes and whole NLRI only, with one Error
// diagnostic ("bgp/message-truncated") at the cut. Update.Attrs aliases msg.
func ParseUpdate(msg []byte, ctx Context) (Update, []Diagnostic) {
	var u Update
	hdr, err := ParseMessageHeader(msg)
	if err != nil {
		return u, []Diagnostic{{SeverityError, "bgp/message-header", 0, 0, err.Error()}}
	}
	if hdr.Type != MessageUpdate {
		return u, []Diagnostic{{SeverityError, "bgp/not-update", 18, 0,
			fmt.Sprintf("message type %d is not UPDATE", hdr.Type)}}
	}
	var diags []Diagnostic
	cut := hdr.Length > len(msg)
	if hdr.Length < len(msg) {
		diags = append(diags, Diagnostic{SeverityWarning, "bgp/message-header", 16, 0,
			fmt.Sprintf("%d bytes follow the message's own length of %d", len(msg)-hdr.Length, hdr.Length)})
		msg = msg[:hdr.Length]
	}
	short := func(off int, what string) []Diagnostic {
		if cut {
			return append(diags, Diagnostic{SeverityError, "bgp/message-truncated", len(msg), 0,
				fmt.Sprintf("message cut at %d of its %d bytes; read as far as whole fields go", len(msg), hdr.Length)})
		}
		return append(diags, Diagnostic{SeverityError, "bgp/update-length", off, 0, what})
	}
	nlri := func(b []byte, base int) []NLRI {
		out, nd := parseNLRI(b, AFIIPv4, ctx.AddPath)
		for _, d := range nd {
			d.Offset += base
			diags = append(diags, d)
		}
		return out
	}
	off := headerLen
	if len(msg)-off < 2 {
		return u, short(off, "UPDATE has no withdrawn routes length")
	}
	wl := int(msg[off])<<8 | int(msg[off+1])
	off += 2
	if len(msg)-off < wl {
		if cut {
			b := msg[off:]
			u.Withdrawn = nlri(b[:wholeNLRI(b, 32, ctx.AddPath)], off)
		}
		return u, short(off, "withdrawn routes run past the message")
	}
	u.Withdrawn = nlri(msg[off:off+wl], off)
	off += wl
	if len(msg)-off < 2 {
		return u, short(off, "UPDATE has no path attribute length")
	}
	al := int(msg[off])<<8 | int(msg[off+1])
	off += 2
	if len(msg)-off < al {
		if cut {
			end := off + wholeAttrs(msg[off:])
			u.Attrs = NewAttrs(msg[off:end:end], ctx)
		}
		return u, short(off, "path attributes run past the message")
	}
	u.Attrs = NewAttrs(msg[off:off+al:off+al], ctx)
	off += al
	if !cut {
		u.Announced = nlri(msg[off:], off)
		return u, diags
	}
	b := msg[off:]
	u.Announced = nlri(b[:wholeNLRI(b, 32, ctx.AddPath)], off)
	return u, short(off, "")
}

// wholeNLRI returns how many leading bytes of b hold whole prefixes: it
// stops before a prefix the bytes end inside. A prefix length beyond
// maxBits ends the walk at len(b), so that parseNLRI reports it.
func wholeNLRI(b []byte, maxBits int, addPath bool) int {
	off := 0
	for off < len(b) {
		n := 1
		if addPath {
			n = 5
		}
		if len(b)-off < n {
			return off
		}
		plen := int(b[off+n-1])
		if plen > maxBits {
			return len(b)
		}
		n += (plen + 7) / 8
		if len(b)-off < n {
			return off
		}
		off += n
	}
	return off
}

// wholeAttrs returns how many leading bytes of b hold whole path attributes.
func wholeAttrs(b []byte) int {
	off := 0
	for {
		if len(b)-off < 3 {
			return off
		}
		hl, n := 3, int(b[off+2])
		if b[off]&0x10 != 0 { // extended length
			if len(b)-off < 4 {
				return off
			}
			hl, n = 4, int(b[off+2])<<8|int(b[off+3])
		}
		if len(b)-off-hl < n {
			return off
		}
		off += hl + n
	}
}
