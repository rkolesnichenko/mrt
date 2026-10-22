package bmpgen_test

import (
	"bytes"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
)

// dir is where the fixtures live.
const dir = "../../testdata/bmp"

// expect is what reading one fixture must give: the number of errors, and
// whether the last is ErrTruncated. Only bmp-garbage is read with a 1 KiB limit.
type expect struct {
	errs      int
	truncated bool
	limit     uint32
}

var expected = map[string]expect{
	"bmp-types":         {},
	"bmp-peer-flags":    {},
	"bmp-addpath":       {},
	"bmp-as2":           {},
	"bmp-ipv6-peers":    {},
	"bmp-eor":           {},
	"bmp-no-peer-up":    {},
	"bmp-bad-open":      {},
	"bmp-reup":          {},
	"bmp-sessions":      {},
	"bmp-stats":         {},
	"bmp-open-extended": {},
	"bmp-locrib-multi":  {},
	"bmp-truncated":     {errs: 1, truncated: true},
	// length 3, version 2, one message over the limit, twelve zero bytes of junk (two headers).
	"bmp-garbage": {errs: 5, limit: 1024},
}

// read returns the messages of a file and its errors.
func read(data []byte, limit uint32) (msgs []bmp.Message, errs []error) {
	for m, err := range bmp.MessagesWith(bytes.NewReader(data), bmp.Options{MaxMessageLen: limit}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, errs
}

func TestGeneratedFiles(t *testing.T) {
	files := bmpgen.Synthetic()
	if len(files) != len(expected) {
		t.Fatalf("%d files, want %d", len(files), len(expected))
	}
	again := bmpgen.Synthetic()
	for i, f := range files {
		if !bytes.Equal(f.Data, again[i].Data) {
			t.Errorf("%s is not deterministic", f.Name)
		}
		t.Run(f.Name, func(t *testing.T) {
			want, ok := expected[f.Name]
			if !ok {
				t.Fatalf("no expectation for %s", f.Name)
			}
			path := filepath.Join(dir, f.Name+".bmp")
			if os.Getenv("MRT_GOLDEN_UPDATE") == "1" {
				if err := os.WriteFile(path, f.Data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			disk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("run MRT_GOLDEN_UPDATE=1 go test ./internal/bmpgen: %v", err)
			}
			if !bytes.Equal(disk, f.Data) {
				t.Fatal("file on disk differs from the generator's output; regenerate")
			}
			msgs, errs := read(disk, want.limit)
			if len(msgs) == 0 {
				t.Fatal("no messages")
			}
			if len(errs) != want.errs {
				t.Fatalf("%d errors, want %d: %v", len(errs), want.errs, errs)
			}
			if want.truncated != (len(errs) > 0 && errors.Is(errs[len(errs)-1], bmp.ErrTruncated)) {
				t.Errorf("last error %v, truncated want %v", errs, want.truncated)
			}
			if f.Name == "bmp-garbage" {
				checkGarbageErrors(t, errs)
			}
			// Lossless unless something was dropped.
			if f.Name != "bmp-garbage" && !want.truncated {
				var raw []byte
				for m, err := range bmp.Messages(bytes.NewReader(disk)) {
					if err != nil {
						var me *bmp.MessageError
						if !errors.As(err, &me) {
							t.Fatalf("error %v is not a *MessageError", err)
						}
						raw = append(raw, me.Raw...)
						continue
					}
					raw = append(raw, m.Raw()...)
				}
				if !bytes.Equal(raw, disk) {
					t.Error("Raw does not reproduce the file")
				}
			}
			for _, m := range msgs {
				if _, err := bmp.ParseMessage(m.Raw()); err != nil {
					t.Errorf("ParseMessage(%T): %v", m, err)
				}
			}
		})
	}
}

// fixture returns the messages of the named file, which must read cleanly.
func fixture(t *testing.T, name string) []bmp.Message {
	t.Helper()
	for _, f := range bmpgen.Synthetic() {
		if f.Name == name {
			msgs, _ := read(f.Data, expected[name].limit)
			return msgs
		}
	}
	t.Fatalf("no fixture %s", name)
	return nil
}

func TestTypesCoverage(t *testing.T) {
	types := map[bmp.Type]bool{}
	reasons := map[bmp.PeerDownReason]bool{}
	for _, m := range fixture(t, "bmp-types") {
		types[m.Header().Type] = true
		if d, ok := m.(bmp.PeerDown); ok {
			reasons[d.Reason] = true
		}
	}
	for ty := bmp.TypeRouteMonitoring; ty <= bmp.TypeRouteMirroring; ty++ {
		if !types[ty] {
			t.Errorf("type %d missing", ty)
		}
	}
	for r := bmp.PeerDownLocalNotification; r <= bmp.PeerDownLocalTLV; r++ {
		if !reasons[r] {
			t.Errorf("Peer Down reason %d missing", r)
		}
	}
}

func TestPeerFlagsCoverage(t *testing.T) {
	seen := map[[2]uint8]bool{}
	for _, m := range fixture(t, "bmp-peer-flags") {
		if rm, ok := m.(bmp.RouteMonitoring); ok {
			seen[[2]uint8{uint8(rm.Peer.Type), rm.Peer.Flags}] = true
		}
	}
	for typ := uint8(0); typ <= 2; typ++ {
		for fl := uint8(0); fl < 16; fl++ {
			if !seen[[2]uint8{typ, fl << 4}] {
				t.Errorf("type %d flags %#x missing", typ, fl<<4)
			}
		}
	}
	for _, fl := range []uint8{0, 0x80} {
		if !seen[[2]uint8{3, fl}] {
			t.Errorf("Loc-RIB flags %#x missing", fl)
		}
	}
}

func TestAS2HasWideAS(t *testing.T) {
	for _, m := range fixture(t, "bmp-as2") {
		if rm, ok := m.(bmp.RouteMonitoring); ok && rm.Peer.AS > 65535 && rm.Peer.Flags&0x20 != 0 {
			return
		}
	}
	t.Error("no A-flag peer with an AS above 65535")
}

func TestGarbageHasTypeNine(t *testing.T) {
	var rm9, other9 bool
	for _, m := range fixture(t, "bmp-garbage") {
		switch m := m.(type) {
		case bmp.RouteMonitoring:
			rm9 = rm9 || m.Peer.Type == 9
		case bmp.Other:
			other9 = other9 || m.Header().Type == 9
		}
	}
	if !rm9 || !other9 {
		t.Errorf("peer type 9: %v, message type 9: %v", rm9, other9)
	}
}

func TestBuilders(t *testing.T) {
	h := bmp.PeerHeader{Type: mrt.PeerRD, Flags: 0x80, Distinguisher: 7,
		Addr: netip.MustParseAddr("2001:db8::9"), AS: 64500, BGPID: netip.MustParseAddr("192.0.2.9")}
	m, err := bmp.ParseMessage(bmpgen.RouteMonitoring(h, []byte{1, 2}))
	if err != nil {
		t.Fatal(err)
	}
	rm := m.(bmp.RouteMonitoring)
	if rm.Peer.Addr != h.Addr || rm.Peer.Distinguisher != 7 || rm.Peer.AS != 64500 || !rm.Peer.Time.IsZero() {
		t.Errorf("peer header %+v", rm.Peer)
	}
	// IPv4 in the last four bytes; invalid address (Loc-RIB) all zeros.
	h4 := bmp.PeerHeader{Addr: netip.MustParseAddr("192.0.2.9")}
	if got := bmpgen.PeerHeader(h4)[10:26]; !bytes.Equal(got, append(make([]byte, 12), 192, 0, 2, 9)) {
		t.Errorf("IPv4 field % x", got)
	}
	if got := bmpgen.PeerHeader(bmp.PeerHeader{Type: mrt.PeerLocRIB})[10:26]; !bytes.Equal(got, make([]byte, 16)) {
		t.Errorf("zero field % x", got)
	}

	// OPENs: AS_TRANS above 65535, and the plain and extended forms agree on content.
	caps := [][]byte{bmpgen.CapMP(bgp.Family{AFI: 1, SAFI: 1}), bmpgen.CapAS4(196618),
		bmpgen.CapAddPath(bgp.AddPathFamily{Family: bgp.Family{AFI: 1, SAFI: 1}, Mode: 3})}
	id := netip.MustParseAddr("192.0.2.1")
	for name, raw := range map[string][]byte{"plain": bmpgen.Open(196618, id, caps...), "extended": bmpgen.OpenExtended(196618, id, caps...)} {
		o, diags := bgp.ParseOpen(raw)
		if len(diags) != 0 {
			t.Fatalf("%s: %v", name, diags)
		}
		if o.MyAS != bgp.ASTrans || o.AS() != 196618 || o.HoldTime != 90 || o.Version != 4 || len(o.Capabilities) != 3 || len(o.AddPath()) != 1 {
			t.Errorf("%s: %+v", name, o)
		}
	}
	if bmpgen.Open(64500, id)[28] != 0 {
		t.Error("an OPEN without capabilities has optional parameters")
	}
	if ext := bmpgen.OpenExtended(64500, id, caps...); ext[28] != 255 || ext[29] != 255 {
		t.Errorf("extended OPEN starts % x", ext[28:30])
	}
}

func TestBadOpenCoverage(t *testing.T) {
	var bodyErrs, errDiags, warnDiags int
	for _, m := range fixture(t, "bmp-bad-open") {
		u, ok := m.(bmp.PeerUp)
		if !ok {
			continue
		}
		b, err := u.Body()
		if err != nil {
			bodyErrs++
			continue
		}
		for _, raw := range [][]byte{b.SentOpen, b.ReceivedOpen} {
			_, diags := bgp.ParseOpen(raw)
			for _, d := range diags {
				if d.Severity == bgp.SeverityError {
					errDiags++
				} else {
					warnDiags++
				}
			}
		}
	}
	if bodyErrs != 2 || errDiags != 1 || warnDiags != 2 {
		t.Errorf("body errors %d, error diagnostics %d, warnings %d; want 2, 1, 2", bodyErrs, errDiags, warnDiags)
	}
}

// checkGarbageErrors pins the five errors of bmp-garbage in order: length 3,
// version 2, two zero headers and an oversize message.
func checkGarbageErrors(t *testing.T, errs []error) {
	t.Helper()
	if len(errs) != 5 {
		t.Fatalf("%d errors, want 5", len(errs))
	}
	var offs [5]int64
	for i, err := range errs {
		var me *bmp.MessageError
		if !errors.As(err, &me) {
			t.Fatalf("error %d is %T, not a *MessageError", i, err)
		}
		offs[i] = me.Offset
		switch i {
		case 0:
			if !errors.Is(err, bmp.ErrMalformed) || me.Header.Length != 3 {
				t.Errorf("error 0: %v, want malformed with length 3", err)
			}
		case 1:
			if !errors.Is(err, bmp.ErrMalformed) || me.Header.Version != 2 {
				t.Errorf("error 1: %v, want malformed version 2", err)
			}
		case 2, 3:
			if !errors.Is(err, bmp.ErrMalformed) || me.Header.Length != 0 || len(me.Raw) != 6 {
				t.Errorf("error %d: %v, want a malformed zero header", i, err)
			}
		default:
			if !errors.Is(err, bmp.ErrTooLarge) {
				t.Errorf("error 4: %v, want ErrTooLarge", err)
			}
		}
	}
	if offs[3]-offs[2] != 6 || !(offs[0] < offs[1] && offs[1] < offs[2] && offs[2] < offs[3]) {
		t.Errorf("offsets %v: want increasing, the zero headers 6 apart", offs)
	}
}

// TestStatsCoverage counts what the accessors say about bmp-stats.
func TestStatsCoverage(t *testing.T) {
	var ok, bad, errs, msgs int
	for _, m := range fixture(t, "bmp-stats") {
		sr, isStats := m.(bmp.StatsReport)
		if !isStats {
			continue
		}
		msgs++
		for s, err := range sr.Stats() {
			if err != nil {
				errs++
				continue
			}
			_, c := s.Counter()
			_, g := s.Gauge()
			_, _, f := s.FamilyGauge()
			if c || g || f {
				ok++
			} else {
				bad++
			}
		}
	}
	// ok: 42 registered stats of every type, 3 more, and the 3 in the
	// mismatched-count messages. bad: type 9999, expired type 24 and the 3
	// wrong lengths. errs: count 5 with 2 stats, count 0 with 1, and a TLV
	// that runs past the message.
	if msgs != 8 || ok != 48 || bad != 5 || errs != 3 {
		t.Errorf("messages %d, registered %d, unreadable %d, errors %d; want 8, 48, 5, 3", msgs, ok, bad, errs)
	}
}
