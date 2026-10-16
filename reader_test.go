package mrt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"
)

// frame builds one record; for ET types the microseconds are part of Length.
func frame(sec uint32, typ Type, sub Subtype, usec uint32, body []byte) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint32(b, sec)
	b = binary.BigEndian.AppendUint16(b, uint16(typ))
	b = binary.BigEndian.AppendUint16(b, uint16(sub))
	if typ.isET() {
		b = binary.BigEndian.AppendUint32(b, uint32(len(body))+4)
		b = binary.BigEndian.AppendUint32(b, usec)
	} else {
		b = binary.BigEndian.AppendUint32(b, uint32(len(body)))
	}
	return append(b, body...)
}

func collect(t *testing.T, in []byte, o Options) (recs []Record, errs []error) {
	t.Helper()
	for rec, err := range RecordsWith(bytes.NewReader(in), o) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		recs = append(recs, rec)
	}
	return
}

func TestRecordsET(t *testing.T) {
	a := frame(1700000000, TypeBGP4MPET, SubMessageAS4, 123456, []byte{1, 2, 3})
	b := frame(1700000001, TypeTableDumpV2, SubGeoPeerTable, 0, []byte{9})
	recs, errs := collect(t, append(append([]byte{}, a...), b...), Options{})
	if len(errs) != 0 || len(recs) != 2 {
		t.Fatalf("recs=%d errs=%v", len(recs), errs)
	}
	h := recs[0].Header()
	if want := time.Unix(1700000000, 123456000); !h.Time.Equal(want) || h.Time.Location() != time.UTC {
		t.Errorf("Time = %v, want %v UTC", h.Time, want)
	}
	if h.Type != TypeBGP4MPET || h.Subtype != SubMessageAS4 || h.Length != 7 {
		t.Errorf("header = %+v", h)
	}
	if !bytes.Equal(recs[0].Raw(), a) || len(recs[0].Raw()) != 16+3 {
		t.Errorf("Raw = %x", recs[0].Raw())
	}
	if !bytes.Equal(recs[1].Raw(), b) || recs[1].Header().Time.Nanosecond() != 0 {
		t.Errorf("second record wrong: %x", recs[1].Raw())
	}
	if _, ok := recs[0].(Other); !ok {
		t.Errorf("type %T, want Other", recs[0])
	}
}

func TestRecordsLossless(t *testing.T) {
	var in []byte
	in = append(in, frame(1, TypeOSPFv2, 0, 0, []byte("abc"))...)
	in = append(in, frame(2, TypeBGP4MPET, 1, 7, nil)...)
	in = append(in, frame(3, Type(999), 5, 0, bytes.Repeat([]byte{7}, 100))...)
	recs, errs := collect(t, in, Options{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	var out []byte
	for _, r := range recs {
		out = append(out, r.Raw()...)
	}
	if !bytes.Equal(in, out) {
		t.Error("concatenated Raw differs from input")
	}
}

func TestRecordsTooLargeSkippedWithoutBuffering(t *testing.T) {
	big := frame(1, TypeTableDumpV2, SubGeoPeerTable, 0, make([]byte, 1<<20))
	ok := frame(2, TypeTableDumpV2, SubGeoPeerTable, 0, []byte{1, 2})
	in := append(append([]byte{}, big...), ok...)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var recs []Record
	var errs []error
	for rec, err := range RecordsWith(bytes.NewReader(in), Options{MaxRecordLen: 64}) {
		if err != nil {
			errs = append(errs, err)
		} else {
			recs = append(recs, rec)
		}
	}
	runtime.ReadMemStats(&after)
	if len(errs) != 1 || len(recs) != 1 {
		t.Fatalf("errs=%v recs=%d", errs, len(recs))
	}
	var re *RecordError
	if !errors.As(errs[0], &re) || !errors.Is(errs[0], ErrTooLarge) || re.Raw != nil || re.Offset != 0 {
		t.Errorf("error = %#v", errs[0])
	}
	if re.Header.Length != 1<<20 {
		t.Errorf("Header.Length = %d", re.Header.Length)
	}
	if !bytes.Equal(recs[0].Raw(), ok) {
		t.Error("record after the large one is wrong")
	}
	if d := after.TotalAlloc - before.TotalAlloc; d >= 128<<10 {
		t.Errorf("allocated %d bytes; the body was buffered", d)
	}
}

func TestRecordsTooLargeET(t *testing.T) {
	big := frame(1, TypeBGP4MPET, 1, 5, make([]byte, 200))
	ok := frame(2, TypeBGP4MP, 1, 0, []byte{1})
	recs, errs := collect(t, append(append([]byte{}, big...), ok...), Options{MaxRecordLen: 64})
	if len(errs) != 1 || !errors.Is(errs[0], ErrTooLarge) || len(recs) != 1 {
		t.Fatalf("errs=%v recs=%d", errs, len(recs))
	}
}

func TestRecordsTruncated(t *testing.T) {
	in := append(frame(1, TypeBGP4MP, 1, 0, []byte{1}), 0, 0, 0, 1, 0, 16, 0, 1, 0, 0, 0, 100)
	in = append(in, make([]byte, 40)...)
	recs, errs := collect(t, in, Options{})
	if len(recs) != 1 || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
		t.Fatalf("recs=%d errs=%v", len(recs), errs)
	}
	var re *RecordError
	if !errors.As(errs[0], &re) || re.Offset != 13 || re.Raw != nil || re.Header.Length != 100 {
		t.Errorf("error = %#v", errs[0])
	}
}

func TestRecordsEmptyAndShort(t *testing.T) {
	if recs, errs := collect(t, nil, Options{}); len(recs)+len(errs) != 0 {
		t.Errorf("empty input: %d recs, %v", len(recs), errs)
	}
	recs, errs := collect(t, make([]byte, 11), Options{})
	if len(recs) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
		t.Errorf("11 bytes: %d recs, %v", len(recs), errs)
	}
	// ET header cut inside the microsecond field.
	et := frame(1, TypeBGP4MPET, 1, 0, nil)[:14]
	recs, errs = collect(t, et, Options{})
	if len(recs) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
		t.Errorf("14-byte ET: %d recs, %v", len(recs), errs)
	}
}

