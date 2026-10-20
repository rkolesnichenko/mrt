package bgp

import (
	"net/netip"
	"reflect"
	"testing"
)

// seg encodes one AS_PATH segment at width 2 or 4.
func seg(width int, typ SegmentType, asns ...uint32) []byte {
	b := []byte{byte(typ), byte(len(asns))}
	for _, a := range asns {
		if width == 2 {
			b = append(b, byte(a>>8), byte(a))
		} else {
			b = append(b, byte(a>>24), byte(a>>16), byte(a>>8), byte(a))
		}
	}
	return b
}

func asPathAttr(segs ...[]byte) []byte  { return attr(0x40, 2, concat(segs...)...) }
func as4PathAttr(segs ...[]byte) []byte { return attr(0xC0, 17, concat(segs...)...) }

func agg2(as uint16) []byte {
	return attr(0xC0, 7, byte(as>>8), byte(as), 192, 0, 2, 7)
}

func agg4(code byte, as uint32) []byte {
	return attr(0xC0, code, byte(as>>24), byte(as>>16), byte(as>>8), byte(as), 192, 0, 2, 8)
}

func path(segs ...Segment) ASPath { return ASPath{Segments: segs} }

func sq(asns ...ASN) Segment { return Segment{ASSequence, asns} }
func st(asns ...ASN) Segment { return Segment{ASSet, asns} }

// codes returns the diagnostic codes in order.
func codes(diags []Diagnostic) []string {
	var out []string
	for _, d := range diags {
		out = append(out, d.Code)
	}
	return out
}

