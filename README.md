# mrt

A streaming reader for MRT routing-information files (RFC 6396, RFC 8050) and a
BGP UPDATE decoder, in Go. It reads what the RIPE RIS and RouteViews route
collectors publish — table dumps and update files, including BGP4MP_ET and
add-path records — and yields typed records or one route per (peer, prefix,
path ID).

It streams with bounded memory, uses `net/netip`, has no dependencies outside
the standard library, never panics on hostile input, keeps going past a
malformed record or attribute, and is checked byte for byte against bgpdump and
field by field against bgpkit-parser.

Status: pre-v1 (0.x), released; see [CHANGELOG.md](CHANGELOG.md) for the
versions. The API may still change between minor versions.

## Install

```sh
go get github.com/rkolesnichenko/mrt                          # the library (Go 1.23+)
go install github.com/rkolesnichenko/mrt/cmd/mrtdump@latest   # the bgpdump -m compatible printer
```

## Routes

```go
f, err := os.Open("updates.20261015.1200.bz2")
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
```

This is `ExampleRoutes` in [example_test.go](example_test.go), which `go test`
runs over a committed fixture (the path differs). A withdrawal has a nil
`Path`, so check `route.Kind` (`KindRIB`, `KindAnnounce`, `KindWithdraw`) before
reading it; a RIB dump's routes are `KindRIB`.

## Holding a table

To keep routes in memory, load the RIB dumps through a `bgp.Interner`: RIB
entries with equal attributes then share one `*bgp.Path`, and paths that differ
share their equal AS paths and community lists. Once the RIBs are in, drop the
interner's whole-path index; UPDATE paths are never shared whole, but they keep
sharing parts. With `PeerStates`, `RoutesWith` also yields a `KindPeerState`
route for each session state change, so a session that goes down can take its
routes with it.

```go
type key struct {
	peer   netip.Addr
	prefix netip.Prefix
	pathID uint32
}
table := make(map[key]*bgp.Path)
in := bgp.NewInterner()
opts := mrt.RoutesOptions{PeerStates: true, Interner: in}

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
			continue // reported and skipped, as with Routes
		}
		k := key{route.Peer.Addr, route.Prefix, route.PathID}
		switch route.Kind {
		case mrt.KindRIB, mrt.KindAnnounce:
			table[k] = route.Path // shared and read-only
		case mrt.KindWithdraw:
			delete(table, k)
		case mrt.KindPeerState:
			if route.OldState == mrt.StateEstablished && route.NewState != mrt.StateEstablished {
				for k := range table {
					if k.peer == route.Peer.Addr {
						delete(table, k)
					}
				}
			}
		}
	}
}

apply("rib.20261020.1400.bz2")
in.DropPathIndex() // the RIBs are in
apply("updates.20261020.1400.bz2")
```

This is `ExampleRoutesWith` in [example_test.go](example_test.go), run by
`go test` over the committed route-views6 RIB and updates slices, then a
synthetic session cycle (the RouteViews updates slice has no state change).
Paths decoded through an interner hold no record bytes, and every route is
deeply equal to the one `Routes` gives without it. The interner is safe for
concurrent use, so one can serve several collectors loaded in parallel; it
grows until it is dropped.

On the 2026-10-20 RIBs of RIS rrc00 (08:00), route-views2 and route-views6
(14:00), loaded into one interner (`internal/heldsize`, log
`.data/heldsize-v0.2.log`): 77,853,052 routes, 12,809,618 distinct paths, and
after `DropPathIndex` 3,886,148,000 live bytes (`HeapAlloc`) retained by `mrt`
(5,266,546,688 bytes of heap spans in use, `HeapInuse`, the resident cost).
The whole-path index cost 1,510,980,216 live bytes before it was dropped, and
the interner's maps of shared parts (their keys and tables, not the shared
arrays) 1,194,734,912 of the 3,886,148,000. The caller's own table is not
counted: the tool's 16-byte stand-in per route is 1,245,648,832 bytes more.
The load took 55.9 s (Go 1.27.2, Apple M3 Max, `GOMAXPROCS=4`,
`GOMEMLIMIT=16GiB`). A `bgp.Path` is 112 bytes (328 in v0.1; log
`.data/pathsize-main.log`).

`mrt.Records` is the layer below: every record, typed (`PeerIndexTable`, `RIB`,
`TableDump`, `BGP4MPMessage`, `BGP4MPStateChange`, or `Other`), each with its
exact bytes in `Raw()`, so concatenating them reproduces the input. The `bgp`
package decodes UPDATE messages and path attributes on their own
(`bgp.ParseUpdate`, `bgp.Attrs.Decode`), for a later BMP or RIS Live reader to
reuse. The exported API is listed in [api/](api/).