func TestRecordsETShortLength(t *testing.T) {
	bad := []byte{0, 0, 0, 1, 0, 17, 0, 1, 0, 0, 0, 2, 0xAA, 0xBB} // Length 2
	ok := frame(2, TypeBGP4MP, 1, 0, []byte{1})
	in := append(append([]byte{}, bad...), ok...)
	recs, errs := collect(t, in, Options{})
	if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) || len(recs) != 1 {
		t.Fatalf("errs=%v recs=%d", errs, len(recs))
	}
	var re *RecordError
	if !errors.As(errs[0], &re) || !bytes.Equal(re.Raw, bad) {
		t.Errorf("error = %#v", errs[0])
	}
	if recs[0].Header().Time.Unix() != 2 {
		t.Error("record after the malformed one was not read")
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRecordsReadError(t *testing.T) {
	boom := errors.New("boom")
	n := 0
	for _, err := range Records(io.MultiReader(bytes.NewReader(frame(1, TypeBGP4MP, 1, 0, nil)), errReader{boom})) {
		n++
		if n == 2 && (!errors.Is(err, boom) || errors.Is(err, ErrTruncated)) {
			t.Errorf("err = %v", err)
		}
	}
	if n != 2 {
		t.Errorf("iterations = %d", n)
	}
}

func TestRecordsStopEarly(t *testing.T) {
	ok := frame(9, TypeBGP4MP, 1, 0, []byte{1})
	cases := map[string][]byte{
		"too large": append(frame(1, TypeBGP4MP, 1, 0, make([]byte, 100)), ok...),
		"ET short":  append([]byte{0, 0, 0, 1, 0, 17, 0, 1, 0, 0, 0, 2, 0xAA, 0xBB}, ok...),
		"record":    append(append([]byte{}, ok...), ok...),
	}
	for name, in := range cases {
		n := 0
		for range RecordsWith(bytes.NewReader(in), Options{MaxRecordLen: 64}) {
			n++
			break
		}
		if n != 1 {
			t.Errorf("%s: iterations = %d", name, n)
		}
	}
}

func TestRecordsLyingLengthBounded(t *testing.T) {
	in := append([]byte{0, 0, 0, 1, 0, 16, 0, 1}, 0x01, 0, 0, 0) // Length 16 MiB
	in = append(in, make([]byte, 100)...)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	recs, errs := collect(t, in, Options{})
	runtime.ReadMemStats(&after)
	if len(recs) != 0 || len(errs) != 1 || !errors.Is(errs[0], ErrTruncated) {
		t.Fatalf("recs=%d errs=%v", len(recs), errs)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d >= 1<<20 {
		t.Errorf("allocated %d bytes for 100 present", d)
	}
}

func TestRecordsLargeBodyExact(t *testing.T) {
	body := bytes.Repeat([]byte{5}, 300<<10)
	in := frame(1, TypeBGP4MP, 1, 0, body)
	recs, errs := collect(t, in, Options{})
	if len(errs) != 0 || len(recs) != 1 || !bytes.Equal(recs[0].Raw(), in) {
		t.Fatalf("recs=%d errs=%v", len(recs), errs)
	}
}

func TestRecordsOffsetAfterET(t *testing.T) {
	et := frame(1, TypeBGP4MPET, 1, 5, []byte{1, 2, 3}) // 16 + 3 = 19 bytes
	bad := []byte{0, 0, 0, 1, 0, 17, 0, 1, 0, 0, 0, 1, 0xAA}
	_, errs := collect(t, append(append([]byte{}, et...), bad...), Options{})
	var re *RecordError
	if len(errs) != 1 || !errors.As(errs[0], &re) || re.Offset != 19 {
		t.Fatalf("errs=%v", errs)
	}
}

func TestRecordErrorMessage(t *testing.T) {
	e := &RecordError{Offset: 42, Err: ErrMalformed}
	if !errors.Is(e, ErrMalformed) || !bytes.Contains([]byte(e.Error()), []byte("42")) {
		t.Errorf("Error() = %q", e.Error())
	}
}
