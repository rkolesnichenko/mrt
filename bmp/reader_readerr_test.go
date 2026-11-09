package bmp

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"
	"testing/iotest"
)

// errAt reads r and returns err with the bytes that reach offset at; sticky,
// it returns err again on every later read, otherwise it goes on reading r.
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

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) { return o.r.Read(p[:min(1, len(p))]) }

// readerErrBodies are body sizes around bufio's 4 KiB buffer, which hands a
// read at least that long straight to the underlying reader, and around
// smallBody, where readMessage starts to grow its buffer.
var readerErrBodies = []int{100, 5000, 10000, 70000, smallBody + 100, 300 << 10}

// randBody is n bytes that gzip cannot squeeze to nothing.
func randBody(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func rangeMessages(r io.Reader, o Options) (msgs int, errs []error) {
	for _, err := range MessagesWith(r, o) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		msgs++
	}
	return
}

// TestMessagesReaderError: a reader error ends the sequence with that error,
// whatever the message size, whether or not the reader returns it again, and
// whether Messages makes its own bufio.Reader or is handed one. Every message
// whose bytes came before the error, or with it, is yielded first, as bufio
// does for the reads it buffers: it holds the error for the next read. So is
// ErrTooLarge for each message skipped as too large. A 6-byte header read is
// shorter than any bufio buffer, so the header cases pin behaviour that was
// already right; the message cases are the ones a dropped error fails.
func TestMessagesReaderError(t *testing.T) {
	boom := errors.New("boom")
	for _, size := range readerErrBodies {
		msg := bmpMsg(99, randBody(size, int64(size)))
		in := cat(msg, msg)
		for _, c := range []struct {
			name string
			at   int
			msgs int
		}{
			{"first header complete", commonHeaderLen, 0},
			{"first message complete", len(msg), 1},
			{"second header complete", len(msg) + commonHeaderLen, 1},
			{"second message complete", 2 * len(msg), 2},
		} {
			for _, sticky := range []bool{true, false} {
				for _, bufSize := range []int{0, 16, 64 << 10} {
					var r io.Reader = &errAt{r: bytes.NewReader(in), at: int64(c.at), err: boom, sticky: sticky}
					if bufSize != 0 {
						r = bufio.NewReaderSize(r, bufSize)
					}
					name := fmt.Sprintf("%d-byte body, error at %s (byte %d), sticky %v, bufio %d", size, c.name, c.at, sticky, bufSize)
					msgs, errs := rangeMessages(r, Options{})
					if msgs != c.msgs || len(errs) != 1 || !errors.Is(errs[0], boom) {
						t.Errorf("%s: %d messages, errors %v; want %d messages, then only %v", name, msgs, errs, c.msgs, boom)
					}
					// Too large: both messages are skipped.
					r = &errAt{r: bytes.NewReader(in), at: int64(c.at), err: boom, sticky: sticky}
					if bufSize != 0 {
						r = bufio.NewReaderSize(r, bufSize)
					}
					_, errs = rangeMessages(r, Options{MaxMessageLen: 64})
					large := c.msgs // ErrTooLarge messages before the error
					if len(errs) != large+1 || !errors.Is(errs[len(errs)-1], boom) {
						t.Errorf("%s, too large: errors %v; want %d ErrTooLarge, then %v", name, errs, large, boom)
						continue
					}
					for _, err := range errs[:large] {
						if !errors.Is(err, ErrTooLarge) {
							t.Errorf("%s, too large: errors %v; want %d ErrTooLarge, then %v", name, errs, large, boom)
						}
					}
				}
			}
		}
	}
}

// TestMessagesReaderErrorResume: a range that breaks after the message whose
// last bytes came with the error, ranged again, yields the error once; a
// third range yields nothing.
func TestMessagesReaderErrorResume(t *testing.T) {
	boom := errors.New("boom")
	msg := bmpMsg(99, randBody(10000, 1))
	seq := Messages(&errAt{r: bytes.NewReader(cat(msg, msg)), at: int64(len(msg)), err: boom})
	first := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("first range: %v", err)
		}
		first++
		break
	}
	msgs := 0
	var errs []error
	for _, err := range seq {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		msgs++
	}
	third := 0
	for range seq {
		third++
	}
	if first != 1 || msgs != 0 || len(errs) != 1 || !errors.Is(errs[0], boom) || third != 0 {
		t.Errorf("first range %d messages; second %d messages, errors %v; third %d results; want 1, then 0 and only %v, then 0", first, msgs, errs, third, boom)
	}
}

// TestMessagesDataEOF: readers that return io.EOF or io.ErrUnexpectedEOF
// with their last bytes give the same messages and errors as a plain reader.
// The last of these is how compress/gzip ends a cut file.
func TestMessagesDataEOF(t *testing.T) {
	for _, size := range readerErrBodies {
		msg := bmpMsg(99, randBody(size, int64(size)))
		in := cat(msg, msg)
		for _, c := range []struct {
			name string
			r    io.Reader
			want int
		}{
			{"data with EOF", iotest.DataErrReader(bytes.NewReader(in)), 2},
			{"one byte with EOF", iotest.DataErrReader(oneByte{bytes.NewReader(in)}), 2},
			{"sticky EOF at the first message's end", &errAt{r: bytes.NewReader(in[:len(msg)]), at: int64(len(msg)), err: io.EOF, sticky: true}, 1},
		} {
			if msgs, errs := rangeMessages(c.r, Options{}); msgs != c.want || len(errs) != 0 {
				t.Errorf("%d-byte body, %s: %d messages, errors %v; want %d messages, no error", size, c.name, msgs, errs, c.want)
			}
		}
		// io.ErrUnexpectedEOF with the first message's last bytes: that
		// message, then ErrTruncated at the second.
		r := &errAt{r: bytes.NewReader(in), at: int64(len(msg)), err: io.ErrUnexpectedEOF, sticky: true}
		msgs, errs := rangeMessages(r, Options{})
		var me *MessageError
		if msgs != 1 || len(errs) != 1 || !errors.As(errs[0], &me) || !errors.Is(errs[0], ErrTruncated) || me.Offset != int64(len(msg)) {
			t.Errorf("%d-byte body, io.ErrUnexpectedEOF at the first message's end: %d messages, errors %v; want 1 message, then ErrTruncated at %d", size, msgs, errs, len(msg))
		}
	}
}

// TestMessagesCutGzip: a gzip file cut anywhere yields every message whose
// bytes gzip still delivers, then one ErrTruncated.
func TestMessagesCutGzip(t *testing.T) {
	var msgs [][]byte
	for i, size := range readerErrBodies {
		msgs = append(msgs, bmpMsg(99, randBody(size, int64(i))))
	}
	in := cat(msgs...)
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(in)
	zw.Close()
	gz := z.Bytes()
	for cut := 20; cut < len(gz); cut += len(gz) / 37 {
		zr, err := gzip.NewReader(bytes.NewReader(gz[:cut]))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(zr)
		whole := 0
		for n := 0; whole < len(msgs) && n+len(msgs[whole]) <= len(got); whole++ {
			n += len(msgs[whole])
		}
		zr, _ = gzip.NewReader(bytes.NewReader(gz[:cut]))
		n, errs := rangeMessages(zr, Options{})
		if n != whole || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
			t.Errorf("gzip cut at %d of %d (%d bytes out): %d messages, errors %v; want %d messages, then ErrTruncated", cut, len(gz), len(got), n, errs, whole)
		}
	}
}
