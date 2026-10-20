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

var (
	errNegativeWorkers = errors.New("mrt: DecompressOptions.Workers is negative")
	errNilContext      = errors.New("mrt: DecompressOptions.Workers > 0 needs a Context")
)

// DecompressWith is Decompress with options. DecompressWith(r,
// DecompressOptions{}) is Decompress(r). With Workers > 0 a bzip2 input is
// decompressed block by block by Workers goroutines, and reading the result
// gives the bytes Decompress gives, then an error where Decompress fails.
// Uncompressed input is returned as Decompress returns it, and no goroutine
// starts. Every goroutine DecompressWith starts has ended when a Read has
// returned io.EOF or an error, or soon after Context is cancelled. Workers < 0,
// or Workers > 0 with a nil Context, is an error.
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
		return gzip.NewReader(br)
	case string(magic) == "BZh":
		return newParallelBzip2(br, opts, hooks), nil
	}
	return br, nil
}
