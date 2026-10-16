package bgp

import (
	"net/netip"
	"testing"
)

func TestParseNLRI(t *testing.T) {
	v4 := Family{AFIIPv4, SAFIUnicast}
	got, d := ParseNLRI([]byte{0x18, 0x0A, 1, 2, 0}, v4, false)
	if len(d) != 0 || len(got) != 2 || got[0].Prefix != netip.MustParsePrefix("10.1.2.0/24") ||
		got[1].Prefix != netip.MustParsePrefix("0.0.0.0/0") || got[0].HasPathID {
		t.Fatalf("got %v %v", got, d)
	}

	got, d = ParseNLRI([]byte{0, 0, 0, 7, 0x18, 0x0A, 1, 2}, v4, true)
	if len(d) != 0 || len(got) != 1 || got[0].PathID != 7 || !got[0].HasPathID {
		t.Fatalf("addpath: %v %v", got, d)
	}

	got, d = ParseNLRI([]byte{0x14, 0x0A, 1, 0xF2}, v4, false)
	if len(got) != 1 || got[0].Prefix != netip.MustParsePrefix("10.1.240.0/20") ||
		len(d) != 1 || d[0].Code != "bgp/prefix-bits" || d[0].Severity != SeverityInfo {
		t.Fatalf("masked: %v %v", got, d)
	}

	// 33 for IPv4: stop, keep the prefix before it.
	got, d = ParseNLRI([]byte{8, 10, 33, 1, 2, 3, 4, 5, 8, 11}, v4, false)
	if len(got) != 1 || len(d) != 1 || d[0].Code != "bgp/nlri-length" || d[0].Severity != SeverityError || d[0].Offset != 2 {
		t.Fatalf("too long: %v %v", got, d)
	}
	// bytes run out
	got, d = ParseNLRI([]byte{24, 10, 1}, v4, false)
	if len(got) != 0 || len(d) != 1 || d[0].Code != "bgp/nlri-length" {
		t.Fatalf("truncated: %v %v", got, d)
	}
	// add-path truncated
	_, d = ParseNLRI([]byte{0, 0, 0}, v4, true)
	if len(d) != 1 || d[0].Code != "bgp/nlri-length" {
		t.Fatalf("addpath truncated: %v", d)
	}
	// IPv6, and 129 is refused
	got, d = ParseNLRI([]byte{48, 0x20, 1, 0x0d, 0xb8, 0, 1}, Family{AFIIPv6, SAFIUnicast}, false)
	if len(d) != 0 || len(got) != 1 || got[0].Prefix != netip.MustParsePrefix("2001:db8:1::/48") {
		t.Fatalf("v6: %v %v", got, d)
	}
	if _, d = ParseNLRI([]byte{129}, Family{AFIIPv6, SAFIUnicast}, false); len(d) != 1 {
		t.Fatalf("129: %v", d)
	}
	// unknown AFI: refused, no panic
	if _, d = ParseNLRI([]byte{8, 1}, Family{25, 65}, false); len(d) != 1 {
		t.Fatalf("afi 25: %v", d)
	}
}
