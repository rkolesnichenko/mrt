package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
)

// records returns the raw bytes of every record in the synthetic file name.
func records(t *testing.T, name string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "synthetic", name))
	if err != nil {
		t.Fatal(err)
	}
	var raws [][]byte
	for rec, err := range mrt.Records(bytes.NewReader(data)) {
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, rec.Raw())
	}
	return raws
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if zr.ModTime.Unix() > 0 || zr.Name != "" {
		t.Errorf("gzip header: ModTime %v, Name %q; want neither", zr.ModTime, zr.Name)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCutFirstAndAlso(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "synthetic", "addpath-rib-v4v6.mrt")
	raws := records(t, "addpath-rib-v4v6.mrt") // peer table, RIB 13/8, RIB 13/10
	if len(raws) != 3 {
		t.Fatalf("fixture has %d records, want 3", len(raws))
	}
	var stdout, stderr bytes.Buffer
	if st := run([]string{"-first", "1", "-also", "13/10=1", in}, nil, &stdout, &stderr); st != 0 {
		t.Fatalf("status %d, stderr %s", st, stderr.String())
	}
	want := append(append([]byte{}, raws[0]...), raws[2]...)
	if got := gunzip(t, stdout.Bytes()); !bytes.Equal(got, want) {
		t.Errorf("cut = %x\nwant %x", got, want)
	}
	if !strings.Contains(stderr.String(), "2 of 3 records") {
		t.Errorf("stderr %q does not report 2 of 3 records", stderr.String())
	}
}

func TestCutSkip(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "synthetic", "otc-large.mrt")
	raws := records(t, "otc-large.mrt")
	if len(raws) < 4 {
		t.Fatalf("fixture has %d records, want at least 4", len(raws))
	}
	var stdout bytes.Buffer
	if st := run([]string{"-skip", "1", "-first", "2", in}, nil, &stdout, io.Discard); st != 0 {
		t.Fatal(st)
	}
	want := append(append([]byte{}, raws[1]...), raws[2]...)
	if got := gunzip(t, stdout.Bytes()); !bytes.Equal(got, want) {
		t.Errorf("-skip 1 -first 2 = %x\nwant records 2 and 3: %x", got, want)
	}
}

func TestCutIsDeterministic(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "synthetic", "otc-large.mrt")
	var a, b bytes.Buffer
	if st := run([]string{"-first", "2", in}, nil, &a, io.Discard); st != 0 {
		t.Fatal(st)
	}
	if st := run([]string{"-first", "2", in}, nil, &b, io.Discard); st != 0 {
		t.Fatal(st)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("two cuts of the same input differ")
	}
}

func TestCutStdin(t *testing.T) {
	raws := records(t, "otc-large.mrt")
	var all []byte
	for _, r := range raws {
		all = append(all, r...)
	}
	var stdout bytes.Buffer
	if st := run([]string{"-first", "100", "-"}, bytes.NewReader(all), &stdout, io.Discard); st != 0 {
		t.Fatal(st)
	}
	if got := gunzip(t, stdout.Bytes()); !bytes.Equal(got, all) {
		t.Error("a cut of more records than the file has is not the whole file")
	}
}

func TestCount(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "synthetic", "addpath-updates.mrt")
	var stdout bytes.Buffer
	if st := run([]string{"-count", in}, nil, &stdout, io.Discard); st != 0 {
		t.Fatal(st)
	}
	want := in + "\n" +
		"  16/9 UPDATE 2\n" +
		"  17/9 UPDATE 1\n" +
		"  records 3\n"
	if got := stdout.String(); got != want {
		t.Errorf("count:\n%s\nwant:\n%s", got, want)
	}
}

func TestCountTruncated(t *testing.T) {
	raws := records(t, "otc-large.mrt")
	var all []byte
	for _, r := range raws {
		all = append(all, r...)
	}
	var stdout bytes.Buffer
	st := run([]string{"-count", "-"}, bytes.NewReader(all[:len(all)-3]), &stdout, io.Discard)
	if st != 0 {
		t.Fatalf("status %d; a truncated input must still be counted", st)
	}
	if !strings.Contains(stdout.String(), "stopped:") {
		t.Errorf("count of a truncated input does not say where it stopped:\n%s", stdout.String())
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-first", "0", "x"},
		{"-also", "13/10", "x"},
		{"-also", "13=1", "x"},
		{"-first", "1", "a", "b"},
		{"-skip", "-1", "-first", "1", "x"},
	} {
		if st := run(args, nil, io.Discard, io.Discard); st != 2 {
			t.Errorf("%q: status %d, want 2", args, st)
		}
	}
}

// TestCountCutMessages: a message whose BGP header claims more bytes than
// the record holds is counted apart, with "cut" after its type (Ruling R17a:
// its type is read, its body is short).
func TestCountCutMessages(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "synthetic", "short-message.mrt")
	var stdout bytes.Buffer
	if st := run([]string{"-count", in}, nil, &stdout, io.Discard); st != 0 {
		t.Fatal(st)
	}
	want := in + "\n" +
		"  16/1 UPDATE 1\n" +
		"  16/1 UPDATE cut 4\n" +
		"  records 5\n"
	if got := stdout.String(); got != want {
		t.Errorf("count:\n%s\nwant:\n%s", got, want)
	}
}

func TestCPUProfile(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "synthetic", "addpath-updates.mrt")
	p := filepath.Join(t.TempDir(), "cpu.prof")
	if st := run([]string{"-cpuprofile", p, "-count", in}, nil, io.Discard, io.Discard); st != 0 {
		t.Fatal(st)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() == 0 {
		t.Error("profile is empty")
	}
}
