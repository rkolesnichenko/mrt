package mrt

// The parallel bzip2 reader of DecompressWith (v0.3 design §6.2).
//
// The caller's Read does all the reading of r. It keeps a window of the
// compressed bytes, scans it for block magics at every bit offset, and follows
// the chain of exact block ends as internal/bzip2's serial reader does, with
// the same Header, Block and StreamEnd calls. Each block magic the scan finds
// is a candidate block start: a copy of the bytes from it goes to a worker,
// which decodes the block that would start there. When the chain reaches a
// block, it takes a worker's result only if it started exactly there, with the
// stream's level, stayed within its copy and gave at most specCap bytes;
// otherwise the chain decodes the block itself, from the window. A candidate
// the chain never lands on is discarded for its position, whatever its decode
// gave. So every byte and every error comes from the chain, in the order the
// serial reader gives them.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/rkolesnichenko/mrt/internal/bzip2"
)

const (
	// maxSegment is the most compressed input one speculative decode gets.
	// A block whose end is not within it is decoded by the chain.
	maxSegment = 8 << 20
	// segmentBudget is the most the segment copies of outstanding jobs hold in
	// all. A job that would take them past it waits for results to be taken,
	// unless none is outstanding (a segment is at most maxSegment).
	segmentBudget = 32 << 20
	// specCap is the most output a speculative decode makes. A block that
	// decodes to more is decoded by the chain. It is at least twice the
	// largest block of the real files (981,320 bytes, .data/v031-blocks.log),
	// and at least 2 MiB.
	specCap = 2 << 20
	// segmentSlack is how far a segment reaches past the byte of the next
	// magic the scan found, where the block ends if that magic is real.
	segmentSlack = 8
	// fillSize is how much one read of r asks for.
	fillSize = 64 << 10
	// compactMin is the least dead space at the window's start that is worth
	// moving the window down for (it is also at least half the window).
	compactMin = 1 << 20
	// chainSlack: the chain's bit reader holds less than 8 bytes it has read
	// and not consumed, so the bytes chainSlack before its read position are
	// behind the chain.
	chainSlack = 16
	// scanAhead: the scan looks at byte i once the window holds byte
	// i+scanAhead-1 or the input has ended; a magic starting in byte i ends
	// by byte i+6, and the stream header after an end magic by byte i+14.
	scanAhead = 16
	// maxEmptyReads is bufio's limit on reads of r returning nothing.
	maxEmptyReads = 100
	// markSlack: pump reads r no further while more than 4 × workers +
	// markSlack marks lie ahead of the chain. A real file has a block magic
	// per block and at most an end magic per stream, and pump stops at
	// 2 × workers outstanding first; marks pile up only where they cannot be
	// dispatched (end magics in garbage, say). The chain still reads r as it
	// needs, and decodes serially once the speculation has nothing ahead.
	markSlack = 64
)

// bzip2Hooks are test hooks (export_test.go). extra returns bit offsets in
// [lo, hi), ascending, to add to the candidates the scan found there.
// discarded is called with each candidate the chain never lands on, took
// with each block the chain takes and whether a worker decoded it, and held
// after each dispatch with the bytes of segment copies outstanding jobs hold
// and their count. All of these run in Read's goroutine. started is called by
// a worker with the job it is about to decode, once it has checked for stop.
// budget, if not 0, replaces segmentBudget.
type bzip2Hooks struct {
	extra     func(lo, hi uint64) []uint64
	discarded func(bit uint64)
	took      func(bit uint64, speculative bool)
	held      func(segBytes int64, outstanding int)
	started   func(bit uint64)
	budget    int64
}

// A mark is a magic the scan found: a candidate block start, or an end magic,
// which is only a boundary for the candidate before it.
type mark struct {
	bit   uint64
	end   bool
	level int       // of the stream in force at bit, as the scan saw it; 0 unknown
	job   *bzip2Job // nil until dispatched
}

// A bzip2Job is one speculative decode: the block at bit, from seg, a copy of
// the input from the byte holding bit.
type bzip2Job struct {
	bit   uint64
	level int
	seg   []byte
	res   chan bzip2Result // buffered, so a worker never waits on it
}

type bzip2Result struct {
	data  []byte
	crc   uint32
	err   error
	end   uint64 // the bit after the block
	whole bool   // the decode stayed within its segment and under specCap
}

