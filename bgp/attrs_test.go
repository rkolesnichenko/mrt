package bgp

import (
	"net/netip"
	"reflect"
	"testing"
)

// attr builds one attribute: flags, code, value (extended length when len > 255).
func attr(flags, code byte, v ...byte) []byte {
	if len(v) > 255 {
		flags |= 0x10
		return append([]byte{flags, code, byte(len(v) >> 8), byte(len(v))}, v...)
	}
	return append([]byte{flags, code, byte(len(v))}, v...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestDecodeSimple(t *testing.T) {
	raw := concat(
		attr(0x40, 1, 0),
		attr(0x40, 3, 192, 0, 2, 1),
		attr(0x80, 4, 0, 0, 0, 10),
		attr(0x40, 5, 0, 0, 0, 100),
		attr(0x40, 6),
		attr(0xC0, 8, 0xFD, 0xE8, 0, 1),
		attr(0x80, 9, 10, 0, 0, 1),
		attr(0x80, 10, 10, 0, 0, 2, 10, 0, 0, 3),
		attr(0xC0, 16, 0, 2, 0xFD, 0xE8, 0, 0, 0, 1),
		attr(0xC0, 32, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3),
		attr(0xC0, 35, 0, 0, 0xFD, 0xE8),
		attr(0xC0, 99, 0xAA),
	)
	p, diags := NewAttrs(raw, Context{AS4: true}).Decode()
	if len(diags) != 0 {
		t.Fatalf("diags = %v", diags)
	}
	if !p.HasOrigin || p.Origin != OriginIGP {
		t.Errorf("origin = %v %v", p.HasOrigin, p.Origin)
	}
	if p.NextHop != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("next hop = %v", p.NextHop)
	}
	if !p.HasMED || p.MED != 10 || !p.HasLocalPref || p.LocalPref != 100 || !p.AtomicAggregate {
		t.Errorf("scalars = %+v", p)
	}
	if !reflect.DeepEqual(p.Communities, []Community{65000<<16 | 1}) {
		t.Errorf("communities = %v", p.Communities)
	}
	if p.OriginatorID != netip.MustParseAddr("10.0.0.1") {
		t.Errorf("originator = %v", p.OriginatorID)
	}
	wantCL := []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")}
	if !reflect.DeepEqual(p.ClusterList, wantCL) {
		t.Errorf("cluster list = %v", p.ClusterList)
	}
	if !reflect.DeepEqual(p.ExtCommunities, []ExtCommunity{{0, 2, 0xFD, 0xE8, 0, 0, 0, 1}}) {
		t.Errorf("ext = %v", p.ExtCommunities)
	}
	if !reflect.DeepEqual(p.LargeCommunities, []LargeCommunity{{1, 2, 3}}) {
		t.Errorf("large = %v", p.LargeCommunities)
	}
	if !p.HasOnlyToCustomer || p.OnlyToCustomer != 65000 {
		t.Errorf("otc = %v %v", p.HasOnlyToCustomer, p.OnlyToCustomer)
	}
	if !reflect.DeepEqual(p.Unknown, []Attribute{{0xC0, 99, []byte{0xAA}}}) {
		t.Errorf("unknown = %v", p.Unknown)
	}
}

func TestDecodeMalformedKeepsGoing(t *testing.T) {
	raw := concat(attr(0x80, 4, 0, 0, 10), attr(0x40, 5, 0, 0, 0, 100))
	p, diags := NewAttrs(raw, Context{}).Decode()
	if p.HasMED {
		t.Error("MED decoded")
	}
	if !reflect.DeepEqual(p.Unknown, []Attribute{{0x80, 4, []byte{0, 0, 10}}}) {
		t.Errorf("unknown = %v", p.Unknown)
	}
	if len(diags) != 1 || diags[0].Code != "bgp/attr-length" || diags[0].Attr != AttrMED ||
		diags[0].Severity != SeverityWarning || diags[0].Offset != 0 {
		t.Errorf("diags = %+v", diags)
	}
	if !p.HasLocalPref || p.LocalPref != 100 {
		t.Error("LOCAL_PREF lost")
	}
}

func TestDecodeBadOrigin(t *testing.T) {
	p, diags := NewAttrs(attr(0x40, 1, 7), Context{}).Decode()
	if p.HasOrigin || len(p.Unknown) != 1 || len(diags) != 1 || diags[0].Code != "bgp/attr-value" {
		t.Errorf("p=%+v diags=%+v", p, diags)
	}
}

func TestDecodeTruncatedFraming(t *testing.T) {
	raw := concat(attr(0x40, 1, 0), []byte{0x40, 3, 50, 1, 2, 3})
	p, diags := NewAttrs(raw, Context{}).Decode()
	if !p.HasOrigin {
		t.Error("origin lost")
	}
	if len(p.Unknown) != 1 || !reflect.DeepEqual(p.Unknown[0].Value, raw[4:]) {
		t.Errorf("unknown = %v", p.Unknown)
	}
	if len(diags) != 1 || diags[0].Code != "bgp/attr-truncated" || diags[0].Severity != SeverityError || diags[0].Offset != 4 {
		t.Errorf("diags = %+v", diags)
	}
	// every cut of a valid block decodes without panic
	full := concat(attr(0x40, 1, 0), attr(0xC0, 32, make([]byte, 300)...))
	for i := range full {
		NewAttrs(full[:i], Context{}).Decode()
	}
}

func TestDecodeDuplicateFirstWins(t *testing.T) {
	raw := concat(attr(0x80, 4, 0, 0, 0, 10), attr(0x80, 4, 0, 0, 0, 20))
	p, diags := NewAttrs(raw, Context{}).Decode()
	if p.MED != 10 {
		t.Errorf("MED = %d", p.MED)
	}
	if len(p.Unknown) != 1 || p.Unknown[0].Value[3] != 20 {
		t.Errorf("unknown = %v", p.Unknown)
	}
	if len(diags) != 1 || diags[0].Code != "bgp/attr-duplicate" || diags[0].Severity != SeverityWarning || diags[0].Offset != 7 {
		t.Errorf("diags = %+v", diags)
	}
}

func TestAllWalksRawInOrder(t *testing.T) {
	big := make([]byte, 300)
	raw := concat(attr(0x40, 1, 0), attr(0xC0, 99, big...), attr(0x40, 6))
	var got []Attribute
	for at, err := range NewAttrs(raw, Context{}).All() {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, at)
	}
	want := []Attribute{{0x40, 1, []byte{0}}, {0xD0, 99, big}, {0x40, 6, []byte{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	// broken framing: one error, then stop
	n := 0
	for _, err := range NewAttrs(concat(attr(0x40, 1, 0), []byte{0x40, 3, 9}), Context{}).All() {
		n++
		if n == 2 && err == nil {
			t.Error("want error")
		}
	}
	if n != 2 {
		t.Errorf("yielded %d", n)
	}
}

func TestDecodeMalformedFirstCopyStillWins(t *testing.T) {
	raw := concat(attr(0x80, 4, 0, 0, 10), attr(0x80, 4, 0, 0, 0, 20))
	p, diags := NewAttrs(raw, Context{}).Decode()
	if p.HasMED || len(p.Unknown) != 2 {
		t.Fatalf("p = %+v", p)
	}
	if len(diags) != 2 || diags[0].Code != "bgp/attr-length" || diags[1].Code != "bgp/attr-duplicate" ||
		diags[1].Severity != SeverityWarning || diags[1].Offset != 6 {
		t.Errorf("diags = %+v", diags)
	}
}

func TestDecodeDuplicateUnknownAndDeferredCodes(t *testing.T) {
	// A 1-byte value is malformed for AS_PATH, AS4_PATH, AGGREGATOR and
	// AS4_AGGREGATOR, so their first copy adds its own diagnostic.
	malformed := map[byte]string{2: "bgp/aspath-width", 17: "bgp/attr-value", 7: "bgp/attr-length", 18: "bgp/attr-length"}
	for _, code := range []byte{99, 2, 17, 7, 18, 14, 15} {
		raw := concat(attr(0xC0, code, 1), attr(0xC0, code, 2))
		p, diags := NewAttrs(raw, Context{}).Decode()
		if len(p.Unknown) != 2 {
			t.Errorf("code %d: unknown = %v", code, p.Unknown)
		}
		want := 1
		if first, ok := malformed[code]; ok {
			want = 2
			if len(diags) == 2 && (diags[0].Code != first || diags[0].Offset != 0) {
				t.Errorf("code %d: diags[0] = %+v", code, diags[0])
			}
		}
		if len(diags) != want {
			t.Errorf("code %d: diags = %+v", code, diags)
			continue
		}
		if last := diags[want-1]; last.Code != "bgp/attr-duplicate" || last.Attr != AttrCode(code) || last.Offset != 4 {
			t.Errorf("code %d: diags = %+v", code, diags)
		}
	}
}
