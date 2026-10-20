package bzip2

// This file is not from the Go source. It adds what the copy is for: decoding
// one block from a bit offset and knowing exactly where it ended. NewReader is
// rebuilt on it, so the copy's serial reader is the code that Decoder.Block
// runs; the decoding itself (readBlock, readFromBlock, setup, the Huffman,
// move-to-front and CRC code) is the standard code, unchanged.

import (
	"io"
	"slices"
)

// BlockMagic starts every block and EndMagic ends every stream, 48 bits each.
// Neither is byte-aligned in general.
const BlockMagic, EndMagic uint64 = 0x314159265359, 0x177245385090

// BitReader is the copy's bit reader. Its Read methods return zeros once the
// input ends or fails, and Err then reports it; the end of the input is
// io.ErrUnexpectedEOF. Err reports only the input's errors, never a fault in
// the data.
type BitReader = bitReader

// NewBitReader returns a BitReader at the first bit of r.
func NewBitReader(r io.ByteReader) *BitReader {
	return &BitReader{r: r}
}

// Offset returns the number of bits consumed since the reader was made.
func (br *bitReader) Offset() uint64 {
	return 8*br.read - uint64(br.bits)
}

// peekMagic returns the next 48 bits without consuming them, or 0 with Err
// set if the input ends first. ReadBits64 leaves the bits it returns in br.n
// (at most 55 bits are held), so giving them back is adding 48 to br.bits.
func (br *bitReader) peekMagic() uint64 {
	v := br.ReadBits64(48)
	if br.err == nil {
		br.bits += 48
	}
	return v
}

// readByte is the reader's next byte, read at a byte boundary (br.bits is 0)
// as compress/bzip2 reads the bytes after an end of stream: io.EOF is
// returned as is.
func (br *bitReader) readByte() (byte, error) {
	b, err := br.r.ReadByte()
	if err == nil {
		br.read++
	}
	return b, err
}

// FoldCRC folds a block's CRC into the CRC of its stream so far. The stream's
// trailer holds the fold of all its blocks' CRCs, starting from 0.
func FoldCRC(stream, block uint32) uint32 {
	return (stream<<1 | stream>>31) ^ block
}

// A Decoder decodes blocks. Its zero value is ready to use. It keeps its
// block buffer (up to 3.6 MB, at level 9) between calls; it is not safe for
// concurrent use.
type Decoder struct {
	r reader
}

// growBy is how much a block's output buffer grows at a time. The output is
// never sized from what the input says: RLE can expand a hostile block about
// 51-fold.
const growBy = 64 << 10

// Block decodes the block at br, which must be at the block's magic, for a
// stream of the given level ('1' to '9' in the stream header, here 1 to 9).
// It reads the magic and the block, and returns the block's bytes in a new
// slice, the CRC stored in the block, and the error compress/bzip2 gives for
// this block:
//   - If the bytes do not match the stored CRC, the bytes and the error: the
//     standard reader returns all the bytes and then fails.
//   - If br's input ended or failed (br.Err() is not nil), that error, and the
//     bytes if the block still decoded: the standard reader returns the bytes
//     of one Read call, as many as fit in it, together with the error.
//   - If the block does not decode, no bytes and the error.
//
// Afterwards br.Offset() is the block's end: the bit after its last, where the
// next block magic or the end magic starts.
func (d *Decoder) Block(br *BitReader, level int) (data []byte, storedCRC uint32, err error) {
	if level < 1 || level > 9 {
		return nil, 0, StructuralError("invalid compression level")
	}
	// The standard code reads d.r.br. br is copied in and back out; it may
	// be d.r.br itself (NewReader's reader).
	d.r.br = *br
	data, err = d.block(level)
	*br = d.r.br
	if br.err != nil {
		err = br.err // as the standard reader's Read prefers it
	}
	return data, d.r.wantBlockCRC, err
}

