package bgp

import (
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"unsafe"
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
	if p.NextHop() != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("next hop = %v", p.NextHop())
	}
	if !p.HasMED || p.MED != 10 || !p.HasLocalPref || p.LocalPref != 100 || !p.AtomicAggregate {
		t.Errorf("scalars = %+v", p)
	}
	if !reflect.DeepEqual(p.Communities, []Community{65000<<16 | 1}) {
		t.Errorf("communities = %v", p.Communities)
	}
	if p.OriginatorID() != netip.MustParseAddr("10.0.0.1") {
		t.Errorf("originator = %v", p.OriginatorID())
	}
	wantCL := []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")}
	if !reflect.DeepEqual(p.ClusterList(), wantCL) {
		t.Errorf("cluster list = %v", p.ClusterList())
	}
	if !reflect.DeepEqual(p.ExtCommunities(), []ExtCommunity{{0, 2, 0xFD, 0xE8, 0, 0, 0, 1}}) {
		t.Errorf("ext = %v", p.ExtCommunities())
	}
	if !reflect.DeepEqual(p.LargeCommunities, []LargeCommunity{{1, 2, 3}}) {
		t.Errorf("large = %v", p.LargeCommunities)
	}
	if !p.HasOnlyToCustomer || p.OnlyToCustomer != 65000 {
		t.Errorf("otc = %v %v", p.HasOnlyToCustomer, p.OnlyToCustomer)
	}
	if !reflect.DeepEqual(p.Unknown(), []Attribute{{0xC0, 99, []byte{0xAA}}}) {
		t.Errorf("unknown = %v", p.Unknown())
	}
}

