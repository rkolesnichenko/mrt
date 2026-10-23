package bgp

import (
	"net/netip"
	"reflect"
	"testing"
)

var (
	v4uni = Family{AFIIPv4, SAFIUnicast}
	v6uni = Family{AFIIPv6, SAFIUnicast}
)

func capTLV(code byte, val ...byte) []byte { return append([]byte{code, byte(len(val))}, val...) }

// capParam wraps capabilities in a type 2 optional parameter.
func capParam(caps ...[]byte) []byte {
	var v []byte
	for _, c := range caps {
		v = append(v, c...)
	}
	return append([]byte{2, byte(len(v))}, v...)
}

// openMsg builds an OPEN with My AS 23456, hold time 90, BGP ID 192.0.2.1 and the given
// already-framed optional parameters.
func openMsg(params ...[]byte) []byte {
	var p []byte
	for _, x := range params {
		p = append(p, x...)
	}
	body := []byte{4, 0x5b, 0xa0, 0, 90, 192, 0, 2, 1, byte(len(p))}
	return msgOf(1, append(body, p...)...)
}

// openMsgExt is openMsg in the RFC 9072 form.
func openMsgExt(params ...[]byte) []byte {
	var p []byte
	for _, x := range params { // convert 1-byte parameter lengths to 2 bytes
		for len(x) > 0 {
			n := int(x[1])
			p = append(p, x[0], 0, x[1])
			p = append(p, x[2:2+n]...)
			x = x[2+n:]
		}
	}
	body := []byte{4, 0x5b, 0xa0, 0, 90, 192, 0, 2, 1, 255, 255, byte(len(p) >> 8), byte(len(p))}
	return msgOf(1, append(body, p...)...)
}

var (
	capMP      = capTLV(1, 0, 1, 0, 1)
	capMP6     = capTLV(1, 0, 2, 0, 1)
	capAS4     = capTLV(65, 0xfa, 0x56, 0xea, 0x00) // 4200000000
	capAddPath = capTLV(69, 0, 1, 1, 3, 0, 2, 1, 1)
)

func wantCodes(t *testing.T, d []Diagnostic, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(codes(d), want) {
		t.Fatalf("diagnostics %v, want %v (%+v)", codes(d), want, d)
	}
}

func checkBasic(t *testing.T, o Open, d []Diagnostic) {
	t.Helper()
	wantCodes(t, d)
	if o.Version != 4 || o.MyAS != 23456 || o.HoldTime != 90 || o.BGPID != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("fixed fields: %+v", o)
	}
	if o.AS() != 4200000000 || !o.AS4() {
		t.Errorf("AS %d AS4 %v", o.AS(), o.AS4())
	}
	if got := o.Multiprotocol(); !reflect.DeepEqual(got, []Family{v4uni, v6uni}) {
		t.Errorf("Multiprotocol %v", got)
	}
	want := []AddPathFamily{{v4uni, 3}, {v6uni, 1}}
	if got := o.AddPath(); !reflect.DeepEqual(got, want) {
		t.Errorf("AddPath %v, want %v", got, want)
	}
	var got []CapabilityCode
	for _, c := range o.Capabilities {
		got = append(got, c.Code)
	}
	if want := []CapabilityCode{1, 1, 65, 69}; !reflect.DeepEqual(got, want) {
		t.Errorf("capability order %v, want %v", got, want)
	}
}

func TestParseOpenBasic(t *testing.T) {
	// capabilities in one parameter, and spread over several
	for _, msg := range [][]byte{
		openMsg(capParam(capMP, capMP6, capAS4, capAddPath)),
		openMsg(capParam(capMP), capParam(capMP6), []byte{1, 2, 9, 9}, capParam(capAS4, capAddPath)),
	} {
		o, d := ParseOpen(msg)
		checkBasic(t, o, d)
	}
}

func TestParseOpenExtendedParams(t *testing.T) {
	o, d := ParseOpen(openMsgExt(capParam(capMP, capMP6, capAS4, capAddPath)))
	checkBasic(t, o, d)
	// RFC 9072 §3: the extended form is detected by the type, whatever the 1-byte length says.
	msg := openMsgExt(capParam(capMP, capMP6, capAS4, capAddPath))
	msg[28] = 7
	o, d = ParseOpen(msg)
	checkBasic(t, o, d)
}

