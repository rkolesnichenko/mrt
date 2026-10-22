package bmp_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/rkolesnichenko/mrt/bmp"
)

// fuzzMsg builds a version 3 BMP message of type typ around body.
func fuzzMsg(typ byte, body ...byte) []byte {
	h := make([]byte, 6, 6+len(body))
	h[0] = 3
	binary.BigEndian.PutUint32(h[1:], uint32(6+len(body)))
	h[5] = typ
	return append(h, body...)
}

// fuzzPeer is a per-peer header: global instance IPv4 peer 192.0.2.9, AS 64500.
func fuzzPeer() []byte {
	b := make([]byte, 42)
	copy(b[22:26], []byte{192, 0, 2, 9})
	binary.BigEndian.PutUint32(b[26:], 64500)
	copy(b[30:34], []byte{192, 0, 2, 9})
	binary.BigEndian.PutUint32(b[34:], 1700000000)
	return b
}

func FuzzMessages(f *testing.F) {
	marker := bytes.Repeat([]byte{0xff}, 16)
	update := append(append([]byte{}, marker...), 0, 23, 2, 0, 0, 0, 0) // End-of-RIB
	open := append(append([]byte{}, marker...), 0, 29, 1, 4, 0x5b, 0xa0, 0, 90, 192, 0, 2, 1, 0)
	peer := fuzzPeer()
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	msgs := [][]byte{
		fuzzMsg(4, 0, 1, 0, 2, 'h', 'i'),
		fuzzMsg(0, cat(peer, update)...),
		fuzzMsg(1, cat(peer, []byte{0, 0, 0, 1, 0, 0, 0, 4, 0, 0, 0, 7})...),
		fuzzMsg(2, cat(peer, []byte{4})...),
		fuzzMsg(3, cat(peer, make([]byte, 16), []byte{0, 179, 0xc3, 0x50}, open, open)...),
		fuzzMsg(5, 0, 0, 0, 2, 0, 1),
		fuzzMsg(6, cat(peer, []byte{0, 0, 0, 3}, update[:3])...),
		fuzzMsg(99, 1, 2, 3),
		{3, 0, 0, 0, 3, 4},             // Length below the header's own size
		[]byte("OBMP\x00\x01\x00\x00"), // an OpenBMP frame
	}
	var all []byte
	for _, m := range msgs {
		f.Add(m)
		all = append(all, m...)
	}
	f.Add(all)
	f.Add(all[:len(all)/2])
	f.Add(all[:len(all)-1])

	f.Fuzz(func(t *testing.T, in []byte) {
		var raw []byte
		lossy := false
		for m, err := range bmp.MessagesWith(bytes.NewReader(in), bmp.Options{MaxMessageLen: 1 << 12}) {
			if err != nil {
				if errors.Is(err, bmp.ErrTooLarge) || errors.Is(err, bmp.ErrTruncated) {
					lossy = true
				}
				var me *bmp.MessageError
				if errors.As(err, &me) {
					if errors.Is(err, bmp.ErrMalformed) {
						raw = append(raw, me.Raw...)
					}
				} else if !errors.Is(err, bmp.ErrMalformed) {
					t.Fatalf("error %v is not a *MessageError", err)
				}
				continue
			}
			raw = append(raw, m.Raw()...)
			again, perr := bmp.ParseMessage(m.Raw())
			if perr != nil {
				t.Fatalf("Messages yielded a message that ParseMessage refuses: %v", perr)
			}
			if !reflect.DeepEqual(m, again) {
				t.Fatalf("ParseMessage(Raw) = %#v, want %#v", again, m)
			}
		}
		if !lossy && !bytes.Equal(raw, in) {
			t.Fatalf("lossless violated: %d bytes back from %d", len(raw), len(in))
		}
	})
}
