//go:build race

package openbmp_test

// raceEnabled: the race detector inflates allocation, so the allocation
// counts skip under -race.
const raceEnabled = true
