package bgp

// HasRare reports whether p carries a rare part (for tests in package bgp_test).
func HasRare(p Path) bool { return p.rare != nil }