func TestParseOpenNoParams(t *testing.T) {
	o, d := ParseOpen(openMsg())
	wantCodes(t, d)
	if o.Capabilities != nil || o.AS() != 23456 || o.AS4() || o.Multiprotocol() != nil || o.AddPath() != nil {
		t.Errorf("%+v", o)
	}
}

func TestParseOpenErrors(t *testing.T) {
	good := openMsg(capParam(capMP, capMP6, capAS4, capAddPath))
	badMarker := append([]byte(nil), good...)
	badMarker[3] = 0
	overrun := append([]byte(nil), good...)
	overrun[28] = 200
	extOverrun := openMsgExt(capParam(capMP))
	extOverrun[30] = 200 // high byte of the extended length
	short := msgOf(1, 4, 0, 1)
	paramOverrun := openMsg([]byte{2, 5, 1, 4, 0, 1})        // parameter says 5 bytes, 4 are present
	lonelyByte := openMsg([]byte{2, 7, 1, 4, 0, 1, 0, 1, 9}) // a whole capability, then one byte where a header should start
	capValueOverrun := openMsg([]byte{2, 4, 1, 9, 0, 1})     // capability says 9 bytes, 2 are left in its parameter
	cut := good[:len(good)-3]

	cases := []struct {
		name string
		msg  []byte
		want []string
	}{
		{"bad marker", badMarker, []string{DiagMessageHeader}},
		{"keepalive", msgOf(4), []string{DiagNotOpen}},
		{"update", msgOf(2, 0, 0, 0, 0), []string{DiagNotOpen}},
		{"short fixed part", short, []string{DiagOpenLength}},
		{"params length past message", overrun, []string{DiagOpenLength}},
		{"extended params length past message", extOverrun, []string{DiagOpenLength}},
		{"parameter past the optional parameters", paramOverrun, []string{DiagOpenLength}},
		{"one byte where a capability header should start", lonelyByte, []string{DiagOpenLength}},
		{"capability value past its parameter", capValueOverrun, []string{DiagOpenLength}},
		{"cut message", cut, []string{DiagMessageTruncated}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, d := ParseOpen(c.msg)
			wantCodes(t, d, c.want...)
			for _, x := range d {
				if x.Severity != SeverityError {
					t.Errorf("severity %d", x.Severity)
				}
			}
		})
	}

	t.Run("overrun keeps earlier capabilities", func(t *testing.T) {
		// Two parameters; the second claims 50 bytes. The first stays.
		msg := openMsg(capParam(capMP), []byte{2, 50, 65, 4})
		o, d := ParseOpen(msg)
		wantCodes(t, d, DiagOpenLength)
		if len(o.Capabilities) != 1 || o.Capabilities[0].Code != CapMultiprotocol || o.Version != 4 {
			t.Errorf("%+v", o)
		}
		// Declared length past the message: whole parameters present are kept.
		msg = openMsg(capParam(capMP), capParam(capMP6))
		msg[28] = 200
		o, d = ParseOpen(msg)
		wantCodes(t, d, DiagOpenLength)
		if len(o.Capabilities) != 2 {
			t.Errorf("%d capabilities kept", len(o.Capabilities))
		}
	})

	t.Run("capability overruns keep earlier capabilities", func(t *testing.T) {
		for _, tail := range [][]byte{{2, 1, 1}, {2, 4, 1, 9, 0, 1}} {
			// the first capability, then a capability that runs past its correctly sized parameter
			msg := openMsg(capParam(capMP), tail)
			o, d := ParseOpen(msg)
			wantCodes(t, d, DiagOpenLength)
			if len(o.Capabilities) != 1 || o.Capabilities[0].Code != CapMultiprotocol {
				t.Errorf("%v: %+v", tail, o.Capabilities)
			}
		}
	})

	t.Run("cut message without an overrun", func(t *testing.T) {
		msg := openMsg(capParam(capMP))
		msg[17] += 5 // header claims 5 more bytes than are present
		o, d := ParseOpen(msg)
		wantCodes(t, d, DiagMessageTruncated)
		if d[0].Severity != SeverityError || len(o.Capabilities) != 1 {
			t.Errorf("%+v %+v", d, o)
		}
	})

	t.Run("capability 65 of 3 bytes", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(65, 1, 2, 3))))
		wantCodes(t, d, DiagCapabilityValue)
		if d[0].Severity != SeverityWarning {
			t.Errorf("severity %d", d[0].Severity)
		}
		if o.AS4() || o.AS() != 23456 {
			t.Errorf("AS4 %v AS %d", o.AS4(), o.AS())
		}
		if len(o.Capabilities) != 1 || !reflect.DeepEqual(o.Capabilities[0].Value, []byte{1, 2, 3}) {
			t.Errorf("raw entry lost: %+v", o.Capabilities)
		}
		// a later valid one is used
		o, _ = ParseOpen(openMsg(capParam(capTLV(65, 1, 2, 3), capAS4)))
		if !o.AS4() || o.AS() != 4200000000 {
			t.Errorf("AS4 %v AS %d", o.AS4(), o.AS())
		}
	})

	t.Run("capability 1 of 3 bytes", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(1, 0, 1, 0), capMP6)))
		wantCodes(t, d, DiagCapabilityValue)
		if got := o.Multiprotocol(); !reflect.DeepEqual(got, []Family{v6uni}) || len(o.Capabilities) != 2 {
			t.Errorf("%v %d", got, len(o.Capabilities))
		}
	})

	t.Run("ADD-PATH of 5 bytes", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 3, 0))))
		wantCodes(t, d, DiagCapabilityValue)
		if len(o.AddPath()) != 0 || len(o.Capabilities) != 1 {
			t.Errorf("%v %d", o.AddPath(), len(o.Capabilities))
		}
	})
}

