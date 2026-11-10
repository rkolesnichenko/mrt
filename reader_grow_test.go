package mrt

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// TestReadRecordGrowsOnlyForBytes: when a body stated larger than the bytes
// present ends exactly where readRecord's buffer is full, readRecord returns the
// end without growing the buffer first: its capacity is the one it has for
// a body ending a byte earlier, mid-buffer. That holds whether the reader
// returns io.EOF with the last bytes or on the next Read (bufio keeps it
// either way, and Peek finds it), and an error of the reader's own that comes
// with the last bytes, held by readFull, is the one returned, not an io.EOF
// read past it. The rows end at the first full buffer and at the
// second, whose size the allocator's size classes decide, so it is read
// from readRecord itself.
func TestReadRecordGrowsOnlyForBytes(t *testing.T) {
	hdr := make([]byte, 12)
	errOwn := errors.New("the reader's own error")
	read := func(present int, mode string) ([]byte, int64, error) {
		body := bytes.Repeat([]byte{0xa5}, present)
		var r io.Reader = bytes.NewReader(body)
		switch mode {
		case "io.EOF with the last bytes":
			r = iotest.DataErrReader(r)
		case "held own error":
			r = &lastBytesErr{r: bytes.NewReader(body), err: errOwn}
		}
		raw, got, err := readRecord(&stream{br: bufio.NewReader(r)}, hdr, int64(present)+300_000)
		ok := err == io.EOF || err == io.ErrUnexpectedEOF
		if mode == "held own error" {
			ok = err == errOwn
		}
		if !ok || got != int64(present) || !bytes.Equal(raw[len(hdr):], body) {
			t.Fatalf("%d bytes, %s: got %d, error %v; want %d", present, mode, got, err, present)
		}
		return raw, got, err
	}
	// One byte past the first full buffer: the capacity of the second.
	second, _, _ := read(smallBody+1, "io.EOF on the next read")
	if cap(second) <= len(hdr)+smallBody {
		t.Fatalf("capacity %d after the first full buffer, want more than %d", cap(second), len(hdr)+smallBody)
	}
	for _, present := range []int{smallBody, cap(second) - len(hdr)} {
		for _, mode := range []string{"io.EOF with the last bytes", "io.EOF on the next read", "held own error"} {
			full, _, _ := read(present, mode)
			short, _, _ := read(present-1, mode)
			if cap(full) != cap(short) {
				t.Errorf("%d bytes, %s: capacity %d, and %d for a byte fewer", present, mode, cap(full), cap(short))
			}
		}
	}
}

// lastBytesErr returns err with the last bytes of r, and io.EOF after.
type lastBytesErr struct {
	r   *bytes.Reader
	err error
}

func (l *lastBytesErr) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 && l.r.Len() == 0 {
		return n, l.err
	}
	return n, err
}