func TestAS4Merge(t *testing.T) {
	ctx := Context{AS4: false}
	addr7 := netip.MustParseAddr("192.0.2.7")
	addr8 := netip.MustParseAddr("192.0.2.8")

	t.Run("prepend", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456, 23456, 300)),
			as4PathAttr(seg(4, ASSequence, 196608, 196609, 300)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags = %+v", diags)
		}
		if want := path(sq(100, 196608, 196609, 300)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if want := path(sq(100, 23456, 23456, 300)); !reflect.DeepEqual(p.ReceivedASPath(), want) {
			t.Errorf("ReceivedASPath = %v (aliased by the merge?)", p.ReceivedASPath())
		}
		if want := path(sq(196608, 196609, 300)); !reflect.DeepEqual(p.AS4Path(), want) {
			t.Errorf("AS4Path = %v", p.AS4Path())
		}
		if len(p.Unknown()) != 0 {
			t.Errorf("unknown = %v", p.Unknown())
		}
	})

	t.Run("AS_PATH shorter", func(t *testing.T) {
		as4Off := len(asPathAttr(seg(2, ASSequence, 100, 23456)))
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456)),
			as4PathAttr(seg(4, ASSequence, 196608, 196609, 300)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if want := path(sq(100, 23456)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if want := path(sq(196608, 196609, 300)); !reflect.DeepEqual(p.AS4Path(), want) {
			t.Errorf("AS4Path = %v", p.AS4Path())
		}
		if len(diags) != 1 || diags[0].Code != "bgp/as4path-ignored" || diags[0].Severity != SeverityInfo ||
			diags[0].Offset != as4Off || diags[0].Attr != AttrAS4Path {
			t.Errorf("diags = %+v", diags)
		}
	})

	t.Run("equal length", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 23456, 23456)),
			as4PathAttr(seg(4, ASSequence, 196608, 196609)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags = %+v", diags)
		}
		if want := path(sq(196608, 196609)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
	})

	t.Run("AS_SET counts 1", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 1, 23456), seg(2, ASSet, 2, 3)),
			as4PathAttr(seg(4, ASSequence, 70000), seg(4, ASSet, 2, 3)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags = %+v", diags)
		}
		if want := path(sq(1, 70000), st(2, 3)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
	})

	t.Run("leading confed sequence", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASConfedSequence, 64512, 64513), seg(2, ASSequence, 23456, 23456)),
			as4PathAttr(seg(4, ASSequence, 196608, 196609)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags = %+v", diags)
		}
		want := path(Segment{ASConfedSequence, []ASN{64512, 64513}}, sq(196608, 196609))
		if !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
	})

	t.Run("AGGREGATOR not AS_TRANS", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456)),
			agg2(100),
			as4PathAttr(seg(4, ASSequence, 70000)),
			agg4(18, 70000),
		)
		as4Off := len(asPathAttr(seg(2, ASSequence, 100, 23456))) + len(agg2(100))
		p, diags := NewAttrs(raw, ctx).Decode()
		if aggOf(p) != (aggResult{Aggregator{100, addr7}, true}) {
			t.Errorf("aggregator = %+v", aggOf(p))
		}
		if want := path(sq(100, 23456)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if want := path(sq(70000)); !reflect.DeepEqual(p.AS4Path(), want) {
			t.Errorf("AS4Path = %v", p.AS4Path())
		}
		if len(diags) != 1 || diags[0].Code != "bgp/as4path-ignored" || diags[0].Severity != SeverityInfo ||
			diags[0].Offset != as4Off || diags[0].Attr != AttrAS4Path {
			t.Errorf("diags = %+v", diags)
		}
	})

	t.Run("AGGREGATOR not AS_TRANS, AS4_AGGREGATOR without AS4_PATH", func(t *testing.T) {
		raw := concat(agg2(100), agg4(18, 70000))
		p, diags := NewAttrs(raw, ctx).Decode()
		if aggOf(p).Agg != (Aggregator{100, addr7}) {
			t.Errorf("aggregator = %+v", aggOf(p))
		}
		if len(diags) != 1 || diags[0].Code != "bgp/as4path-ignored" ||
			diags[0].Offset != len(agg2(100)) || diags[0].Attr != AttrAS4Aggregator {
			t.Errorf("diags = %+v", diags)
		}
	})

	t.Run("AGGREGATOR is AS_TRANS", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456)),
			agg2(23456),
			as4PathAttr(seg(4, ASSequence, 70000)),
			agg4(18, 70000),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags = %+v", diags)
		}
		if aggOf(p) != (aggResult{Aggregator{70000, addr8}, true}) {
			t.Errorf("aggregator = %+v", aggOf(p))
		}
		if want := path(sq(100, 70000)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
	})

	t.Run("AS4_AGGREGATOR alone", func(t *testing.T) {
		p, diags := NewAttrs(agg4(18, 70000), ctx).Decode()
		if len(diags) != 0 || aggOf(p) != (aggResult{Aggregator{70000, addr8}, true}) {
			t.Errorf("p = %+v diags = %+v", p, diags)
		}
	})

	t.Run("malformed AGGREGATOR, AS4_AGGREGATOR used", func(t *testing.T) {
		raw := concat(attr(0xC0, 7, 0, 100, 1), agg4(18, 70000))
		p, diags := NewAttrs(raw, ctx).Decode()
		if aggOf(p) != (aggResult{Aggregator{70000, addr8}, true}) {
			t.Errorf("aggregator = %+v", aggOf(p))
		}
		if !reflect.DeepEqual(codes(diags), []string{"bgp/attr-length"}) || len(p.Unknown()) != 1 {
			t.Errorf("diags = %+v unknown = %v", diags, p.Unknown())
		}
	})

	t.Run("no AS_PATH", func(t *testing.T) {
		p, diags := NewAttrs(as4PathAttr(seg(4, ASSequence, 70000)), ctx).Decode()
		if len(p.ASPath.Segments) != 0 {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if !reflect.DeepEqual(codes(diags), []string{"bgp/as4path-ignored"}) {
			t.Errorf("diags = %+v", diags)
		}
	})

	t.Run("AGGREGATOR not AS_TRANS, no AS4_AGGREGATOR", func(t *testing.T) {
		// The AGGREGATOR rule needs both aggregators (RFC 6793 §4.2.3, Ruling R5).
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456)),
			agg2(100),
			as4PathAttr(seg(4, ASSequence, 70000)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if len(diags) != 0 {
			t.Fatalf("diags = %+v", diags)
		}
		if want := path(sq(100, 70000)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if aggOf(p) != (aggResult{Aggregator{100, addr7}, true}) {
			t.Errorf("aggregator = %+v", aggOf(p))
		}
	})

	t.Run("AS4_PATH confed segments discarded", func(t *testing.T) {
		asp := asPathAttr(seg(2, ASSequence, 100, 23456))
		raw := concat(
			asp,
			as4PathAttr(seg(4, ASConfedSequence, 64512), seg(4, ASSequence, 70000), seg(4, ASConfedSet, 64513)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if want := path(sq(70000)); !reflect.DeepEqual(p.AS4Path(), want) {
			t.Errorf("AS4Path = %v", p.AS4Path())
		}
		if want := path(sq(100, 70000)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if len(diags) != 1 || diags[0].Code != "bgp/attr-value" || diags[0].Attr != AttrAS4Path ||
			diags[0].Severity != SeverityWarning || diags[0].Offset != len(asp) {
			t.Errorf("diags = %+v", diags)
		}
		if len(p.Unknown()) != 0 {
			t.Errorf("unknown = %v", p.Unknown())
		}
	})

	t.Run("AS4_PATH only confed segments", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456)),
			as4PathAttr(seg(4, ASConfedSequence, 64512)),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if want := path(sq(100, 23456)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if len(p.AS4Path().Segments) != 0 {
			t.Errorf("AS4Path = %v", p.AS4Path())
		}
		if len(diags) != 1 || diags[0].Code != "bgp/attr-value" || diags[0].Attr != AttrAS4Path ||
			diags[0].Severity != SeverityWarning {
			t.Errorf("diags = %+v", diags)
		}
		if len(p.Unknown()) != 1 || p.Unknown()[0].Code != AttrAS4Path {
			t.Errorf("unknown = %v", p.Unknown())
		}
	})

	t.Run("AS4_PATH zero length", func(t *testing.T) {
		// RFC 6793 §6: too short to carry one AS, so malformed; AS_PATH keeps
		// its own rule that an empty value is the empty path.
		raw := concat(asPathAttr(seg(2, ASSequence, 100, 23456)), attr(0xC0, 17))
		p, diags := NewAttrs(raw, ctx).Decode()
		if want := path(sq(100, 23456)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if len(diags) != 1 || diags[0].Code != "bgp/attr-value" || diags[0].Attr != AttrAS4Path ||
			diags[0].Severity != SeverityWarning {
			t.Errorf("diags = %+v", diags)
		}
		if !reflect.DeepEqual(p.Unknown(), []Attribute{{0xC0, 17, []byte{}}}) {
			t.Errorf("unknown = %v", p.Unknown())
		}
		p, diags = NewAttrs(attr(0x40, 2), ctx).Decode()
		if len(diags) != 0 || len(p.Unknown()) != 0 || len(p.ASPath.Segments) != 0 {
			t.Errorf("empty AS_PATH: p = %+v diags = %+v", p, diags)
		}
	})

	t.Run("AS4_PATH malformed", func(t *testing.T) {
		raw := concat(
			asPathAttr(seg(2, ASSequence, 100, 23456)),
			attr(0xC0, 17, 2, 1, 0, 1, 0x11),
		)
		p, diags := NewAttrs(raw, ctx).Decode()
		if want := path(sq(100, 23456)); !reflect.DeepEqual(p.ASPath, want) {
			t.Errorf("ASPath = %v", p.ASPath)
		}
		if !reflect.DeepEqual(codes(diags), []string{"bgp/attr-value"}) || len(p.Unknown()) != 1 {
			t.Errorf("diags = %+v unknown = %v", diags, p.Unknown())
		}
	})
}

func TestMergeAS4(t *testing.T) {
	cs := func(asns ...ASN) Segment { return Segment{ASConfedSequence, asns} }
	cset := func(asns ...ASN) Segment { return Segment{ASConfedSet, asns} }
	tests := []struct {
		name        string
		asPath, as4 ASPath
		want        ASPath
		wantOK      bool
	}{
		{"plan example", path(sq(100, 23456, 23456, 300)), path(sq(196608, 196609, 300)),
			path(sq(100, 196608, 196609, 300)), true},
		{"shorter", path(sq(1)), path(sq(2, 3)), path(sq(1)), false},
		{"both empty", ASPath{}, ASPath{}, ASPath{}, true},
		{"empty AS4_PATH", path(sq(1, 2)), ASPath{}, path(sq(1, 2)), true},
		{"set taken whole", path(st(1, 2), sq(23456)), path(sq(70000)),
			path(st(1, 2), sq(70000)), true},
		{"set in AS4_PATH", path(sq(1, 23456), st(2, 3)), path(sq(70000), st(2, 3)),
			path(sq(1, 70000), st(2, 3)), true},
		{"confed leads, need 0", path(cs(64512), cset(64513, 64514), sq(23456)), path(sq(70000)),
			path(cs(64512), cset(64513, 64514), sq(70000)), true},
		{"confed adjacent after whole segment", path(sq(1), cs(64512), sq(23456)), path(sq(70000)),
			path(sq(1), cs(64512), sq(70000)), true},
		{"confed after split sequence not taken", path(sq(1, 2, 3), cs(64512), sq(4)), path(sq(70000), sq(80000)),
			path(sq(1, 2, 70000), sq(80000)), true},
		{"set then sequence not joined", path(st(1, 2), sq(23456, 23456)), path(sq(70000, 70001)),
			path(st(1, 2), sq(70000, 70001)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.asPath.String()
			got, ok := mergeAS4(tc.asPath, tc.as4)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v", ok)
			}
			if ok && !reflect.DeepEqual(got.String(), tc.want.String()) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if ok && got.Len() != tc.asPath.Len() {
				t.Errorf("len %d, AS_PATH's %d", got.Len(), tc.asPath.Len())
			}
			if tc.asPath.String() != before {
				t.Errorf("AS_PATH changed to %q", tc.asPath)
			}
		})
	}
	// The result shares no backing array with AS_PATH: appending to it must
	// not write into the input.
	in := path(Segment{ASSequence, make([]ASN, 2, 8)})
	in.Segments[0].ASNs[0], in.Segments[0].ASNs[1] = 1, 23456
	got, ok := mergeAS4(in, path(sq(70000)))
	if !ok || len(got.Segments) != 1 {
		t.Fatalf("got %v %v", got, ok)
	}
	got.Segments[0].ASNs[0] = 99
	if in.Segments[0].ASNs[0] != 1 {
		t.Error("merged path aliases AS_PATH")
	}
}

func TestAS4PathIgnoredWith4ByteContext(t *testing.T) {
	asp := asPathAttr(seg(4, ASSequence, 100, 70000))
	raw := concat(asp, as4PathAttr(seg(4, ASSequence, 80000)), agg4(7, 100), agg4(18, 70000))
	p, diags := NewAttrs(raw, Context{AS4: true}).Decode()
	if want := path(sq(100, 70000)); !reflect.DeepEqual(p.ASPath, want) || !reflect.DeepEqual(p.ReceivedASPath(), want) {
		t.Errorf("ASPath = %v, received %v", p.ASPath, p.ReceivedASPath())
	}
	if want := path(sq(80000)); !reflect.DeepEqual(p.AS4Path(), want) {
		t.Errorf("AS4Path = %v", p.AS4Path())
	}
	if !aggOf(p).OK || aggOf(p).Agg.AS != 100 {
		t.Errorf("aggregator = %+v", aggOf(p))
	}
	if len(diags) != 1 || diags[0].Code != "bgp/as4path-ignored" || diags[0].Severity != SeverityInfo ||
		diags[0].Offset != len(asp) || diags[0].Attr != AttrAS4Path {
		t.Errorf("diags = %+v", diags)
	}

	// AS4_AGGREGATOR alone is ignored too.
	p, diags = NewAttrs(concat(agg4(7, 100), agg4(18, 70000)), Context{AS4: true}).Decode()
	if aggOf(p).Agg.AS != 100 || len(diags) != 1 || diags[0].Attr != AttrAS4Aggregator ||
		diags[0].Code != "bgp/as4path-ignored" || diags[0].Offset != 11 {
		t.Errorf("p = %+v diags = %+v", p, diags)
	}
}

func TestASPathWidthNeverSniffed(t *testing.T) {
	// 4-byte encoding read at width 2: SEQ count 1 takes 00 01, then 11 70
	// is not a segment.
	raw := concat(attr(0x40, 1, 0), attr(0x40, 2, 2, 1, 0x00, 0x01, 0x11, 0x70))
	p, diags := NewAttrs(raw, Context{AS4: false}).Decode()
	if len(p.ASPath.Segments) != 0 || len(p.ReceivedASPath().Segments) != 0 {
		t.Errorf("ASPath = %v, received %v", p.ASPath, p.ReceivedASPath())
	}
	if len(diags) != 1 || diags[0].Code != "bgp/aspath-width" || diags[0].Severity != SeverityWarning ||
		diags[0].Offset != 4 || diags[0].Attr != AttrASPath {
		t.Errorf("diags = %+v", diags)
	}
	if !reflect.DeepEqual(p.Unknown(), []Attribute{{0x40, 2, []byte{2, 1, 0x00, 0x01, 0x11, 0x70}}}) {
		t.Errorf("unknown = %v", p.Unknown())
	}
	if !p.HasOrigin {
		t.Error("ORIGIN lost")
	}
}

func TestASPathParse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		v     []byte
		want  ASPath
		ok    bool
	}{
		{"empty", 2, nil, ASPath{}, true},
		{"2-byte", 2, concat(seg(2, ASSequence, 1, 2), seg(2, ASSet, 3)), path(sq(1, 2), st(3)), true},
		{"4-byte", 4, seg(4, ASConfedSet, 70000, 64512), path(Segment{ASConfedSet, []ASN{70000, 64512}}), true},
		{"count 0", 2, []byte{2, 0}, ASPath{}, false},
		{"type 0", 2, []byte{0, 1, 0, 1}, ASPath{}, false},
		{"type 5", 4, []byte{5, 1, 0, 0, 0, 1}, ASPath{}, false},
		{"short header", 2, concat(seg(2, ASSequence, 1), []byte{2}), ASPath{}, false},
		{"missing bytes", 4, []byte{2, 2, 0, 0, 0, 1, 0, 0}, ASPath{}, false},
		{"huge count", 4, []byte{2, 255, 0, 0, 0, 1}, ASPath{}, false},
		{"2-byte read as 4", 4, seg(2, ASSequence, 1, 2), ASPath{}, false},
	} {
		got, ok := parseASPath(tc.v, tc.width)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v %v", tc.name, got, ok)
		}
	}
}

