package bmp

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt/bgp"
)

func TestParseMessageEachType(t *testing.T) {
	peer := defaultPeer().bytes()
	stats := cat(u32(2), tlv(0, u32(7)...), tlv(7, u64(9)...))
	up := cat(make([]byte, 16), u16(179), u16(50000), openMsg(), openMsg())
	for _, c := range []struct {
		name  string
		typ   byte
		body  []byte
		check func(t *testing.T, m Message, b []byte)
	}{
		{"route monitoring", 0, cat(peer, emptyUpdate), func(t *testing.T, m Message, b []byte) {
			rm, ok := m.(RouteMonitoring)
			if !ok {
				t.Fatalf("%T, want RouteMonitoring", m)
			}
			if rm.Peer.AS != 64500 || !bytes.Equal(rm.Update, emptyUpdate) {
				t.Errorf("Peer.AS %d Update % x", rm.Peer.AS, rm.Update)
			}
			if cap(rm.Update) != len(rm.Update) || &rm.Update[0] != &b[48] {
				t.Errorf("Update is not a slice of Raw with cap == len (cap %d, len %d)", cap(rm.Update), len(rm.Update))
			}
			if mt, err := rm.MessageType(); mt != bgp.MessageUpdate || err != nil {
				t.Errorf("MessageType (%v, %v)", mt, err)
			}
			rm.Update = rm.Update[:18]
			if _, err := rm.MessageType(); !errors.Is(err, bgp.ErrShortMessage) {
				t.Errorf("MessageType of a cut header: error %v, want bgp.ErrShortMessage", err)
			}
		}},
		{"stats report", 1, cat(peer, stats), func(t *testing.T, m Message, b []byte) {
			s, ok := m.(StatsReport)
			if !ok {
				t.Fatalf("%T, want StatsReport", m)
			}
			if s.Count != 2 || s.Peer.AS != 64500 {
				t.Errorf("Count %d Peer.AS %d", s.Count, s.Peer.AS)
			}
		}},
		{"peer down", 2, cat(peer, []byte{4}), func(t *testing.T, m Message, b []byte) {
			d, ok := m.(PeerDown)
			if !ok {
				t.Fatalf("%T, want PeerDown", m)
			}
			if d.Reason != PeerDownRemoteNoData || len(d.Data) != 0 {
				t.Errorf("Reason %d Data % x", d.Reason, d.Data)
			}
		}},
		{"peer up", 3, cat(peer, up), func(t *testing.T, m Message, b []byte) {
			u, ok := m.(PeerUp)
			if !ok {
				t.Fatalf("%T, want PeerUp", m)
			}
			if !bytes.Equal(u.Data, up) || cap(u.Data) != len(u.Data) || &u.Data[0] != &b[48] {
				t.Errorf("Data % x, want % x as a slice of Raw", u.Data, up)
			}
		}},
		{"initiation", 4, tlv(2, []byte("r1")...), func(t *testing.T, m Message, b []byte) {
			if _, ok := m.(Initiation); !ok {
				t.Fatalf("%T, want Initiation", m)
			}
		}},
		{"termination", 5, tlv(1, 0, 4), func(t *testing.T, m Message, b []byte) {
			if _, ok := m.(Termination); !ok {
				t.Fatalf("%T, want Termination", m)
			}
		}},
		{"route mirroring", 6, cat(peer, tlv(1, 0, 1)), func(t *testing.T, m Message, b []byte) {
			rm, ok := m.(RouteMirroring)
			if !ok {
				t.Fatalf("%T, want RouteMirroring", m)
			}
			if rm.Peer.AS != 64500 {
				t.Errorf("Peer.AS %d", rm.Peer.AS)
			}
		}},
		{"other", 9, []byte{1, 2, 3}, func(t *testing.T, m Message, b []byte) {
			if _, ok := m.(Other); !ok {
				t.Fatalf("%T, want Other", m)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := bmpMsg(c.typ, c.body)
			m, err := ParseMessage(b)
			if err != nil {
				t.Fatalf("ParseMessage: %v", err)
			}
			if want := (Header{Version: 3, Length: uint32(len(b)), Type: Type(c.typ)}); m.Header() != want {
				t.Errorf("Header %+v, want %+v", m.Header(), want)
			}
			if !bytes.Equal(m.Raw(), b) || &m.Raw()[0] != &b[0] {
				t.Errorf("Raw is not the input")
			}
			c.check(t, m, b)
		})
	}
}

func TestParseMessageErrors(t *testing.T) {
	peer := defaultPeer().bytes()
	good := bmpMsg(0, peer, emptyUpdate)
	short5 := []byte{3, 0, 0, 0, 5, 4, 0} // length 5 with 7 bytes present
	v2 := bmpMsg(0, peer, emptyUpdate)
	v2[0] = 2
	for _, c := range []struct {
		name    string
		b       []byte
		want    error
		header  bool // the header was read
		keepRaw bool
	}{
		{"shorter than the common header", good[:5], ErrTruncated, false, false},
		{"len(b) < Length", good[:len(good)-1], ErrTruncated, true, false},
		{"len(b) > Length", append(bytes.Clone(good), 0), ErrMalformed, true, true},
		{"Length < 6", short5, ErrMalformed, true, true},
		{"version 2", v2, ErrMalformed, true, true},
		{"per-peer header past Length", bmpMsg(0, peer[:41]), ErrMalformed, true, true},
		{"peer up without its per-peer header", bmpMsg(3, peer[:41]), ErrMalformed, true, true},
		{"peer down without its per-peer header", bmpMsg(2, peer[:30]), ErrMalformed, true, true},
		{"stats report without its count", bmpMsg(1, peer), ErrMalformed, true, true},
		{"stats report with 3 bytes of count", bmpMsg(1, peer, []byte{0, 0, 1}), ErrMalformed, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := ParseMessage(c.b)
			if m != nil {
				t.Errorf("message %T with the error", m)
			}
			var me *MessageError
			if !errors.As(err, &me) {
				t.Fatalf("error %v (%T), want *MessageError", err, err)
			}
			if !errors.Is(err, c.want) {
				t.Errorf("error %v, want %v", err, c.want)
			}
			if c.want == ErrMalformed && errors.Is(err, ErrTruncated) || c.want == ErrTruncated && errors.Is(err, ErrMalformed) {
				t.Errorf("error %v wraps both", err)
			}
			if me.Offset != 0 {
				t.Errorf("Offset %d, want 0", me.Offset)
			}
			if c.header {
				want := Header{Version: c.b[0], Length: uint32(c.b[1])<<24 | uint32(c.b[2])<<16 | uint32(c.b[3])<<8 | uint32(c.b[4]), Type: Type(c.b[5])}
				if me.Header != want {
					t.Errorf("Header %+v, want %+v", me.Header, want)
				}
			} else if me.Header != (Header{}) {
				t.Errorf("Header %+v, want zero", me.Header)
			}
			if c.keepRaw && !bytes.Equal(me.Raw, c.b) || !c.keepRaw && me.Raw != nil {
				t.Errorf("Raw % x", me.Raw)
			}
			if !strings.HasPrefix(err.Error(), "bmp: message at offset 0: ") || strings.Count(err.Error(), "bmp: ") != 1 {
				t.Errorf("Error() %q", err.Error())
			}
		})
	}
}

