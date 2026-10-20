# mrt — MRT reader and BGP UPDATE decoder for Go

Design and rationale: `docs/superpowers/specs/2026-10-16-mrt-design.md` (the source of truth; the
rulings below amend it where they say so). This file is the operating contract.

## What we're building

`github.com/rkolesnichenko/mrt`: a dependency-free Go module that reads the MRT files RIPE RIS and
RouteViews publish (RFC 6396, RFC 8050) and decodes BGP UPDATEs. Output is typed `Record`s, or one
`Route` per (peer, prefix, path ID). `mrtdump` is a `bgpdump -m` compatible printer, the subject of
the differential test.

## Non-negotiable principles

1. **Lossless `Raw`.** Every `Record` keeps its exact bytes; concatenating `Raw()` (plus
   `RecordError.Raw` of every `ErrMalformed`) reproduces the input unless an `ErrTooLarge` or
   `ErrTruncated` was yielded. `TestLossless` and the fuzz property enforce it; never weaken them.
2. **Errors are values; never panic.** A bad record is a `*RecordError` and the stream goes on; a bad
   attribute goes raw to `Path.Unknown()` with a `Diagnostic`. No count read from input sizes an
   allocation before it is checked against the bytes present (R10: bodies over 64 KiB are read
   through a growing buffer).
3. **No `net`, no dependencies.** `go list -deps ./... | grep -x net` prints nothing; root `go.mod`
   has no `require`, ever. Only `internal/compare` (nested module) may require gobgp.
4. **Go 1.23 floor** (`iter.Seq2`). Do not raise it. `net/netip` only; no `net.IP`/`net.IPNet`.
5. **AS width comes from the record type, never from the bytes.** `BGP4MP_MESSAGE` 2-byte,
   `*_AS4` 4-byte, TABLE_DUMP_V2 entries 4-byte, TABLE_DUMP 2-byte (`bgp.Context.AS4`). A 2-byte
   AS_PATH that does not parse at width 2 is a Warning and an empty path, never re-read at width 4.
6. **Import direction `cmd → mrt → bgp`.** `bgp` never imports `mrt`. No dependency on any rpsl module;
   ASNs are `bgp.ASN`.
