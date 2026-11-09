package bmp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
)

// DefaultMaxMessageLen is the message length limit when Options.MaxMessageLen
// is 0.
const DefaultMaxMessageLen = 1 << 20

// Options configures MessagesWith.
type Options struct {
	MaxMessageLen uint32 // 0 means DefaultMaxMessageLen (1 MiB)
}

// Messages reads BMP messages from r with default options.
func Messages(r io.Reader) iter.Seq2[Message, error] {
	return MessagesWith(r, Options{})
}

// MessagesWith reads BMP messages from r, a TCP capture or a file. Each
// message owns its bytes. A malformed or oversize message yields a
// *MessageError and reading continues; a truncated stream or a read error
// yields one error and ends the iteration.
//
// A Length below 6 cannot hold its own header: the 6 bytes are one malformed
// message and reading goes on after them. So are 6 bytes that begin an
// OpenBMP frame ("OBMP"), which ParseMessage also refuses.
//
// The sequence reads r as it goes: ranging over it again after a break goes on
// with the next message, and once the stream has ended it yields nothing. It
// must not be ranged over by two goroutines at once.
func MessagesWith(r io.Reader, opts Options) iter.Seq2[Message, error] {
	limit := opts.MaxMessageLen
	if limit == 0 {
		limit = DefaultMaxMessageLen
	}
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	s := &stream{br: br}
	var (
		off  int64 // of the next message in the stream
		done bool  // the stream ended, at its end or with an error
	)
	return func(yield func(Message, error) bool) {
		if done {
			return
		}
		// end yields the error that ends the stream, if any.
		end := func(err error) {
			done = true
			if err != nil {
				yield(nil, err)
			}
		}
		// hb escapes to the heap (bufio passes it to the underlying reader), so it is
		// declared once per range, not once per message.
		var hb [commonHeaderLen]byte
		for {
			start := off
			n, err := s.readFull(hb[:])
			off += int64(n)
			if err != nil {
				if n == 0 && err == io.EOF {
					end(nil)
					return
				}
				end(endErr(start, Header{}, err))
				return
			}
			h := parseHeader(hb[:])
			if bytes.HasPrefix(hb[:], openBMPMagic) {
				if !yield(nil, &MessageError{Offset: start, Header: h, Raw: slices.Clone(hb[:]),
					Err: fmt.Errorf("%w: an OpenBMP frame (OBMP header), not a BMP message; unwrap it with openbmp.Parse", ErrMalformed)}) {
					return
				}
				continue
			}
			if h.Length < commonHeaderLen {
				if !yield(nil, &MessageError{Offset: start, Header: h, Raw: slices.Clone(hb[:]),
					Err: fmt.Errorf("%w: length %d is shorter than the common header", ErrMalformed, h.Length)}) {
					return
				}
				continue
			}
			bodyLen := int64(h.Length) - commonHeaderLen
			if h.Length > limit {
				n, err := s.discard(bodyLen)
				off += n
				if err != nil {
					end(endErr(start, h, err))
					return
				}
				if !yield(nil, &MessageError{Offset: start, Header: h, Err: ErrTooLarge}) {
					return
				}
				continue
			}
			raw, got, err := readMessage(s, hb[:], bodyLen)
			off += got
			if err != nil {
				end(endErr(start, h, err))
				return
			}
			msg, derr := decode(h, raw)
			if derr != nil {
				if !errors.Is(derr, ErrMalformed) {
					derr = fmt.Errorf("%w: %w", ErrMalformed, derr)
				}
				if !yield(nil, &MessageError{Offset: start, Header: h, Raw: raw, Err: derr}) {
					return
				}
				continue
			}
			if !yield(msg, nil) {
				return
			}
		}
	}
}

// endErr classifies the error that ended a read: a short read is truncation,
// anything else is the reader's own error.
func endErr(off int64, h Header, err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return &MessageError{Offset: off, Header: h, Err: ErrTruncated}
	}
	return fmt.Errorf("bmp: reading message at offset %d: %w", off, err)
}

// stream is the bufio.Reader that Messages reads, with the error that came
// with the bytes completing a read held for the next read. It and its methods
// are a copy of reader.go's in package mrt.
type stream struct {
	br  *bufio.Reader
	err error
}

// readFull is io.ReadFull, except that an error the reader returns with the
// bytes that complete p is held and returned by the next read, not dropped.
// bufio does the same for the reads it buffers, but hands a read at least as
// long as its buffer straight to the underlying reader and returns that
// reader's error with the bytes, once; io.ReadFull drops it when the bytes
// complete p. So the message those bytes complete is yielded, and the error
// comes after it, whichever way bufio read them.
func (s *stream) readFull(p []byte) (int, error) {
	if s.err != nil && len(p) > 0 {
		err := s.err
		s.err = nil
		return 0, err
	}
	n := 0
	for n < len(p) {
		k, err := s.br.Read(p[n:])
		n += k
		if err != nil {
			if n == len(p) {
				s.err = err
				return n, nil
			}
			if err == io.EOF && n > 0 {
				err = io.ErrUnexpectedEOF
			}
			return n, err
		}
	}
	return n, nil
}

// discard skips n bytes, keeping a reader error as readFull does. io.CopyN
// drops one that comes with the last bytes, and bufio.Reader.Discard refills
// its buffer even while it holds an error, reading past it; Peek does not.
// So each pass fills the buffer with Peek and discards only what it holds.
func (s *stream) discard(n int64) (int64, error) {
	if s.err != nil && n > 0 {
		err := s.err
		s.err = nil
		return 0, err
	}
	var got int64
	for got < n {
		if s.br.Buffered() == 0 {
			if _, err := s.br.Peek(1); err != nil {
				return got, err
			}
		}
		k, _ := s.br.Discard(int(min(n-got, int64(s.br.Buffered()))))
		got += int64(k)
	}
	return got, nil
}

// smallBody is the largest body allocated in one piece before any of it is read.
const smallBody = 64 << 10

// readMessage reads bodyLen bytes after hdr into one slice holding both. It is
// a copy of reader.go:readRecord in package mrt, growth rule included: a body
// up to smallBody is allocated exactly; a larger one doubles as bytes arrive,
// so a header that lies about its length allocates, in total, about four
// times the bytes present, and under five.
// It returns the number of body bytes consumed.
func readMessage(s *stream, hdr []byte, bodyLen int64) ([]byte, int64, error) {
	if bodyLen <= smallBody {
		raw := make([]byte, len(hdr)+int(bodyLen))
		copy(raw, hdr)
		n, err := s.readFull(raw[len(hdr):])
		return raw, int64(n), err
	}
	raw := make([]byte, len(hdr), len(hdr)+smallBody)
	copy(raw, hdr)
	var got int64
	for got < bodyLen {
		chunk := int64(cap(raw) - len(raw))
		if chunk == 0 {
			chunk = int64(len(raw))
		}
		chunk = min(chunk, bodyLen-got)
		raw = slices.Grow(raw, int(chunk))
		n, err := s.readFull(raw[len(raw) : len(raw)+int(chunk)])
		raw = raw[:len(raw)+n]
		got += int64(n)
		if err != nil {
			return raw, got, err
		}
	}
	return raw, got, nil
}
