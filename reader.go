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
func RecordsWith(r io.Reader, opts Options) iter.Seq2[Record, error] {
	limit := opts.MaxRecordLen
	if limit == 0 {
		limit = DefaultMaxRecordLen
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
			if h.Length > limit {
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
			raw, got, err := readRecord(br, hb[:hlen], bodyLen)
			off += got
			if err != nil {
				yield(nil, endErr(start, h, err))
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

// smallBody is the largest body allocated in one piece before any of it is read.
const smallBody = 64 << 10

// readRecord reads bodyLen bytes after hdr into one slice holding both. A body
// up to smallBody is allocated exactly; a larger one grows as bytes arrive, so
// a header that lies about its length costs about twice the bytes present.
// It returns the number of body bytes consumed.
func readRecord(br *bufio.Reader, hdr []byte, bodyLen int64) ([]byte, int64, error) {
	if bodyLen <= smallBody {
		raw := make([]byte, len(hdr)+int(bodyLen))
		copy(raw, hdr)
		n, err := io.ReadFull(br, raw[len(hdr):])
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
		n, err := io.ReadFull(br, raw[len(raw):len(raw)+int(chunk)])
		raw = raw[:len(raw)+n]
		got += int64(n)
		if err != nil {
			return raw, got, err
		}
	}
	return raw, got, nil
}
