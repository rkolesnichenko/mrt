# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module follows [Semantic Versioning](https://semver.org/): until v1.0.0,
a minor version may change the API.

## [Unreleased]

## [0.3.0] - 2026-10-21

Faster decompression. Every figure below comes from a command run on
2026-10-20 or 2026-10-21 with Go 1.27.2 on an Apple M3 Max, `GOMAXPROCS=4`,
and names its log; the first line of the baseline and gate logs
(`.data/decompress-v03-*.log`) records the settings.

### Added

- `mrt.DecompressWith(r, DecompressOptions{Workers, Context})`.
  `DecompressWith(r, DecompressOptions{})` is `Decompress(r)`. With
  `Workers > 0` a bzip2 input is decoded block by block on `Workers`
  goroutines, along the exact block ends (a block is taken from a speculative
  decode only when it starts where the previous block ended, else it is
  decoded serially), and a gzip input is decompressed one goroutine ahead of
  the reader, at most 256 KiB of output. The bytes read, and the error where
  `Decompress` fails, are the same. `Context` is required when `Workers > 0`
  and `Workers < 0` is an error; cancel the `Context` when you stop reading
  before EOF or an error. The API diff (`api/mrt.txt`): `func DecompressWith(r
  io.Reader, opts DecompressOptions) (io.Reader, error)` and `type
  DecompressOptions struct { Workers int; Context context.Context }`.
- `internal/bzip2`: a copy of Go 1.27.2's `compress/bzip2` (BSD-3, The Go
  Authors; the licence is kept in `internal/bzip2/LICENSE`) with block-level
  entry points. The root module still has no `require`.
  `FuzzBzip2` checks its serial reader against `compress/bzip2`, and
  `FuzzDecompress` checks `DecompressWith` against `Decompress`; both are in
  `scripts/fuzz.sh`.

### Changed

- `mrtdump`, `internal/heldsize` and `mrtcut` read through `DecompressWith`.

### Measured

- Before: decompression was 84% of a full `Routes` pass over rv6-rib, 87%
  over rv2-rib and 41% over rrc00-bview (`.data/decompress-v03-baseline.log`).
- bzip2, `Workers: 4` (`.data/decompress-v03-gate-bzip2-fix.log`). Decompress
  only: rv2-rib 20.59 s to 5.39 s (3.82x), rv6-rib 7.44 s to 1.90 s (3.91x).
  Decompress plus `Routes`: rv2-rib 22.95 s to 6.09 s (3.77x), rv6-rib 8.35 s
  to 2.12 s (3.95x). The output hashes are identical and the route counts
  match (17,471,006 and 4,723,276, 0 errors).
- gzip, `Workers: 4` (`.data/decompress-v03-gate-gzip.log`), rrc00-bview:
  decompress plus `Routes` 13.94 s to 9.51 s (1.47x). Decompress only, a
  caller that just copies the bytes is slower: 5.68 s to 6.81 s (0.83x). The
  output is identical.
- Equivalence on real data: `DecompressWith` (4 workers) and `Decompress` give
  identical bytes on all 19 files of the real-data run (4 rv2 and 12 rrc00
  updates files, rv6-rib, rv2-rib, rrc00-bview;
  `.data/realdata-decompress-v03.log`).
- On a small file the parallel path is faster but allocates about four times
  as much. The two-block bench file reads at about 52 MB/s with `Decompress`
  and about 74 MB/s with `DecompressWith` (`BenchmarkBzip2`,
  `BenchmarkDecompressWith`), and allocates 15.4 MB per operation against
  3.7 MB (`.data/bench-v03.log`). That each worker keeping its own decoder
  and buffers accounts for the difference is not measured.

### Not measured

`Workers` other than 4, other machines, resident memory (RSS), and several
files being loaded in parallel at once. By design a block in flight takes
about twice its decoded size, so the worst case for hostile input is
`2 × Workers × (8 MiB + ~2 × 46.6 MB)`; the typical case is about
`2 × Workers × 1 MB`. These are bounds from the design (§6.2), not
measurements.

### Consumer check

