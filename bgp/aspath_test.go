package bgp

import "testing"

func TestASPathLen(t *testing.T) {
	p := ASPath{Segments: []Segment{
		{ASConfedSequence, []ASN{64512, 64513}},
		{ASSequence, []ASN{1, 2, 3}},
		{ASSet, []ASN{4, 5}},
	}}
	if got := p.Len(); got != 4 { // 3 + set counts 1 + confed 0
		t.Fatalf("Len = %d, want 4", got)
	}
	if got := (ASPath{}).Len(); got != 0 {
		t.Fatalf("empty Len = %d, want 0", got)
	}
}

func TestASPathOrigin(t *testing.T) {
	for _, c := range []struct {
		name   string
		p      ASPath
		want   ASN
		wantOK bool
	}{
		{"empty", ASPath{}, 0, false},
		{"sequence", ASPath{[]Segment{{ASSequence, []ASN{1, 2}}}}, 2, true},
		{"ends in set", ASPath{[]Segment{{ASSequence, []ASN{1}}, {ASSet, []ASN{3, 4}}}}, 0, false},
		{"empty last segment", ASPath{[]Segment{{ASSequence, nil}}}, 0, false},
	} {
		got, ok := c.p.Origin()
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: Origin = (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

func TestASPathString(t *testing.T) {
	for _, c := range []struct {
		name string
		p    ASPath
		want string
	}{
		{"empty", ASPath{}, ""},
		{"set", ASPath{[]Segment{{ASSequence, []ASN{1, 2}}, {ASSet, []ASN{3, 4}}}}, "1 2 {3,4}"},
		{"confed sequence", ASPath{[]Segment{{ASConfedSequence, []ASN{64512, 64513}}, {ASSequence, []ASN{1}}}}, "(64512 64513) 1"},
		{"confed set", ASPath{[]Segment{{ASConfedSet, []ASN{64512, 64513}}, {ASSequence, []ASN{1}}}}, "[64512,64513] 1"},
	} {
		if got := c.p.String(); got != c.want {
			t.Errorf("%s: String = %q, want %q", c.name, got, c.want)
		}
	}
}