func TestParseOpenTrailingBytes(t *testing.T) {
	msg := append(openMsg(capParam(capMP)), 1, 2)
	o, d := ParseOpen(msg)
	wantCodes(t, d, DiagMessageHeader)
	if d[0].Severity != SeverityWarning || len(o.Capabilities) != 1 {
		t.Errorf("%+v %+v", d, o)
	}
	// bytes inside the message after the declared parameters
	m := openMsg(capParam(capMP), []byte{9, 9, 9})
	m[28] -= 3
	_, d = ParseOpen(m)
	wantCodes(t, d, DiagOpenLength)
	if d[0].Severity != SeverityWarning {
		t.Errorf("severity %d", d[0].Severity)
	}
}

func TestParseOpenAliasesMessage(t *testing.T) {
	msg := openMsg(capParam(capAS4))
	o, _ := ParseOpen(msg)
	msg[len(msg)-1] = 0x01
	if o.Capabilities[0].Value[3] != 0x01 {
		t.Error("Value does not alias the message")
	}
}

func TestOpenAddPathRules(t *testing.T) {
	t.Run("mode 0", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 3, 0, 2, 1, 0))))
		wantCodes(t, d, DiagCapabilityValue)
		if d[0].Severity != SeverityWarning {
			t.Errorf("severity %d", d[0].Severity)
		}
		want := []AddPathFamily{{v4uni, 3}, {v6uni, 0}}
		if got := o.AddPath(); !reflect.DeepEqual(got, want) {
			t.Errorf("AddPath %v, want %v", got, want)
		}
		both, _ := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 3))))
		if s := AddPathFamilies(o, both); s != 0 {
			t.Errorf("sender with a bad Mode: %b", s)
		}
		if s := AddPathFamilies(both, o); s != 0 {
			t.Errorf("receiver with a bad Mode: %b", s)
		}
		if s := AddPathFamilies(both, both); !s.Has(v4uni) {
			t.Error("control: good capability negotiates")
		}
	})
	t.Run("mode 4", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 4))))
		wantCodes(t, d, DiagCapabilityValue)
		if len(o.AddPath()) != 1 || AddPathFamilies(o, o) != 0 {
			t.Errorf("%v", o.AddPath())
		}
	})
	t.Run("second capability", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 3), capTLV(69, 0, 2, 1, 3))))
		wantCodes(t, d, DiagCapabilityValue)
		if d[0].Severity != SeverityWarning {
			t.Errorf("severity %d", d[0].Severity)
		}
		if got := o.AddPath(); !reflect.DeepEqual(got, []AddPathFamily{{v4uni, 3}}) || len(o.Capabilities) != 2 {
			t.Errorf("%v %d", got, len(o.Capabilities))
		}
		if s := AddPathFamilies(o, o); !s.Has(v4uni) || s.Has(v6uni) {
			t.Errorf("set %b", s)
		}
	})
	t.Run("first instance malformed, second ignored", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1), capTLV(69, 0, 2, 1, 3))))
		wantCodes(t, d, DiagCapabilityValue, DiagCapabilityValue)
		if len(o.AddPath()) != 0 {
			t.Errorf("%v", o.AddPath())
		}
	})
	t.Run("family twice", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 1, 0, 1, 1, 3))))
		wantCodes(t, d, DiagCapabilityValue)
		if got := o.AddPath(); !reflect.DeepEqual(got, []AddPathFamily{{v4uni, 1}}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("empty capability", func(t *testing.T) {
		o, d := ParseOpen(openMsg(capParam(capTLV(69))))
		wantCodes(t, d)
		if len(o.AddPath()) != 0 || len(o.Capabilities) != 1 {
			t.Errorf("%v", o.AddPath())
		}
	})
}