## mrtdump

`mrtdump [-m | -M] [-l] [-u] FILE...` prints what `bgpdump -m -l -u` prints,
line for line. See [docs/mrtdump.md](docs/mrtdump.md).

## Oracles

Two independent decoders are the test oracles, both built at pinned versions by
`scripts/build-oracles.sh`: bgpdump (RIPE-NCC/bgpdump master at
`caf2cb07bc3cf06f1720c4cb274811599e9bda6a`) for `mrtdump`'s output, and
bgpkit-parser 0.22.0 for `Routes`, field by field. Their output on every
fixture is committed in `testdata/golden`, so the tests need neither installed;
with them on PATH (or `MRT_ORACLES_REQUIRED=1` in CI) the tests regenerate and
compare. Every difference between mrt and an oracle, with the reason and which
side is right, is in [testdata/bgpdump/divergences.md](testdata/bgpdump/divergences.md),
and a test checks that list in both directions.

On real files (opt-in, `MRT_REALDATA`), `mrtdump -m -l -u` and bgpdump were
compared line by line on 2026-10-16: RIS rrc00's bview (55,579,615 lines),
route-views2's RIB (17,454,476), route-views6's RIB (4,702,913) and the
14:00-14:59 updates of rrc00 and route-views2. Every differing line is a
documented divergence (bgpdump cuts long community fields); none was
unclassified. Source: `.data/realdata-*-r21.log`, kept by the maintainer and
not committed. On 2026-10-20 route-views6's 14:00 RIB was compared again
(4,723,276 lines, 2,097 known divergences S16, 0 unclassified; log
`.data/realdata-rv6-v0.2.log`).

## Performance

On the 2026-10-20 14:00 route-views6 RIB (25,709,978 bytes compressed,
471,448,090 decompressed), `internal/compare` measured `mrt.Routes`, every path
decoded, at 2,055,757,256 bytes allocated in 24,470,640 allocations, and gobgp
v4.10.0 at 3,689,220,904 bytes in 116,626,320 (ratio 0.557), with one route per
gobgp entry (4,723,276) and no errors. `mrt.Records` alone allocates 1.179
bytes per decompressed byte. A full `Routes` pass from the compressed file took
8.102 s, of which bzip2 decompression 6.987 s (86.2%), at normal priority (Go
1.27.2, Apple M3 Max, `GOMAXPROCS=4`; log `.data/compare-v0.2.log`).

v0.1 figures:
On the 2026-10-16 14:00 route-views6 RIB (25,434,533 bytes compressed,
470,078,858 decompressed), `internal/compare` measured bytes allocated while
decoding every entry and attribute: `mrt.Routes` 3,117,745,208, gobgp v4.10.0
3,680,394,472 (ratio 0.847), with one route per gobgp entry (4,702,913) and no
errors. `mrt.Records` alone allocates 1.179 bytes per decompressed byte. Go
1.27.2, Apple M3 Max, `GOMAXPROCS=4` (log `.data/compare-rv6-r21.log`). The
standard library's bzip2 dominates the wall time of a full pass: 24.269 s of
30.748 s, measured under the maintainer's memory watchdog, which runs the
command at macOS background priority (`taskpolicy -b`).

Background priority slows these runs about fourfold. On 2026-10-20 the same
benchmarks over the committed fixtures gave, at normal priority and then under
`taskpolicy -b`: `Routes` over route-views6's RIB slice 441.55 and 101.91 MB/s,
`Records` over route-views2's 6,969.71 and 1,374.06 MB/s, bzip2 decompression
52.63 and 13.70 MB/s (logs `.data/bench-v011-normal.log` and
`.data/bench-v011-background.log`). `Routes` decodes about eight times faster
than bzip2 decompresses, at either priority. (The v0.2 full pass above was timed at normal priority.)

## Data and licence

The code is MIT (see [LICENSE](LICENSE)). The committed fixtures in
`testdata/mrt` are slices of RouteViews archive files, which are CC BY 4.0
(University of Oregon Route Views Project, https://www.routeviews.org); each
is attributed in [testdata/README.md](testdata/README.md). RIPE RIS files are
not redistributed: RIPE NCC's terms do not permit it, so no RIS bytes are in
the repository. RIS files are read only by the opt-in real-data run, which
downloads them at test time.
