package bmp_test

import (
	"fmt"
	"log"
	"os"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
)

// ExampleSession_Routes reads OpenBMP frames, as a Kafka consumer gets them
// (here from a file of concatenated frames; each Kafka message is one frame).
// A Session holds the ADD-PATH state one monitored router's Peer Ups
// establish, so each router hash has its own. The frame's bytes alias the
// buffer the consumer was given: range over the routes before reusing it.
func ExampleSession_Routes() {
	f, err := os.Open("../testdata/bmp/obmp-v4-router.obmp")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	sessions := make(map[[16]byte]*bmp.Session)
	var n, bad int
	first := true
	for frame, err := range openbmp.Frames(f) {
		if err != nil {
			log.Fatal(err) // a read error or a frame the reader cannot delimit
		}
		h, msg, err := openbmp.Parse(frame)
		if err != nil {
			bad++
			continue
		}
		s := sessions[h.RouterHash]
		if s == nil {
			s = bmp.NewSession(bmp.RoutesOptions{Router: h.RouterIP, PeerStates: true})
			sessions[h.RouterHash] = s
		}
		for route, err := range s.Routes(bmp.ParseMessage(msg)) {
			if err != nil {
				// A bad message is reported and the stream goes on.
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
				fmt.Printf("%s via AS%d at %s (%s), path %s, origin AS%d\n",
					route.Prefix, route.Peer.AS, route.Router, route.View, route.Path.ASPath, origin)
			}
		}
	}
	fmt.Println(len(sessions), "routers,", n, "routes,", bad, "errors")
	// Output:
	// 198.51.100.0/24 via AS64510 at 192.0.2.1 (adj-rib-in-pre), path 64501 64502, origin AS64502
	// 1 routers, 32 routes, 0 errors
}

// ExampleRoutesWith reads a byte stream of BMP messages, as a router's TCP
// session carries them (here a file), and counts what comes out by Kind.
// PeerStates yields a KindPeerState route for each Peer Up and Peer Down and
// EndOfRIB one for each End-of-RIB marker. A table keyed by prefix must also
// key by Router, Peer and View: one peer's Adj-RIB-In before and after policy
// are different views of the same prefix.
func ExampleRoutesWith() {
	f, err := os.Open("../testdata/bmp/bmp-types.bmp")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	opts := bmp.RoutesOptions{PeerStates: true, EndOfRIB: true}
	counts := make(map[mrt.Kind]int)
	var bad int
	for route, err := range bmp.RoutesWith(bmp.Messages(f), opts) {
		if err != nil {
			// ErrTruncated or a read error ends the stream (errors.Is).
			bad++
			continue
		}
		counts[route.Kind]++
	}
	fmt.Println(counts, bad, "errors")
	// Output:
	// map[A:1 S:13] 0 errors
}
