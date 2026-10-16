package bgp

import (
	"errors"
	"net/netip"
	"testing"
)

func msgOf(typ byte, body ...byte) []byte {
	n := 19 + len(body)
	m := make([]byte, 16, n)
	for i := range m {
		m[i] = 0xFF
	}
	return append(append(m, byte(n>>8), byte(n), typ), body...)
}

func TestParseUpdate(t *testing.T) {
	attrs := concat(attr(0x40, 1, 0), attr(0x40, 2, 2, 1, 0, 0, 0xFD, 0xE8))
	body := concat([]byte{0, 2, 8, 10}, []byte{0, byte(len(attrs))}, attrs, []byte{0x18, 0xC0, 0, 2})
	u, d := ParseUpdate(msgOf(2, body...), Context{AS4: true})
	if len(d) != 0 || len(u.Withdrawn) != 1 || u.Withdrawn[0].Prefix != netip.MustParsePrefix("10.0.0.0/8") ||
		len(u.Announced) != 1 || u.Announced[0].Prefix != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatalf("%+v %v", u, d)
	}
	p, d := u.Attrs.Decode()
	if len(d) != 0 || !p.HasOrigin || len(p.ASPath.Segments) != 1 || p.ASPath.Segments[0].ASNs[0] != 65000 {
		t.Fatalf("%+v %v", p, d)
	}
}

func TestParseUpdateEndOfRIB(t *testing.T) {
	u, d := ParseUpdate(msgOf(2, 0, 0, 0, 0), Context{})
	if len(d) != 0 || len(u.Withdrawn) != 0 || len(u.Announced) != 0 || len(u.Attrs.Raw()) != 0 {
		t.Fatalf("%+v %v", u, d)
	}
}

func TestParseUpdateBadHeader(t *testing.T) {
	good := msgOf(2, 0, 0, 0, 0)

	bad := append([]byte(nil), good...)
	bad[5] = 0
	if _, err := ParseMessageHeader(bad); !errors.Is(err, ErrMarker) {
		t.Fatalf("marker: %v", err)
	}
	u, d := ParseUpdate(bad, Context{})
	if len(d) != 1 || d[0].Code != "bgp/message-header" || d[0].Severity != SeverityError || u.Attrs.Raw() != nil {
		t.Fatalf("%v", d)
	}

	long := append([]byte(nil), good...)
	long[17] = 200
	if _, err := ParseMessageHeader(long); !errors.Is(err, ErrShortMessage) {
		t.Fatalf("long: %v", err)
	}
	if _, d = ParseUpdate(long, Context{}); len(d) != 1 || d[0].Code != "bgp/message-header" {
		t.Fatalf("%v", d)
	}
	small := append([]byte(nil), good...)
	small[17] = 5
	if _, err := ParseMessageHeader(small); !errors.Is(err, ErrShortMessage) {
		t.Fatalf("small: %v", err)
	}
	if _, err := ParseMessageHeader(good[:18]); !errors.Is(err, ErrShortMessage) {
		t.Fatalf("18 bytes: %v", err)
	}
	if h, err := ParseMessageHeader(good); err != nil || h != (MessageHeader{MessageUpdate, 23}) {
		t.Fatalf("%v %v", h, err)
	}

	u, d = ParseUpdate(msgOf(4), Context{})
	if len(d) != 1 || d[0].Code != "bgp/not-update" || d[0].Severity != SeverityError || len(u.Announced) != 0 {
		t.Fatalf("%v", d)
	}
}

func TestParseUpdateTrailingBytes(t *testing.T) {
	m := append(msgOf(2, 0, 0, 0, 0), 0x18, 1, 2, 3) // bytes past the header's length
	u, d := ParseUpdate(m, Context{})
	if len(d) != 1 || d[0].Code != "bgp/message-header" || d[0].Severity != SeverityWarning || len(u.Announced) != 0 {
		t.Fatalf("%+v %v", u, d)
	}
}

func TestParseUpdateLengthOverrun(t *testing.T) {
	// withdrawn length past the body
	u, d := ParseUpdate(msgOf(2, 0, 9, 8, 10), Context{})
	if len(d) != 1 || d[0].Code != "bgp/update-length" || d[0].Severity != SeverityError || len(u.Withdrawn) != 0 {
		t.Fatalf("%+v %v", u, d)
	}
	// attribute length past the body: withdrawn kept
	u, d = ParseUpdate(msgOf(2, 0, 2, 8, 10, 0, 50, 1), Context{})
	if len(d) != 1 || d[0].Code != "bgp/update-length" || len(u.Withdrawn) != 1 || len(u.Attrs.Raw()) != 0 {
		t.Fatalf("%+v %v", u, d)
	}
	// no room for the length fields
	for _, body := range [][]byte{{}, {0}, {0, 0}, {0, 0, 0}} {
		if _, d = ParseUpdate(msgOf(2, body...), Context{}); len(d) != 1 || d[0].Code != "bgp/update-length" {
			t.Errorf("%x: %v", body, d)
		}
	}
	// NLRI error offset is in the message
	u, d = ParseUpdate(msgOf(2, 0, 0, 0, 0, 8, 10, 40), Context{})
	if len(u.Announced) != 1 || len(d) != 1 || d[0].Code != "bgp/nlri-length" || d[0].Offset != 25 {
		t.Fatalf("%+v %v", u, d)
	}
}

func TestParseUpdateAddPath(t *testing.T) {
	u, d := ParseUpdate(msgOf(2, 0, 0, 0, 0, 0, 0, 0, 5, 0x18, 192, 0, 2), Context{AddPath: true})
	if len(d) != 0 || len(u.Announced) != 1 || u.Announced[0].PathID != 5 {
		t.Fatalf("%+v %v", u, d)
	}
}
