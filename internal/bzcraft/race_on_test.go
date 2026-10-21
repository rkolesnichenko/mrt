//go:build race

package bzcraft

// raceEnabled: the race detector slows the decode of a large input about
// fourfold, so TestBuilt skips its compress/bzip2 decode under -race.
const raceEnabled = true
