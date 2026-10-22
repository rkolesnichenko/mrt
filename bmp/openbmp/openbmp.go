// Package openbmp reads the binary OBMP v1.7 envelope that CAIDA's and
// RouteViews' Kafka streams wrap around each BMP message.
//
// The layout has no published specification; it follows bgpkit-parser,
// libbgpstream, gobmp and obmp-collector's rv-kafka-to-bmp.py, which agree.
// The text form ("V: 1.7" lines) is not read. The package imports nothing from
// bmp: pass the BMP bytes Parse returns to bmp.ParseMessage.
package openbmp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/netip"
	"time"
)

// Header is the OBMP envelope in front of one BMP message.
type Header struct {
	Major, Minor     uint8
	Flags            uint8
	ObjectType       uint8
	Time             time.Time // the collector's timestamp, UTC; zero when both fields are 0
	CollectorHash    [16]byte
	CollectorAdminID string
	RouterHash       [16]byte
	RouterIP         netip.Addr
	RouterGroup      string
	RowCount         uint32
	Length           int // header length, as stated and checked
	MessageLen       int // BMP message length, as stated and checked
}

var (
	ErrNotOpenBMP = errors.New("openbmp: no OBMP header")
	ErrVersion    = errors.New("openbmp: unsupported header version")
	ErrMalformed  = errors.New("openbmp: malformed header")
	ErrTruncated  = errors.New("openbmp: truncated frame")
)

const (
	// FlagRouter is set on a router message; FlagIPv6 when the router IP is IPv6.
	FlagRouter = 0x80
	FlagIPv6   = 0x40

	// ObjectBMPRaw is the object type of a raw BMP message.
	ObjectBMPRaw = 12

	prefixLen = 12 // magic, version, header length, message length
	minHeader = 78 // the header with an empty admin ID and router group

	// MaxFrameLen is the largest frame Frames reads.
	MaxFrameLen = 16 << 20
)

var magic = []byte("OBMP")

// cursor reads a header front to back and remembers the first short read.
type cursor struct {
	b   []byte
	bad string
}

func (c *cursor) take(n int, what string) []byte {
	if len(c.b) < n {
		if c.bad == "" {
			c.bad = what
		}
		c.b = nil
		return make([]byte, n)
	}
	v := c.b[:n]
	c.b = c.b[n:]
	return v
}

func (c *cursor) u8(what string) uint8   { return c.take(1, what)[0] }
func (c *cursor) u16(what string) uint16 { return binary.BigEndian.Uint16(c.take(2, what)) }
func (c *cursor) u32(what string) uint32 { return binary.BigEndian.Uint32(c.take(4, what)) }

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrMalformed}, args...)...)
}

// Parse reads one Kafka frame: the header and the BMP message after it. The
// returned message aliases frame, with cap equal to len.
func Parse(frame []byte) (Header, []byte, error) {
	var h Header
	if !bytes.HasPrefix(frame, magic) {
		return h, nil, ErrNotOpenBMP
	}
	c := &cursor{b: frame[4:]}
	h.Major, h.Minor = c.u8("version"), c.u8("version")
	if c.bad != "" {
		return h, nil, malformed("cut short in the %s", c.bad)
	}
	if h.Major != 1 || h.Minor != 7 {
		return h, nil, fmt.Errorf("%w: %d.%d", ErrVersion, h.Major, h.Minor)
	}
	h.Length = int(c.u16("header length"))
	h.MessageLen = int(c.u32("message length"))
	h.Flags = c.u8("flags")
	h.ObjectType = c.u8("object type")
	sec, usec := c.u32("timestamp"), c.u32("timestamp")
	copy(h.CollectorHash[:], c.take(16, "collector hash"))
	h.CollectorAdminID = string(c.take(int(c.u16("admin ID length")), "admin ID"))
	copy(h.RouterHash[:], c.take(16, "router hash"))
	ip := c.take(16, "router IP")
	h.RouterGroup = string(c.take(int(c.u16("router group length")), "router group"))
	h.RowCount = c.u32("row count")
	if c.bad != "" {
		return h, nil, malformed("cut short in the %s", c.bad)
	}
	if h.Flags&FlagRouter == 0 {
		return h, nil, malformed("flags %#x: not a router message", h.Flags)
	}
	if h.ObjectType != ObjectBMPRaw {
		return h, nil, malformed("object type %d, not BMP raw (%d)", h.ObjectType, ObjectBMPRaw)
	}
	if h.RowCount != 1 {
		return h, nil, malformed("row count %d", h.RowCount)
	}
	if parsed := len(frame) - len(c.b); h.Length != parsed {
		return h, nil, malformed("stated header length %d, parsed %d", h.Length, parsed)
	}
	if h.MessageLen != len(c.b) {
		return h, nil, malformed("message length %d, %d bytes follow", h.MessageLen, len(c.b))
	}
	if h.Flags&FlagIPv6 != 0 {
		h.RouterIP = netip.AddrFrom16([16]byte(ip))
	} else {
		h.RouterIP = netip.AddrFrom4([4]byte(ip))
	}
	if sec != 0 || usec != 0 {
		h.Time = time.Unix(int64(sec), int64(usec)*1000).UTC()
	}
	return h, c.b[:len(c.b):len(c.b)], nil
}

// Frames reads concatenated frames from r, a capture file. Each frame is its
// own buffer, self-delimited by the header and message lengths. A frame has no
// resync point, so a bad prefix, a frame over MaxFrameLen or a stream that ends
// inside a frame yields one error (the last wrapping ErrTruncated) and ends the
// sequence. A clean end at a frame boundary ends it without an error.
func Frames(r io.Reader) iter.Seq2[[]byte, error] {
	done := false
	return func(yield func([]byte, error) bool) {
		for !done {
			frame, err := readFrame(r)
			if err != nil || frame == nil {
				done = true
				if err != nil {
					yield(nil, err)
				}
				return
			}
			if !yield(frame, nil) {
				return
			}
		}
	}
}

// readFrame returns nil, nil at a clean end.
func readFrame(r io.Reader) ([]byte, error) {
	head := make([]byte, prefixLen)
	n, err := io.ReadFull(r, head)
	if n == 0 && err == io.EOF {
		return nil, nil
	}
	if k := min(n, len(magic)); !bytes.Equal(head[:k], magic[:k]) {
		return nil, ErrNotOpenBMP
	}
	if err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil, fmt.Errorf("%w: %d bytes of the 12-byte prefix", ErrTruncated, n)
		}
		return nil, err
	}
	hl := int(binary.BigEndian.Uint16(head[6:]))
	ml := int(binary.BigEndian.Uint32(head[8:]))
	if hl < minHeader {
		return nil, malformed("stated header length %d is below the minimum %d", hl, minHeader)
	}
	if total := hl + ml; total > MaxFrameLen {
		return nil, fmt.Errorf("openbmp: frame of %d bytes exceeds %d", total, MaxFrameLen)
	}
	// The rest grows with the bytes that arrive, not with the stated length.
	rest, err := io.ReadAll(io.LimitReader(r, int64(hl+ml-prefixLen)))
	if err != nil {
		return nil, err
	}
	if len(rest) != hl+ml-prefixLen {
		return nil, fmt.Errorf("%w: %d of %d bytes", ErrTruncated, prefixLen+len(rest), hl+ml)
	}
	return append(head, rest...), nil
}
