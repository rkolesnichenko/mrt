package mrt

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
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
	Offset int64  // of the record's first byte in the stream
	Header Header // zero when the header was not read completely
	Raw    []byte // nil when the record was over MaxRecordLen and skipped, or truncated
	Err    error  // ErrMalformed, ErrTooLarge, ErrTruncated
}

func (e *RecordError) Error() string {
	return fmt.Sprintf("mrt: record at offset %d: %v", e.Offset, e.Err)
}

func (e *RecordError) Unwrap() error { return e.Err }

// Records reads MRT records from r with default options.
func Records(r io.Reader) iter.Seq2[Record, error] {
	return RecordsWith(r, Options{})
}

// RecordsWith reads MRT records from r. Each record owns its bytes. A
// malformed or oversize record yields a *RecordError and reading continues; a
// truncated stream or a read error yields one error and ends the iteration.
func RecordsWith(r io.Reader, opts Options) iter.Seq2[Record, error] {
	max := opts.MaxRecordLen
	if max == 0 {
		max = DefaultMaxRecordLen
	}
	return func(yield func(Record, error) bool) {
		br, ok := r.(*bufio.Reader)
		if !ok {
			br = bufio.NewReader(r)
		}
		var off int64
		for {
			start := off
			var hb [16]byte
			n, err := io.ReadFull(br, hb[:12])
			off += int64(n)
			if err != nil {
				if n == 0 && err == io.EOF {
					return
				}
				yield(nil, endErr(start, Header{}, err))
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
					n, err := io.ReadFull(br, body[12:])
					off += int64(n)
					h.Time = time.Unix(sec, 0).UTC()
					if err != nil {
						yield(nil, endErr(start, h, err))
						return
					}
					if !yield(nil, &RecordError{Offset: start, Header: h, Raw: body,
						Err: fmt.Errorf("%w: extended-timestamp record shorter than 4 bytes", ErrMalformed)}) {
						return
					}
					continue
				}
				n, err := io.ReadFull(br, hb[12:16])
				off += int64(n)
				if err != nil {
					yield(nil, endErr(start, Header{}, err))
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
			if h.Length > max {
				n, err := io.CopyN(io.Discard, br, bodyLen)
				off += n
				if err != nil {
					yield(nil, endErr(start, h, err))
					return
				}
				if !yield(nil, &RecordError{Offset: start, Header: h, Err: ErrTooLarge}) {
					return
				}
				continue
			}
			raw := make([]byte, hlen+int(bodyLen))
			copy(raw, hb[:hlen])
			n, err = io.ReadFull(br, raw[hlen:])
			off += int64(n)
			if err != nil {
				yield(nil, endErr(start, h, err))
				return
			}
			rec, derr := decodeBody(h, raw)
			if derr != nil {
				if !errors.Is(derr, ErrMalformed) {
					derr = fmt.Errorf("%w: %v", ErrMalformed, derr)
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
