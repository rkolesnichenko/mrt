package openbmp_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
)

// bgpkitVector is the frame in bgpkit-parser 0.22.0's test_open_bmp_header: a
// 100-byte header and a 51-byte BMP Peer Down, then one stray byte (0x0a) that
// bgpkit's header-only test never reads.
const bgpkitVector = "4f424d500107006400000033800c6184b9c2000c602cbf4f072f3ae149d23486024bc3dadfc4000a69732d63632d626d7031c677060bdd020a9e92be000200de2e3180df3369000000000000000000000000000c726f7574652d76696577733500000001030000003302000000000000000000000000000000000000000000003fda060e00000da30000000061523c36000c0e1c0200000a"

func TestParseBgpkitVector(t *testing.T) {
	frame, err := hex.DecodeString(bgpkitVector)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := openbmp.Parse(frame); !errors.Is(err, openbmp.ErrMalformed) {
		t.Errorf("with the stray byte: %v, want ErrMalformed", err)
	}
	frame = frame[:len(frame)-1]
	h, msg, err := openbmp.Parse(frame)
	if err != nil {
		t.Fatal(err)
	}
	if h.CollectorAdminID != "is-cc-bmp1" || h.RouterGroup != "route-views5" || h.RowCount != 1 ||
		h.Major != 1 || h.Minor != 7 || h.ObjectType != 12 || h.Flags != 0x80 ||
		h.Length != 100 || h.MessageLen != 51 {
		t.Errorf("header %+v", h)
	}
	if want := netip.MustParseAddr("128.223.51.105"); h.RouterIP != want {
		t.Errorf("router IP %v, want %v", h.RouterIP, want)
	}
	if h.Time.IsZero() || h.Time.Location() != time.UTC {
		t.Errorf("time %v", h.Time)
	}
	m, err := bmp.ParseMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(bmp.PeerDown); !ok {
		t.Errorf("%T, want bmp.PeerDown", m)
	}
	if cap(msg) != len(msg) {
		t.Errorf("cap %d, len %d", cap(msg), len(msg))
	}
}

// frame builds a valid frame around a small BMP message.
func frame(h openbmp.Header) []byte {
	return bmpgen.OBMP(h, bmpgen.Termination())
}

func TestParseRouterIP(t *testing.T) {
	for _, ip := range []string{"192.0.2.7", "2001:db8::7"} {
		a := netip.MustParseAddr(ip)
		f := frame(openbmp.Header{RouterIP: a, CollectorAdminID: "c", RouterGroup: "g",
			Time: time.Unix(1700000000, 5000).UTC()})
		h, msg, err := openbmp.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		if h.RouterIP != a || (h.Flags&openbmp.FlagIPv6 != 0) != a.Is6() {
			t.Errorf("%s: got %v flags %#x", ip, h.RouterIP, h.Flags)
		}
		if !h.Time.Equal(time.Unix(1700000000, 5000)) || h.CollectorAdminID != "c" || h.RouterGroup != "g" {
			t.Errorf("header %+v", h)
		}
		if !bytes.Equal(msg, bmpgen.Termination()) {
			t.Error("message differs")
		}
	}
	// A zero timestamp is the zero Time.
	h, _, err := openbmp.Parse(frame(openbmp.Header{RouterIP: netip.MustParseAddr("192.0.2.7")}))
	if err != nil || !h.Time.IsZero() {
		t.Errorf("time %v, err %v", h.Time, err)
	}
}

func TestParseErrors(t *testing.T) {
	good := frame(openbmp.Header{RouterIP: netip.MustParseAddr("192.0.2.7"), CollectorAdminID: "adm"})
	// Offsets: 4 major, 5 minor, 6-7 header length, 8-11 message length, 12
	// flags, 13 object type, 22-37 hash, 38-39 admin ID length.
	with := func(f func(b []byte) []byte) []byte { return f(bytes.Clone(good)) }
	set := func(i int, v byte) []byte { return with(func(b []byte) []byte { b[i] = v; return b }) }
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, openbmp.ErrNotOpenBMP},
		{"no magic", set(0, 'X'), openbmp.ErrNotOpenBMP},
		{"text form", []byte("V: 1.7\nC_HASH_ID: x\n\n" + strings.Repeat("x", 100)), openbmp.ErrNotOpenBMP},
		{"version 1.6", set(5, 6), openbmp.ErrVersion},
		{"version 2.7", set(4, 2), openbmp.ErrVersion},
		{"not a router message", set(12, 0x00), openbmp.ErrMalformed},
		{"object type 11", set(13, 11), openbmp.ErrMalformed},
		{"row count 2", with(func(b []byte) []byte { b[len(b)-len(bmpgen.Termination())-1] = 2; return b }), openbmp.ErrMalformed},
		{"header length too big", set(7, good[7]+1), openbmp.ErrMalformed},
		{"header length too small", set(7, good[7]-1), openbmp.ErrMalformed},
		{"message length shorter", set(11, good[11]-1), openbmp.ErrMalformed},
		{"message length longer", set(11, good[11]+1), openbmp.ErrMalformed},
		{"cut in the admin ID", good[:40+1], openbmp.ErrMalformed},
		{"cut in the version", good[:5], openbmp.ErrMalformed},
		{"magic only", good[:4], openbmp.ErrMalformed},
	}
	for _, tt := range tests {
		_, _, err := openbmp.Parse(tt.in)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	// A message length beyond the bytes left is malformed by truncation
	// (spec §8); one short of them is not truncation.
	if _, _, err := openbmp.Parse(set(11, good[11]+1)); !errors.Is(err, openbmp.ErrMalformed) || !errors.Is(err, openbmp.ErrTruncated) {
		t.Errorf("message length longer: %v, want ErrMalformed and ErrTruncated", err)
	}
	if _, _, err := openbmp.Parse(set(11, good[11]-1)); errors.Is(err, openbmp.ErrTruncated) {
		t.Errorf("message length shorter: %v, want no ErrTruncated", err)
	}
}