routecheck v0.6.0 pins mrt v0.1.0 and does not build against v0.2.0 or later,
because `bgp.Path.NextHop`, `MPUnreach` and `ReceivedASPath` became methods in
v0.2. Built in a throwaway copy against this branch and against v0.2.1, it
fails with the same errors in both (in `observe/session.go` and
`internal/mrtgen`), none about `Decompress`. v0.3 adds nothing to break it.

## [0.2.1] - 2026-10-20

Tests and internal tools only: no change to the API (`api/` is unchanged) or
to what the library decodes.

### Fixed

- `FuzzRecords` and `FuzzRoutes` barely fuzzed. Every new input was minimized
  for up to Go's default 60 s, during which nothing else ran, and 64 KiB seeds
  made that slow: in the v0.2.0 check they ran 77 and 71 executions in 15 s
  (`.data/check-v0.2-final.log`). Seeds are now cut at 4 KiB (still whole
  records of every kind) and `scripts/fuzz.sh` bounds minimization to 2 s
  (`FUZZMINIMIZETIME`). The v0.2.1 check, with the hardened `FuzzRoutes` below,
  ran 1,403,198 and 342,767 in 15 s (`.data/check-v021.log`).
- mrtdump's hand-built test inputs are now synthetic fixtures
  (`testdata/synthetic/mrtdump-*.mrt`) with goldens from both oracles, so the
  expected lines are checked against bgpdump and bgpkit-parser. The new
  differences are documented: examples under B4 and B5, and K8 (a non-empty
  ATOMIC_AGGREGATE), K9 (`*_LOCAL` messages, which `Routes` skips) and K10
  (abbreviated MP_REACH_NLRI in a legacy TABLE_DUMP record).

### Changed

- Tests from the v0.2 reviews: `Path.Clone` is checked by reflection for every
  reference field; the rare part's size is pinned; the interner keeps contexts
  apart by `AddPath` too and shares paths across RIB blocks of the rv6-rib
  fixture;
  peer-state events are checked field by field, also with an interner in
  `FuzzRoutes`; `Route.Originated` is tested on RIB_GENERIC; the diagnostic-code
  test also rejects any `Diagnostic` whose code is not a `Diag*` constant.

## [0.2.0] - 2026-10-20

Routes a reconciliation library can hold. Every figure below comes from a
command run on 2026-10-20 with Go 1.27.2 on an Apple M3 Max
(`.data/machine-v0.2.log`); the log is named. Each entry says which
environment settings its log records.

### Added

- `bgp.Interner` (`NewInterner`, `(*Interner).Decode`, `Len`,
  `DropPathIndex`): decodes attribute blocks into shared paths. RIB entries
  (TABLE_DUMP_V2 and TABLE_DUMP) with equal attributes get one `*Path`; every
  path shares its equal AS paths and community, extended community and large
  community lists. Blocks with a Warning or Error diagnostic, and UPDATE
  blocks, are decoded fresh and share only those parts. Every path it returns
  is a `Clone` and deep-equals what `Attrs.Decode` gives. Safe for concurrent
  use. `DropPathIndex` frees the whole-path index after the RIBs are loaded.
  `Decode` returns a pointer, a noted exception to "parsed values are values"
  (design §3).
- `mrt.RoutesWith` and `RoutesOptions{PeerStates, Interner}`. `PeerStates`
  yields a `KindPeerState` route (`Kind.String()` "S", with `Route.OldState`
  and `Route.NewState`) for every BGP4MP state change, in stream order among
  the routes. `Interner` decodes the paths through a `bgp.Interner`.
- `Route.Originated`: the entry's origination time for routes from
  TABLE_DUMP_V2 and TABLE_DUMP entries.
- `bgp.Path.Clone`: a deep copy that shares no memory with the attribute
  block or the original.
- Typed extended communities: `ExtCommunity.Type`, `Subtype`, `Transitive`,
  `RouteTarget` and `RouteOrigin` (`ExtCommunityAdmin`), `Color`,
  `Encapsulation`, `LinkBandwidth`, `OriginValidation` (`ValidationState`:
  `ValidationValid`, `ValidationNotFound`, `ValidationInvalid`, with `String`;
  the values are RFC 8097's wire encoding, so the zero value is
  `ValidationValid`), and `String`
  (`rt:AS:local`, `ro:a.b.c.d:local`, `color:N`, `encap:N`, `lbw:AS:F`,
  `ov:valid`, or `0x` and the 8 bytes in hex when a label would hide bytes).
