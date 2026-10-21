// Package bzcraft builds the crafted bzip2 inputs that load the parallel
// bzip2 reader's bounds (DecompressWith; v0.3.1 design §5.3, v0.3.2 design
// §5.2), for the tests of package mrt and for decompbench -mem -crafted. It is
// test-only and deterministic, and nothing it builds is committed.
//
// EndMagicFlood and SparseBlockMagics are bytes that only look like bzip2.
// ResultsLoad, SegmentLoad, UnderCapBlocks and BoundLoad are real bzip2: they
// compress generated plaintext with the system bzip2 (bzip2 -9 -c), and
// return an error wrapping ErrNoBzip2 when it is missing, so callers can skip.
// Each is built once per process; the slice returned is shared, so callers
// must not modify it. The first three are under 15 MB, for the unit tests;
// BoundLoad is 100 MiB, for the opt-in gate only.
//
// The sizes follow from bzip2 -9's block: 899,981 bytes after bzip2's first
// run-length encoding, which writes a run of 4 to 255 equal bytes as 5 (the
// block is cut once it holds that many, so it holds up to 899,985). A run of
// 11 or 12 bytes then decodes to 11 or 12 bytes per 5 in the block: blocks of
// runs alternating 11 and 12 bytes long hold about 179,997 runs and decode to
// about 2,070,000 bytes, just under the reader's 2 MiB cap on a speculative
// decode (specCap).
package bzcraft

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os/exec"
	"sync"

	"github.com/rkolesnichenko/mrt/internal/bzip2"
)

// ErrNoBzip2 is the error the bzip2-built inputs wrap when the system bzip2
// is missing.
var ErrNoBzip2 = errors.New("bzcraft: no bzip2 command")

const (
	// SpecCap is the parallel reader's cap on a speculative decode's output
	// (specCap in package mrt), which the inputs are sized against.
	SpecCap = 2 << 20
	// maxSegment is the most compressed input one speculative decode gets
	// (maxSegment in package mrt).
	maxSegment = 8 << 20

	// zeroBytes is ResultsLoad's first stream: zeros, one block of 176,471
	// runs of 255 bytes, which decodes to 45,000,000 bytes, over SpecCap, so
	// the reader's chain decodes it itself.
	zeroBytes = 45_000_000
	// underCapRuns is UnderCapBlocks' plaintext: runs alternating 11 and 12
	// bytes long, about 40 blocks of 179,997 runs (the last one shorter).
	underCapRuns = 40 * 179_990
	// segmentRuns is SegmentLoad's plaintext before padding: two blocks.
	segmentRuns = 2 * 179_990
	// segmentBlockBytes is the compressed size SegmentLoad pads each of its
	// blocks to (in whole 2-byte words): under maxSegment, so a worker
	// decodes the block whole, and two of them under 15 MB.
	segmentBlockBytes = 7_400_000
)

// Plaintext sizes, which bzcraft's tests check through compress/bzip2.
const (
	underCapSize    = underCapRuns / 2 * 23 // 82,795,400
	resultsLoadSize = zeroBytes + underCapSize
	segmentLoadSize = segmentRuns / 2 * 23 // 4,139,770
)

var (
	blockMagic = []byte{0x31, 0x41, 0x59, 0x26, 0x53, 0x59} // the block magic's 48 bits
	endMagic   = []byte{0x17, 0x72, 0x45, 0x38, 0x50, 0x90} // the end magic's 48 bits
)

// EndMagicFlood is "BZh9" and then end magics, past the look-ahead of the
// given Workers: one 8 MiB segment and 1 MiB per job outstanding, plus 1 MiB.
func EndMagicFlood(workers int) []byte {
	return append([]byte("BZh9"), bytes.Repeat(endMagic, (8+2*workers+1)<<20/6)...)
}

// SparseBlockMagics is "BZh9" and then, to 32 MiB, a block magic followed by
// 200 KiB of zeros, repeated: candidates whose segments are 200 KiB each.
func SparseBlockMagics() []byte {
	unit := append(bytes.Clone(blockMagic), make([]byte, 200<<10)...)
	in := []byte("BZh9")
	for len(in) < 32<<20 {
		in = append(in, unit...)
	}
	return in[:32<<20]
}

// UnderCapBlocks is one bzip2 -9 stream of about 40 blocks, each decoding to
// just under SpecCap (the last one less): workers decode every block whole
// and make its exact-size copy. Its plaintext is 82,795,400 bytes of runs
// alternating 11 and 12 bytes long, each of a letter from 'a' to 'p' other
// than the one before.
func UnderCapBlocks() ([]byte, error) { return underCap() }

