package bgp

import (
	"bytes"
	"errors"
	"net/netip"
	"slices"
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

	// A header length beyond the bytes present is not a header error
	// (Ruling R17a): the message was cut, and ParseUpdate says so.
	long := append([]byte(nil), good...)
	long[17] = 200
	if h, err := ParseMessageHeader(long); err != nil || h != (MessageHeader{MessageUpdate, 200}) {
		t.Fatalf("long: %v %v", h, err)
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

// TestParseUpdateTruncated: a message whose header length is beyond the bytes
// present is parsed as far as the bytes go, whole withdrawn prefixes, whole
// attributes and whole NLRI only, with one Error "bgp/message-truncated"
// (Ruling R17a).
func TestParseUpdateTruncated(t *testing.T) {
	origin := attr(0x40, 1, 0)
	path := attr(0x40, 2, 2, 1, 0, 0, 0xFD, 0xE8)
	attrs := concat(origin, path)
	withdrawn := []byte{8, 10, 0x18, 198, 51, 100}
	nlri := []byte{0x18, 192, 0, 2, 0x18, 203, 0, 113}
	full := msgOf(2, concat([]byte{0, byte(len(withdrawn))}, withdrawn,
		[]byte{0, byte(len(attrs))}, attrs, nlri)...)
	if u, d := ParseUpdate(full, Context{AS4: true}); len(d) != 0 || len(u.Announced) != 2 || len(u.Withdrawn) != 2 {
		t.Fatalf("whole message: %+v %v", u, d)
	}
	pfx := func(ns []NLRI) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Prefix.String())
		}
		return out
	}
	wStart := 19 + 2
	aStart := wStart + len(withdrawn) + 2
	nStart := aStart + len(attrs)
	for _, tc := range []struct {
		name      string
		cut       int // bytes kept
		withdrawn []string
		attrs     []byte
		announced []string
	}{
		{"inside the withdrawn routes length", 20, nil, nil, nil},
		{"inside the withdrawn routes", wStart + 2 + 2, []string{"10.0.0.0/8"}, nil, nil},
		{"after the withdrawn routes", wStart + len(withdrawn) + 1, []string{"10.0.0.0/8", "198.51.100.0/24"}, nil, nil},
		{"inside an attribute header", aStart + len(origin) + 2, []string{"10.0.0.0/8", "198.51.100.0/24"}, origin, nil},
		{"inside the path attributes", aStart + len(origin) + 5, []string{"10.0.0.0/8", "198.51.100.0/24"}, origin, nil},
		{"after the path attributes", nStart, []string{"10.0.0.0/8", "198.51.100.0/24"}, attrs, nil},
		{"inside the NLRI", nStart + 6, []string{"10.0.0.0/8", "198.51.100.0/24"}, attrs, []string{"192.0.2.0/24"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := full[:tc.cut]
			u, d := ParseUpdate(msg, Context{AS4: true})
			if len(d) != 1 || d[0].Code != "bgp/message-truncated" || d[0].Severity != SeverityError || d[0].Offset != tc.cut {
				t.Errorf("diagnostics %v, want one Error bgp/message-truncated at %d", d, tc.cut)
			}
			if got := pfx(u.Withdrawn); !slices.Equal(got, tc.withdrawn) {
				t.Errorf("withdrawn %v, want %v", got, tc.withdrawn)
			}
			if !bytes.Equal(u.Attrs.Raw(), tc.attrs) {
				t.Errorf("attributes %x, want %x", u.Attrs.Raw(), tc.attrs)
			}
			if got := pfx(u.Announced); !slices.Equal(got, tc.announced) {
				t.Errorf("announced %v, want %v", got, tc.announced)
			}
		})
	}

	// Add-path: an NLRI cut inside its path identifier is left out too.
	ap := msgOf(2, 0, 0, 0, 0, 0, 0, 0, 5, 0x18, 192, 0, 2, 0, 0)
	ap[17] += 6
	u, d := ParseUpdate(ap, Context{AddPath: true})
	if len(d) != 1 || d[0].Code != "bgp/message-truncated" || len(u.Announced) != 1 || u.Announced[0].PathID != 5 {
		t.Errorf("add-path: %+v %v", u, d)
	}
	// A malformed prefix before the cut is still reported as such.
	bad := msgOf(2, 0, 0, 0, 0, 40, 1, 2)
	bad[17] += 3
	if _, d := ParseUpdate(bad, Context{}); len(d) != 2 || d[0].Code != "bgp/nlri-length" || d[1].Code != "bgp/message-truncated" {
		t.Errorf("bad prefix length before the cut: %v", d)
	}
}