- The diagnostic codes as constants: `bgp.DiagMessageHeader`,
  `DiagNotUpdate`, `DiagUpdateLength`, `DiagMessageTruncated`,
  `DiagNLRILength`, `DiagNLRIFamily`, `DiagPrefixBits`, `DiagAttrTruncated`,
  `DiagAttrLength`, `DiagAttrDuplicate`, `DiagAttrValue`, `DiagASPathWidth`,
  `DiagAS4PathIgnored`. The values are unchanged.
- `ExampleRoutesWith` and a README section on holding a table; and
  `internal/heldsize`, which measures what a held table of full RIBs retains.

### Changed

- **Breaking: `bgp.Path` is compact**, 112 bytes on 64-bit platforms instead
  of 328 (`.data/heldsize-v0.2.log`, `.data/pathsize-main.log`; 68 bytes on
  32-bit platforms, pinned by `TestPathSize`). The
  rare attributes moved behind one unexported pointer, nil when a path has
  none of them, and these fields became methods:
  - `NextHop` → `NextHop() netip.Addr` (zero when absent);
  - `ReceivedASPath` → `ReceivedASPath() ASPath` (equal to `ASPath` unless
    AS4_PATH was merged);
  - `AS4Path` → `AS4Path() ASPath`;
  - `Aggregator` and `HasAggregator` → `Aggregator() (Aggregator, bool)`;
  - `ExtCommunities` → `ExtCommunities() []ExtCommunity`;
  - `OriginatorID` → `OriginatorID() netip.Addr`;
  - `ClusterList` → `ClusterList() []netip.Addr`;
  - `MPUnreach` → `MPUnreach() *MPUnreach`;
  - `Unknown` → `Unknown() []Attribute`.

  The fields that stay (`ASPath`, `Communities`, `LargeCommunities`,
  `MPReach`, `MED`, `LocalPref`, `OnlyToCustomer`, `Origin`, `HasOrigin`,
  `HasMED`, `HasLocalPref`, `AtomicAggregate`, `HasOnlyToCustomer`) are in a
  new order. A `Path` is now produced only by decoding. NEXT_HOP and the rare
  attributes are unexported fields now, so `encoding/json` (and anything else
  that reflects over exported fields) no longer marshals them; `Path` has no
  `MarshalJSON`.
- **Ruling R20 amended:** in a RIB entry, a full-form (RFC 4760)
  MP_REACH_NLRI keeps its family and next hops but not its NLRI:
  `MPReach.RawNLRI` is nil (it held the NLRI bytes, aliasing the record).
  route-views6 writes this form on every entry. The record supplies the
  prefix, and the bytes stay in `Attrs.Raw()`. Outside a RIB nothing changed.
- Tests: the bgpkit-parser oracle compares extended communities, by their
  labels; three bgpkit goldens were re-recorded from bgpkit-parser 0.22.0 and
  gained them (`otc-large`, `rv2-updates-et`, `rv6-updates-et`).
- Documentation: `Routes`, `RoutesOptions` and `Route` say what is shared;
  `testdata/README.md` describes R20 as amended.

### Measured

