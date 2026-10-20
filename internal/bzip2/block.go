package bzip2

// This file is not from the Go source. It adds what the copy is for: decoding
// one block from a bit offset and knowing exactly where it ended
// (Decoder.Block), and the standard stream handling as entry points of their
// own (Decoder.Header, Decoder.StreamEnd), so that a reader built on them
// needs none of its own. NewReader is rebuilt on them, so the copy's serial
// reader runs that code. The decoding itself (readBlock, readFromBlock, setup,
// the Huffman, move-to-front and CRC code) is the standard code, unchanged.

import "io"

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

// PeekMagic returns the next 48 bits without consuming them, or 0 with Err
// set if the input ends first. ReadBits64 leaves the bits it returns in br.n
// (at most 55 bits are held), so giving them back is adding 48 to br.bits.
func (br *bitReader) PeekMagic() uint64 {
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
// block buffer (up to 3.6 MB, at level 9) and its output chunks (2 MiB)
// between calls; it is not safe for concurrent use.
type Decoder struct {
	r      reader
	chunks [][]byte // a block's bytes, before they are copied out
}

// A block's bytes are decoded into chunks of chunkSize, and then copied into
// one slice of their exact length. So the output is never sized from what
// the input says (RLE can expand a hostile block about 51-fold), and a block
// costs at most twice its size in memory, once in chunks and once in the
// slice returned. The Decoder keeps keepChunks chunks between calls, which
// hold a real block (about 1 MB), so it then allocates only the returned
// slice.
const (
	chunkSize  = 1 << 20
	keepChunks = 2
)

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
	// The standard code reads d.r.br, so br is copied in and back out.
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
	n := 0
	for i := 0; ; i++ {
		if i == len(d.chunks) {
			d.chunks = append(d.chunks, make([]byte, chunkSize))
		}
		m := bz2.readFromBlock(d.chunks[i]) // fills the chunk unless the block ends
		n += m
		if m < chunkSize {
			break
		}
	}
	data := make([]byte, 0, n)
	for _, c := range d.chunks {
		data = append(data, c[:min(len(c), n-len(data))]...)
	}
	if len(d.chunks) > keepChunks {
		clear(d.chunks[keepChunks:])
		d.chunks = d.chunks[:keepChunks]
	}
	if updateCRC(0, data) != bz2.wantBlockCRC {
		return data, StructuralError("block checksum mismatch")
	}
	return data, nil
}

// Header reads a stream header at br: "BZh" and the level digit. It returns
// the level, 1 to 9, and the error compress/bzip2 gives for a bad header.
// Afterwards br is at the stream's first block magic, or at its end magic if
// it has no block.
func (d *Decoder) Header(br *BitReader) (level int, err error) {
	return d.header(br, true)
}

// header runs the standard setup on br, after "BZ" if needMagic is false.
func (d *Decoder) header(br *BitReader, needMagic bool) (int, error) {
	d.r.br = *br
	err := d.r.setup(needMagic)
	*br = d.r.br
	if br.err != nil {
		err = br.err // as the standard reader's Read prefers it
	}
	if err != nil {
		return 0, err
	}
	return d.r.blockSize / (100 * 1000), nil
}

// StreamEnd reads a stream's end at br, which must be at the end magic: the
// magic, the stream CRC, which must equal streamCRC (the FoldCRC of the
// stream's blocks' stored CRCs), and then what compress/bzip2 accepts after
// an end of stream:
//   - the end of the input: nextLevel 0 and io.EOF;
//   - another stream, "BZh" and a level digit: its level, with br at its
//     first block magic (or its end magic);
//   - anything else, one byte then the end of the input included: 0 and the
//     error the standard reader gives (io.ErrUnexpectedEOF for one byte).
//
// Telling the end magic from a block magic is the caller's: br.PeekMagic.
func (d *Decoder) StreamEnd(br *BitReader, streamCRC uint32) (nextLevel int, err error) {
	nextLevel, err = d.streamEnd(br, streamCRC)
	if br.err != nil {
		err = br.err // as the standard reader's Read prefers it
	}
	if err != nil {
		nextLevel = 0
	}
	return nextLevel, err
}

func (d *Decoder) streamEnd(br *BitReader, streamCRC uint32) (int, error) {
	if br.ReadBits64(48) != EndMagic {
		return 0, StructuralError("bad magic value found")
	}
	wantCRC := uint32(br.ReadBits64(32))
	if br.err != nil {
		return 0, br.err
	}
	if streamCRC != wantCRC {
		return 0, StructuralError("file checksum mismatch")
	}
	// Skip ahead to byte boundary. Is there a stream concatenated to this
	// one? It would start with BZ.
	if br.bits%8 != 0 {
		br.ReadBits(br.bits % 8)
	}
	b, err := br.readByte()
	if err != nil {
		return 0, err // io.EOF as is: the input ends here
	}
	c, err := br.readByte()
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	if b != 'B' || c != 'Z' {
		return 0, StructuralError("bad magic value in continuation file")
	}
	return d.header(br, false)
}

// streamReader is NewReader's reader: compress/bzip2's Read, made of
// Decoder.Header, Decoder.Block and Decoder.StreamEnd.
type streamReader struct {
	br      BitReader
	d       Decoder
	started bool   // the first stream header is read
	level   int    // of the current stream
	crc     uint32 // the current stream's CRC so far
	data    []byte // the rest of the current block
	err     error  // returned once data is drained, and from then on
	errNow  bool   // err comes with the next Read's bytes, the rest dropped
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
	br := &z.br
	if !z.started {
		if z.level, z.err = z.d.Header(br); z.err != nil {
			return
		}
		z.started = true
	}
	for {
		switch br.PeekMagic() {
		case BlockMagic:
			data, crc, err := z.d.Block(br, z.level)
			z.crc = FoldCRC(z.crc, crc)
			z.data, z.err = data, err
			z.errNow = br.Err() != nil
			return
		case EndMagic:
			if z.level, z.err = z.d.StreamEnd(br, z.crc); z.err != nil {
				return
			}
			z.crc = 0
		default:
			z.err = StructuralError("bad magic value found")
			if br.err != nil {
				z.err = br.err // as the standard reader's Read prefers it
			}
			return
		}
	}
}