type parallelBzip2 struct {
	ctx     context.Context
	workers int
	budget  int64 // segmentBudget, or the test hook's
	hooks   bzip2Hooks

	// The input: win holds r's bytes from offset winOff; srcErr is the error
	// r returned (io.EOF at its end), kept, after which r is not read again.
	src    *bufio.Reader
	srcErr error
	empty  int // reads of r in a row that returned nothing
	win    []byte
	winOff int64
	rpos   int64 // the next byte the chain reads

	// The scan: scanAt is the next byte it looks at, scanLevel the level of
	// the stream it is in. marks are the magics it found from the chain's
	// position on, in order; every mark before bit dispatchAt is dispatched
	// or not a candidate, and none from there on is dispatched. zombies are
	// dispatched jobs the chain passed, whose results are still to come.
	// outstanding counts dispatched jobs whose results the chain has not
	// received: at most 2 × workers. segHeld is the bytes of their segment
	// copies: at most segmentBudget. quit is closed by stop, before jobs.
	scanInit    bool
	scanAt      int64
	scanLevel   int
	marks       []mark
	dispatchAt  uint64
	found       []mark // scratch for scan
	zombies     []*bzip2Job
	outstanding int
	segHeld     int64
	jobs        chan *bzip2Job // nil until the first dispatch
	quit        chan struct{}
	wg          sync.WaitGroup
	stopped     bool

	// The chain, as internal/bzip2's serial reader keeps it. br reads the
	// window from bit base.
	d      bzip2.Decoder
	br     *bzip2.BitReader // nil until the stream header is read
	base   uint64
	level  int
	crc    uint32
	data   []byte
	err    error
	errNow bool
}

func newParallelBzip2(src *bufio.Reader, opts DecompressOptions, hooks *bzip2Hooks) *parallelBzip2 {
	z := &parallelBzip2{src: src, ctx: opts.Context, workers: opts.Workers, budget: segmentBudget}
	if hooks != nil {
		z.hooks = *hooks
		if hooks.budget != 0 {
			z.budget = hooks.budget
		}
	}
	return z
}