- Held table (`internal/heldsize`, log `.data/heldsize-v0.2.log`): the
  2026-10-20 RIBs of RIS rrc00 (bview 08:00), route-views2 and route-views6
  (14:00), loaded through `RoutesWith` into one `Interner`: 77,853,052
  routes, 0 record errors, 12,809,618 distinct paths (`Len`). After
  `DropPathIndex`, `mrt` retains 3,886,148,000 live bytes (`HeapAlloc`).
  `HeapInuse`, the bytes in the Go heap's spans that hold any object, is
  5,266,546,688; resident memory (RSS) was not measured. Of the live bytes, the
  interner's maps of shared parts are 1,194,734,912 (`HeapAlloc`); the paths
  with the arrays they share are 2,691,413,088. The whole-path index cost
  1,510,980,216 more before it was dropped. The caller's table is not
  counted (the tool's 16-byte stand-in per route: 1,245,648,832 bytes). The
  load took 55.9 s. The run's GOMAXPROCS and GOMEMLIMIT were not recorded in
  its log (the plan, `docs/superpowers/plans/2026-10-20-mrt-v0.2.md` Task 9
  Step 1, runs it as Task 3 Step 3 does, with `GOMAXPROCS=4
  GOMEMLIMIT=16GiB`, which is what was intended); Go 1.27.2 on an Apple M3
  Max is from `.data/machine-v0.2.log`, recorded the same day. `heldsize` now
  prints its Go version, GOOS/GOARCH, GOMAXPROCS and memory limit first, so
  future runs record their environment.
- Against gobgp v4.10.0 on the route-views6 RIB (25,709,978 bytes
  compressed, 471,448,090 decompressed; `.data/compare-v0.2.log`, which
  records `GOMAXPROCS 4`):
  `mrt.Routes` with every path decoded allocates 2,055,757,256 bytes in
  24,470,640 allocations and gobgp 3,689,220,904 bytes in 116,626,320 (ratio
  0.557; 0.1.0 reported 0.847 on the 2026-10-16 file), one route per gobgp
  entry (4,723,276), no errors.
  `mrt.Records` alone allocates 1.179 bytes per decompressed byte. A full
  `Routes` pass from the compressed file took 8.102 s at normal priority, of
  which bzip2 6.987 s (86.2%).
- Real data, route-views6 RIB, `mrtdump -m -l -u` against bgpdump
  (`TestRealData`, log `.data/realdata-rv6-v0.2.log`): 265,734 records, 0
  record errors, 4,723,276 lines compared, 2,097 known divergences S16, 0
  unclassified.

## [0.1.1] - 2026-10-20

### Fixed

- `Records` and `RecordsWith`: ranging over the sequence again after a
  `break` goes on with the next record. It used to start a new read buffer,
  drop the bytes the first one had read ahead and count offsets from 0 again;
  the rest of the stream was then skipped without an error, or read from
  mid-record and reported as one `ErrTruncated`. Once the stream has ended, ranging
  again yields nothing. `Routes` likewise keeps its peer index table across
  ranges; the routes left in the record it broke in are not yielded.
- `mrtdump` stops at the first failed write to standard output (exit status
  3), instead of decoding the rest of the input first.

### Changed

- Documentation: `docs/mrtdump.md` said `*_LOCAL` messages print no line;
  mrtdump prints them as `BGP4MP_LOCAL` (only `Routes` skips them). Exit
  status 3 also covers a failed write. A header that lies about its length
  allocates, in total, about four times the bytes present and under five, not
  about twice (`TestRecordsLyingLengthAllocation`). README no longer says nothing is
  released.
- The 0.1.0 throughput figures (`Records` 1,333.56 MB/s, `Routes` 95.83 MB/s,
  bzip2 13.72 MB/s, and the 30.748 s full pass) were measured at macOS
  background priority. On 2026-10-20, at normal priority, the same benchmarks
  gave 6,969.71, 441.55 and 52.63 MB/s, and under `taskpolicy -b` 1,374.06,
  101.91 and 13.70 MB/s (logs `.data/bench-v011-normal.log`,
  `.data/bench-v011-background.log`). Allocation figures are unaffected.
- Tests: `TestMatchesBgpdump` fails when bgpdump is killed by a signal on a
  fixture other than the one it is known to abort on (B2), so an oracle that
  dies at once no longer passes. `TestRealData` checks each file's size and
  sha256 against MANIFEST. Two tests that failed only on Windows (a path
  separator, and process-wide allocation measured around a single decode) are
  fixed.
- CI: tests also run on macOS, Windows and linux/386; `internal/compare` is
  vetted and tested; a failing fuzz job uploads its inputs.
  `.gitattributes` turns off end-of-line conversion.

## [0.1.0] - 2026-10-16

First release. Every figure below comes from a command run on
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

[Unreleased]: https://github.com/rkolesnichenko/mrt/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/rkolesnichenko/mrt/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/rkolesnichenko/mrt/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/rkolesnichenko/mrt/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/rkolesnichenko/mrt/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/rkolesnichenko/mrt/releases/tag/v0.1.0
