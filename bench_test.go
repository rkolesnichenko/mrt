package mrt_test

// Benchmarks over the committed fixtures (spec §7.6), no .data needed:
//
//	go test -run '^$' -bench . -benchmem .
//
// Each reads the decompressed fixture from memory, so b.SetBytes makes MB/s
// the parsing rate over MRT bytes; BenchmarkBzip2 measures decompression
// alone. internal/compare measures a whole RIB against gobgp.

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
)

// benchFixtures are the decompressed real fixtures in testdata/mrt.
func benchFixtures(b *testing.B) (names []string, data [][]byte) {
	b.Helper()
	paths, err := filepath.Glob("testdata/mrt/*.gz")
	if err != nil || len(paths) == 0 {
		b.Fatalf("no fixtures in testdata/mrt: %v", err)
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			b.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			b.Fatal(err)
		}
		d, err := io.ReadAll(zr)
		f.Close()
		if err != nil {
			b.Fatal(err)
		}
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".gz"))
		data = append(data, d)
	}
	return names, data
}

// BenchmarkRecords: Records alone, every record framed and its fixed fields
// read.
func BenchmarkRecords(b *testing.B) {
	names, data := benchFixtures(b)
	for i, name := range names {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data[i])))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				n := 0
				for _, err := range mrt.Records(bytes.NewReader(data[i])) {
					if err != nil {
						b.Fatal(err)
					}
					n++
				}
				if n == 0 {
					b.Fatal("no records")
				}
			}
		})
	}
}

// BenchmarkRoutes: Routes over Records, every path decoded.
func BenchmarkRoutes(b *testing.B) {
	names, data := benchFixtures(b)
	for i, name := range names {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data[i])))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				n := 0
				for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(data[i]))) {
					if err != nil {
						b.Fatal(err)
					}
					if rt.Kind != mrt.KindWithdraw && rt.Path == nil {
						b.Fatal("a route without its path")
					}
					n++
				}
				if n == 0 {
					b.Fatal("no routes")
				}
			}
		})
	}
}

// BenchmarkBzip2: Decompress of a bzip2 file to io.Discard. SetBytes is the
// decompressed size, so MB/s is output bytes per second.
func BenchmarkBzip2(b *testing.B) {
	const path = "testdata/bench/rv2-updates-et.20261015.1200.bz2"
	compressed, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	want, err := os.ReadFile("testdata/mrt/rv2-updates-et.20261015.1200.gz")
	if err != nil {
		b.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(want))
	if err != nil {
		b.Fatal(err)
	}
	if want, err = io.ReadAll(zr); err != nil {
		b.Fatal(err)
	}
	r, err := mrt.Decompress(bytes.NewReader(compressed))
	if err != nil {
		b.Fatal(err)
	}
	if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, want) {
		b.Fatalf("%s does not decompress to the gzip fixture's bytes (error %v)", path, err)
	}
	b.SetBytes(int64(len(want)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r, err := mrt.Decompress(bytes.NewReader(compressed))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecompressWith: DecompressWith (GOMAXPROCS workers) of the same
// bzip2 file to io.Discard; compare with BenchmarkBzip2.
func BenchmarkDecompressWith(b *testing.B) {
	const path = "testdata/bench/rv2-updates-et.20261015.1200.bz2"
	compressed, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	zf, err := os.Open("testdata/mrt/rv2-updates-et.20261015.1200.gz")
	if err != nil {
		b.Fatal(err)
	}
	defer zf.Close()
	zr, err := gzip.NewReader(zf)
	if err != nil {
		b.Fatal(err)
	}
	want, err := io.ReadAll(zr)
	if err != nil {
		b.Fatal(err)
	}
	run := func(w io.Writer) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, err := mrt.DecompressWith(bytes.NewReader(compressed), mrt.DecompressOptions{Workers: runtime.GOMAXPROCS(0), Context: ctx})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(w, r); err != nil {
			b.Fatal(err)
		}
	}
	var got bytes.Buffer
	run(&got)
	if !bytes.Equal(got.Bytes(), want) {
		b.Fatalf("%s does not decompress to the gzip fixture's bytes", path)
	}
	b.SetBytes(int64(len(want)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		run(io.Discard)
	}
}
