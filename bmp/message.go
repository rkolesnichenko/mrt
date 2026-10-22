package bmp

import (
	"errors"
	"fmt"
	"strings"
)

// Type is a BMP message type (RFC 7854 section 4.1).
type Type uint8

// BMP message types.
const (
	TypeRouteMonitoring Type = 0
	TypeStatsReport     Type = 1
	TypePeerDown        Type = 2
	TypePeerUp          Type = 3
	TypeInitiation      Type = 4
	TypeTermination     Type = 5
	TypeRouteMirroring  Type = 6
)

// Header is the BMP common header.
type Header struct {
	Version uint8
	Length  uint32 // as on the wire: the whole message, common header included
	Type    Type
}

// commonHeaderLen is the length of the common header.
const commonHeaderLen = 6

// Message is sealed: the types in this package are its only implementations.
type Message interface {
	Header() Header
	Raw() []byte // the whole message, exactly as read
	isMessage()
}

// message is the part every message type embeds.
type message struct {
	hdr Header
	raw []byte
}

func (m message) Header() Header { return m.hdr }
func (m message) Raw() []byte    { return m.raw }
func (message) isMessage()       {}

// Errors of reading. Each is wrapped by a *MessageError.
var (
	ErrTruncated = errors.New("bmp: truncated message")
	ErrMalformed = errors.New("bmp: malformed message")
	ErrTooLarge  = errors.New("bmp: message longer than MaxMessageLen")
	ErrNoPeerUp  = errors.New("bmp: route monitoring before its peer's peer up")
)

// MessageError describes a message that could not be returned.
type MessageError struct {
	Offset int64  // of the message's first byte in the stream; -1 when not known
	Header Header // zero when the header was not read completely
	Raw    []byte // nil when over MaxMessageLen and skipped, or truncated
	Err    error  // ErrMalformed, ErrTooLarge, ErrTruncated, ErrNoPeerUp
}

// Error says "bmp: message at offset N: cause", or "bmp: message (offset
// unknown): cause" when Offset is negative, with the cause's own "bmp: "
// prefix dropped.
func (e *MessageError) Error() string {
	cause := strings.TrimPrefix(fmt.Sprint(e.Err), "bmp: ")
	if e.Offset < 0 {
		return "bmp: message (offset unknown): " + cause
	}
	return fmt.Sprintf("bmp: message at offset %d: %s", e.Offset, cause)
}

func (e *MessageError) Unwrap() error { return e.Err }

// short is the error of a fixed field that runs past the message.
func short(what string) error { return fmt.Errorf("%w: %s runs past the message", ErrMalformed, what) }
