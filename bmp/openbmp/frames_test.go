package openbmp_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"runtime"
	"strconv"
	"testing"

	"github.com/rkolesnichenko/mrt/bmp/openbmp"
)

// TestFramesAllocations: Frames allocates one buffer per frame, plus a few
// objects per range.
func TestFramesAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector inflates allocation")
	}
	if strconv.IntSize != 64 {
		t.Skip("the counts are measured on 64-bit platforms")
	}
	for _, name := range []string{"obmp-v4-router.obmp", "obmp-v6-router.obmp"} {
		disk := readFixture(t, name)
		frames, errs := collect(t, bytes.NewReader(disk))
		if len(errs) != 0 {
			t.Fatalf("%s: %v", name, errs)
		}
		r := bytes.NewReader(disk)
		got := testing.AllocsPerRun(10, func() {
			r.Reset(disk)
			for range openbmp.Frames(r) {
			}
		})
		t.Logf("%s: %.0f allocations for %d frames", name, got, len(frames))
		if limit := float64(len(frames) + 4); got > limit {
			t.Errorf("%s: %.0f allocations for %d frames, want at most %.0f (1 per frame)", name, got, len(frames), limit)
		}
	}
}

// bigFrame is a frame whose BMP message is n bytes of a pattern. Frames does
// not parse the message, so it need not be BMP.
func bigFrame(n int) []byte {
	f := frame(openbmp.Header{RouterIP: netip.MustParseAddr("192.0.2.7")})
	hl := int(binary.BigEndian.Uint16(f[6:]))
	f = f[:hl:hl]
	binary.BigEndian.PutUint32(f[8:], uint32(n))
	for i := range n {
		f = append(f, byte(i*7))
	}
	return f
}

// TestFramesLarge: a frame over the size read in one piece (64 KiB) comes
// out whole, also from a reader that returns one byte at a time, and a cut
// one is ErrTruncated.
func TestFramesLarge(t *testing.T) {
	big := bigFrame(300 << 10)
	small := frame(openbmp.Header{RouterIP: netip.MustParseAddr("192.0.2.7")})
	in := bytes.Join([][]byte{small, big, small}, nil)
	for _, r := range []io.Reader{bytes.NewReader(in), oneByte{bytes.NewReader(in)}} {
		frames, errs := collect(t, r)
		if len(errs) != 0 || len(frames) != 3 || !bytes.Equal(frames[1], big) || !bytes.Equal(bytes.Join(frames, nil), in) {
			t.Fatalf("%d frames, errors %v; want the three frames", len(frames), errs)
		}
	}
	frames, errs := collect(t, bytes.NewReader(in[:len(small)+len(big)-1]))
	if len(frames) != 1 || len(errs) != 1 || !errors.Is(errs[0], openbmp.ErrTruncated) {
		t.Errorf("cut inside the large frame: %d frames, errors %v; want 1 frame and ErrTruncated", len(frames), errs)
	}
}

// TestFramesClaimBoundedAlloc: a frame that states nearly MaxFrameLen but
// holds 1,000 bytes allocates in proportion to the bytes present, not the
// stated length (CLAUDE.md principle 2).
func TestFramesClaimBoundedAlloc(t *testing.T) {
	f := bigFrame(1000)
	hl := int(binary.BigEndian.Uint16(f[6:]))
	binary.BigEndian.PutUint32(f[8:], uint32(openbmp.MaxFrameLen-hl))
	const runs = 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range runs {
		frames, errs := collect(t, bytes.NewReader(f))
		if len(frames) != 0 || len(errs) != 1 || !errors.Is(errs[0], openbmp.ErrTruncated) {
			t.Fatalf("%d frames, errors %v; want ErrTruncated", len(frames), errs)
		}
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / runs; per > 256<<10 {
		t.Errorf("a %d-byte frame stating %d bytes allocates %d bytes, want at most %d", len(f), openbmp.MaxFrameLen, per, 256<<10)
	}
}
