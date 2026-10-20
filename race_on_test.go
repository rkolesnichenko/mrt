//go:build race

package mrt

// raceEnabled: the race detector inflates allocation, so tests that measure
// TotalAlloc skip their bound under -race.
const raceEnabled = true
