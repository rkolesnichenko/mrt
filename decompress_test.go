package mrt

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

// "hello mrt" compressed once with the system bzip2.
const bzip2Hello = "425a6839314159265359535611ee00000111804000024694002000310c082069ea609f067b82ee48a70a120a6ac23dc0"

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readAll(t *testing.T, in []byte) []byte {
	t.Helper()
	r, err := Decompress(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDecompress(t *testing.T) {
	two := append(gz(t, []byte("hello ")), gz(t, []byte("mrt"))...)
	if got := readAll(t, two); string(got) != "hello mrt" {
		t.Errorf("gzip: %q", got)
	}
	if got := readAll(t, unhex(t, bzip2Hello)); string(got) != "hello mrt" {
		t.Errorf("bzip2: %q", got)
	}
	raw := frame(ts, TypeTableDumpV2, SubGeoPeerTable, 0, []byte{9})
	if got := readAll(t, raw); !bytes.Equal(got, raw) {
		t.Errorf("raw: % x", got)
	}
	// Fewer than 3 bytes: unchanged, even when they start like gzip.
	for _, in := range [][]byte{{0x1f}, {0x1f, 0x8b}, []byte("BZ")} {
		if got := readAll(t, in); !bytes.Equal(got, in) {
			t.Errorf("% x -> % x", in, got)
		}
	}
	// A broken gzip header is the error.
	if _, err := Decompress(strings.NewReader("\x1f\x8b\x00")); err == nil {
		t.Error("bad gzip header: no error")
	}
}

func TestDecompressEmpty(t *testing.T) {
	if got := readAll(t, nil); len(got) != 0 {
		t.Errorf("%q", got)
	}
	rec := frame(ts, TypeTableDumpV2, SubGeoPeerTable, 0, []byte{9})
	in := append(gz(t, nil), gz(t, rec)...)
	r, err := Decompress(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rc, err := range Records(r) {
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rc.Raw(), rec) {
			t.Errorf("% x", rc.Raw())
		}
		n++
	}
	if n != 1 {
		t.Errorf("records %d", n)
	}
}
