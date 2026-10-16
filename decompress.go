package mrt

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
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