func TestAddPathFamilyModes(t *testing.T) {
	for mode, want := range map[uint8][2]bool{0: {false, false}, 1: {true, false}, 2: {false, true}, 3: {true, true}, 4: {false, false}} {
		f := AddPathFamily{v4uni, mode}
		if f.Receive() != want[0] || f.Send() != want[1] {
			t.Errorf("mode %d: receive %v send %v", mode, f.Receive(), f.Send())
		}
	}
}

func openWithMode(mode int) Open { // mode < 0: no ADD-PATH capability
	if mode < 0 {
		o, _ := ParseOpen(openMsg(capParam(capMP)))
		return o
	}
	o, _ := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, byte(mode)))))
	return o
}

func TestAddPathFamiliesTable(t *testing.T) {
	for s := -1; s <= 3; s++ {
		for r := -1; r <= 3; r++ {
			if s == 0 || r == 0 {
				continue // Mode 0 is invalid: covered in TestOpenAddPathRules
			}
			want := (s == 2 || s == 3) && (r == 1 || r == 3)
			got := AddPathFamilies(openWithMode(s), openWithMode(r))
			if got.Has(v4uni) != want || got.Has(v6uni) {
				t.Errorf("sender %d receiver %d: set %b, want in-set %v", s, r, got, want)
			}
			// not symmetric: the arguments swapped follow the swapped rule
			swapped := (r == 2 || r == 3) && (s == 1 || s == 3)
			if AddPathFamilies(openWithMode(r), openWithMode(s)).Has(v4uni) != swapped {
				t.Errorf("swapped %d %d", r, s)
			}
		}
	}
	// per family: only the family both sides agree on
	a, _ := ParseOpen(openMsg(capParam(capTLV(69, 0, 1, 1, 2, 0, 2, 1, 2))))
	b, _ := ParseOpen(openMsg(capParam(capTLV(69, 0, 2, 1, 1, 0, 1, 1, 2))))
	if s := AddPathFamilies(a, b); s != (FamilySet(0)).With(v6uni) {
		t.Errorf("set %b", s)
	}
	// a family without prefixes cannot be in a FamilySet
	c, _ := ParseOpen(openMsg(capParam(capTLV(69, 0, 25, 70, 3))))
	if s := AddPathFamilies(c, c); s != 0 || len(c.AddPath()) != 1 {
		t.Errorf("set %b, tuples %v", s, c.AddPath())
	}
	if (Open{}).AS() != 0 || AddPathFamilies(Open{}, Open{}) != 0 {
		t.Error("zero Open")
	}
}

func TestParseOpenAllocationBounded(t *testing.T) {
	// A parameter claiming 255 bytes with 2 present must not size anything by the claim.
	msg := openMsg([]byte{2, 255, 65, 255})
	o, d := ParseOpen(msg)
	wantCodes(t, d, DiagOpenLength)
	if len(o.Capabilities) != 0 {
		t.Errorf("%+v", o)
	}
	// many zero-length capabilities: exactly counted
	var caps []byte
	for i := 0; i < 100; i++ {
		caps = append(caps, 200, 0)
	}
	o, d = ParseOpen(openMsg(append([]byte{2, byte(len(caps))}, caps...)))
	wantCodes(t, d)
	if len(o.Capabilities) != 100 || cap(o.Capabilities) != 100 {
		t.Errorf("len %d cap %d", len(o.Capabilities), cap(o.Capabilities))
	}
}