func (d *Decoder) block(level int) ([]byte, error) {
	bz2 := &d.r
	bz2.wantBlockCRC = 0
	if bz2.br.ReadBits64(48) != BlockMagic {
		return nil, StructuralError("bad magic value found")
	}
	// As setup does for a stream header.
	bz2.blockSize = 100 * 1000 * level
	if bz2.blockSize > len(bz2.tt) {
		bz2.tt = make([]uint32, bz2.blockSize)
	}
	if err := bz2.readBlock(); err != nil {
		return nil, err
	}
	var data []byte
	for {
		if len(data) == cap(data) {
			data = slices.Grow(data, growBy)
		}
		n := bz2.readFromBlock(data[len(data):cap(data)])
		if n == 0 {
			break
		}
		data = data[:len(data)+n]
	}
	if updateCRC(0, data) != bz2.wantBlockCRC {
		return data, StructuralError("block checksum mismatch")
	}
	return data, nil
}

// streamReader is NewReader's reader: compress/bzip2's handling of stream
// headers, stream ends and concatenated streams, around Decoder.Block.
type streamReader struct {
	d       Decoder // d.r.br is the stream's bit reader
	started bool    // the first stream header is read
	level   int     // of the current stream
	crc     uint32  // the current stream's CRC so far
	data    []byte  // the rest of the current block
	err     error   // returned once data is drained, and from then on
	errNow  bool    // err comes with the next Read's bytes, the rest dropped
}

func (z *streamReader) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		if len(z.data) == 0 {
			return 0, z.err
		}
		return 0, nil
	}
	for {
		if len(z.data) > 0 {
			n := copy(buf, z.data)
			z.data = z.data[n:]
			if z.errNow {
				z.data = nil
				return n, z.err
			}
			return n, nil
		}
		if z.err != nil {
			return 0, z.err
		}
		z.next()
	}
}

// next reads the next block into data, or ends with err set, as
// compress/bzip2's Read and read do.
func (z *streamReader) next() {
	br := &z.d.r.br
	if !z.started {
		if err := z.header(true); err != nil {
			z.fail(err)
			return
		}
		z.started = true
	}
	for {
		switch br.peekMagic() {
		case BlockMagic:
			data, crc, err := z.d.Block(br, z.level)
			z.crc = FoldCRC(z.crc, crc)
			z.data, z.err = data, err
			z.errNow = br.Err() != nil
			return

		case EndMagic:
			br.ReadBits64(48)
			wantCRC := uint32(br.ReadBits64(32))
			if br.err != nil {
				z.fail(br.err)
				return
			}
			if z.crc != wantCRC {
				z.fail(StructuralError("file checksum mismatch"))
				return
			}
			// Skip ahead to byte boundary. Is there a stream
			// concatenated to this one? It would start with BZ.
			if br.bits%8 != 0 {
				br.ReadBits(br.bits % 8)
			}
			b, err := br.readByte()
			if err == io.EOF {
				z.err = io.EOF
				return
			}
			if err != nil {
				z.fail(err)
				return
			}
			c, err := br.readByte()
			if err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				z.fail(err)
				return
			}
			if b != 'B' || c != 'Z' {
				z.fail(StructuralError("bad magic value in continuation file"))
				return
			}
			if err := z.header(false); err != nil {
				z.fail(err)
				return
			}

		default:
			z.fail(StructuralError("bad magic value found"))
			return
		}
	}
}

// header reads a stream header with the standard setup, after "BZ" if
// needMagic is false.
func (z *streamReader) header(needMagic bool) error {
	if err := z.d.r.setup(needMagic); err != nil {
		return err
	}
	z.level = z.d.r.blockSize / (100 * 1000)
	z.crc = 0
	return nil
}

// fail ends the stream with err, or with the bit reader's error if it has
// one, as compress/bzip2's Read prefers that.
func (z *streamReader) fail(err error) {
	if z.d.r.br.err != nil {
		err = z.d.r.br.err
	}
	z.err = err
}