func TestDecodeMalformedKeepsGoing(t *testing.T) {
	raw := concat(attr(0x80, 4, 0, 0, 10), attr(0x40, 5, 0, 0, 0, 100))
	p, diags := NewAttrs(raw, Context{}).Decode()
	if p.HasMED {
		t.Error("MED decoded")
	}
	if !reflect.DeepEqual(p.Unknown(), []Attribute{{0x80, 4, []byte{0, 0, 10}}}) {
		t.Errorf("unknown = %v", p.Unknown())
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
	if p.HasOrigin || len(p.Unknown()) != 1 || len(diags) != 1 || diags[0].Code != "bgp/attr-value" {
		t.Errorf("p=%+v diags=%+v", p, diags)
	}
}

func TestDecodeTruncatedFraming(t *testing.T) {
	raw := concat(attr(0x40, 1, 0), []byte{0x40, 3, 50, 1, 2, 3})
	p, diags := NewAttrs(raw, Context{}).Decode()
	if !p.HasOrigin {
		t.Error("origin lost")
	}
	if len(p.Unknown()) != 1 || !reflect.DeepEqual(p.Unknown()[0].Value, raw[4:]) {
		t.Errorf("unknown = %v", p.Unknown())
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
	if len(p.Unknown()) != 1 || p.Unknown()[0].Value[3] != 20 {
		t.Errorf("unknown = %v", p.Unknown())
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
	if p.HasMED || len(p.Unknown()) != 2 {
		t.Fatalf("p = %+v", p)
	}
	if len(diags) != 2 || diags[0].Code != "bgp/attr-length" || diags[1].Code != "bgp/attr-duplicate" ||
		diags[1].Severity != SeverityWarning || diags[1].Offset != 6 {
		t.Errorf("diags = %+v", diags)
	}
}

func TestDecodeDuplicateUnknown(t *testing.T) {
	// A 1-byte value is malformed for AS_PATH, AS4_PATH, AGGREGATOR,
	// AS4_AGGREGATOR, MP_REACH_NLRI and MP_UNREACH_NLRI, so their first copy adds its own diagnostic.
	malformed := map[byte]string{2: "bgp/aspath-width", 17: "bgp/attr-value", 7: "bgp/attr-length", 18: "bgp/attr-length", 14: "bgp/attr-length", 15: "bgp/attr-length"}
	for _, code := range []byte{99, 2, 17, 7, 18, 14, 15} {
		raw := concat(attr(0xC0, code, 1), attr(0xC0, code, 2))
		p, diags := NewAttrs(raw, Context{}).Decode()
		if len(p.Unknown()) != 2 {
			t.Errorf("code %d: unknown = %v", code, p.Unknown())
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

// cloneBlocks returns blocks that between them set every field Clone copies.
func cloneBlocks() [][]byte {
	nhv4 := []byte{192, 0, 2, 1}
	return [][]byte{
		concat(
			attr(0x40, 1, 0),
			asPathAttr(seg(2, ASSequence, 64500, 64501), seg(2, ASSet, 1, 2)),
			as4PathAttr(seg(4, ASSequence, 4200000000, 64501)),
			attr(0x40, 3, 192, 0, 2, 1),
			attr(0xC0, 8, 0xFD, 0xE8, 0, 1, 0xFD, 0xE8, 0, 2),
			attr(0xC0, 16, 0, 2, 0xFD, 0xE8, 0, 0, 0, 1),
			attr(0xC0, 32, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3),
			attr(0x80, 9, 10, 0, 0, 1),
			attr(0x80, 10, 10, 0, 0, 2, 10, 0, 0, 3),
			attr(0xC0, 7, 0xFD, 0xE8, 192, 0, 2, 7),
			attr(0x80, 14, concat([]byte{0, 2, 1, 32}, b16(v6global), b16(v6ll), []byte{0, 48, 0x20, 1, 0x0d, 0xb8, 0, 1})...),
			attr(0x80, 15, 0, 1, 1, 24, 198, 51, 100),
			attr(0xC0, 99, 0xAA, 0xBB),
		),
		concat(
			attr(0x80, 14, concat([]byte{0, 25, 65, 4}, nhv4, []byte{0, 0xDE, 0xAD})...),
		),
		concat(
			attr(0x80, 15, 0, 25, 65, 0xBE, 0xEF),
		),
	}
}

func TestPathClone(t *testing.T) {
	for i, orig := range cloneBlocks() {
		saved := slices.Clone(orig)
		block := slices.Clone(orig)
		p, _ := NewAttrs(block, Context{}).Decode()
		c := p.Clone()
		if !reflect.DeepEqual(c, p) {
			t.Fatalf("block %d: clone differs:\n%+v\n%+v", i, c, p)
		}
		if p.rare != nil && c.rare == p.rare || p.MPReach != nil && c.MPReach == p.MPReach {
			t.Fatalf("block %d: clone shares a pointer", i)
		}
		want, _ := NewAttrs(saved, Context{}).Decode()

		for j := range block {
			block[j] = 0xEE
		}
		scribble(reflect.ValueOf(&p).Elem())
		if !reflect.DeepEqual(c, want) {
			t.Errorf("block %d: clone changed after the source was overwritten:\n%+v\n%+v", i, c, want)
		}
	}
	// the three blocks together must have exercised every pointer and slice
	p, _ := NewAttrs(cloneBlocks()[0], Context{}).Decode()
	_, hasAgg := p.Aggregator()
	if p.MPReach == nil || len(p.MPReach.NLRI) == 0 || p.MPUnreach() == nil || len(p.MPUnreach().Withdrawn) == 0 ||
		len(p.Unknown()) == 0 || len(p.ASPath.Segments) == 0 || len(p.AS4Path().Segments) == 0 || len(p.ClusterList()) == 0 ||
		len(p.Communities) == 0 || len(p.LargeCommunities) == 0 || len(p.ExtCommunities()) == 0 ||
		!p.rare.merged || len(p.ReceivedASPath().Segments) == 0 || !hasAgg || !p.OriginatorID().IsValid() || !p.NextHop().IsValid() {
		t.Fatalf("block 0 left a field unset: %+v %+v", p, p.rare)
	}
	q, _ := NewAttrs(cloneBlocks()[1], Context{}).Decode()
	u, _ := NewAttrs(cloneBlocks()[2], Context{}).Decode()
	if q.MPReach == nil || len(q.MPReach.RawNLRI) == 0 || u.MPUnreach() == nil || len(u.MPUnreach().RawNLRI) == 0 {
		t.Fatalf("raw NLRI blocks left a field unset: %+v %+v", q.MPReach, u.MPUnreach())
	}
}

func TestPathCloneNilAndEmpty(t *testing.T) {
	var z Path
	if c := z.Clone(); !reflect.DeepEqual(c, z) || c.Communities != nil || c.MPReach != nil || c.rare != nil {
		t.Errorf("zero path: %+v", c)
	}
	e := Path{
		Communities: []Community{}, LargeCommunities: []LargeCommunity{},
		ASPath:  ASPath{Segments: []Segment{{ASSequence, []ASN{}}, {ASSet, nil}}},
		MPReach: &MPReach{NLRI: []NLRI{}, RawNLRI: []byte{}},
		rare: &pathRare{
			clusterList: []netip.Addr{}, extCommunities: []ExtCommunity{}, unknown: []Attribute{{Value: []byte{}}, {}},
			receivedASPath: ASPath{Segments: []Segment{}}, as4Path: ASPath{Segments: []Segment{{ASSequence, []ASN{}}}},
			mpUnreach: &MPUnreach{Withdrawn: []NLRI{}, RawNLRI: []byte{}},
		},
	}
	c := e.Clone()
	if !reflect.DeepEqual(c, e) {
		t.Errorf("empty slices not preserved: %#v", c)
	}
}

// scribble overwrites every slice element reachable from v, through pointers,
// structs (unexported fields included) and slices, with garbage.
func scribble(v reflect.Value) {
	if v.CanAddr() && !v.CanSet() { // reached through an unexported field
		v = reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			scribble(v.Elem())
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(netip.Addr{}) || v.Type() == reflect.TypeOf(netip.Prefix{}) {
			return // opaque values, replaced by the slice case
		}
		for i := 0; i < v.NumField(); i++ {
			scribble(v.Field(i))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			e := v.Index(i)
			switch {
			case e.Type() == reflect.TypeOf(netip.Addr{}):
				e.Set(reflect.ValueOf(netip.AddrFrom4([4]byte{0xEE, 0xEE, 0xEE, 0xEE})))
			case e.Type() == reflect.TypeOf(netip.Prefix{}):
				e.Set(reflect.ValueOf(netip.MustParsePrefix("238.238.238.238/32")))
			case e.Kind() == reflect.Uint8 || e.Kind() == reflect.Uint16 || e.Kind() == reflect.Uint32:
				e.SetUint(0xEEEEEEEE & (1<<uint(e.Type().Bits()) - 1))
			case e.Kind() == reflect.Array:
				for k := 0; k < e.Len(); k++ {
					e.Index(k).SetUint(0xEE)
				}
			default:
				scribble(e)
			}
		}
	}
}
