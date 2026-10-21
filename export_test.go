package mrt

import "io"

// Bzip2Hooks are the parallel bzip2 reader's test hooks. Extra(lo, hi)
// returns bit offsets in [lo, hi), ascending, to add to the candidate block
// starts the scan found there; Discarded is called with every candidate the
// chain never lands on; Took with every block the chain takes, and whether a
// worker's result was used; Held after every dispatch, with the bytes of the
// segment copies the outstanding jobs hold and their count. These run in the
// goroutine that calls Read. Started is called by a worker with the block it
// is about to decode, once it has checked for stop; it runs in the worker's
// goroutine. Any of them may be nil.
type Bzip2Hooks struct {
	Extra     func(lo, hi uint64) []uint64
	Discarded func(bit uint64)
	Took      func(bit uint64, speculative bool)
	Held      func(segBytes int64, outstanding int)
	Started   func(bit uint64)
}

// DecompressWithBzip2Hooks is DecompressWith with the parallel bzip2 reader's
// test hooks.
func DecompressWithBzip2Hooks(r io.Reader, opts DecompressOptions, h Bzip2Hooks) (io.Reader, error) {
	return decompressWith(r, opts, &bzip2Hooks{
		extra: h.Extra, discarded: h.Discarded, took: h.Took, held: h.Held, started: h.Started,
	})
}

// DecompressWithHooks is DecompressWithBzip2Hooks with the first three hooks.
func DecompressWithHooks(r io.Reader, opts DecompressOptions,
	extra func(lo, hi uint64) []uint64, discarded func(bit uint64), took func(bit uint64, speculative bool),
) (io.Reader, error) {
	return DecompressWithBzip2Hooks(r, opts, Bzip2Hooks{Extra: extra, Discarded: discarded, Took: took})
}

// SpecCap is the most output a speculative decode makes.
const SpecCap = specCap

// RaceEnabled reports whether the race detector is on, for tests that cut
// their work under it.
const RaceEnabled = raceEnabled

// GzipQueueBytes and GzipChunkBytes are the gzip readahead's bounds.
const (
	GzipQueueBytes = gzipQueueBytes
	GzipChunkBytes = gzipChunkBytes
)