var underCap = sync.OnceValues(func() ([]byte, error) {
	return compress(newRuns(underCapRuns))
})

// ResultsLoad is two bzip2 -9 streams: one block of 45,000,000 zeros, over
// SpecCap, which the chain decodes itself while workers decode the blocks
// after it, and then UnderCapBlocks, whose blocks each decode to just under
// SpecCap. So workers finish results ahead of the chain, up to the
// 2 × Workers outstanding. Its plaintext is 127,795,400 bytes.
func ResultsLoad() ([]byte, error) { return resultsLoad() }

var resultsLoad = sync.OnceValues(func() ([]byte, error) {
	zeros, err := compress(io.LimitReader(zeroReader{}, zeroBytes))
	if err != nil {
		return nil, err
	}
	rest, err := underCap()
	if err != nil {
		return nil, err
	}
	return append(zeros, rest...), nil
})

// SegmentLoad is one bzip2 -9 stream of two blocks, each decoding to just
// under SpecCap, padded to 7,400,000 compressed bytes each (in all under
// 15 MB) with no-op code-length deltas: compressed segments as large as fit
// in 15 MB, each under the reader's 8 MiB segment, so workers decode them
// whole. Its plaintext is 4,139,770 bytes.
func SegmentLoad() ([]byte, error) { return segmentLoad() }

var segmentLoad = sync.OnceValues(func() ([]byte, error) {
	in, err := compress(newRuns(segmentRuns))
	if err != nil {
		return nil, err
	}
	return padBlocks(in, segmentBlockBytes)
})

// Repeated returns n copies of stream, a bzip2 stream of one block, with the
// block padded (as SegmentLoad's are) so that each copy is unit bytes, or one
// less. Each copy is a stream of its own, so the result is a valid
// multi-stream input that decodes to n copies of stream's plaintext, with a
// block every unit bytes.
func Repeated(stream []byte, unit, n int) ([]byte, error) {
	starts, _, _, err := Blocks(stream)
	if err != nil {
		return nil, err
	}
	if len(starts) != 1 {
		return nil, fmt.Errorf("bzcraft: %d blocks, want 1", len(starts))
	}
	if pad := (unit - len(stream)) &^ 1; pad > 0 {
		at, err := basePoint(stream, starts[0])
		if err != nil {
			return nil, err
		}
		stream = insertPad(stream, at, pad)
	}
	return bytes.Repeat(stream, n), nil
}

// boundUnit and boundCopies are BoundLoad's streams: 4 MiB each, 25 of them.
const (
	boundUnit   = 4 << 20
	boundCopies = 25
	boundRuns   = 100 // each stream's plaintext: 1,150 bytes of runs
)

// BoundLoad is 25 bzip2 -9 streams, each one block of 1,150 bytes of runs
// padded to 4 MiB (Repeated): 100 MiB, built in memory for TestBoundTerms
// and decompbench, far too large to commit, unlike the other inputs. Its
// blocks are segments of just under 4 MiB, so at Workers 14 eight of them
// fill the 32 MiB budget of segment copies, and the 2 × 14 jobs' lead lets
// the window grow to its bound; at Workers 4 four of them fill the 16 MiB
// lead. Its plaintext is 28,750 bytes.
func BoundLoad() ([]byte, error) { return boundLoad() }

var boundLoad = sync.OnceValues(func() ([]byte, error) {
	stream, err := compress(newRuns(boundRuns))
	if err != nil {
		return nil, err
	}
	return Repeated(stream, boundUnit, boundCopies)
})

// compress returns src compressed by bzip2 -9.
func compress(src io.Reader) ([]byte, error) {
	if _, err := exec.LookPath("bzip2"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoBzip2, err)
	}
	cmd := exec.Command("bzip2", "-9", "-c")
	cmd.Stdin = src
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bzcraft: bzip2 -9: %v: %s", err, stderr.Bytes())
	}
	return out, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// runs reads as n runs, alternating 11 and 12 bytes long, each of a letter
// from 'a' to 'p' other than the one before, from a fixed seed.
type runs struct {
	left int
	made int
	rng  *rand.Rand
	prev int
	run  [12]byte
	buf  []byte // the current run's bytes not yet read
}

func newRuns(n int) *runs {
	return &runs{left: n, rng: rand.New(rand.NewPCG(0x6d7274, 0x627a)), prev: -1}
}

