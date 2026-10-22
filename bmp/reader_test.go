package bmp

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"runtime"
	"testing"
	"testing/iotest"
)

// testStream is one message of each framing-relevant kind, concatenated.
func testStream() (msgs [][]byte, in []byte) {
	peer := defaultPeer().bytes()
	msgs = [][]byte{
		bmpMsg(4, tlv(1, []byte("hello")...)),
		bmpMsg(0, peer, emptyUpdate),
		bmpMsg(2, peer, []byte{4}),
		bmpMsg(3, peer, make([]byte, 16), u16(179), u16(50000), openMsg(), openMsg()),
		bmpMsg(1, peer, u32(1), tlv(0, u32(7)...)),
		bmpMsg(5, tlv(0, u16(1)...)),
		bmpMsg(99, []byte{1, 2, 3}),
	}
	return msgs, cat(msgs...)
}

// readAll ranges over MessagesWith and returns what came back, in order:
// messages, errors, and the concatenation of Raw and of malformed Raw.
func readAll(in []byte, opts Options) (msgs []Message, errs []error, raw []byte) {
	for m, err := range MessagesWith(bytes.NewReader(in), opts) {
		if err != nil {
			errs = append(errs, err)
			var me *MessageError
			if errors.As(err, &me) && errors.Is(err, ErrMalformed) {
				raw = append(raw, me.Raw...)
			}
			continue
		}
		msgs = append(msgs, m)
		raw = append(raw, m.Raw()...)
	}
	return
}

func TestMessagesSequence(t *testing.T) {
	want, in := testStream()
	msgs, errs, _ := readAll(in, Options{})
	if len(errs) != 0 || len(msgs) != len(want) {
		t.Fatalf("%d messages, errors %v; want %d", len(msgs), errs, len(want))
	}
	for i, m := range msgs {
		if !bytes.Equal(m.Raw(), want[i]) {
			t.Errorf("message %d: Raw % x, want % x", i, m.Raw(), want[i])
		}
	}
	// Offsets: a malformed message after the first n shows the offset of byte n.
	for i := range want {
		pre := cat(want[:i]...)
		_, errs, _ := readAll(cat(pre, []byte{3, 0, 0, 0, 2, 0}), Options{})
		var me *MessageError
		if len(errs) != 1 || !errors.As(errs[0], &me) || me.Offset != int64(len(pre)) {
			t.Fatalf("after %d messages: errors %v, want one at offset %d", i, errs, len(pre))
		}
	}
}

func TestMessagesTooLarge(t *testing.T) {
	big := bmpMsg(99, make([]byte, 194)) // 200 bytes
	small := bmpMsg(4, tlv(1, 'x'))
	msgs, errs, _ := readAll(cat(small, big, small), Options{MaxMessageLen: 100})
	var me *MessageError
	if len(errs) != 1 || !errors.As(errs[0], &me) || !errors.Is(errs[0], ErrTooLarge) {
		t.Fatalf("errors %v, want one ErrTooLarge", errs)
	}
	if me.Raw != nil || me.Offset != int64(len(small)) || me.Header.Length != 200 {
		t.Errorf("Raw %v Offset %d Header %+v", me.Raw, me.Offset, me.Header)
	}
	if len(msgs) != 2 || !bytes.Equal(msgs[1].Raw(), small) {
		t.Errorf("%d messages; the one after the large one was not read", len(msgs))
	}
}

func TestMessagesShortLength(t *testing.T) {
	bad := []byte{3, 0, 0, 0, 3, 4}
	next := bmpMsg(4, tlv(1, 'x'))
	msgs, errs, raw := readAll(cat(bad, next), Options{})
	var me *MessageError
	if len(errs) != 1 || !errors.As(errs[0], &me) || !errors.Is(errs[0], ErrMalformed) {
		t.Fatalf("errors %v, want one ErrMalformed", errs)
	}
	if !bytes.Equal(me.Raw, bad) || me.Offset != 0 {
		t.Errorf("Raw % x Offset %d", me.Raw, me.Offset)
	}
	if len(msgs) != 1 || !bytes.Equal(msgs[0].Raw(), next) {
		t.Fatalf("messages %d, want the next one read from byte 6", len(msgs))
	}
	if !bytes.Equal(raw, cat(bad, next)) {
		t.Errorf("not lossless")
	}
}

// The stream reader refuses what ParseMessage refuses.
func TestMessagesAgreeWithParseMessage(t *testing.T) {
	for name, in := range map[string][]byte{
		"length 0":    {3, 0, 0, 0, 0, 0},
		"length 5":    {3, 0, 0, 0, 5, 0},
		"OpenBMP":     cat([]byte("OBMP"), []byte{0, 1, 0, 0, 0, 0, 0, 0}),
		"bad version": cat([]byte{2}, bmpMsg(4, tlv(1, 'x'))[1:]),
	} {
		t.Run(name, func(t *testing.T) {
			_, perr := ParseMessage(in)
			_, errs, _ := readAll(in, Options{})
			if perr == nil || len(errs) == 0 || !errors.Is(perr, ErrMalformed) || !errors.Is(errs[0], ErrMalformed) {
				t.Errorf("ParseMessage error %v, Messages errors %v; both want ErrMalformed", perr, errs)
			}
		})
	}
}

