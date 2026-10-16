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

Status: pre-v1 (0.x). The API may still change between minor versions. Nothing
is released yet; see [CHANGELOG.md](CHANGELOG.md).

## Install

```sh
go get github.com/rkolesnichenko/mrt                          # the library (Go 1.23+)
go install github.com/rkolesnichenko/mrt/cmd/mrtdump@latest   # the bgpdump -m compatible printer
```

## Routes

```go
f, err := os.Open("updates.20261015.1200.bz2") // or .gz, or raw MRT
if err != nil {
	log.Fatal(err)
}
defer f.Close()

r, err := mrt.Decompress(f) // gzip, bzip2 or raw, by magic bytes
if err != nil {
	log.Fatal(err)
}
for route, err := range mrt.Routes(mrt.Records(r)) {
	if err != nil { // a bad record or entry; the stream goes on
		continue
	}
	origin, _ := route.Path.ASPath.Origin()
	fmt.Printf("%s via AS%d, path %s, origin AS%d\n", route.Prefix, route.Peer.AS, route.Path.ASPath, origin)
}
```

This is `ExampleRoutes` in [example_test.go](example_test.go), which `go test`
runs over a committed fixture. A withdrawal has a nil `Path`; check
`route.Kind` (`KindRIB`, `KindAnnounce`, `KindWithdraw`) before reading it.

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
not committed.

## Performance

On the 2026-10-16 14:00 route-views6 RIB (25,434,533 bytes compressed,
470,078,858 decompressed), `internal/compare` measured bytes allocated while
decoding every entry and attribute: `mrt.Routes` 3,117,745,208, gobgp v4.10.0
3,680,394,472 (ratio 0.847), with one route per gobgp entry (4,702,913) and no
errors. `mrt.Records` alone allocates 1.179 bytes per decompressed byte. Go
1.27.2, Apple M3 Max, `GOMAXPROCS=4` (log `.data/compare-rv6-r21.log`). The
standard library's bzip2 dominates the wall time of a full pass: 24.269 s of
30.748 s.

## Data and licence

The code is MIT (see [LICENSE](LICENSE)). The committed fixtures in
`testdata/mrt` are slices of RouteViews archive files, which are CC BY 4.0
(University of Oregon Route Views Project, https://www.routeviews.org); each
is attributed in [testdata/README.md](testdata/README.md). RIPE RIS files are
not redistributed: RIPE NCC's terms do not permit it, so no RIS bytes are in
the repository. RIS files are read only by the opt-in real-data run, which
downloads them at test time.
