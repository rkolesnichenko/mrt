package bgp_test

import (
	"slices"
	"testing"
	"unsafe"

	"github.com/rkolesnichenko/mrt/bgp"
)

// TestPathSize pins the compact layout (spec §6.4a): 112 bytes is a size class
// of its own, so a held Path costs 112 bytes, not the 352 of v0.1.
func TestPathSize(t *testing.T) {
	if n := unsafe.Sizeof(bgp.Path{}); n != 112 {
		t.Errorf("unsafe.Sizeof(bgp.Path{}) = %d, want 112", n)
	}
}

func rawAttr(flags, code byte, v ...byte) []byte {
	return append([]byte{flags, code, byte(len(v))}, v...)
}

// TestPathRareOnlyWhenNeeded: the attributes most paths carry need no rare
// part; any rare attribute allocates it.
func TestPathRareOnlyWhenNeeded(t *testing.T) {
	common := func(as4 bool) []byte {
		asPath := rawAttr(0x40, 2, 2, 2, 0xFB, 0xF4, 0xFB, 0xF5) // 64500 64501 at width 2
		if as4 {
			asPath = rawAttr(0x40, 2, 2, 2, 0, 0, 0xFB, 0xF4, 0, 0, 0xFB, 0xF5)
		}
		return slices.Concat(
			rawAttr(0x40, 1, 0),                                   // ORIGIN
			asPath,                                                // AS_PATH
			rawAttr(0x40, 3, 192, 0, 2, 1),                        // NEXT_HOP
			rawAttr(0x80, 4, 0, 0, 0, 10),                         // MULTI_EXIT_DISC
			rawAttr(0x40, 5, 0, 0, 0, 100),                        // LOCAL_PREF
			rawAttr(0xC0, 8, 0xFB, 0xF4, 0, 1),                    // COMMUNITIES
			rawAttr(0xC0, 32, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3), // LARGE_COMMUNITY
			rawAttr(0xC0, 35, 0, 0, 0xFB, 0xF4),                   // OTC
		)
	}
	for _, as4 := range []bool{false, true} {
		ctx := bgp.Context{AS4: as4}
		p, diags := bgp.NewAttrs(common(as4), ctx).Decode()
		if len(diags) != 0 || !p.HasOrigin || !p.HasMED || !p.HasLocalPref || !p.HasOnlyToCustomer ||
			len(p.Communities) != 1 || len(p.LargeCommunities) != 1 || p.ASPath.Len() != 2 || !p.NextHop().IsValid() {
			t.Fatalf("as4 %v: common block decoded as %+v, %v", as4, p, diags)
		}
		if bgp.HasRare(p) {
			t.Errorf("as4 %v: common attributes allocated a rare part", as4)
		}
		agg := rawAttr(0xC0, 7, 0xFB, 0xF4, 192, 0, 2, 7)
		if as4 {
			agg = rawAttr(0xC0, 7, 0, 0, 0xFB, 0xF4, 192, 0, 2, 7)
		}
		p, diags = bgp.NewAttrs(slices.Concat(common(as4), agg), ctx).Decode()
		if a, ok := p.Aggregator(); len(diags) != 0 || !ok || a.AS != 64500 {
			t.Fatalf("as4 %v: aggregator = %v %v, diags %v", as4, a, ok, diags)
		}
		if !bgp.HasRare(p) {
			t.Errorf("as4 %v: AGGREGATOR allocated no rare part", as4)
		}
	}
	// Each other rare attribute allocates it too.
	for name, a := range map[string][]byte{
		"ORIGINATOR_ID":  rawAttr(0x80, 9, 10, 0, 0, 1),
		"CLUSTER_LIST":   rawAttr(0x80, 10, 10, 0, 0, 2),
		"EXT_COMMUNITY":  rawAttr(0xC0, 16, 0, 2, 0xFB, 0xF4, 0, 0, 0, 1),
		"MP_UNREACH":     rawAttr(0x80, 15, 0, 2, 1),
		"unknown":        rawAttr(0xC0, 99, 0xAA),
		"malformed MED":  rawAttr(0x80, 4, 0, 0, 10),
		"AS4_PATH":       rawAttr(0xC0, 17, 2, 1, 0, 3, 0, 0),
		"AS4_AGGREGATOR": rawAttr(0xC0, 18, 0, 3, 0, 0, 192, 0, 2, 8),
	} {
		p, _ := bgp.NewAttrs(slices.Concat(common(false), a), bgp.Context{}).Decode()
		if !bgp.HasRare(p) {
			t.Errorf("%s allocated no rare part", name)
		}
	}
}
