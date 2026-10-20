package bzip2

import (
	"bytes"
	stdbzip2 "compress/bzip2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const benchFile = "../../testdata/bench/rv2-updates-et.20261015.1200.bz2"

// inputs returns every testdata/bzip2 file and the bench file, by name.
func inputs(t testing.TB) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob("../../testdata/bzip2/*.bz2")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no testdata/bzip2 files: %v", err)
	}
	paths = append(paths, benchFile)
	out := make(map[string][]byte)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = b
	}
	return out
}

// readUpToError reads r to its first error, with io.ReadAll when size is 0
// and with Reads of size bytes otherwise. The bytes of the Read that returns
// the error are kept, as io.ReadAll keeps them.
func readUpToError(r io.Reader, size int) ([]byte, error) {
	if size == 0 {
		return io.ReadAll(r)
	}
	var out []byte
	buf := make([]byte, size)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// compareWithStdlib checks that NewReader gives the bytes compress/bzip2
// gives up to the first error, and fails exactly when it fails, reading with
// io.ReadAll and with Reads of each of sizes.
func compareWithStdlib(t testing.TB, in []byte, sizes ...int) {
	t.Helper()
	for _, size := range append([]int{0}, sizes...) {
		got, gotErr := readUpToError(NewReader(bytes.NewReader(in)), size)
		want, wantErr := readUpToError(stdbzip2.NewReader(bytes.NewReader(in)), size)
		if !bytes.Equal(got, want) || (gotErr == nil) != (wantErr == nil) {
			t.Errorf("read size %d (0 is io.ReadAll): got %d bytes, error %v; compress/bzip2 %d bytes, error %v (bytes equal: %v)",
				size, len(got), gotErr, len(want), wantErr, bytes.Equal(got, want))
		}
	}
}

// afterEnd are bytes after a stream's end that compress/bzip2 checks: one
// byte, a "BZ" with a bad or missing header, and a header with no block.
var afterEnd = []string{"B", "BZ", "XY", "BX", "BZh", "BZx9", "BZh0", "BZh:", "BZh1", "BZh9", "BZh91AY&SY"}

func TestReaderMatchesStdlib(t *testing.T) {
	for name, in := range inputs(t) {
		// A small file is cut at every offset, so that the cut inside a
		// block's last byte is among the cuts: level1.bz2 cut at 186 bytes
		// still decodes its block, and the standard reader returns the
		// bytes of one Read with the error. Reads of 7 bytes check that.
		// A large one is cut at 50 offsets and read with io.ReadAll only,
		// to keep the test quick under -race.
		small := len(in) <= 4096
		var sizes []int
		cuts := 50
		if small {
			sizes = []int{7}
			cuts = len(in)
		}
		cases := map[string][]byte{"whole": in}
		for i := 0; i < cuts; i++ {
			cut := i * len(in) / cuts
			cases[fmt.Sprintf("cut-%d", cut)] = in[:cut]
		}
		for i := 0; i < 50; i++ {
			bit := i * len(in) * 8 / 50
			flipped := bytes.Clone(in)
			flipped[bit/8] ^= 0x80 >> (bit % 8)
			cases[fmt.Sprintf("flip-bit-%d", bit)] = flipped
		}
		for _, tail := range afterEnd {
			cases[fmt.Sprintf("then-%q", tail)] = append(bytes.Clone(in), tail...)
		}
		for c, in := range cases {
			t.Run(name+"/"+c, func(t *testing.T) {
				t.Parallel()
				compareWithStdlib(t, in, sizes...)
			})
		}
	}
}

// bitsAt returns the n bits of b at bit offset off, read without the
// package's bit reader.
func bitsAt(b []byte, off uint64, n int) uint64 {
	var v uint64
	for i := uint64(0); i < uint64(n); i++ {
		p := off + i
		v = v<<1 | uint64(b[p/8]>>(7-p%8)&1)
	}
	return v
}

// magics returns the bit offsets of every 48-bit BlockMagic and EndMagic in
// b, found by a scan at every bit offset.
func magics(b []byte) (blocks, ends []uint64) {
	const mask = 1<<48 - 1
	var v uint64
	for p := uint64(0); p < uint64(len(b))*8; p++ {
		v = (v<<1 | uint64(b[p/8]>>(7-p%8)&1)) & mask
		if p < 47 {
			continue
		}
		switch v {
		case BlockMagic:
			blocks = append(blocks, p-47)
		case EndMagic:
			ends = append(ends, p-47)
		}
	}
	return blocks, ends
}

func TestBlockEnds(t *testing.T) {
	t.Parallel()
	all := inputs(t)
	for _, name := range []string{"level1.bz2", filepath.Base(benchFile)} {
		t.Run(name, func(t *testing.T) {
			in := all[name]
			want, err := io.ReadAll(stdbzip2.NewReader(bytes.NewReader(in)))
			if err != nil {
				t.Fatal(err)
			}
			blocks, ends := magics(in)
			if len(blocks) == 0 || len(ends) != 1 || blocks[0] != 32 {
				t.Fatalf("scan found block magics at %v and end magics at %v; want blocks from bit 32 and one end", blocks, ends)
			}
			next := append(append([]uint64{}, blocks[1:]...), ends[0])
			level := int(in[3] - '0')

			br := NewBitReader(bytes.NewReader(in))
			br.ReadBits64(32)
			var d, fresh Decoder
			var got []byte
			var stream uint32
			for i, start := range blocks {
				if br.Offset() != start {
					t.Fatalf("block %d: reader at bit %d, want %d", i, br.Offset(), start)
				}
				data, crc, err := d.Block(br, level)
				if err != nil {
					t.Fatalf("block %d at bit %d: %v", i, start, err)
				}
				if br.Offset() != next[i] {
					t.Fatalf("block %d at bit %d: ended at bit %d, want the next magic at %d", i, start, br.Offset(), next[i])
				}
				got = append(got, data...)
				stream = FoldCRC(stream, crc)

				// The same block from a reader made at the byte that holds its
				// first bit, after skipping the bits before it in that byte.
				br2 := NewBitReader(bytes.NewReader(in[start/8:]))
				br2.ReadBits(uint(start % 8))
				data2, crc2, err := fresh.Block(br2, level)
				if err != nil || crc2 != crc || !bytes.Equal(data2, data) {
					t.Fatalf("block %d from a fresh reader: crc %08x, %d bytes, error %v; want crc %08x, %d bytes", i, crc2, len(data2), err, crc, len(data))
				}
				if end := start/8*8 + br2.Offset(); end != next[i] || br2.Err() != nil {
					t.Fatalf("block %d from a fresh reader: ended at bit %d (error %v), want %d", i, end, br2.Err(), next[i])
				}
			}
			if !bytes.Equal(got, want) {
				t.Errorf("blocks hold %d bytes, not the %d compress/bzip2 gives", len(got), len(want))
			}
			if wantCRC := uint32(bitsAt(in, ends[0]+48, 32)); stream != wantCRC {
				t.Errorf("folded CRC %08x, stream CRC %08x", stream, wantCRC)
			}
		})
	}
}

// bitWriter appends bits, most significant first.
type bitWriter struct {
	b    []byte
	acc  uint64
	nacc uint
}

func (w *bitWriter) write(v uint64, n uint) {
	w.acc = w.acc<<n | v&(1<<n-1)
	w.nacc += n
	for w.nacc >= 8 {
		w.nacc -= 8
		w.b = append(w.b, byte(w.acc>>w.nacc))
	}
}

// TestBlockSpanning8MiB pads the bench file's first block past 8 MiB with
// no-op code-length deltas: "1 0" (length + 1) then "1 1" (length - 1),
// inserted after the first Huffman table's 5-bit base length. The block's
// bits are unbounded this way, and both decoders still accept it.
func TestBlockSpanning8MiB(t *testing.T) {
	t.Parallel()
	in, err := os.ReadFile(benchFile)
	if err != nil {
		t.Fatal(err)
	}
	want, err := io.ReadAll(stdbzip2.NewReader(bytes.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	level := int(in[3] - '0')

	// Where the first block ends, and where its first table's deltas begin.
	var d Decoder
	br := NewBitReader(bytes.NewReader(in))
	br.ReadBits64(32)
	if _, _, err := d.Block(br, level); err != nil {
		t.Fatal(err)
	}
	firstEnd := br.Offset()

	br = NewBitReader(bytes.NewReader(in))
	br.ReadBits64(32)
	if br.ReadBits64(48) != BlockMagic {
		t.Fatal("no block magic at bit 32")
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
		t.Fatalf("first table's base length %d; the pairs need 1 to 19", base)
	}
	at := br.Offset()

	const padBytes = 8 << 20 // a multiple of 2 bytes: whole 0xBBBB words
	w := &bitWriter{b: make([]byte, 0, len(in)+padBytes+8)}
	for p := uint64(0); p < at; p++ {
		w.write(bitsAt(in, p, 1), 1)
	}
	for i := 0; i < padBytes/2; i++ {
		w.write(0xBBBB, 16) // 1011 1011 1011 1011: four +1 -1 pairs
	}
	p := at
	for ; p%8 != 0; p++ {
		w.write(bitsAt(in, p, 1), 1)
	}
	if w.nacc != 0 {
		t.Fatalf("writer not byte-aligned after the padding: %d bits pending", w.nacc)
	}
	padded := append(w.b, in[p/8:]...)
	if len(padded) != len(in)+padBytes {
		t.Fatalf("padded stream is %d bytes, want %d", len(padded), len(in)+padBytes)
	}

	br = NewBitReader(bytes.NewReader(padded))
	br.ReadBits64(32)
	if _, _, err := d.Block(br, level); err != nil {
		t.Fatalf("padded block: %v", err)
	}
	if got, wantEnd := br.Offset(), firstEnd+8*padBytes; got != wantEnd {
		t.Fatalf("padded block ended at bit %d, want %d", got, wantEnd)
	}
	if got := br.Offset() - 32; got <= 8*(8<<20) {
		t.Fatalf("padded block is %d bits, not longer than 8 MiB", got)
	}

	std, err := io.ReadAll(stdbzip2.NewReader(bytes.NewReader(padded)))
	if err != nil || !bytes.Equal(std, want) {
		t.Fatalf("compress/bzip2 on the padded stream: %d bytes, error %v; want the %d original bytes", len(std), err, len(want))
	}
	got, err := io.ReadAll(NewReader(bytes.NewReader(padded)))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("NewReader on the padded stream: %d bytes, error %v; want the %d original bytes", len(got), err, len(want))
	}
}
