package openbmp_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"strconv"
	"testing"
	"testing/iotest"

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
	want := fmt.Sprintf("openbmp: truncated frame: %d of %d bytes", len(big)-1, len(big))
	if len(frames) != 1 || len(errs) != 1 || !errors.Is(errs[0], openbmp.ErrTruncated) || errs[0].Error() != want {
		t.Errorf("cut inside the large frame: %d frames, errors %v; want 1 frame and %q", len(frames), errs, want)
	}
}

// errAt reads r and returns err with the read that reaches offset at. When
// sticky it returns err from then on; otherwise later reads go on.
type errAt struct {
	r             *bytes.Reader
	at            int64
	err           error
	sticky, fired bool
}

func (e *errAt) Read(p []byte) (int, error) {
	if e.fired && e.sticky {
		return 0, e.err
	}
	pos := e.r.Size() - int64(e.r.Len())
	if !e.fired && pos < e.at && pos+int64(len(p)) >= e.at {
		n, _ := e.r.Read(p[:e.at-pos])
		e.fired = true
		return n, e.err
	}
	return e.r.Read(p)
}

// TestFramesReaderError: a reader error ends the sequence with that error,
// whether or not the reader returns it again. An error that comes with a
// frame's last bytes keeps that frame from being yielded; one that comes with
// bytes of the next frame's 12-byte prefix, including the bytes that complete
// it, comes after the first frame.
func TestFramesReaderError(t *testing.T) {
	boom := errors.New("boom")
	for _, size := range []int{100, 300 << 10} {
		f := bigFrame(size)
		in := bytes.Join([][]byte{f, f}, nil)
		for _, c := range []struct {
			name   string
			at     int
			frames int
		}{
			{"first prefix complete", 12, 0},
			{"first frame complete", len(f), 0},
			{"inside second prefix", len(f) + 5, 1},
			{"second prefix complete", len(f) + 12, 1},
		} {
			for _, sticky := range []bool{true, false} {
				r := &errAt{r: bytes.NewReader(in), at: int64(c.at), err: boom, sticky: sticky}
				frames, errs := collect(t, r)
				if len(frames) != c.frames || len(errs) != 1 || errs[0] != boom {
					t.Errorf("%d-byte message, error at %s (byte %d), sticky %v: %d frames, errors %v; want %d frames, then only %v",
						size, c.name, c.at, sticky, len(frames), errs, c.frames, boom)
				}
			}
		}
	}
}

// TestFramesDataEOF: a reader that returns io.EOF with its last bytes
// (iotest.DataErrReader) reads the same frames and the same truncation errors
// as one that returns io.EOF on its own, and a stream ending inside a prefix
// is ErrTruncated also when the reader says io.ErrUnexpectedEOF.
func TestFramesDataEOF(t *testing.T) {
	f := bigFrame(100)
	trunc := openbmp.ErrTruncated.Error()
	for _, c := range []struct {
		name   string
		in     []byte
		frames int
		err    string // "" for no error
	}{
		{"two frames", bytes.Join([][]byte{f, f}, nil), 2, ""},
		{"inside the prefix", f[:5], 0, trunc + ": 5 bytes of the 12-byte prefix"},
		{"after the prefix", f[:12], 0, fmt.Sprintf("%s: 12 of %d bytes", trunc, len(f))},
		{"inside the second prefix", append(bytes.Clone(f), f[:7]...), 1, trunc + ": 7 bytes of the 12-byte prefix"},
		{"after the second prefix", append(bytes.Clone(f), f[:12]...), 1, fmt.Sprintf("%s: 12 of %d bytes", trunc, len(f))},
		{"inside the frame", f[:len(f)-1], 0, fmt.Sprintf("%s: %d of %d bytes", trunc, len(f)-1, len(f))},
		{"bad magic", append([]byte("OBMX"), f[4:12]...), 0, openbmp.ErrNotOpenBMP.Error()},
	} {
		for _, r := range []struct {
			name string
			r    io.Reader
		}{
			{"plain", bytes.NewReader(c.in)},
			{"data with EOF", iotest.DataErrReader(bytes.NewReader(c.in))},
			{"one byte with EOF", iotest.DataErrReader(oneByte{bytes.NewReader(c.in)})},
		} {
			frames, errs := collect(t, r.r)
			var got string
			if len(errs) > 0 {
				got = errs[0].Error()
			}
			if len(frames) != c.frames || len(errs) > 1 || got != c.err {
				t.Errorf("%s, %s reader: %d frames, errors %v; want %d frames, error %q", c.name, r.name, len(frames), errs, c.frames, c.err)
			}
		}
	}
	r := &errAt{r: bytes.NewReader(f), at: 5, err: io.ErrUnexpectedEOF, sticky: true}
	if frames, errs := collect(t, r); len(frames) != 0 || len(errs) != 1 || errs[0].Error() != trunc+": 5 bytes of the 12-byte prefix" {
		t.Errorf("io.ErrUnexpectedEOF inside the prefix: %d frames, errors %v; want ErrTruncated", len(frames), errs)
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