func TestAggregatorWidths(t *testing.T) {
	want := Aggregator{70000, netip.MustParseAddr("192.0.2.8")}
	p, diags := NewAttrs(agg2(100), Context{AS4: false}).Decode()
	if len(diags) != 0 || aggOf(p) != (aggResult{Aggregator{100, netip.MustParseAddr("192.0.2.7")}, true}) {
		t.Errorf("2-byte: p = %+v diags = %+v", p, diags)
	}
	p, diags = NewAttrs(agg4(7, 70000), Context{AS4: true}).Decode()
	if len(diags) != 0 || aggOf(p) != (aggResult{want, true}) {
		t.Errorf("4-byte: p = %+v diags = %+v", p, diags)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		ctx  Context
	}{
		{"8 bytes under AS4:false", agg4(7, 70000), Context{AS4: false}},
		{"6 bytes under AS4:true", agg2(100), Context{AS4: true}},
		{"AS4_AGGREGATOR 6 bytes", attr(0xC0, 18, 0, 100, 192, 0, 2, 7), Context{AS4: false}},
	} {
		p, diags := NewAttrs(tc.raw, tc.ctx).Decode()
		if aggOf(p).OK || len(p.Unknown()) != 1 || len(diags) != 1 || diags[0].Code != "bgp/attr-length" ||
			diags[0].Attr != AttrCode(tc.raw[1]) || diags[0].Severity != SeverityWarning {
			t.Errorf("%s: p = %+v diags = %+v", tc.name, p, diags)
		}
	}
}

// aggResult is Path.Aggregator's two results as one comparable value.
type aggResult struct {
	Agg Aggregator
	OK  bool
}

func aggOf(p Path) aggResult { a, ok := p.Aggregator(); return aggResult{a, ok} }
