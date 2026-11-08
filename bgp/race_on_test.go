//go:build race

package bgp

// raceEnabled: under the race detector sync.Pool drops items at random, so
// anything that formats (fmt's printer pool) allocates a varying amount; the
// allocation counts skip under -race.
const raceEnabled = true