func TestMessagesTruncated(t *testing.T) {
	m := bmpMsg(4, tlv(1, []byte("hello")...))
	for _, cut := range []int{3, 6, 7, len(m) - 1} {
		_, in := testStream()
		in = cat(in, m[:cut])
		msgs, errs, _ := readAll(in, Options{})
		var me *MessageError
		if len(errs) != 1 || !errors.As(errs[0], &me) || !errors.Is(errs[0], ErrTruncated) {
			t.Fatalf("cut %d: errors %v, want one ErrTruncated", cut, errs)
		}
		if me.Raw != nil || len(msgs) != 7 || me.Offset != int64(len(in)-cut) {
			t.Errorf("cut %d: Raw %v, %d messages, Offset %d", cut, me.Raw, len(msgs), me.Offset)
		}
		if (cut < 6) != (me.Header == Header{}) {
			t.Errorf("cut %d: Header %+v", cut, me.Header)
		}
	}
}

var errFoo = errors.New("foo")

func TestMessagesReadError(t *testing.T) {
	_, in := testStream()
	r := io.MultiReader(bytes.NewReader(in[:70]), iotest.ErrReader(errFoo))
	var n int
	var last error
	for _, err := range MessagesWith(r, Options{}) {
		n++
		last = err
	}
	if !errors.Is(last, errFoo) || errors.Is(last, ErrTruncated) {
		t.Errorf("last error %v, want one wrapping errFoo", last)
	}
	if n < 2 {
		t.Errorf("%d results, want messages then the error", n)
	}
}

func TestMessagesBreakAndResume(t *testing.T) {
	want, in := testStream()
	seq := MessagesWith(bytes.NewReader(in), Options{})
	var got [][]byte
	for m := range seq {
		got = append(got, m.Raw())
		if len(got) == 3 {
			break
		}
	}
	for m := range seq {
		got = append(got, m.Raw())
	}
	if !bytes.Equal(cat(got...), cat(want...)) || len(got) != len(want) {
		t.Errorf("resumed range yielded %d messages", len(got))
	}
	for range seq {
		t.Error("yielded after the stream ended")
	}
}

func TestMessagesGarbagePrefix(t *testing.T) {
	_, stream := testStream()
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 200; i++ {
		junk := make([]byte, 1+rng.Intn(50))
		rng.Read(junk)
		in := cat(junk, stream)
		var raw []byte
		lossy := false
		for m, err := range Messages(bytes.NewReader(in)) {
			if err != nil {
				if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrTruncated) {
					lossy = true
				}
				var me *MessageError
				if errors.As(err, &me) {
					raw = append(raw, me.Raw...)
				}
				continue
			}
			raw = append(raw, m.Raw()...)
		}
		if !lossy && !bytes.Equal(raw, in) {
			t.Fatalf("junk % x: concatenation differs from the input", junk)
		}
	}
}

// countReader counts the bytes read from it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestMessagesConsumesEverything(t *testing.T) {
	_, stream := testStream()
	junk := []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7}
	in := cat(junk, stream)
	cr := &countReader{r: bytes.NewReader(in)}
	for range Messages(cr) {
	}
	if cr.n != int64(len(in)) {
		t.Errorf("read %d of %d bytes", cr.n, len(in))
	}
}

// A header that claims 1 MiB over 70 KiB present allocates under five times
// the bytes present (the readRecord growth rule), and ends in ErrTruncated.
func TestMessagesBound(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector inflates TotalAlloc")
	}
	const present = 70 << 10
	in := cat([]byte{3, 0, 0x10, 0, 0, 99}, make([]byte, present)) // Length 1 MiB
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	msgs, errs, _ := readAll(in, Options{})
	runtime.ReadMemStats(&after)
	if len(msgs) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
		t.Fatalf("messages %d, errors %v", len(msgs), errs)
	}
	d := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d bytes present: allocated %d (%.2fx)", present, d, float64(d)/present)
	if d >= 5*present {
		t.Errorf("allocated %d bytes for %d present", d, present)
	}
}

func TestMessagesLossless(t *testing.T) {
	_, stream := testStream()
	cases := map[string][]byte{
		"valid":            stream,
		"short length":     cat(stream, []byte{3, 0, 0, 0, 1, 4}, stream),
		"bad version":      cat(stream, []byte{9, 0, 0, 0, 8, 4, 1, 2}, stream),
		"OpenBMP":          cat(stream, []byte("OBMP\x00\x01"), stream),
		"peer-less":        cat(stream, bmpMsg(0, []byte{1, 2, 3}), stream),
		"short stats":      cat(bmpMsg(1, defaultPeer().bytes(), []byte{0, 0}), stream),
		"all bad messages": cat([]byte{3, 0, 0, 0, 0, 0}, []byte{3, 0, 0, 0, 5, 0}),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, errs, raw := readAll(in, Options{})
			if name != "valid" && len(errs) == 0 {
				t.Error("no malformed message was reported")
			}
			if !bytes.Equal(raw, in) {
				t.Errorf("concatenation of Raw differs from the input (%d vs %d bytes)", len(raw), len(in))
			}
		})
	}
}