// TestParseShortFrameBoundedAlloc is CLAUDE.md principle 2: a short frame that
// claims a 64 KiB admin ID or router group allocates nothing sized by the
// claim.
func TestParseShortFrameBoundedAlloc(t *testing.T) {
	good := frame(openbmp.Header{RouterIP: netip.MustParseAddr("192.0.2.7")})
	// With an empty admin ID, its length is at 38-39 and the router group's at 72-73.
	claim := func(at, cut int) []byte {
		b := bytes.Clone(good[:cut])
		b[at], b[at+1] = 0xFF, 0xFF
		return b
	}
	for name, in := range map[string][]byte{
		"admin ID":     claim(38, 50),
		"router group": claim(72, 80),
	} {
		if _, _, err := openbmp.Parse(in); !errors.Is(err, openbmp.ErrMalformed) {
			t.Fatalf("%s: %v, want ErrMalformed", name, err)
		}
		const runs = 100
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range runs {
			openbmp.Parse(in)
		}
		runtime.ReadMemStats(&after)
		if per := (after.TotalAlloc - before.TotalAlloc) / runs; per > 1024 {
			t.Errorf("%s: a %d-byte frame claiming 65535 bytes allocates %d bytes per Parse, want at most 1024", name, len(in), per)
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bmp", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func collect(t *testing.T, r io.Reader) (frames [][]byte, errs []error) {
	t.Helper()
	for f, err := range openbmp.Frames(r) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		frames = append(frames, f)
	}
	return frames, errs
}

func TestFramesLossless(t *testing.T) {
	for _, name := range []string{"obmp-v4-router.obmp", "obmp-v6-router.obmp"} {
		disk := readFixture(t, name)
		frames, errs := collect(t, bytes.NewReader(disk))
		if len(errs) != 0 || len(frames) == 0 {
			t.Fatalf("%s: %d frames, errors %v", name, len(frames), errs)
		}
		if got := bytes.Join(frames, nil); !bytes.Equal(got, disk) {
			t.Errorf("%s: frames do not reproduce the file", name)
		}
		// A reader that returns one byte at a time reads the same frames.
		again, errs := collect(t, io.NopCloser(oneByte{bytes.NewReader(disk)}))
		if len(errs) != 0 || len(again) != len(frames) {
			t.Errorf("%s: byte reader gave %d frames, errors %v", name, len(again), errs)
		}
		// A cut last frame is one ErrTruncated and the end.
		cut := disk[:len(disk)-3]
		frames2, errs := collect(t, bytes.NewReader(cut))
		if len(frames2) != len(frames)-1 || len(errs) != 1 || !errors.Is(errs[0], openbmp.ErrTruncated) {
			t.Errorf("%s cut: %d frames, errors %v", name, len(frames2), errs)
		}
	}
}

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) { return o.r.Read(p[:min(1, len(p))]) }

func TestFramesErrors(t *testing.T) {
	good := frame(openbmp.Header{RouterIP: netip.MustParseAddr("192.0.2.7")})
	tests := []struct {
		name string
		in   []byte
		want error // nil: any error
	}{
		{"junk", append(bytes.Clone(good), "junk junk junk junk"...), openbmp.ErrNotOpenBMP},
		{"short prefix", good[:7], openbmp.ErrTruncated},
		{"header length below the minimum", append([]byte("OBMP\x01\x07\x00\x10\x00\x00\x00\x00"), good[12:]...), openbmp.ErrMalformed},
		{"over 16 MiB", append(bytes.Clone(good[:8]), 0x7f, 0, 0, 0), nil},
	}
	for _, tt := range tests {
		frames, errs := collect(t, bytes.NewReader(tt.in))
		if len(errs) != 1 {
			t.Errorf("%s: errors %v", tt.name, errs)
			continue
		}
		if tt.want != nil && !errors.Is(errs[0], tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, errs[0], tt.want)
		}
		if tt.name == "junk" && len(frames) != 1 {
			t.Errorf("junk: %d frames before the error", len(frames))
		}
	}
	if f, errs := collect(t, bytes.NewReader(nil)); len(f) != 0 || len(errs) != 0 {
		t.Errorf("empty input: %d frames, errors %v", len(f), errs)
	}
}
