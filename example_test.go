package mrt_test

import (
	"fmt"
	"log"
	"os"

	"github.com/rkolesnichenko/mrt"
)

// ExampleRoutes reads a bzip2-compressed RouteViews updates file and prints
// the first announcement and a count of the routes. The same code reads a RIB
// dump: its routes have Kind mrt.KindRIB.
func ExampleRoutes() {
	f, err := os.Open("testdata/bench/rv2-updates-et.20261015.1200.bz2")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	r, err := mrt.Decompress(f) // gzip, bzip2 or raw, by magic bytes
	if err != nil {
		log.Fatal(err)
	}
	var n, bad int
	first := true
	for route, err := range mrt.Routes(mrt.Records(r)) {
		if err != nil {
			// A bad record or entry is reported and the stream goes on;
			// ErrTruncated or a read error ends it (errors.Is(err, mrt.ErrTruncated)).
			bad++
			continue
		}
		n++
		if route.Kind != mrt.KindAnnounce { // a withdrawal has no Path
			continue
		}
		if first {
			first = false
			origin, _ := route.Path.ASPath.Origin()
			fmt.Printf("%s via AS%d, path %s, origin AS%d\n", route.Prefix, route.Peer.AS, route.Path.ASPath, origin)
		}
	}
	fmt.Println(n, "routes,", bad, "errors")
	// Output:
	// 207.38.90.0/24 via AS57866, path 57866 1299 30083, origin AS30083
	// 25306 routes, 0 errors
}