// Read is internal/bzip2's serial Read, and a cancelled Context ends it.
func (z *parallelBzip2) Read(buf []byte) (int, error) {
	if z.err == nil || len(z.data) > 0 {
		if err := z.ctx.Err(); err != nil {
			z.data, z.err = nil, err
			z.stop()
		}
	}
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

// next reads the next block into data, or ends with err set, as the serial
// reader does. Once err is set the workers have exited.
func (z *parallelBzip2) next() {
	if z.br == nil {
		z.br = bzip2.NewBitReader(chainSource{z})
		if z.level, z.err = z.d.Header(z.br); z.err != nil {
			z.stop()
			return
		}
	}
	for {
		z.pump()
		switch z.br.PeekMagic() {
		case bzip2.BlockMagic:
			z.block()
			return
		case bzip2.EndMagic:
			if z.level, z.err = z.d.StreamEnd(z.br, z.crc); z.err != nil {
				z.stop()
				return
			}
			z.crc = 0
		default:
			z.err = bzip2.StructuralError("bad magic value found")
			if err := z.br.Err(); err != nil {
				z.err = err // as the standard reader's Read prefers it
			}
			z.stop()
			return
		}
	}
}

// block takes the block at the chain's position: a worker's result if one is
// usable, or else the chain's own decode.
func (z *parallelBzip2) block() {
	pos := z.base + z.br.Offset()
	res := z.speculation(pos)
	switch {
	case z.err != nil: // cancelled while waiting
	case res != nil:
		if z.hooks.took != nil {
			z.hooks.took(pos, true)
		}
		z.crc = bzip2.FoldCRC(z.crc, res.crc)
		z.data, z.err, z.errNow = res.data, res.err, false
		z.seek(res.end)
	default:
		if z.hooks.took != nil {
			z.hooks.took(pos, false)
		}
		data, crc, err := z.d.Block(z.br, z.level)
		z.crc = bzip2.FoldCRC(z.crc, crc)
		z.data, z.err = data, err
		z.errNow = z.br.Err() != nil
	}
	if z.err != nil {
		z.stop()
	}
}

// speculation returns the worker's result for the block at pos if the chain
// can use it: it started at pos, with the chain's level, stayed within its
// segment, so it read exactly the bits the chain would, and stayed under
// specCap. Candidates before pos are discarded. If the Context is cancelled
// while it waits, it sets err.
func (z *parallelBzip2) speculation(pos uint64) *bzip2Result {
	z.passed(pos)
	if len(z.marks) == 0 || z.marks[0].bit != pos {
		return nil
	}
	m := z.marks[0]
	z.marks[0] = mark{} // so the array keeps no job's segment
	z.marks = z.marks[1:]
	switch {
	case m.job == nil:
		return nil
	case m.level != z.level:
		z.zombies = append(z.zombies, m.job)
		return nil
	}
	z.taken(m.job)
	select {
	case res := <-m.job.res:
		if !res.whole {
			return nil
		}
		return &res
	case <-z.ctx.Done():
		z.data, z.err = nil, z.ctx.Err()
		return nil
	}
}

// seek moves the chain to bit end, past a block a worker decoded.
func (z *parallelBzip2) seek(end uint64) {
	z.rpos = int64(end / 8)
	z.base = end / 8 * 8
	z.br = bzip2.NewBitReader(chainSource{z})
	z.br.ReadBits(uint(end % 8))
}

// passed discards the marks before bit, which the chain has passed.
func (z *parallelBzip2) passed(bit uint64) {
	i := 0
	for ; i < len(z.marks) && z.marks[i].bit < bit; i++ {
		z.discard(&z.marks[i])
	}
	clear(z.marks[:i]) // so the array keeps no job's segment
	z.marks = z.marks[i:]
}

func (z *parallelBzip2) discard(m *mark) {
	if m.end {
		return
	}
	if m.job != nil {
		z.zombies = append(z.zombies, m.job)
	}
	if z.hooks.discarded != nil {
		z.hooks.discarded(m.bit)
	}
}

// pump keeps the workers busy: it scans, dispatches candidates, and reads r
// while that lets more candidates go, up to 2 × workers outstanding, a lead
// over the chain of one segment and 1 MiB per outstanding job,
// 4 × workers + markSlack marks ahead of the chain, and the next candidate
// within segmentBudget.
//
// So the speculation holds at most segmentBudget of segment copies, at most
// 2 × workers results of at most specCap bytes each, and in each worker's
// decode, besides its Decoder's tables, at most specCap and one chunk before
// it gives up (v0.3.1 design §5.3). The window and the chain's own decodes
// are bounded as in v0.3.
func (z *parallelBzip2) pump() {
	for {
		z.scan()
		z.reap()
		if z.dispatch() || z.outstanding >= 2*z.workers || z.srcErr != nil ||
			z.winEnd()-z.rpos >= maxSegment+int64(2*z.workers)<<20 ||
			len(z.marks) > 4*z.workers+markSlack {
			return
		}
		z.fill()
	}
}

// reap drops the zombies whose results have come.
func (z *parallelBzip2) reap() {
	k := 0
	for _, j := range z.zombies {
		select {
		case <-j.res:
			z.taken(j)
		default:
			z.zombies[k] = j
			k++
		}
	}
	clear(z.zombies[k:])
	z.zombies = z.zombies[:k]
}

// taken drops j from the outstanding jobs, once the chain has received its
// result or is about to.
func (z *parallelBzip2) taken(j *bzip2Job) {
	z.outstanding--
	z.segHeld -= int64(len(j.seg))
}

// dispatch sends candidates, in order, to the workers. A candidate's segment
// runs to the next magic found (plus segmentSlack bytes), at most maxSegment
// bytes, and to the end of the input; a candidate with no magic after it waits
// until the window holds a whole segment or the input has ended. A candidate
// whose segment would take segHeld past segmentBudget waits while any job is
// outstanding; dispatch then returns true, held.
func (z *parallelBzip2) dispatch() (held bool) {
	if z.stopped {
		return false
	}
	end := z.winEnd()
	for i := z.undispatched(); i < len(z.marks); i = z.undispatched() {
		if z.outstanding >= 2*z.workers {
			return false
		}
		m := &z.marks[i]
		from := int64(m.bit / 8)
		to := from + maxSegment
		if i+1 < len(z.marks) {
			to = min(to, int64(z.marks[i+1].bit/8)+segmentSlack)
		} else if to > end && z.srcErr == nil {
			return false
		}
		to = min(to, end)
		if z.outstanding > 0 && z.segHeld+(to-from) > z.budget {
			return true
		}
		if z.jobs == nil {
			z.jobs = make(chan *bzip2Job, 2*z.workers)
			z.quit = make(chan struct{})
			z.wg.Add(z.workers)
			for range z.workers {
				go bzip2Worker(z.jobs, z.quit, z.ctx.Done(), z.hooks.started, &z.wg)
			}
		}
		m.job = &bzip2Job{
			bit:   m.bit,
			level: m.level,
			seg:   bytes.Clone(z.win[from-z.winOff : to-z.winOff]),
			res:   make(chan bzip2Result, 1),
		}
		z.jobs <- m.job // never blocks: at most 2 × workers are outstanding
		z.outstanding++
		z.segHeld += to - from
		z.dispatchAt = m.bit + 1
		if z.hooks.held != nil {
			z.hooks.held(z.segHeld, z.outstanding)
		}
	}
	return false
}

// undispatched returns the index of the first candidate not dispatched, or
// len(marks). It moves dispatchAt past the marks before it, which are end
// magics or of unknown level and are never dispatched, so that each mark is
// looked at once.
func (z *parallelBzip2) undispatched() int {
	i, _ := slices.BinarySearchFunc(z.marks, z.dispatchAt, func(m mark, bit uint64) int { return cmp.Compare(m.bit, bit) })
	for ; i < len(z.marks); i++ {
		if m := &z.marks[i]; !m.end && m.job == nil && m.level != 0 {
			break
		}
		z.dispatchAt = z.marks[i].bit + 1
	}
	return i
}

// bzip2Worker decodes jobs with one Decoder, which keeps its buffers between
// blocks, until jobs is closed or done is. Having taken a job, it returns
// without decoding it if quit or done is closed, so no job a worker takes
// after stop or a cancel starts, and each worker finishes at most the block
// it is decoding. A decode gives up past specCap of output or past its segment:
// the chain never takes such a result, and decodes the block itself.
func bzip2Worker(jobs <-chan *bzip2Job, quit, done <-chan struct{}, started func(bit uint64), wg *sync.WaitGroup) {
	defer wg.Done()
	var d bzip2.Decoder
	for {
		select {
		case <-done:
			return
		case j, ok := <-jobs:
			if !ok {
				return
			}
			select {
			case <-quit:
				return
			case <-done:
				return
			default:
			}
			if started != nil {
				started(j.bit)
			}
			br := bzip2.NewBitReader(bytes.NewReader(j.seg))
			br.ReadBits(uint(j.bit % 8))
			data, crc, err := d.SpeculativeBlock(br, j.level, specCap)
			whole := br.Err() == nil && !errors.Is(err, bzip2.ErrOverCap)
			j.res <- bzip2Result{data: data, crc: crc, err: err, end: j.bit/8*8 + br.Offset(), whole: whole}
		}
	}
}

// stop ends the speculation: it discards the marks, closes quit and then jobs,
// drains the jobs no worker has taken, and waits for every worker to exit.
// So no job a worker takes after stop is called starts, and each worker
// finishes at most the block it is decoding. The chain is done with the input
// when it is called.
func (z *parallelBzip2) stop() {
	if z.stopped {
		return
	}
	z.stopped = true
	for i := range z.marks {
		z.discard(&z.marks[i])
	}
	z.marks, z.found, z.zombies, z.win = nil, nil, nil, nil
	z.d = bzip2.Decoder{}
	if z.jobs != nil {
		close(z.quit)
		close(z.jobs)
		for range z.jobs {
		}
		z.wg.Wait()
	}
}

func (z *parallelBzip2) winEnd() int64 { return z.winOff + int64(len(z.win)) }

// fill reads r once into the window, after moving it down if that frees
// enough. r's error is kept, and r is read no more; empty reads end with
// io.ErrNoProgress at bufio's limit.
func (z *parallelBzip2) fill() {
	z.compact()
	z.win = slices.Grow(z.win, fillSize)
	n, err := z.src.Read(z.win[len(z.win) : len(z.win)+fillSize])
	z.win = z.win[:len(z.win)+n]
	switch {
	case err != nil:
		z.srcErr = err
		z.scan() // to the end: the window grows no more
	case n > 0:
		z.empty = 0
	default:
		if z.empty++; z.empty == maxEmptyReads {
			z.srcErr = io.ErrNoProgress
		}
	}
}

// compact drops the window's bytes that nothing needs: those behind the chain,
// the scan and the first candidate not dispatched (a mark of unknown level is
// never dispatched). It scans the window first, so the scan is never behind;
// candidates behind the chain are discarded (they lie inside the block the
// chain is decoding).
func (z *parallelBzip2) compact() {
	z.scan()
	keep := max(z.rpos-chainSlack, z.winOff)
	z.passed(uint64(keep) * 8)
	if i := z.undispatched(); i < len(z.marks) {
		keep = min(keep, int64(z.marks[i].bit/8))
	}
	keep = min(keep, z.scanAt)
	if dead := keep - z.winOff; dead >= compactMin && dead >= int64(len(z.win))/2 {
		z.win = z.win[:copy(z.win, z.win[dead:])]
		z.winOff = keep
	}
}

// magicTable[b] has bit s set when a block magic starting at bit s of a byte
// has b as its next byte, and bit 8+s likewise for the end magic. A magic at
// bit 8i+s covers bits 8-s to 15-s of its 48 in byte i+1, so that byte decides
// whether to look closer.
var magicTable = func() (t [256]uint16) {
	for s := 0; s < 8; s++ {
		t[byte(bzip2.BlockMagic>>(32+s))] |= 1 << s
		t[byte(bzip2.EndMagic>>(32+s))] |= 1 << (8 + s)
	}
	return t
}()

// scan finds the magics in the window from scanAt on, as far as scanAhead
// allows, and appends them to marks with the level of the stream each is in.
func (z *parallelBzip2) scan() {
	end := z.winEnd()
	if !z.scanInit {
		if end < 4 && z.srcErr == nil {
			return
		}
		z.scanInit = true
		if end >= 4 {
			z.scanLevel = headerLevel(z.win[3]) // winOff is 0 until the scan starts
		}
	}
	limit := end - scanAhead
	if z.srcErr != nil {
		limit = end - 5 // a magic starting in byte end-6 is the last one
	}
	lo := z.scanAt
	if limit <= lo {
		return
	}
	found := z.found[:0]
	for i := lo; i < limit; i++ {
		t := magicTable[z.win[i+1-z.winOff]]
		if t == 0 {
			continue
		}
		v := z.load64(i)
		for s := 0; s < 8; s++ {
			if t>>s&0x101 == 0 {
				continue
			}
			p := uint64(i)*8 + uint64(s)
			if p+48 > uint64(end)*8 {
				break
			}
			switch v >> (16 - s) & (1<<48 - 1) {
			case bzip2.BlockMagic:
				found = append(found, mark{bit: p})
			case bzip2.EndMagic:
				found = append(found, mark{bit: p, end: true})
			}
		}
	}
	z.scanAt = limit
	if z.hooks.extra != nil {
		found = withExtra(found, z.hooks.extra(uint64(lo)*8, uint64(limit)*8))
	}
	for i := range found {
		m := &found[i]
		if !m.end {
			m.level = z.scanLevel
			continue
		}
		// A next stream starts after the end magic, the stream CRC and the
		// padding to a byte. Without one, the level stays: either the end
		// magic is false, or no block follows.
		h := int64((m.bit+80+7)/8) - z.winOff
		if h+4 <= int64(len(z.win)) && string(z.win[h:h+3]) == "BZh" {
			if l := headerLevel(z.win[h+3]); l != 0 {
				z.scanLevel = l
			}
		}
	}
	z.marks = append(z.marks, found...)
	z.found = found[:0]
}

// load64 returns the 8 bytes from byte i, big-endian, zeros past the window.
func (z *parallelBzip2) load64(i int64) uint64 {
	b := z.win[i-z.winOff:]
	if len(b) >= 8 {
		return binary.BigEndian.Uint64(b)
	}
	var v uint64
	for k := 0; k < 8; k++ {
		v <<= 8
		if k < len(b) {
			v |= uint64(b[k])
		}
	}
	return v
}

// headerLevel is the level of a stream header's level digit, or 0.
func headerLevel(b byte) int {
	if b < '1' || b > '9' {
		return 0
	}
	return int(b - '0')
}

// withExtra merges the test hook's candidates into found, both ascending.
func withExtra(found []mark, extra []uint64) []mark {
	if len(extra) == 0 {
		return found
	}
	out := make([]mark, 0, len(found)+len(extra))
	i := 0
	for _, m := range found {
		for ; i < len(extra) && extra[i] <= m.bit; i++ {
			if extra[i] < m.bit {
				out = append(out, mark{bit: extra[i]})
			}
		}
		out = append(out, m)
	}
	for ; i < len(extra); i++ {
		out = append(out, mark{bit: extra[i]})
	}
	return out
}

// chainSource is the chain's view of the input: the window, grown from r as
// the chain reads past its end.
type chainSource struct{ z *parallelBzip2 }

func (s chainSource) ReadByte() (byte, error) {
	z := s.z
	for z.rpos >= z.winEnd() {
		if z.srcErr != nil {
			return 0, z.srcErr
		}
		z.fill()
	}
	b := z.win[z.rpos-z.winOff]
	z.rpos++
	return b, nil
}
