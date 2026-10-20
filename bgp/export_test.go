package bgp

import "unsafe"

// HasRare reports whether p carries a rare part (for tests in package bgp_test).
func HasRare(p Path) bool { return p.rare != nil }

// RareSize is unsafe.Sizeof of the unexported rare part (for TestPathRareSize).
func RareSize() uintptr { return unsafe.Sizeof(pathRare{}) }
