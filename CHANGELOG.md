# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module follows [Semantic Versioning](https://semver.org/): until v1.0.0,
a minor version may change the API.

To release, `scripts/release.sh` needs the `[Unreleased]` heading renamed to
`## [0.1.0] - YYYY-MM-DD` and a link line `[0.1.0]: https://github.com/rkolesnichenko/mrt/releases/tag/v0.1.0`
at the end of this file. The date is set when the release is made.

## [Unreleased]

First release: 0.1.0. Every figure below comes from a command run on
2026-10-16 (Go 1.27.2, Apple M3 Max, `GOMAXPROCS=4`); the log is named.

### Added

- `bgp`: a BGP UPDATE and path-attribute decoder, with no knowledge of MRT
  beyond a decoding `Context` (`AS4`, `AddPath`, `RIB`).
  - `ParseUpdate`, `ParseMessageHeader`, `ParseNLRI`, `Attrs.All` and
    `Attrs.Decode` into a `Path`: ORIGIN, AS_PATH, NEXT_HOP, MED, LOCAL_PREF,
    ATOMIC_AGGREGATE, AGGREGATOR, COMMUNITIES, extended and large
    communities, ONLY_TO_CUSTOMER (RFC 9234), ORIGINATOR_ID, CLUSTER_LIST,
    MP_REACH_NLRI and MP_UNREACH_NLRI; every other attribute raw in
    `Path.Unknown`.
  - AS width comes from the context, never from the bytes. AS4_PATH and
    AS4_AGGREGATOR are merged by RFC 6793 section 4.2.3 for 2-byte input.
  - A malformed attribute never stops decoding: its bytes go to `Unknown` and a
    `Diagnostic` with a stable code (`bgp/attr-length`, `bgp/attr-duplicate`,
    `bgp/message-truncated`, ...) names it. A BGP message cut short is read as
    far as its bytes go.
  - Lengths are checked before anything is allocated.
- `mrt`: the reader.
  - `Records` and `RecordsWith` stream `PeerIndexTable`, `RIB`, `TableDump`,
    `BGP4MPMessage`, `BGP4MPStateChange` and `Other`, for TABLE_DUMP,
    TABLE_DUMP_V2, BGP4MP and BGP4MP_ET, including the RFC 8050 add-path
    subtypes and RIB_GENERIC. Each record owns its bytes; concatenating `Raw()`
    reproduces the input. A bad record is a `*RecordError` and the stream goes
    on; a record over `MaxRecordLen` is skipped unbuffered; a header that
    claims more bytes than the stream holds costs at most about twice the bytes
    present.
  - `Routes` yields one `Route` per (peer, prefix, path ID) from RIB entries and
    UPDATEs, with the path decoded once per entry or UPDATE and shared. A RIB
    entry or an UPDATE that a fault leaves without routes is a `*RecordError`
    (offset unknown), never silence.
  - `Decompress` detects gzip (multistream too) and bzip2 by magic bytes.
- `cmd/mrtdump`: `mrtdump [-m | -M] [-l] [-u] FILE...`, bgpdump `-m`'s output
  line for line (docs/mrtdump.md). Exit status 0, 1 (malformed input
  reported on stderr), 2 (bad command line), 3 (unreadable file).
- Tooling.
  - `scripts/check.sh` (gofmt, build, vet, `-race` tests, coverage, no `net`
    import, no `require` in go.mod, the API golden, staticcheck, govulncheck),
    `scripts/fuzz.sh` (five fuzz targets, shardable), `scripts/build-oracles.sh`
    (bgpdump at `caf2cb07bc3cf06f1720c4cb274811599e9bda6a`, bgpkit-parser
    0.22.0), `scripts/fetch-fixtures.sh`, `scripts/fetch-mrt.sh` and
    `scripts/release.sh`.
  - `api/mrt.txt` and `api/bgp.txt`: every exported signature, held by
    `go run ./internal/apisurface -check`.
  - Eight RouteViews fixture slices, thirteen synthetic ones written by
    `internal/mrtgen`, and the oracles' output on all of them in
    `testdata/golden`; `testdata/bgpdump/divergences.md` lists every
    difference from the oracles (B1-B8, K1-K7, S1-S17, C1-C5) and a test checks
    it in both directions.
  - `internal/compare`: a nested module that measures `mrt` against gobgp.

### Measured

- Real data, `mrtdump -m -l -u` against `TZ=UTC bgpdump -m -l -u`, line by
  line (`TestRealData`, logs `.data/realdata-*-r21.log`), 0 unclassified
  differences in every file:

  | file (2026-10-16) | records | lines compared | known divergence S16 |
  |---|---|---|---|
  | RIS rrc00 bview.0800 | 1,430,233 | 55,579,615 | 33,552 |
  | route-views2 rib.1400 | 1,124,621 | 17,454,476 | 4,055 |
  | route-views6 rib.1400 | 265,789 | 4,702,913 | 2,249 |
  | 16 updates files (rrc00 x12, route-views2 x4), 14:00-14:59 | not summed | not summed | 1,862 |

  No record errors in the three RIBs. Hand check on route-views6: bgpkit-parser
  0.22.0 counts 265,789 records (`.data/handcheck-records.log`) and 4,702,913
  elements (`.data/handcheck-elems.log`), the same as mrt.
- Against gobgp v4.10.0 on that route-views6 RIB (25,434,533 bytes compressed,
  470,078,858 decompressed; `.data/compare-rv6-r21.log`), `mrt.Routes` with
  every path decoded allocates 3,117,745,208 bytes in 23,962,058 allocations
  and gobgp 3,680,394,472 bytes in 116,287,098 (ratio 0.847), one route per
  gobgp entry (4,702,913) and no errors. `mrt.Records` alone allocates 1.179
  bytes per decompressed byte. bzip2 is 24.269 s of a 30.748 s full pass.
- Benchmarks (`.data/bench-r20.log`): `Records` over a route-views2 RIB slice
  1,333.56 MB/s; `Routes` over route-views6's 95.83 MB/s, 16,763,052 B/op;
  `BenchmarkBzip2` 13.72 MB/s.

### Known limits

- RIS files are not committed (RIPE NCC's terms); they are exercised only by
  the opt-in real-data run.
- bgpdump cuts long community fields and AS paths (S16, S17); the part of
  mrtdump's output past the cut is checked against no oracle.
- RFC 7606 actions (treat-as-withdraw and so on) are not applied; the
  diagnostics carry the faults.
