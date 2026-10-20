package mrt

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"io"
)

// Decompress returns a reader of r's MRT bytes: gzip is recognised by the
// bytes 1f 8b (concatenated members are all read), bzip2 by "BZh", and
// anything else, including an empty input, is returned as it is. A broken
// gzip header is returned as the error.
func Decompress(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(3)
	if len(magic) < 3 { // too short to be compressed
		if err != nil && err != io.EOF {
			return nil, err
		}
		return br, nil
	}
	switch {
	case magic[0] == 0x1f && magic[1] == 0x8b:
		return gzip.NewReader(br)
	case string(magic) == "BZh":
		return bzip2.NewReader(br), nil
	}
	return br, nil
}

// DecompressOptions are the options of DecompressWith.
type DecompressOptions struct {
	// Workers is how many goroutines decompress a bzip2 input in parallel;
	// for gzip, any Workers > 0 runs decompression one goroutine ahead of
	// the reader. 0 is Decompress: no goroutines.
	Workers int
	// Context stops every goroutine DecompressWith started when it is
	// cancelled; Read then returns Context.Err(). Required when Workers > 0.
	Context context.Context
}

const (
	// gzipQueueBytes is the most decompressed output the gzip readahead
	// holds that Read has not returned (v0.3 design §6.3).
	gzipQueueBytes = 256 << 10
	// gzipChunkBytes is the size of one chunk of the queue.
	gzipChunkBytes = 32 << 10
)

var (
	errNegativeWorkers = errors.New("mrt: DecompressOptions.Workers is negative")
	errNilContext      = errors.New("mrt: DecompressOptions.Workers > 0 needs a Context")
)

// DecompressWith is Decompress with options. DecompressWith(r,
// DecompressOptions{}) is Decompress(r). With Workers > 0 a bzip2 input is
// decompressed block by block by Workers goroutines, and a gzip input by one
// goroutine that runs at most 256 KiB of output ahead of Read; reading the
// result gives the bytes Decompress gives, then an error where Decompress
// fails. A broken gzip header is returned as the error, as Decompress
// returns it. Uncompressed input is returned as Decompress returns it, and no
// goroutine starts. Workers < 0, or Workers > 0 with a nil Context, is an
// error.
//
// The goroutines read r, so r must not be read by anything else once
// DecompressWith has returned. Every goroutine DecompressWith starts has
// ended when a Read has returned io.EOF or an error other than Context's.
// A caller that stops reading before then must cancel Context to release
// them. After a cancel, a Read before the output has ended returns
// Context.Err(), and the goroutines end soon after, except that the gzip
// goroutine, if it is blocked inside r.Read (r is a pipe or stdin, say), ends
// only when that Read returns, since a Context cannot interrupt it.
func DecompressWith(r io.Reader, opts DecompressOptions) (io.Reader, error) {
	return decompressWith(r, opts, nil)
}

func decompressWith(r io.Reader, opts DecompressOptions, hooks *bzip2Hooks) (io.Reader, error) {
	switch {
	case opts.Workers < 0:
		return nil, errNegativeWorkers
	case opts.Workers > 0 && opts.Context == nil:
		return nil, errNilContext
	case opts.Workers == 0:
		return Decompress(r)
	}
	// Decompress's sniffing, with bzip2 read in parallel.
	br := bufio.NewReader(r)
	magic, err := br.Peek(3)
	if len(magic) < 3 {
		if err != nil && err != io.EOF {
			return nil, err
		}
		return br, nil
	}
	switch {
	case magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		return newGzipReadahead(zr, opts.Context), nil
	case string(magic) == "BZh":
		return newParallelBzip2(br, opts, hooks), nil
	}
	return br, nil
}

// gzipReadahead is DecompressWith's gzip reader (v0.3 design §6.3): one
// goroutine reads the gzip.Reader into chunks of gzipChunkBytes, and Read
// takes them in order. The chunks are the queue's whole memory: there are
// gzipQueueBytes/gzipChunkBytes of them, each filled by the goroutine, sent
// on full, taken by Read and given back on free once Read has returned all
// of it. So the decompressed output not yet returned is at most
// gzipQueueBytes.
type gzipReadahead struct {
	ctx  context.Context
	full chan gzipChunk // filled by the goroutine, in order
	free chan []byte    // chunks Read has returned in full
	done chan struct{}  // closed when the goroutine exits

	chunk []byte // the chunk Read is returning, nil if none
	data  []byte // its bytes Read has not returned
	err   error  // the error that ends the output, once known
	ended bool   // err is set: data is the last of the output
}

// A gzipChunk is a chunk's buffer, the n bytes the goroutine filled it
// with, and the error the gzip.Reader returned after them, if any.
type gzipChunk struct {
	buf []byte
	n   int
	err error
}

func newGzipReadahead(zr *gzip.Reader, ctx context.Context) *gzipReadahead {
	const chunks = gzipQueueBytes / gzipChunkBytes
	g := &gzipReadahead{
		ctx:  ctx,
		full: make(chan gzipChunk, chunks),
		free: make(chan []byte, chunks),
		done: make(chan struct{}),
	}
	go g.run(zr, chunks)
	return g
}

// run fills chunks from zr until zr returns an error, which it sends with the
// last bytes, or the Context is cancelled. It makes at most chunks of them.
func (g *gzipReadahead) run(zr *gzip.Reader, chunks int) {
	defer close(g.done)
	cancelled := g.ctx.Done()
	made := 0
	for {
		var buf []byte
		select {
		case buf = <-g.free:
		default:
			if made < chunks {
				buf = make([]byte, gzipChunkBytes)
				made++
				break
			}
			select {
			case buf = <-g.free:
			case <-cancelled:
				return
			}
		}
		n := 0
		var err error
		for n < len(buf) && err == nil {
			select {
			case <-cancelled:
				return
			default:
			}
			var k int
			k, err = zr.Read(buf[n:])
			n += k
		}
		select {
		case g.full <- gzipChunk{buf, n, err}:
		case <-cancelled:
			return
		}
		if err != nil {
			return
		}
	}
}

// Read returns the goroutine's chunks in order, then its error once the
// goroutine has exited. After a cancel it returns Context.Err() at once.
func (g *gzipReadahead) Read(p []byte) (int, error) {
	if !g.ended || len(g.data) > 0 {
		if err := g.ctx.Err(); err != nil {
			g.chunk, g.data, g.err, g.ended = nil, nil, err, true
			return 0, err
		}
	}
	for len(g.data) == 0 {
		if g.ended {
			return 0, g.err
		}
		if len(p) == 0 {
			return 0, nil
		}
		select {
		case c := <-g.full:
			g.chunk, g.data = c.buf, c.buf[:c.n]
			if c.err != nil {
				g.err, g.ended = c.err, true
				<-g.done
			}
		case <-g.ctx.Done():
			g.chunk, g.data, g.err, g.ended = nil, nil, g.ctx.Err(), true
			return 0, g.err
		}
		g.release()
	}
	n := copy(p, g.data)
	g.data = g.data[n:]
	g.release()
	return n, nil
}

// release gives the chunk back to the goroutine once Read has returned all of
// it.
func (g *gzipReadahead) release() {
	if len(g.data) == 0 && g.chunk != nil {
		if !g.ended {
			g.free <- g.chunk // never blocks: free holds every chunk
		}
		g.chunk = nil
	}
}
