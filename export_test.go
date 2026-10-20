package mrt

import "io"

// DecompressWithHooks is DecompressWith with the parallel bzip2 reader's test
// hooks: extra(lo, hi) returns bit offsets in [lo, hi), ascending, to add to
// the candidate block starts the scan found there; discarded is called with
// every candidate the chain never lands on; took with every block the chain
// takes, and whether a worker's result was used. Any of them may be nil. The
// hooks run in the goroutine that calls Read.
func DecompressWithHooks(r io.Reader, opts DecompressOptions,
	extra func(lo, hi uint64) []uint64, discarded func(bit uint64), took func(bit uint64, speculative bool),
) (io.Reader, error) {
	return decompressWith(r, opts, &bzip2Hooks{extra: extra, discarded: discarded, took: took})
}

// RaceEnabled reports whether the race detector is on, for tests that cut
// their work under it.
const RaceEnabled = raceEnabled
