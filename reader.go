package mrt

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"
	"time"
)

// DefaultMaxRecordLen is the record length limit when Options.MaxRecordLen is 0.
const DefaultMaxRecordLen = 16 << 20

var (
	ErrTruncated = errors.New("mrt: truncated record")
	ErrMalformed = errors.New("mrt: malformed record")
	ErrTooLarge  = errors.New("mrt: record longer than MaxRecordLen")
)

// Options configures RecordsWith.
type Options struct {
	MaxRecordLen uint32 // 0 means DefaultMaxRecordLen (16 MiB)
}

// RecordError describes a record that could not be returned. Reading
// continues after it, except for ErrTruncated and read errors.
type RecordError struct {
	Offset int64  // of the record's first byte in the stream; -1 when not known (from Routes)
	Header Header // zero when the header was not read completely
	Raw    []byte // nil when the record was over MaxRecordLen and skipped, or truncated
	Err    error  // ErrMalformed, ErrTooLarge, ErrTruncated
}

// Error says "mrt: record at offset N: cause", or "mrt: record (offset
// unknown): cause" when Offset is negative (as from Routes), with the cause's
// own "mrt: " prefix dropped.
func (e *RecordError) Error() string {
	cause := strings.TrimPrefix(fmt.Sprint(e.Err), "mrt: ")
	if e.Offset < 0 {
		return "mrt: record (offset unknown): " + cause
	}
	return fmt.Sprintf("mrt: record at offset %d: %s", e.Offset, cause)
}

func (e *RecordError) Unwrap() error { return e.Err }

// Records reads MRT records from r with default options.
func Records(r io.Reader) iter.Seq2[Record, error] {
	return RecordsWith(r, Options{})
}

// RecordsWith reads MRT records from r. Each record owns its bytes. A
// malformed or oversize record yields a *RecordError and reading continues; a
// truncated stream or a read error yields one error and ends the iteration.
//
// The sequence reads r as it goes: ranging over it again after a break goes on
// with the next record, and once the stream has ended it yields nothing. It
// must not be ranged over by two goroutines at once.
func RecordsWith(r io.Reader, opts Options) iter.Seq2[Record, error] {
	limit := opts.MaxRecordLen
	if limit == 0 {
		limit = DefaultMaxRecordLen
	}
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	s := &stream{br: br}
	var (
		off  int64 // of the next record in the stream
		done bool  // the stream ended, at its end or with an error
	)
	return func(yield func(Record, error) bool) {
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
		// declared once per range, not once per record. Each record reads
		// only the bytes it has just written.
		var hb [16]byte
		for {
			start := off
			n, err := s.readFull(hb[:12])
			off += int64(n)
			if err != nil {
				if n == 0 && err == io.EOF {
					end(nil)
					return
				}
				end(endErr(start, Header{}, err))
				return
			}
			typ := Type(binary.BigEndian.Uint16(hb[4:]))
			h := Header{
				Type:    typ,
				Subtype: Subtype(binary.BigEndian.Uint16(hb[6:])),
				Length:  binary.BigEndian.Uint32(hb[8:]),
			}
			sec := int64(binary.BigEndian.Uint32(hb[0:]))
			var usec int64
			hlen := 12
			if typ.isET() {
				if h.Length < 4 {
					// The Length bytes are skipped; they are the record's body.
					body := make([]byte, 12+int(h.Length))
					copy(body, hb[:12])
					n, err := s.readFull(body[12:])
					off += int64(n)
					h.Time = time.Unix(sec, 0).UTC()
					if err != nil {
						end(endErr(start, h, err))
						return
					}
					if !yield(nil, &RecordError{Offset: start, Header: h, Raw: body,
						Err: fmt.Errorf("%w: extended-timestamp record shorter than 4 bytes", ErrMalformed)}) {
						return
					}
					continue
				}
				n, err := s.readFull(hb[12:16])
				off += int64(n)
				if err != nil {
					end(endErr(start, Header{}, err))
					return
				}
				usec = int64(binary.BigEndian.Uint32(hb[12:]))
				hlen = 16
			}
			h.Time = time.Unix(sec, usec*1000).UTC()
			bodyLen := int64(h.Length)
			if hlen == 16 {
				bodyLen -= 4
			}
			if h.Length > limit {
				n, err := s.discard(bodyLen)
				off += n
				if err != nil {
					end(endErr(start, h, err))
					return
				}
				if !yield(nil, &RecordError{Offset: start, Header: h, Err: ErrTooLarge}) {
					return
				}
				continue
			}
			raw, got, err := readRecord(s, hb[:hlen], bodyLen)
			off += got
			if err != nil {
				end(endErr(start, h, err))
				return
			}
			rec, derr := decodeBody(h, raw)
			if derr != nil {
				if !errors.Is(derr, ErrMalformed) {
					derr = fmt.Errorf("%w: %w", ErrMalformed, derr)
				}
				if !yield(nil, &RecordError{Offset: start, Header: h, Raw: raw, Err: derr}) {
					return
				}
				continue
			}
			if !yield(rec, nil) {
				return
			}
		}
	}
}

// endErr classifies the error that ended a read: a short read is truncation,
// anything else is the reader's own error.
func endErr(off int64, h Header, err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return &RecordError{Offset: off, Header: h, Err: ErrTruncated}
	}
	return fmt.Errorf("mrt: reading record at offset %d: %w", off, err)
}

// stream is the bufio.Reader that Records reads, with the error that came
// with the bytes completing a read held for the next read.
// bmp/reader.go has a copy of it and its methods; change both, and
// openbmp's frameReader, which holds errors the same way without bufio.
type stream struct {
	br  *bufio.Reader
	err error
}

// readFull is io.ReadFull, except that an error the reader returns with the
// bytes that complete p is held and returned by the next read, not dropped.
// bufio does the same for the reads it buffers, but when its buffer is empty
// it hands a read at least as long as the buffer straight to the underlying
// reader and returns that reader's error with the bytes, once; io.ReadFull
// drops it when the bytes complete p. So the record those bytes complete is
// yielded, and the error comes after it, whichever way bufio read them.
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

// end returns the error the next read would return with no bytes, or nil if
// there are bytes to read: the error held from the read that filled the
// last buffer, else the reader's own end, which Peek finds without reading
// past an error. readRecord calls it before growing its buffer, so that a body
// ending where the buffer is full is not given room it will not fill.
func (s *stream) end() error {
	if s.err != nil {
		err := s.err
		s.err = nil
		return err
	}
	_, err := s.br.Peek(1)
	return err
}

// smallBody is the largest body allocated in one piece before any of it is read.
const smallBody = 64 << 10

// readRecord reads bodyLen bytes after hdr into one slice holding both. A body
// up to smallBody is allocated exactly; a larger one doubles as bytes arrive,
// so a header that lies about its length allocates, in total, about four
// times the bytes present, and under five. A body that ends where the buffer
// is full is returned without growing it once more (stream.end).
// It returns the number of body bytes consumed.
func readRecord(s *stream, hdr []byte, bodyLen int64) ([]byte, int64, error) {
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
			if err := s.end(); err != nil {
				return raw, got, err
			}
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
