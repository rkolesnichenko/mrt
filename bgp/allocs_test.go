package bgp

import (
	"fmt"
	"testing"
)

// TestParseASPathAllocations: a single segment of up to 16 ASNs is one
// allocation, any other path two, whatever its number of segments.
func TestParseASPathAllocations(t *testing.T) {
	asns := func(n int) []uint32 {
		a := make([]uint32, n)
		for i := range a {
			a[i] = uint32(64496 + i)
		}
		return a
	}
	for _, tc := range []struct {
		name  string
		width int
		v     []byte
		want  float64
	}{
		{"empty", 4, nil, 0},
		{"1 ASN", 4, seg(4, ASSequence, asns(1)...), 1},
		{"4 ASNs", 4, seg(4, ASSequence, asns(4)...), 1},
		{"5 ASNs", 4, seg(4, ASSequence, asns(5)...), 1},
		{"16 ASNs", 4, seg(4, ASSequence, asns(16)...), 1},
		{"17 ASNs", 4, seg(4, ASSequence, asns(17)...), 2},
		{"255 ASNs", 4, seg(4, ASSequence, asns(255)...), 2},
		{"sequence and set", 4, concat(seg(4, ASSequence, asns(3)...), seg(4, ASSet, asns(2)...)), 2},
		{"five segments", 2, concat(seg(2, ASSequence, 1), seg(2, ASSet, 2), seg(2, ASSequence, 3), seg(2, ASConfedSet, 4), seg(2, ASSequence, 5)), 2},
	} {
		if _, ok := parseASPath(tc.v, tc.width); !ok {
			t.Fatalf("%s: does not parse", tc.name)
		}
		if got := testing.AllocsPerRun(100, func() { parseASPath(tc.v, tc.width) }); got != tc.want {
			t.Errorf("%s: %v allocations, want %v", tc.name, got, tc.want)
		}
	}
}

// TestParseASPathSegmentsApart: the segments of a parsed path share no
// capacity, so appending to one never writes into another.
func TestParseASPathSegmentsApart(t *testing.T) {
	for _, n := range []int{1, 3, 4, 5, 8, 12, 16, 17} {
		var v []byte
		for i := range 3 {
			s := make([]uint32, n)
			for j := range s {
				s[j] = uint32(100*i + j)
			}
			v = concat(v, seg(4, ASSequence, s...))
		}
		p, ok := parseASPath(v, 4)
		if !ok || len(p.Segments) != 3 {
			t.Fatalf("%d ASNs: %v %v", n, p, ok)
		}
		want := fmt.Sprint(p)
		for i := range p.Segments {
			if s := p.Segments[i].ASNs; cap(s) != len(s) {
				t.Errorf("%d ASNs: segment %d has cap %d, len %d", n, i, cap(s), len(s))
			}
			_ = append(p.Segments[i].ASNs, 999999)
		}
		_ = append(p.Segments, Segment{ASSet, []ASN{1}})
		if got := fmt.Sprint(p); got != want {
			t.Errorf("%d ASNs: appending changed the path: %s, was %s", n, got, want)
		}
	}
	one, _ := parseASPath(seg(4, ASSequence, 1, 2), 4)
	if s := one.Segments; cap(s) != len(s) || cap(s[0].ASNs) != len(s[0].ASNs) {
		t.Errorf("single segment: Segments cap %d, ASNs cap %d", cap(s), cap(s[0].ASNs))
	}
}

// TestParseNLRIAllocations: the prefixes of a run are one allocation, sized
// exactly, also when the run stops at a broken prefix.
func TestParseNLRIAllocations(t *testing.T) {
	var v4, v6, addPath []byte
	for i := range 50 {
		v4 = append(v4, 24, 10, byte(i), 0)
		v6 = append(v6, 48, 0x20, 0x01, 0x0d, 0xb8, 0, byte(i))
		addPath = append(addPath, 0, 0, 0, byte(i), 16, 10, byte(i))
	}
	broken := append(append([]byte{}, v4...), 33, 1, 2, 3, 4, 5)
	for _, tc := range []struct {
		name    string
		b       []byte
		afi     AFI
		addPath bool
		n       int
		diags   int
	}{
		{"v4", v4, AFIIPv4, false, 50, 0},
		{"v6", v6, AFIIPv6, false, 50, 0},
		{"add-path", addPath, AFIIPv4, true, 50, 0},
		{"broken after 50", broken, AFIIPv4, false, 50, 1},
		{"one", v4[:4], AFIIPv4, false, 1, 0},
	} {
		got, diags := parseNLRI(tc.b, tc.afi, tc.addPath)
		if len(got) != tc.n || cap(got) != tc.n || len(diags) != tc.diags {
			t.Errorf("%s: %d prefixes (cap %d), %d diagnostics; want %d, %d", tc.name, len(got), cap(got), len(diags), tc.n, tc.diags)
		}
		want := 1.0
		if tc.diags > 0 {
			want = 3 // the prefixes, the diagnostics slice and the message
		}
		if a := testing.AllocsPerRun(100, func() { parseNLRI(tc.b, tc.afi, tc.addPath) }); a != want {
			t.Errorf("%s: %v allocations, want %v", tc.name, a, want)
		}
	}
	if got, _ := parseNLRI(nil, AFIIPv4, false); got != nil {
		t.Errorf("empty: %v, want nil", got)
	}
}
