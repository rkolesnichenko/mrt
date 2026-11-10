package openbmp

import (
	"bytes"
	"errors"
	"testing"
	"testing/iotest"
)

// TestReadRestHeldErrorNoGrow: when a frame stated larger than the bytes
// present ends exactly where readRest's buffer is full, and the reader
// returns its error with those last bytes (held by read), readRest returns
// ErrTruncated without doubling the buffer first: it allocates as often as
// for a frame ending one byte earlier, mid-buffer.
func TestReadRestHeldErrorNoGrow(t *testing.T) {
	head := make([]byte, 12)
	total := smallFrame + 100_000
	allocs := func(rest int) float64 {
		body := make([]byte, rest)
		return testing.AllocsPerRun(20, func() {
			fr := &frameReader{r: iotest.DataErrReader(bytes.NewReader(body))}
			f, err := fr.readRest(head, total)
			if f != nil || !errors.Is(err, ErrTruncated) {
				t.Fatalf("%d bytes after the prefix: frame %d bytes, error %v; want ErrTruncated", rest, len(f), err)
			}
		})
	}
	full, short := allocs(smallFrame-len(head)), allocs(smallFrame-len(head)-1)
	if full != short {
		t.Errorf("a frame ending at the full buffer allocates %v times, one ending a byte earlier %v", full, short)
	}
}
