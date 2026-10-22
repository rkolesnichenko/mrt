package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

// benchMessages is the least number of messages BenchmarkBMP decodes per
// iteration.
const benchMessages = 50_000

// benchStream is every message of testdata/bmp/*.bmp, file after file, the
// whole repeated until it holds at least benchMessages messages. The messages
// alias the files' bytes read once.
func benchStream(b *testing.B) [][]byte {
	b.Helper()
	paths, err := filepath.Glob("../../testdata/bmp/*.bmp")
	if err != nil || len(paths) == 0 {
		b.Fatalf("no .bmp fixtures: %v", err)
	}
	var base [][]byte
	for _, p := range paths {
		streams, err := readBMPStreams(p)
		if err != nil {
			b.Fatal(err)
		}
		base = append(base, streams[0]...)
	}
	stream := make([][]byte, 0, benchMessages+len(base))
	for len(stream) < benchMessages {
		stream = append(stream, base...)
	}
	return stream
}

// BenchmarkBMP decodes the same messages, already framed (a Kafka consumer's
// input), with mrt (bmp.Session.Routes over bmp.ParseMessage, every route
// yielded and its path decoded) and with gobgp (ParseBMPMessageWithOptions
// with the Peer Up state of gobgpSession; it decodes every attribute and
// NLRI). One iteration is the whole stream through one session. B/msg and
// allocs/msg are TotalAlloc and Mallocs over the run per message decoded
// (spec §9.6); routes/msg is what each side counted.
func BenchmarkBMP(b *testing.B) {
	streams := [][][]byte{benchStream(b)}
	msgs := len(streams[0])
	for _, side := range []struct {
		name string
		walk func([][][]byte) (routes, errs int)
	}{
		{"mrt", walkMrtBMP},
		{"gobgp", walkGobgpBMP},
	} {
		b.Run(side.name, func(b *testing.B) {
			b.ReportAllocs()
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			b.ResetTimer()
			routes := 0
			for range b.N {
				routes, _ = side.walk(streams)
			}
			b.StopTimer()
			runtime.ReadMemStats(&after)
			n := float64(b.N) * float64(msgs)
			b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/n, "B/msg")
			b.ReportMetric(float64(after.Mallocs-before.Mallocs)/n, "allocs/msg")
			b.ReportMetric(float64(routes)/float64(msgs), "routes/msg")
		})
	}
}