func TestParseMessageOpenBMPFrame(t *testing.T) {
	// The binary OBMP header (spec §5.4) as CAIDA and RouteViews send it, then a BMP message.
	bmp := bmpMsg(4, tlv(2, []byte("r1")...))
	frame := cat([]byte("OBMP"), []byte{1, 7, 0, 0}, u32(uint32(len(bmp))), []byte{0x80, 12}, make([]byte, 8+16+2+16+16+2), u32(1), bmp)
	for _, b := range [][]byte{frame, frame[:4]} {
		_, err := ParseMessage(b)
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("error %v, want ErrMalformed", err)
		}
		if s := err.Error(); !strings.Contains(s, "OpenBMP") || !strings.Contains(s, "openbmp.Parse") {
			t.Errorf("Error() %q does not name OpenBMP and openbmp.Parse", s)
		}
		var me *MessageError
		if errors.As(err, &me) && (me.Header != Header{} || !bytes.Equal(me.Raw, b)) {
			t.Errorf("Header %+v Raw %d bytes, want zero and the frame", me.Header, len(me.Raw))
		}
	}
}

func TestMessageErrorText(t *testing.T) {
	e := &MessageError{Offset: 1234, Err: ErrMalformed}
	if got, want := e.Error(), "bmp: message at offset 1234: malformed message"; got != want {
		t.Errorf("Error() %q, want %q", got, want)
	}
	e = &MessageError{Offset: -1, Err: ErrTruncated}
	if got, want := e.Error(), "bmp: message (offset unknown): truncated message"; got != want {
		t.Errorf("Error() %q, want %q", got, want)
	}
	if !errors.Is(e, ErrTruncated) {
		t.Error("Unwrap does not reach ErrTruncated")
	}
}
