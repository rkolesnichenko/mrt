package bgp

import (
	"net/netip"
	"testing"
)

func TestExtCommunity(t *testing.T) {
	tests := []struct {
		raw     ExtCommunity
		str     string
		typ, st uint8
		trans   bool
		rt, ro  *ExtCommunityAdmin
		color   *uint32
		encap   *uint16
		lbwAS   ASN
		lbw     *float32
		ov      *ValidationState
	}{
		{raw: ExtCommunity{0x00, 0x02, 0xFB, 0xF4, 0, 0, 0, 100}, str: "rt:64500:100", typ: 0, st: 2, trans: true,
			rt: &ExtCommunityAdmin{AS: 64500, Local: 100}},
		{raw: ExtCommunity{0x02, 0x03, 0xFA, 0x56, 0xEA, 0x01, 0x00, 0x0A}, str: "ro:4200000001:10", typ: 2, st: 3, trans: true,
			ro: &ExtCommunityAdmin{AS: 4200000001, Local: 10}},
		{raw: ExtCommunity{0x01, 0x02, 0xC0, 0x00, 0x02, 0x01, 0x00, 0x64}, str: "rt:192.0.2.1:100", typ: 1, st: 2, trans: true,
			rt: &ExtCommunityAdmin{Addr: netip.MustParseAddr("192.0.2.1"), Local: 100}},
		{raw: ExtCommunity{0x41, 0x03, 0xC0, 0x00, 0x02, 0x01, 0x00, 0x07}, str: "ro:192.0.2.1:7", typ: 0x41, st: 3,
			ro: &ExtCommunityAdmin{Addr: netip.MustParseAddr("192.0.2.1"), Local: 7}},
		{raw: ExtCommunity{0x40, 0x02, 0xFB, 0xF4, 0, 0, 0, 1}, str: "rt:64500:1", typ: 0x40, st: 2,
			rt: &ExtCommunityAdmin{AS: 64500, Local: 1}},
		{raw: ExtCommunity{0x03, 0x0B, 0, 0, 0, 0, 0, 100}, str: "color:100", typ: 3, st: 0x0B, trans: true, color: ptr(uint32(100))},
		{raw: ExtCommunity{0x03, 0x0C, 0, 0, 0, 0, 0, 8}, str: "encap:8", typ: 3, st: 0x0C, trans: true, encap: ptr(uint16(8))},
		{raw: ExtCommunity{0x40, 0x04, 0xFB, 0xF4, 0x4C, 0x3E, 0xBC, 0x20}, str: "lbw:64500:5e+07", typ: 0x40, st: 4,
			lbwAS: 64500, lbw: ptr(float32(5e7))},
		{raw: ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 2}, str: "ov:invalid", typ: 0x43, st: 0, ov: ptr(ValidationInvalid)},
		{raw: ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 0}, str: "ov:valid", typ: 0x43, st: 0, ov: ptr(ValidationValid)},
		{raw: ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 1}, str: "ov:not-found", typ: 0x43, st: 0, ov: ptr(ValidationNotFound)},
		{raw: ExtCommunity{0x43, 0x00, 0, 0, 0, 0, 0, 7}, str: "0x4300000000000007", typ: 0x43, st: 0},
		{raw: ExtCommunity{0x1A, 0x70, 0x01, 0x00, 0x00, 0x00, 0x40, 0x81}, str: "0x1a70010000004081", typ: 0x1A, st: 0x70, trans: true},
		{raw: ExtCommunity{0x80, 0x08, 0, 0, 0xFB, 0xF4, 0, 0}, str: "0x80080000fbf40000", typ: 0x80, st: 8, trans: true},
	}
	for _, tc := range tests {
		t.Run(tc.str, func(t *testing.T) {
			c := tc.raw
			if got := c.String(); got != tc.str {
				t.Errorf("String = %q, want %q", got, tc.str)
			}
			if c.Type() != tc.typ || c.Subtype() != tc.st || c.Transitive() != tc.trans {
				t.Errorf("Type/Subtype/Transitive = %#x/%#x/%v", c.Type(), c.Subtype(), c.Transitive())
			}
			a, ok := c.RouteTarget()
			if ok != (tc.rt != nil) || (ok && a != *tc.rt) {
				t.Errorf("RouteTarget = %v, %v", a, ok)
			}
			a, ok = c.RouteOrigin()
			if ok != (tc.ro != nil) || (ok && a != *tc.ro) {
				t.Errorf("RouteOrigin = %v, %v", a, ok)
			}
			if v, ok := c.Color(); ok != (tc.color != nil) || (ok && v != *tc.color) {
				t.Errorf("Color = %v, %v", v, ok)
			}
			if v, ok := c.Encapsulation(); ok != (tc.encap != nil) || (ok && v != *tc.encap) {
				t.Errorf("Encapsulation = %v, %v", v, ok)
			}
			if as, f, ok := c.LinkBandwidth(); ok != (tc.lbw != nil) || (ok && (f != *tc.lbw || as != tc.lbwAS)) {
				t.Errorf("LinkBandwidth = %v, %v, %v", as, f, ok)
			}
			if v, ok := c.OriginValidation(); ok != (tc.ov != nil) || (ok && v != *tc.ov) {
				t.Errorf("OriginValidation = %v, %v", v, ok)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
