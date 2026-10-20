package mrt_test

import (
	"fmt"
	"log"
	"net/netip"
	"os"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
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

// ExampleRoutesWith keeps a routing table. It loads a RIB dump through an
// Interner, so entries with equal attributes share one Path, drops the
// interner's path index once the RIB is in, and then applies updates: an
// announcement replaces a route, a withdrawal deletes it, and a session that
// leaves Established drops all of its peer's routes. The RouteViews updates
// slice has no state change, so a synthetic session cycle is applied last.
func ExampleRoutesWith() {
	type key struct {
		peer   netip.Addr
		as     bgp.ASN // a peer is its address and AS
		prefix netip.Prefix
		pathID uint32
	}
	table := make(map[key]*bgp.Path)
	in := bgp.NewInterner()
	opts := mrt.RoutesOptions{PeerStates: true, Interner: in}
	var bad, dropped int

	apply := func(name string) {
		f, err := os.Open(name)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		r, err := mrt.Decompress(f)
		if err != nil {
			log.Fatal(err)
		}
		for route, err := range mrt.RoutesWith(mrt.Records(r), opts) {
			if err != nil {
				bad++
				continue
			}
			k := key{route.Peer.Addr, route.Peer.AS, route.Prefix, route.PathID}
			switch route.Kind {
			case mrt.KindRIB, mrt.KindAnnounce:
				table[k] = route.Path // shared and read-only
			case mrt.KindWithdraw:
				delete(table, k)
			case mrt.KindPeerState:
				if route.OldState == mrt.StateEstablished && route.NewState != mrt.StateEstablished {
					for k := range table {
						if k.peer == route.Peer.Addr && k.as == route.Peer.AS {
							delete(table, k)
							dropped++
						}
					}
				}
			}
		}
	}

	apply("testdata/mrt/rv6-rib.20261015.1200.gz")
	fmt.Println(len(table), "routes,", in.Len(), "distinct paths")
	in.DropPathIndex() // the RIBs are in; UPDATE paths are never shared whole

	apply("testdata/mrt/rv6-updates-et.20261015.1200.gz")
	fmt.Println(len(table), "routes after the updates")

	apply("testdata/synthetic/state-changes.mrt")
	fmt.Println(len(table), "routes;", dropped, "dropped by a session reset;", bad, "errors")
	// Output:
	// 24870 routes, 8391 distinct paths
	// 25441 routes after the updates
	// 25441 routes; 1 dropped by a session reset; 0 errors
}