func (r *runs) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(r.buf) == 0 {
			if r.left == 0 {
				break
			}
			r.left--
			c := r.rng.IntN(16)
			if r.prev >= 0 {
				c = (r.prev + 1 + r.rng.IntN(15)) % 16
			}
			r.prev = c
			r.buf = r.run[:11+r.made%2]
			r.made++
			for i := range r.buf {
				r.buf[i] = 'a' + byte(c)
			}
		}
		k := copy(p[n:], r.buf)
		n += k
		r.buf = r.buf[k:]
	}
	if n == 0 && len(p) > 0 {
		return 0, io.EOF
	}
	return n, nil
}

// Blocks returns the bit offsets of the start and the end of every block of
// the bzip2 input in, across its streams, and each block's decoded size.
func Blocks(in []byte) (starts, ends []uint64, sizes []int, err error) {
	var d bzip2.Decoder
	br := bzip2.NewBitReader(bytes.NewReader(in))
	level, err := d.Header(br)
	if err != nil {
		return nil, nil, nil, err
	}
	for level != 0 {
		var crc uint32
		for br.PeekMagic() == bzip2.BlockMagic {
			start := br.Offset()
			data, stored, err := d.Block(br, level)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("block %d: %v", len(starts), err)
			}
			starts, ends, sizes = append(starts, start), append(ends, br.Offset()), append(sizes, len(data))
			crc = bzip2.FoldCRC(crc, stored)
		}
		if level, err = d.StreamEnd(br, crc); err != nil && err != io.EOF {
			return nil, nil, nil, err
		}
	}
	return starts, ends, sizes, nil
}

// padBlocks pads every block of in to size compressed bytes, as package mrt's
// paddedBench pads one: whole 0xBBBB words, each four no-op code-length deltas
// ("1 0", +1, then "1 1", -1), after the first Huffman table's base length.
// The padding is whole bytes, so the bits after it keep their alignment, and
// every block decodes as before. Blocks are padded from the last, so the
// offsets of those before do not move.
func padBlocks(in []byte, size int) ([]byte, error) {
	starts, ends, _, err := Blocks(in)
	if err != nil {
		return nil, err
	}
	for i := len(starts) - 1; i >= 0; i-- {
		pad := (size - int((ends[i]-starts[i])/8)) &^ 1
		if pad <= 0 {
			continue
		}
		at, err := basePoint(in, starts[i])
		if err != nil {
			return nil, fmt.Errorf("block %d: %v", i, err)
		}
		in = insertPad(in, at, pad)
	}
	return in, nil
}

// basePoint returns the bit after the first Huffman table's base length in
// the block that starts at bit start.
func basePoint(in []byte, start uint64) (uint64, error) {
	br := bzip2.NewBitReader(bytes.NewReader(in[start/8:]))
	br.ReadBits(uint(start % 8))
	if br.ReadBits64(48) != bzip2.BlockMagic {
		return 0, errors.New("no block magic")
	}
	br.ReadBits64(32 + 1 + 24) // CRC, randomized, origPtr
	used := br.ReadBits(16)
	for i := 0; i < 16; i++ {
		if used&(1<<(15-i)) != 0 {
			br.ReadBits(16)
		}
	}
	br.ReadBits(3) // number of trees
	for n := br.ReadBits(15); n > 0; n-- {
		for br.ReadBit() {
		}
	}
	if base := br.ReadBits(5); base < 1 || base > 19 {
		return 0, fmt.Errorf("first table's base length %d; the deltas need 1 to 19", base)
	}
	if err := br.Err(); err != nil {
		return 0, err
	}
	return start/8*8 + br.Offset(), nil
}

// insertPad returns in with pad bytes of 0xBB inserted at bit at (pad even).
func insertPad(in []byte, at uint64, pad int) []byte {
	k := uint(at % 8)
	i := int(at / 8)
	out := make([]byte, 0, len(in)+pad)
	out = append(out, in[:i]...)
	if k == 0 {
		out = append(out, bytes.Repeat([]byte{0xBB}, pad)...)
		return append(out, in[i:]...)
	}
	// The byte holding at gives its first k bits, then the padding, then its
	// last 8-k bits. The padding's pattern, 1011, repeats every 4 bits, so
	// every whole output byte inside it starts at the pattern's bit
	// (8-k) mod 4: 0xBB rotated by that many bits.
	b := in[i]
	fill := byte(uint(0xBBBB) >> (8 - (8-k)%4))
	// The first output byte: b's first k bits, then the padding's first 8-k.
	out = append(out, b&^(0xFF>>k)|0xBB>>k)
	out = append(out, bytes.Repeat([]byte{fill}, pad-1)...)
	// The last: the padding's last k bits (those of a 0xBB byte), then b's
	// last 8-k.
	out = append(out, byte(0xBB<<(8-k))|b&(0xFF>>k))
	return append(out, in[i+1:]...)
}
