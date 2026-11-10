package mrt

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"
	"unsafe"
)

// TestGzipReadaheadChunks is the behaviour TestDecompressWithGzipQueue's
// constants stand for: over 2 MiB of output, read 1,000 bytes at a time, the
// readahead hands out chunk buffers of gzipChunkBytes, at most
// gzipQueueBytes/gzipChunkBytes distinct ones, reused, and the bytes are the
// input's. A goroutine that made a chunk more, or chunks of another size,
// fails here.
func TestGzipReadaheadChunks(t *testing.T) {
	plain := make([]byte, 2<<20)
	for i := range plain {
		plain[i] = byte(i*7 + i>>10)
	}
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(plain)
	zw.Close()
	zr, err := gzip.NewReader(&z)
	if err != nil {
		t.Fatal(err)
	}
	// Cancelled when the test ends, so that a failure mid-stream leaves no
	// goroutine behind.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newGzipReadahead(zr, ctx)
	bufs := map[*byte]bool{}
	var got []byte
	p := make([]byte, 1000)
	for {
		n, err := g.Read(p)
		got = append(got, p[:n]...)
		if g.chunk != nil {
			bufs[unsafe.SliceData(g.chunk)] = true
			if len(g.chunk) != gzipChunkBytes || cap(g.chunk) != gzipChunkBytes {
				t.Fatalf("a chunk of length %d, capacity %d; want %d", len(g.chunk), cap(g.chunk), gzipChunkBytes)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("read %d bytes, not the %d written", len(got), len(plain))
	}
	const chunks = gzipQueueBytes / gzipChunkBytes
	if len(bufs) > chunks || len(bufs) < 2 {
		t.Errorf("%d distinct chunk buffers over %d bytes, want 2 to %d", len(bufs), len(plain), chunks)
	}
}