7. **Conventions C1-C3** (spec §3), held by `go run ./internal/apisurface -check`: `X(in)` /
   `XWith(in, XOptions)`; whole-value parsers return `(T, []Diagnostic)`, single values `(T, error)`;
   sentinels use `errors.New`; parsed values are values. `api/*.txt` is the golden of every exported
   signature. The one exception (user's decision 2026-10-20, v0.1 design §3):
   `(*bgp.Interner).Decode` returns a `*Path`, because the path is shared.
8. **Interning changes nothing.** With and without a `bgp.Interner`, routes, paths and diagnostics
   are deeply equal. `TestInternerMatchesDecode`, `TestRoutesInternerSameRoutes` and FuzzRoutes hold
   it; never weaken them.

## Decoding rules settled by rulings — get these right

- **AS4 (R4-R6).** AS_PATH with segment count 0 or type outside 1-4 is malformed. AGGREGATOR's rule
  applies only when BOTH AGGREGATOR and AS4_AGGREGATOR are present (RFC 6793 §4.2.3; bgpdump's
  `process_asn32_trans` agrees). Confederation segments in AS4_PATH are discarded with a Warning
  `bgp/attr-value` and the rest is kept and merged; two AS_SEQUENCEs at the junction are joined.
- **`Family.HasPrefixes` (R9)** is true exactly for AFI 1/2 x SAFI 1/2. `ParseNLRI` refuses any other
  family with one Error `bgp/nlri-family`.
- **RIB_GENERIC (R11).** For a family without prefixes the NLRI is not self-describing: `RawNLRI` is
  every byte after the SAFI, `EntryCount` 0, no entries, not malformed. For prefix families the NLRI
  is parsed by `bgp.ParseNLRI`. RIB_GENERIC_ADDPATH entries carry no path ID (RFC 8050 §4.2).
  Trailing bytes after `EntryCount` entries are ignored.
- **Routes (R12, R13, R22).** A RIB entry with a missing peer index or broken framing, a BGP4MP
  message whose `MessageType()` fails, and an UPDATE that yields no route but has a Warning or Error
  diagnostic (the threshold mrtdump exits 1 on) are each a `*RecordError{Offset: -1, ...}` of the
  record, wrapping `ErrMalformed`; `Error()` then says "offset unknown". After a PEER_INDEX_TABLE
  record error the table is cleared (never resolve against a stale one). Non-UPDATE messages, LOCAL
  subtypes, families without prefixes and End-of-RIB (no route, no diagnostic) are skipped
  silently. `Route.Diagnostics` offsets count from the BGP message for UPDATE routes (attribute
  diagnostics rebased) and from the entry's attribute block for RIB routes. The zero `Kind` is no
  route (`KindRIB` is 1).
- **Truncated messages (R17, R17a).** `ParseMessageHeader` errors only for len < 19, a bad marker or
  header length < 19; a length beyond the bytes present is not a header error. `ParseUpdate` parses
  as far as the bytes go (whole prefixes and attributes) and adds one Error `bgp/message-truncated`.
- **MP_REACH in a RIB entry (R20, amended 2026-10-20 by the user).** First byte non-zero is the
  abbreviated RFC 6396 §4.3.4 form; zero is the full RFC 4760 form. The full form gets no
  diagnostic (route-views6 writes it on every entry); its family and next hops are decoded and its
  NLRI is not kept (`RawNLRI` nil: the record supplies the prefix; the bytes stay in
  `Attrs.Raw()`). Outside a RIB the NLRI is parsed. Next hops of length 16/32 are IPv6 for any AFI
  (R8); `Path.NextHop()` is the NEXT_HOP attribute only.
- **Compact `Path` (v0.2 design §6.4a).** `Path` keeps as fields only what most paths carry.
  NEXT_HOP and the rare attributes are behind methods, the rare ones in one unexported part that is
  nil when a path has none. `Path` is 112 bytes on 64-bit platforms (68 on 32-bit), pinned by
  `TestPathSize`.
- **Duplicates and malformed attributes.** First duplicate wins (RFC 7606 §3 g), later ones to
  `Unknown()` with a Warning. RFC 7606 actions are out of scope: diagnostics carry the fault.

## Oracles and goldens

- Oracles are pinned: bgpdump RIPE-NCC/bgpdump master at `caf2cb07bc3cf06f1720c4cb274811599e9bda6a`
  (not the v1.6.2 tag), bgpkit-parser 0.22.0, built by `scripts/build-oracles.sh` into
  `$HOME/.local/bin`. Changing a pin is a deliberate change: re-record, review the diff.
- **A golden is oracle output, never hand-edited.** `MRT_GOLDEN_UPDATE=1 go test -run TestMatches .`
  rewrites them from the live oracles; read `git diff testdata/golden`. `TestGoldens` and
  `TestMatchesBgpkit` need no oracle installed; the `*Live` tests skip without them, unless
  `MRT_ORACLES_REQUIRED=1` (CI) makes that fatal.
- **`testdata/bgpdump/divergences.md` is two-directional**: every line on which mrt and an oracle
  differ must be in a `pair` block, and every block line must still differ
  (`TestDivergencesDocumented`). Entries are keyed by fixture + exact lines, never line numbers.
  Where bgpdump is wrong, bgpkit-parser decides; where they disagree or both differ from mrt, the RFC.
- **Platform independence (R16).** bgpdump truncates a community field at BUFSIZ (1024 macOS, 8192
  glibc), so no fixture route may have such a field over 1000 characters
  (`TestCommunityFieldsFitBgpdumpBuffer`). Goldens may be gzip; `testdata/` stays under 15 MB
  (`du -sh testdata`); `go test ./...` stays near a minute.
- Synthetic fixtures come from `internal/mrtgen` (test-only, deterministic) and are also decoded by
  both oracles, so the encoder and the decoder never only agree with each other.

## Test data rules

- **No RIS bytes in the repository (R18).** RIPE NCC's terms forbid redistribution. Committed
  fixtures are RouteViews (CC BY 4.0, attributed in `testdata/README.md`) or synthetic. RIS is read
  only by the opt-in real-data run, which downloads at test time. Asking RIPE NCC for permission is
  the user's call.
- Rebuild the RouteViews slices with `scripts/fetch-fixtures.sh`; it prints the lines for
  `testdata/README.md` (source URL, date, source sha256, slice sha256).

## Commands

- `scripts/check.sh` — gofmt, build, vet, `go test -race`, coverage, no-`net`, no-`require`,
  apisurface, staticcheck and govulncheck when installed. `FUZZTIME=15s scripts/check.sh` also runs
  the fuzz targets. On this machine always run it guarded:
  `memguard.sh 8 3600 env GOMAXPROCS=4 FUZZTIME=15s scripts/check.sh`
  (output to a file, then `echo "exit=$?"`; it must end SUCCESS and `check: ok`).
- `scripts/fuzz.sh` — five targets (FuzzRecords, FuzzRoutes, FuzzParseUpdate, FuzzDecodeAttrs,
  FuzzASPathMerge). `FUZZTIME=1m`, `FUZZ_SHARD=i/n` (CI runs two shards; `i/n` is validated).
- `MRT_API_UPDATE=1 go run ./internal/apisurface -check` rewrites `api/`; review `git diff api/`, it
  is the API change list.
- `scripts/build-oracles.sh [PREFIX]` — builds the two oracles (needs autoconf, automake, cargo).
- `scripts/fetch-fixtures.sh` — rebuilds `testdata/mrt` from RouteViews.
- **Real data (opt-in).** `scripts/fetch-mrt.sh` downloads into `.data/realdata/` and pins name, URL,
  size and sha256 in `.data/realdata/MANIFEST` (not under memguard: its `taskpolicy -b` throttles
  network). Then, one file at a time, alone:
  `memguard.sh 8 4000 env GOMAXPROCS=4 MRT_REALDATA=$PWD/.data go test -count=1 -timeout 60m -run 'TestRealData/rv6-rib' -v .`
  (`rv2-rib`, `rrc00-bview`, `(rv2-updates|rrc00-updates)` likewise). It fails on any mismatch that
  `classify` does not name (S16, S17, B5). Before calling a release done, check one count and its
  denominator by hand: `bgpkit-parser -r FILE` (records) and `-e FILE` (elements).
- **gobgp comparison (release gate).** `cd internal/compare && go build -o /tmp/compare . && cd ../..`,
  then `memguard.sh 8 4000 env GOMAXPROCS=4 /tmp/compare .data/realdata/rv6-rib.YYYYMMDD.HHMM.bz2`.
  Passes only if Routes yields 0 errors, one route per gobgp entry, and allocates less than gobgp.
  `go vet ./...` and `go test ./...` there are separate from the root's.
- `scripts/release.sh vX.Y.Z` — see the header of the script. Rehearse with `--dry-run`. Never run it
  without the user's go-ahead: it pushes a tag.

## Heavy runs

Real-data runs, `internal/compare` and the benchmarks hold hundreds of MB. Run them one at a time,
alone, under `memguard.sh`, never beside `check.sh` or each other. `memguard.sh LIMIT_GB TIMEOUT_S
CMD...` is the maintainer's local memory watchdog, not part of this repo: it refuses to start
without enough free memory, kills the command past LIMIT_GB of RSS, memory pressure, swap growth
or TIMEOUT_S, and ends with SUCCESS, FAILURE, MEMLIMIT, PRESSURE, SWAP, LOWMEM, TIMEOUT or
NOT STARTED. Without it, run the same command with a memory cap of your own (e.g. `GOMEMLIMIT`
and a watchdog). Every figure in a doc or the
CHANGELOG comes from a command run in this work, with its log named; otherwise it is unknown.

`internal/heldsize` is the heaviest: it holds about 6.7 GB `HeapAlloc` at its first reading
(`.data/heldsize-v0.2.log`, reading 0: 6,655,422,952), so it runs alone with a memory limit above
that: `env GOMAXPROCS=4 GOMEMLIMIT=16GiB go run ./internal/heldsize
.data/realdata/rrc00-bview.*.gz .data/realdata/rv2-rib.*.bz2 .data/realdata/rv6-rib.*.bz2`. The
`memguard.sh 8 ...` limit used above would kill it; under memguard give it a limit of at least 12.

## Process

- Pre-v1: keep releasing 0.x; break the API while it is cheap. Commits on a branch, never on `main`;
  creating the GitHub repository, pushing, merging and tagging wait for the user's explicit go-ahead.
- Judge a subagent's work by `git diff`. Before merging, an independent reviewer reads the whole diff.
