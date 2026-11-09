package mrt

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
// smallBody, where readRecord starts to grow its buffer.
var readerErrBodies = []int{100, 5000, 10000, 70000, smallBody + 100, 300 << 10}

// randBody is n bytes that gzip cannot squeeze to nothing.
func randBody(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func rangeRecords(r io.Reader, o Options) (recs int, errs []error) {
	for _, err := range RecordsWith(r, o) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs++
	}
	return
}

// TestRecordsReaderError: a reader error ends the sequence with that error,
// whatever the record size, whether or not the reader returns it again, and
// whether Records makes its own bufio.Reader or is handed one. Every record
// whose bytes came before the error, or with it, is yielded first, as bufio
// does for the reads it buffers: it holds the error for the next read. So is
// ErrTooLarge for each record skipped as too large. A 12-byte header read is
// shorter than any bufio buffer, so the header cases pin behaviour that was
// already right; the record cases are the ones a dropped error fails.
func TestRecordsReaderError(t *testing.T) {
	boom := errors.New("boom")
	for _, size := range readerErrBodies {
		rec := frame(1, TypeOSPFv2, 1, 0, randBody(size, int64(size)))
		in := bytes.Join([][]byte{rec, rec}, nil)
		for _, c := range []struct {
			name string
			at   int
			recs int
		}{
			{"first header complete", 12, 0},
			{"first record complete", len(rec), 1},
			{"second header complete", len(rec) + 12, 1},
			{"second record complete", 2 * len(rec), 2},
		} {
			for _, sticky := range []bool{true, false} {
				for _, bufSize := range []int{0, 16, 64 << 10} {
					var r io.Reader = &errAt{r: bytes.NewReader(in), at: int64(c.at), err: boom, sticky: sticky}
					if bufSize != 0 {
						r = bufio.NewReaderSize(r, bufSize)
					}
					name := fmt.Sprintf("%d-byte body, error at %s (byte %d), sticky %v, bufio %d", size, c.name, c.at, sticky, bufSize)
					recs, errs := rangeRecords(r, Options{})
					if recs != c.recs || len(errs) != 1 || !errors.Is(errs[0], boom) {
						t.Errorf("%s: %d records, errors %v; want %d records, then only %v", name, recs, errs, c.recs, boom)
					}
					// Too large: both records are skipped.
					r = &errAt{r: bytes.NewReader(in), at: int64(c.at), err: boom, sticky: sticky}
					if bufSize != 0 {
						r = bufio.NewReaderSize(r, bufSize)
					}
					_, errs = rangeRecords(r, Options{MaxRecordLen: 64})
					large := c.recs // ErrTooLarge records before the error
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

// TestRecordsReaderErrorET: the same for extended-timestamp records, whose
// header is read in two parts.
func TestRecordsReaderErrorET(t *testing.T) {
	boom := errors.New("boom")
	for _, size := range []int{10000, 70000} {
		rec := frame(1, TypeISISET, 1, 5, randBody(size, int64(size)))
		in := bytes.Join([][]byte{rec, rec}, nil)
		for _, at := range []int{len(rec), 2 * len(rec)} {
			r := &errAt{r: bytes.NewReader(in), at: int64(at), err: boom}
			recs, errs := rangeRecords(r, Options{})
			if want := at / len(rec); recs != want || len(errs) != 1 || !errors.Is(errs[0], boom) {
				t.Errorf("%d-byte ET body, error at byte %d: %d records, errors %v; want %d records, then only %v", size, at, recs, errs, want, boom)
			}
		}
	}
}

// TestRecordsReaderErrorResume: a range that breaks after the record whose
// last bytes came with the error, ranged again, yields the error once; a
// third range yields nothing.
func TestRecordsReaderErrorResume(t *testing.T) {
	boom := errors.New("boom")
	rec := frame(1, TypeOSPFv2, 1, 0, randBody(10000, 1))
	seq := Records(&errAt{r: bytes.NewReader(bytes.Join([][]byte{rec, rec}, nil)), at: int64(len(rec)), err: boom})
	first := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("first range: %v", err)
		}
		first++
		break
	}
	recs := 0
	var errs []error
	for _, err := range seq {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs++
	}
	third := 0
	for range seq {
		third++
	}
	if first != 1 || recs != 0 || len(errs) != 1 || !errors.Is(errs[0], boom) || third != 0 {
		t.Errorf("first range %d records; second %d records, errors %v; third %d results; want 1, then 0 and only %v, then 0", first, recs, errs, third, boom)
	}
}

// TestRecordsDataEOF: readers that return io.EOF or io.ErrUnexpectedEOF with
// their last bytes give the same records and errors as a plain reader. The
// last of these is how compress/gzip ends a cut file.
func TestRecordsDataEOF(t *testing.T) {
	for _, size := range readerErrBodies {
		rec := frame(1, TypeOSPFv2, 1, 0, randBody(size, int64(size)))
		in := bytes.Join([][]byte{rec, rec}, nil)
		for _, c := range []struct {
			name string
			r    io.Reader
		}{
			{"data with EOF", iotest.DataErrReader(bytes.NewReader(in))},
			{"one byte with EOF", iotest.DataErrReader(oneByte{bytes.NewReader(in)})},
			{"sticky EOF at the first record's end", &errAt{r: bytes.NewReader(in[:len(rec)]), at: int64(len(rec)), err: io.EOF, sticky: true}},
		} {
			want := 2
			if c.name == "sticky EOF at the first record's end" {
				want = 1
			}
			if recs, errs := rangeRecords(c.r, Options{}); recs != want || len(errs) != 0 {
				t.Errorf("%d-byte body, %s: %d records, errors %v; want %d records, no error", size, c.name, recs, errs, want)
			}
		}
		// io.ErrUnexpectedEOF with the first record's last bytes: that record,
		// then ErrTruncated at the second.
		r := &errAt{r: bytes.NewReader(in), at: int64(len(rec)), err: io.ErrUnexpectedEOF, sticky: true}
		recs, errs := rangeRecords(r, Options{})
		var re *RecordError
		if recs != 1 || len(errs) != 1 || !errors.As(errs[0], &re) || !errors.Is(errs[0], ErrTruncated) || re.Offset != int64(len(rec)) {
			t.Errorf("%d-byte body, io.ErrUnexpectedEOF at the first record's end: %d records, errors %v; want 1 record, then ErrTruncated at %d", size, recs, errs, len(rec))
		}
	}
}

// TestRecordsCutGzip: a gzip file cut anywhere yields every record whose
// bytes gzip still delivers, then one ErrTruncated.
func TestRecordsCutGzip(t *testing.T) {
	var recs [][]byte
	for i, size := range readerErrBodies {
		recs = append(recs, frame(uint32(i), TypeOSPFv2, 1, 0, randBody(size, int64(i))))
	}
	in := bytes.Join(recs, nil)
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
		for n := 0; whole < len(recs) && n+len(recs[whole]) <= len(got); whole++ {
			n += len(recs[whole])
		}
		zr, _ = gzip.NewReader(bytes.NewReader(gz[:cut]))
		n, errs := rangeRecords(zr, Options{})
		if n != whole || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
			t.Errorf("gzip cut at %d of %d (%d bytes out): %d records, errors %v; want %d records, then ErrTruncated", cut, len(gz), len(got), n, errs, whole)
		}
	}
}
