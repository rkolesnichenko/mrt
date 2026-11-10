package openbmp

import (
	"bytes"
	"errors"
	"runtime"
	"testing"
	"testing/iotest"
)

// TestReadRestHeldErrorNoGrow: when a frame stated larger than the bytes
// present ends exactly where readRest's buffer is full, and the reader
// returns its error with those last bytes (held by read), readRest returns
// ErrTruncated without doubling the buffer first: it allocates no more bytes
// than for a frame ending one byte earlier, mid-buffer. The old code's extra
// growth is a 128 KiB buffer; the slack allowed is a quarter of that, for
// the error's formatting and the race detector, which make allocation
// counts unsteady (TestFramesAllocations skips under -race for that).
func TestReadRestHeldErrorNoGrow(t *testing.T) {
	head := make([]byte, 12)
	total := smallFrame + 100_000
	const runs = 10
	allocated := func(rest int) uint64 {
		body := make([]byte, rest)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range runs {
			fr := &frameReader{r: iotest.DataErrReader(bytes.NewReader(body))}
			f, err := fr.readRest(head, total)
			if f != nil || !errors.Is(err, ErrTruncated) {
				t.Fatalf("%d bytes after the prefix: frame %d bytes, error %v; want ErrTruncated", rest, len(f), err)
			}
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / runs
	}
	full, short := allocated(smallFrame-len(head)), allocated(smallFrame-len(head)-1)
	if full > short+smallFrame/2 {
		t.Errorf("a frame ending at the full buffer allocates %d bytes a read, one ending a byte earlier %d", full, short)
	}
}
