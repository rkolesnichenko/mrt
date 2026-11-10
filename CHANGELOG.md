# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module follows [Semantic Versioning](https://semver.org/): until v1.0.0,
a minor version may change the API.

## [Unreleased]

## [0.6.0] - 2026-11-10

### Added

- `MaxWorkers` (256), the largest `DecompressOptions.Workers` that
  `DecompressWith` accepts. 256 is far above the counts measured (4 and 14)
  and keeps the bzip2 reader's window bound within an `int` on 32-bit
  platforms (it passes `math.MaxInt` there from 508 workers).

### Changed

- `DecompressWith` returns an error for `Workers` over `MaxWorkers`, as it
  does for a negative count; a caller that sizes it from `GOMAXPROCS` passes
  `min(runtime.GOMAXPROCS(0), mrt.MaxWorkers)`. Unlimited, every worker was
  a goroutine started at the first parallel block whatever the input's
  size: on a 234-byte bzip2 input (`testdata/bzip2/multi-stream.bz2`, 364
  bytes decompressed) a million workers passed 3 GB in 5 s
  and 2^40 reached 2,991 MB (memguard stopped both, the second on memory
  pressure), and 2^46 panicked in `make`
  ("makechan: size out of range"), against the never-panic rule; on 64-bit
  platforms counts past `math.MaxInt/2` decoded correctly only because the
  doubled limit wrapped negative and no job was dispatched
  (`.data/workers-panic-probe-v054.log`). `TestDecompressWithOptions` checks
  that 257, 2^20, `math.MaxInt/2 + 1` and `math.MaxInt` are errors, and that `MaxWorkers` itself gives `Decompress`'s bytes (a check
  of `>=` fails it, `.data/workers-sabotage-v060.log`).
- `mrtdump` and `mrtcut` (and `internal/heldsize`, `internal/decompbench`'s
  default, `BenchmarkDecompressWith`) use `min(GOMAXPROCS, MaxWorkers)` workers, so they still run with
  `GOMAXPROCS` over 256: `TestMrtdumpManyProcs` and `TestCountManyProcs`,
  on a bzip2 input, failed with the error before
  (`.data/workers-cmds-red-v060.log`).

## [0.5.3] - 2026-11-10

### Fixed

- `DecompressWith`'s bzip2 window bound overflowed `int` on 32-bit
  platforms from 508 workers (2 × (8 MiB + Workers × 2 MiB + 16) + 64 KiB
  passes 2^31 − 1 there). Wrapped negative, it made the window grow by one
  read's room at a time, recopying the whole window on every read. It now
  saturates at `math.MaxInt`, as does the doubling of the window's capacity
  (which wrapped the same way past half of `math.MaxInt`), and the pump's
  limits on jobs, look-ahead and marks are computed in `int64`. On 64-bit
  platforms only absurd counts were affected (from about 2^41 workers).
  `TestWindowBoundSaturates` checks the bound against the exact value from
  `math/big` for counts up to `math.MaxInt` (red on 64-bit before the fix;
  on 32-bit, CI's linux/386 job runs it), `TestGrowDoublesAtManyWorkers`
  that a full 1 MiB window
  still doubles (before the fix it grew to 1 MiB + 64 KiB;
  `.data/windowbound-red-v053.log`), and `TestGrowCapSaturates` the
  doubling past half of `math.MaxInt`. `Workers` itself has no upper limit:
  it is the number of goroutines and sizes a channel of twice as many jobs.

### Tests

- The seven test gaps left since v0.3.2. The five new tests each fail under
  a sabotage of what they cover (`.data/gaps-sabotage-v053.log`); the
  `paddedBench` change is shown by its output's equality, and the rewrap
  has nothing to fail:
  - `TestGzipReadaheadChunks`: the gzip readahead hands out at most
    `gzipQueueBytes/gzipChunkBytes` distinct chunks of `gzipChunkBytes`
    over 2 MiB (`TestDecompressWithGzipQueue` checked only the constants);
  - `TestGrowLeavesRoomForRead` also requires that grow never more than
    doubles a window beyond one read's room, so small windows stay small;
  - `TestReapTakesFinishedZombies`: `reap` subtracts a finished result's
    bytes and its job, and keeps the pending ones in order;
  - `paddedBench` is built by a new `bzcraft.PadBlock`, which gives the
    same 8,561,662 bytes as the copy of bzcraft's padding it replaces
    (`.data/padbench-same-v053.log`);
  - `TestBoundLoad` checks `bzcraft.BoundLoad` against its doc (25 equal
    one-block streams of 4 MiB less a byte, 1,150 bytes of plaintext each,
    28,750 in all; under `-race` three streams, 4.7 s instead of 39 s;
    `.data/boundload-test-v053.log`, `.data/boundload-race-v053.log`);
  - `TestCPUProfile` also covers a profile that cannot be created;
  - the long comment line in `internal/bzip2/block.go` is rewrapped.

## [0.5.2] - 2026-11-09

Tests and CI only; the module's code is unchanged.

### Changed

- `scripts/fuzz.sh` fails a target that fuzzed nothing. Go starts the
  `-fuzztime` clock before the run over the cached corpus that gathers
  baseline coverage (`src/internal/fuzz/fuzz.go`, go1.27.3), so a corpus
  that takes all of `FUZZTIME` ends the run there, and it passed: at
  `FUZZTIME=2s`, `FuzzDecompress` stopped at 143 of 203 baseline inputs,
  with no execution, and reported PASS (`.data/fuzztime-baseline-v052.log`).
  Such a run now fails, saying so and how to start the corpus afresh
  (`.data/fuzzsh-guard3-v052.log`). In CI, with the corpus kept since
  v0.4.6, baselines take 0 to 2 s of each target's 30 s, `FuzzDecompress`
  the most (210 inputs; `.data/ci-baseline-1ac719c.txt`). A target name that
  matches no fuzz test, which passed, now fails too
  (`.data/fuzzsh-guard2-v052.log`).
- `FUZZ_BASELINE_MAX` (seconds) in `scripts/fuzz.sh` fails a target whose
  baseline took longer, or whose baseline time it cannot read
  (`.data/fuzzsh-guard3-v052.log`).
- The fuzz job moved to `.github/workflows/fuzz.yml`, which `ci.yml` calls
  for every push and pull request (30 s a target, as before) and which also
  runs on main once a day at 03:23 UTC, 2 minutes a target, or by hand.
  Caches belong to a branch, not a workflow, so main's corpus now grows
  daily, not only when something is merged. The daily and manual runs set
  `FUZZ_BASELINE_MAX=10`, a third of a pull request's 30 s, so that a
  corpus grown too large is reported by a failing daily run well before
  pull requests fail. The job now removes setup-go's copy of the corpus
  before restoring its own cache, so that deleting the corpus caches starts
  the corpus afresh. GitHub's limits on schedules (default branch only,
  disabled after 60 days without activity in a public repository, failures
  reported to whoever last edited the cron line) are noted in `fuzz.yml`.
  The check should be named `fuzz / fuzz (shard i/2)`; branch protection
  requires only `ci ok`.
- `compare -bmp` fails when its `Session.Each` pass counts other routes or
  errors than its `Session.Routes` pass (a walk made to drop one route
  fails `TestRunBMP`; `.data/compare-tests-v052.log`).

### Not done

- Pruning the fuzz corpus, which nothing does. The caches are small (each
  shard's 102 KB to 264 KB, the 26 in the repository 4.2 MB;
  `.data/gh-cache-corpus-v052.log`); what a corpus that grows too large
  costs is time, which `FUZZ_BASELINE_MAX` turns into a failing daily run
  with the steps to start it afresh. How fast a 2-minute daily run grows it
  is not yet measured; locally, this work's short runs took `FuzzDecompress`
  from 203 to 337 inputs and its baseline from 3 s to 5 s
  (`.data/fuzzsh-guard-v052.log`, `.data/fuzzsh-guard3-v052.log`).

## [0.5.1] - 2026-11-09

### Changed

- `bgp.Interner.Decode` no longer copies what `Attrs.Decode` has just made.
  It cloned the whole path, every AS path and community array, and then
  dropped the copies for the held ones it found. It now keeps `Decode`'s
  arrays and copies only the parts that alias the block (`Unknown()`
  values and the raw NLRI of MP_REACH and MP_UNREACH), all into one new
  array. Every slice of a returned path still has capacity equal to its
  length, as a clone's had (`Decode`'s merged AS paths, an AS4_PATH with a
  confederation segment dropped and the `Unknown()` list are clipped where
  they are), so an append to a shared array cannot write into what another
  holder sees. A path still holds none of the bytes it was decoded from,
  and still deep-equals `Attrs.Decode`'s (principle 8). On a block it does
  not index whole and whose components it already holds, `Decode` now
  costs one allocation more than `Attrs.Decode`, or two with such parts,
  instead of 7 to 11 more (`TestInternerMissAllocations`).
  - `Routes` with an `Interner` over `testdata/mrt` (go1.27.3 darwin/arm64,
    median of 5 passes each; `.data/interner-allocs-v051-summary.txt`):
    allocations −20% to −38% on every fixture (rv2-updates-et 99,554 →
    68,475, rv2-rib 26,474 → 18,010), bytes −6% to −27%. On update and
    table-dump files the interner cost 31% to 75% more allocations than
    `Routes` without one and now costs −1% to +26%; on RIB files it saves
    more than before. Time not measured.
  - Held memory, every path kept with the `Interner` alive, is 0.3% to 0.8%
    higher on six of the fixtures, whose three runs each agree within 0.5%;
    on rv6-rib and rv6-tabledump runs vary by 2.1% to 3.4%, more than any
    change (`.data/held-interner-v051-summary.txt`). A kept path now has
    `Decode`'s arrays, whose allocations may be larger than their length
    (an AS path's object is sized to an allocation class, with up to three
    unused ASN slots), where the clone's were exact.
  - The `TestAllocations` ceilings for `Routes` with an `Interner` are
    lowered to the new counts, the higher of Go 1.23.0 and 1.27.3 plus 5%
    (`.data/allocs-v051.log`).
- Tests: `TestInternerPathsOwnTheirArrays` walks every field of the paths
  the `Interner` returns, by reflection as the `Clone` guard does, over
  blocks that set every reference field, AS4 merges (one of three
  segments), dropped confederation segments, broken framing, duplicates,
  malformed attributes and a RIB block held whole: every slice must have
  capacity equal to its length and no byte slice may point into the block.
  `TestInternerCopiesOwnArrays` requires the copied parts to point outside
  the block, empty values included (a zero-length slice of the block would
  keep it alive). Leaving out the copies, the empty-value rule, the copies'
  clip, or the clip of the segment lists, their ASNs or the `Unknown()`
  list, each fails them
  (`.data/interner-sabotage-v051.log`); the other clips guard against a
  future `Decode` that leaves spare capacity, which it does not today.

## [0.5.0] - 2026-11-09

### Added

- `bmp.Session.Each(m, err, yield)`: `Session.Routes` with a callback in
  place of the returned sequence, for the per-message Kafka step. It applies
  the message to the session as `Routes` does, passes `yield` the same routes
  and errors in the same order, and returns false if `yield` did. It has
  decoded the message when it returns, so the buffer may then be reused;
  what `yield` was passed may still alias it (paths without an `Interner`,
  a `*MessageError`'s `Raw`). `BenchmarkBMP` in `internal/compare`
  (go1.27.3 darwin/arm64, `GOMAXPROCS=4`, under memguard at normal
  priority, median of 6; `.data/bench-each-v050.log`): 6.948 → 3.948
  allocations and 459.3 → 355.2 bytes per message, 14.4% less time
  (15.35 → 13.14 ms per pass over 50,085 messages). On a message that
  yields nothing it allocates nothing, where `Routes` allocates its sequence
  (`TestSessionEachAllocations`, with a capturing callback made per call).
  `RoutesWith` already worked this way and is unchanged.
  `ExampleSession_Each`; `BenchmarkBMP` has an `mrt-each` side and
  `compare -bmp` an `mrt bmp.Session.Each` pass.
- Tests: `TestRoutesWithMatchesSession` requires `Session.Each`, one
  message at a time, to give exactly what `RoutesWith` gives under every
  combination of `PeerStates`, `EndOfRIB` and `RequirePeerUp`, and
  `FuzzBMPRoutes` with none of them and with all three and an `Interner`;
  `TestSessionEach` covers a
  `yield` that stops (on a Peer Up, whose state still applies, and in a
  message of three routes); `TestSessionErrorPassThrough` covers errors.

### Not done

- The review's slab for RIB paths (one `[]bgp.Path` per RIB record), re-measured
  on v0.4.6 against v0.4.6, alternating, under memguard at normal priority
  (`.data/bench-slab-v046-summary.txt`,
  `.data/pin-slab-v046.log`): allocations −31% on rv2-rib, −21% on rv6-rib
  and −19% on rvwide-rib, but bytes +1.5% to +5.4% and time within the noise
  (−1.2%, +0.2%, +1.4%; fixtures it does not touch moved up to +3.7%). A
  consumer keeping one path per record would hold 18.7, 9.5 and 2.7 times as
  much, since a kept path holds its record's slab.

## [0.4.6] - 2026-11-09

Tests and CI only; the module's code is unchanged.

### Changed

- CI keeps the fuzz corpus. Go grows it under `GOCACHE/fuzz`, which the fuzz
  job's `setup-go` cache holds, but that cache is saved only when its key
  misses: when `go.mod`, `scripts/fuzz.sh`, Go or the runner image changes,
  and then by whichever of the two shards saves first (`gh cache list` showed
  one entry per key and branch, `.data/gh-cache-list-v046.log`). The
  corpus now has a cache of its own, per shard, under a key new on every
  run, saved when the job passes and restored from the newest.
- `FuzzDecompress` ran `DecompressWith` at Workers 2 only; it now takes
  Workers 1 to 8 from the fuzz input. Its seeds read
  `testdata/bzip2/zeros/zeros-1.bz2` (twelve blocks) at Workers 1, 2 and 8,
  so plain `go test` covers both ends. A reader that started two goroutines
  whatever Workers says passed the old target
  (`.data/fuzzdecompress-sabotage-old-v046.log`) and fails the new one: a
  panic at Workers 1, a hang at 8 (`.data/fuzzdecompress-sabotage-v046.log`,
  `.data/fuzzdecompress-sabotage8-v046.log`). The changed signature makes Go
  set aside the corpus cached for the old one.

### Checked

- `Decompress` reads bzip2 with the standard library's `compress/bzip2` and
  `DecompressWith` (Workers > 0) with `internal/bzip2`, a copy of Go 1.27.2's.
  The review asked whether they differ on older Go. They do not: the
  decoder's four files are identical in Go 1.23.0, 1.24.0, 1.25.0, 1.26.0,
  1.26.10 and 1.27.3 but for one doc comment in 1.23.0 and 1.24.0
  (`.data/bzip2-stdlib-diff-v046.log`), and the bzip2 and decompress tests
  pass under Go 1.23.0, 1.24.0 and 1.25.0 (`-run 'Bzip2|Decompress'`,
  `.data/bzip2-oldgo-v046.log`).

### Measured

go1.27.3 darwin/arm64, `GOMAXPROCS=4`, 15 s under memguard, no failure:
`FuzzDecompress` ran 20,209 executions from its seeds alone
(`.data/fuzz-decompress-v046.log`) and 18,659 from the 47 inputs that run
kept (`.data/fuzz-decompress2-v046.log`); the old target ran 43,777 from 332
(`.data/check-v045c.log`). Peak RSS at most 548 MB.

## [0.4.5] - 2026-11-09

### Fixed

- `mrt.Records` and `bmp.Messages` no longer lose a reader error that
  comes with the bytes completing a record or message. Both read through a
  `bufio.Reader`, which, when its buffer is empty, hands a read at least as
  long as the buffer (4 KiB by default, or the size of one the caller
  passes) straight to the underlying reader and returns that reader's error
  with the bytes, once; `io.ReadFull` dropped it, so a reader that did not
  return the error again went on to the next bytes as if none had occurred.
  The skip of a record or message over the length limit lost it the same
  way (`io.CopyN`). Such an error is now held and returned by the next
  read, as bufio already did for the reads it buffers: the record or
  message the bytes complete is yielded, then the error ends the sequence.
  A reader that does return the error again, and every read bufio
  buffered, behave as before; so do readers that return `io.EOF` or
  `io.ErrUnexpectedEOF` with their last bytes, such as `compress/gzip` over
  a cut file. A reader that returns `io.EOF` with bytes and then more
  bytes, against the `io.Reader` contract, now ends there; it was read on
  when bufio handed it a read directly. One change besides: the skip now
  reads through bufio's
  buffer, which gives up with `io.ErrNoProgress` after 100 reads in a row
  that return no bytes and no error, so such a reader now ends the stream
  there; `io.CopyN` kept reading it. One allocation more per `Records` or
  `Messages` call, none per record (`.data/allocs-readers-v045.log`,
  go1.27.3 darwin/arm64: for example 4010 → 4011 over
  `testdata/mrt/rv2-rib.20261015.1200.gz`, 44 → 45 over
  `testdata/bmp/bmp-addpath.bmp`).
- Tests: `TestRecordsReaderError` and `TestMessagesReaderError` put the
  error at the end of each header and each record or message, for body
  sizes from 100 bytes to 300 KiB, over bufio buffers of the default size,
  16 bytes and 64 KiB, and for records skipped as too large, and
  `TestRecordsReaderErrorET` for extended-timestamp records;
  `TestRecordsReaderErrorResume`/`TestMessagesReaderErrorResume` break after
  the record or message and range again, and get the error once;
  `TestRecordsDataEOF`/`TestMessagesDataEOF` and
  `TestRecordsCutGzip`/`TestMessagesCutGzip` pin the behaviour that stays.
- `openbmp`: `Frames` holds a reader error the same way.
  - It no longer loses one that comes with the bytes completing a frame's
    12-byte prefix: the prefix was read with `io.ReadFull`, so a reader
    that did not return the error again went on to the next bytes as if
    none had occurred.
  - A frame whose last bytes come with a reader error is now yielded, then
    the error; v0.4.3 and v0.4.4 dropped the frame. The frames yielded no
    longer depend on whether the reader is a `bufio.Reader`, which already
    held such an error.
  - A stream that ends inside a frame with `io.ErrUnexpectedEOF`, as
    `compress/gzip` does over a cut file, now wraps `ErrTruncated`, as the
    doc comment says; after the prefix it was the bare error. Over a gzip
    of `testdata/bmp/obmp-v4-router.obmp` cut at bytes 20, 27, …, 1126,
    21 of the 159 cuts ended in `ErrTruncated` before, 159 now
    (`.data/obmp-cutgzip-v045.log`).
  - A reader that returns `io.EOF` with bytes and then more bytes, against
    the `io.Reader` contract, now ends there, as it already did through a
    `bufio.Reader`.

  Other errors are unchanged, as is the allocation count
  (`TestFramesAllocations`: 37 for 36 frames).
- Tests: `TestFramesReaderError` puts the error at the end of each frame's
  prefix, inside the second one, and at each frame's end, also through
  bufio buffers of 16 bytes and 64 KiB; `TestFramesReaderErrorResume`
  breaks after the frame and ranges again; `TestFramesDataEOF` reads the
  same frames and truncation errors from readers that return `io.EOF` or
  `io.ErrUnexpectedEOF` with their last bytes; `TestFramesCutGzip` cuts a
  gzip capture.

## [0.4.4] - 2026-11-09

Tests only; the module's code is unchanged.

### Added

- `FuzzBMPMatchesMRT` (`bmp`), the twelfth fuzz target: one fuzzed UPDATE,
  passed through a BMP Route Monitoring after the peer's Peer Up and through
  an MRT BGP4MP record of the matching subtype, gives the same routes
  (prefix, path ID, peer address and AS, path, diagnostics) and the same
  errors, in the same places and with the same cause. It covers 2- and
  4-byte ASNs, ADD-PATH for every prefix family or none, an `Interner` on
  either side, and a BGP header length beyond the bytes present (a cut
  message, R17a) or short of them (trailing bytes).

### Changed

- `FuzzParseUpdate` held only that nothing panics. Under each of its four
  contexts it now also holds:
  - R17a: one `bgp/message-truncated` Error, at the cut, exactly when the
    header states more bytes than the message holds;
  - R17a's "as far as whole fields go": a message that frames soundly at its
    full length, cut at up to 16 places, gives exactly the withdrawn
    prefixes, attributes and NLRI of the full message that end at or before
    the cut, and no NLRI fault the full message lacks;
  - the attributes decoded through an `Interner`, twice so that the second
    reuses its AS paths and community lists, deep-equal `Attrs.Decode`
    (principle 8).
- `FuzzDecodeAttrs` also decodes each block through one `Interner` under all
  eight contexts, twice each, and requires each result to equal that
  context's `Decode`; this covers the interner keeping RIB contexts apart.

### Measured

go1.27.3 darwin/arm64, `GOMAXPROCS=4`, 2 minutes each under memguard, no
failure (`.data/fuzz-v044.log`): `FuzzBMPMatchesMRT` 1,844,202 executions,
`FuzzParseUpdate` 1,876,788, `FuzzDecodeAttrs` 1,021,900.

## [0.4.3] - 2026-11-09

### Changed

- `bmp`: `Session.Routes` no longer moves what it holds for the message to
  the heap: one allocation less per message (Measured). It held a
  Peer Up or Peer Down route by value, which made it larger than the Go
  compiler copies into a closure; that route is now allocated only when
  `PeerStates` yields it. `RoutesWith` with `PeerStates`, which never
  allocated it, now does so once per Peer Up or Peer Down.
- `openbmp`: `Frames` reads each frame into one buffer, sized exactly up to
  64 KiB and doubling beyond, as `bmp.Messages` does; it allocated about four
  objects per frame. A frame that states more bytes than follow allocates at
  most 64 KiB or under four times the bytes present, whichever is larger.
  Errors are unchanged, including a reader error returned with a frame's
  last bytes.
- Tests: `TestSessionRoutesAllocations` (`bmp`) and `TestFramesAllocations`
  (`openbmp`) gate both counts; `TestFramesLarge` and
  `TestFramesClaimBoundedAlloc` cover frames over 64 KiB, and
  `TestFramesReaderError` a reader error at a frame's end.

### Measured

go1.27.3 darwin/arm64, main (2923774) against this release:

- `BenchmarkBMP/mrt` in `internal/compare` (`bmp.Session.Routes` over
  `bmp.ParseMessage`, 50,085 messages; three alternating rounds of
  `-count=2` under memguard at normal priority, `.data/bench-bmp-v043.log`):
  7.950 → 6.949 allocations per message (−12.6%) and 708.0 → 459.6 bytes
  per message (−35.1%).
  Best run of each side 20.27 → 16.64 ms (−18%); one branch round took
  59–73 ms, so means are not compared.
- `testing.AllocsPerRun`, the new tests run against main's code and this
  release's (`.data/allocs-bmp-v043.log`): `Session.Routes` of a message
  that yields nothing 2 → 1; `openbmp.Frames` over
  `testdata/bmp/obmp-v4-router.obmp` and `obmp-v6-router.obmp` (36 frames
  each) 145 → 37 per file.

## [0.4.2] - 2026-11-08

### Changed

- `bgp`: an AS_PATH or AS4_PATH that is one segment of up to 16 ASNs (most
  paths) is decoded into one allocation instead of two; any other path takes
  two, where it took one per segment plus the growth of the segment list.
  As parsed, every segment's ASN capacity equals its length, as before, and
  so now does the segment list's. A run of NLRI (UPDATE, MP_REACH,
  MP_UNREACH) is counted first and allocated once, exactly sized, where it
  grew by appending. Results are unchanged (goldens, `TestLossless`,
  `TestInternerMatchesDecode`); `TestAllocations` ceilings are lowered to
  match.

### Measured

go1.27.3 darwin/arm64, main (a2cdc8d) against this release:

- Routes allocations (allocs/op of `BenchmarkRoutes -benchmem`,
  `.data/bench-v042.log`; the same counts with `testing.AllocsPerRun` in
  `.data/allocs-v041.log` for main and `.data/allocs-v042.log` for this
  release): rv2-rib 126,623 → 94,841 (−25%), rv6-rib
  131,086 → 106,197 (−19%), rv2-updates-et 77,091 → 61,554 (−20%),
  rvwide-rib 54,254 → 41,494 (−24%); −7% to −25% over all eight fixtures.
- Routes bytes per op (`BenchmarkRoutes -benchmem`, `.data/bench-v042.log`):
  −13.5% (rv2-updates-et) to +1.0% (rv2-rib); the RIB fixtures rise by
  0.4–1.0% because a short path's ASN array is rounded up to 4, 8, 12 or 16.
- Time (`BenchmarkRoutes`, three alternating rounds of `-count=2` under
  memguard at normal priority, `.data/bench-v042.log`): the runs were noisy
  (one main run of rv2-rib took 15.9 ms against a best of 4.77 ms), so only
  the best run of each side is compared: 5.1% (rv2-tabledump) to 9.0%
  (rv2-rib, 4.77 → 4.34 ms) faster on all eight fixtures. Means over the
  six runs are not a reliable measure here.

## [0.4.1] - 2026-11-08

### Fixed

- `bmp`: `Initiation.Info`, `Termination.Info`, `PeerDown.Info`,
  `PeerUpBody.Info` and `RouteMirroring.TLVs` yielded nothing when the same
  sequence was ranged a second time; every range now starts at the first TLV.

### Changed

- `Records`/`RecordsWith` and `bmp.Messages`/`MessagesWith` allocate the
  header buffer once per range instead of once per record, one allocation
  less per record or message (Measured).
- Tests: `TestAllocations` in the root and in `bmp` is an allocation gate.
  Records and Messages stay at 2 allocations per record or message, and
  Routes (with and without an `Interner`) stays under per-fixture ceilings.

### Measured

Allocation counts with `testing.AllocsPerRun`, go1.27.3 darwin/arm64, main
(7242118) against this release (log `.data/allocs-v041.log`, which also has
the go1.23.0 counts, within 1 of these for Records and Messages):

- Records over rv2-rib.20261015.1200 (2,000 records): 6,010 → 4,010, so
  3.00 → 2.00 per record; on every `testdata/mrt` fixture the count falls by
  exactly the number of records.
- `bmp.Messages` over the 16 `testdata/bmp/*.bmp` fixtures (185 messages,
  5 errors): 705 → 516.

## [0.4.0] - 2026-10-23

v0.4.0 adds a BMP reader. The API additions are listed below; `git diff v0.3.2 v0.4.0 -- api/`
shows all of them (254 added lines, none removed: `api/bgp.txt` 37, `api/bmp.txt`
174, `api/bmp_openbmp.txt` 25, `api/mrt.txt` 18).

### Added

- Package `bmp`: reads BMP (RFC 7854, RFC 8671 Adj-RIB-Out, RFC 9069 Loc-RIB,
  RFC 9736 Peer Up TLVs, RFC 9972 statistics). `Messages`/`MessagesWith` yield
  typed messages (each with its exact bytes in `Raw`), `ParseMessage` decodes
  one, and `Routes`/`RoutesWith`/`NewSession` yield `mrt.Route`, the same type
  `mrt.Routes` yields. A `Session` takes each peer's ADD-PATH state from its
  Peer Up, per family and direction; a Route Monitoring with no usable Peer Up
  is decoded without ADD-PATH and carries the Warning `bmp/addpath-unknown`
  (`RoutesOptions.RequirePeerUp` refuses it with `ErrNoPeerUp`).
- Package `bmp/openbmp`: the OpenBMP binary 1.7 envelope (`Frames`, `Parse`);
  no other version and no text form.
- `bgp.ParseOpen`, `bgp.ParseNotification`, `bgp.AddPathFamilies`, the `Open`,
  `Notification`, `Capability` and `AddPathFamily` types, `bgp.FamilySet`, and
  `Context.AddPathFamilies` (ADD-PATH per family instead of for the whole
  message).
- `mrt.Route` gains `Router` and `View`; `mrt.Peer` gains `Type` and
  `Distinguisher`; new types `mrt.View` and `mrt.PeerType`; new `mrt.KindEndOfRIB`.
- `internal/bmpgen` (synthetic BMP and OpenBMP fixtures in `testdata/bmp`),
  the `bgpkit-bmp` oracle harness built by `scripts/build-oracles.sh`, a gobgp
  BMP comparison in `internal/compare` (`go run . -bmp FILE`), and the opt-in
  `scripts/fetch-bmp.sh` and `TestRealDataBMP` (not run: see below).

### Changed

- Nothing for MRT callers: `mrt.Routes` and `mrt.RoutesWith` yield the same
  routes as in 0.3.2, with the new fields zero (`TestMRTRoutesHaveZeroBMPFields`),
  and no existing exported signature changed. One compile-time effect: adding
  fields to `mrt.Peer` breaks unkeyed `mrt.Peer{...}` composite literals
  outside the package (`go vet` flags them). Keyed literals still build;
  routecheck uses keyed ones and builds and tests against this branch. For the
  same reason `bgp.Context`, which gained `AddPathFamilies`, breaks unkeyed
  `bgp.Context{...}` literals outside the package; routecheck builds.
- The new fields make the values larger (`unsafe.Sizeof` on darwin/arm64,
  Go 1.27.3, main d3b3cdd against this branch; log `.data/sizes-v0.4.log`):
  `mrt.Route` 200 to 240 bytes, `mrt.Peer` 56 to 64, `bgp.Context` 3 to 4.

### Measured

`BenchmarkBMP` (`internal/compare`) over the largest synthetic stream, 50,085
messages (its `msgs` metric), 5 runs, go1.27.3 darwin/arm64 on an Apple M3
Max, run with `GOMAXPROCS=4` and `MEMGUARD_NORMAL_PRIORITY=1` (log
`.data/bmp-bench-v0.4b.log`): mrt 704.0 to 704.1 B/msg and 8.483 allocs/msg;
gobgp (pkg/packet/bmp) 872.7 B/msg and 22.73 allocs/msg. mrt allocates less
than gobgp per message in all 5 runs (ratio 0.807 by bytes, arithmetic:
704.1 / 872.7). Wall time per pass was 19.5 to 20.0 ms for mrt and 26.8 to
27.6 ms for gobgp; the gate is allocations.

Not measured: a real BMP feed. No capture exists yet (the user's decision), so
the gobgp gate is re-checked on a capture once one exists, and the real-data
comparison against bgpkit-bmp has not run.

## [0.3.2] - 2026-10-21

v0.3.2 closes the items v0.3.1 left open: the `mrtcut` slowdown on gzip, the
parts of the bzip2 bound that were arithmetic only, and four statements in
the v0.3.1 entry that were wrong or did not reproduce (corrected in place
below, each marked "corrected in 0.3.2"). The API (`api/`) is
unchanged. Figures come from commands run on 2026-10-21 with Go 1.27.2 on an
Apple M3 Max, `GOMAXPROCS=4`, and name their log under `.data/` (git-ignored).
Heap figures are sampled every 2 ms, so they are estimates. Default-GC heap
figures vary between sessions; the GOGC=10 figures are the gate.

### Fixed

- `mrtcut -count` on a gzip file was 9.48% slower than with v0.2.1 (mean of
  10 alternating pairs, 6.400 s to 7.007 s; `.data/v032-diagnosis.md`).
  `mrtdump` on the same file was 8.24% faster. The reading, not a proven
  cause: the gzip readahead handed 32 KiB chunks between goroutines, about
  124,000 hand-offs for 4.07 GB (arithmetic: 4,065,645,452 / 32,768 =
  124,073.7), each waking a thread, and `mrtcut -count`
  does too little per record to hide it. The chunk is now 128 KiB (the
  queue stays 256 KiB, two chunks). After the change, `mrtcut` is 0.46%
  slower than v0.2.1 (6.304 s to 6.333 s, 10 pairs;
  `.data/v032-mrtcut-after.log`, lines 372-373) and `mrtdump` is 10.79%
  faster (47.113 s to 42.030 s, 3 pairs; `.data/v032-mrtdump-after.log`,
  line 53). `DecompressWith` plus `Routes` on the same file is 2.28% faster
  than v0.3.1 (9.647 s to 9.427 s, 3 pairs; `.data/v032-gate-gzip.log`), and
  the decompress-only `DecompressWith` takes 6.12 s against v0.3.1's 6.88 s
  (means of 3 runs; `Decompress` 5.69 s against 5.71 s, so the ratio goes
  from 0.83 to 0.93). v0.3.1's 8.3 s / 7.26 s alternation was most likely
  memguard's 1 s wall-time resolution; v0.3.1 was not re-measured.
- The bzip2 reader's compressed window could run past its bound, because
  `slices.Grow` chose its capacity. It is now grown explicitly, and the
  bound holds because `fill` never needs more (the derivation in
  `windowBound`). Before the fix a 38 MiB input at 4 workers took its capacity to
  34,709,504 bytes against a bound of 33,620,000
  (`.data/v032-window-cap-scan.log`, line 11).

### Changed

- The window term of the bound is 65,568 bytes more than the `2L` of the
  v0.3.1 arithmetic (the code's bound includes a 64 KiB read slack). The
  totals still round to 216.7 MB at 4 workers and 389.0 MB at 14 (arithmetic:
  216,663,520 + 65,568 = 216,729,088 and 388,978,400 + 65,568 = 389,043,968
  bytes). The `DecompressWith` doc now says that while the window grows, the
  old array is live until the copy ends, beyond the headline figure: 67.1 MB at
  14 workers (142,671,904 - 75,563,040), 142.7 MB for both arrays in the last
  growth step.
  The headline bound is unchanged.
- Housekeeping: `zeros-1.bz2` and `zeros-9.bz2` moved to
  `testdata/bzip2/zeros/`; `TestDecompressWithCancelDuringSerialBlock` is
  `TestDecompressWithCancelBetweenCappedReads` (it also passes on v0.3.0);
  the tests of `SpeculativeBlock` cover its check before any output and the
  dropped chunks; `decompbench -blocks` prints no cap when every file failed
  and `decompbench -mem -crafted` runs without a FILE; `mrtcut` has
  `-cpuprofile`; the large crafted inputs are cut under `-race`.

### Measured

- The bound, term by term, each on an input that loads it, from the opt-in
  `MRT_BOUNDGATE=1 go test -run 'TestBoundTerms$' .` (final code,
  `.data/v032-bound-terms.log`, lines 101-144). `bound-load` is a 100 MiB
  input of 25 streams padded to 4 MiB; all inputs but `zeros-9` are
  bzip2-built (internal/bzcraft):

  | term | workers | bound | measured peak | input |
  |---|---|---|---|---|
  | segment copies | 4 | 33,554,432 | 16,777,184 (50.0%) | bound-load |
  | segment copies | 14 | 33,554,432 | 33,554,368 (100.0%) | bound-load |
  | results not yet taken | 4 | 16,777,216 | 14,489,758 (86.4%) | results-load |
  | results not yet taken | 14 | 58,720,256 | 55,889,068 (95.2%) | results-load |
  | window (capacity) | 4 | 33,620,000 | 33,554,432 (99.8%) | bound-load |
  | window (capacity) | 14 | 75,563,040 | 75,563,040 (100.0%) | bound-load |
  | window, old and new array while it grows | 4 | 67,174,432 | 50,331,648 (74.9%) | bound-load |
  | window, old and new array while it grows | 14 | 142,671,904 | 142,671,904 (100.0%) | bound-load |

  The W4 ceilings have reasons, and the bound keeps its figures. The
  segments are limited by the lead (16 MiB at 4 workers), not by the budget;
  the window stops at its last doubling step (33,554,432), and the step to
  the bound is never taken at 4 workers. The 32 MiB budget is a hard
  guarantee and the lead an input-dependent ceiling; segments of jobs the
  chain has passed can be held outside the lead (a reading of the code, not
  measured).
- The terms the counters do not see are measured by the heap, with
  `GOGC=10`, in `decompbench -mem -crafted` (`.data/v032-gate-mem.log`):
  the chain's serial decode and the workers' decodes on `zeros-9` (about
  42 MB per block, not a full 46.6 MB hostile block) grow the heap by
  130.2 MB at 4 workers (line 80) and 174.3 MB at 14 (line 92); the exact-size
  copies of `under-cap-blocks` by 48.6 MB and 143.1 MB (lines 85, 97).
- Heap growth, `GOGC=10`, the largest over all inputs: 146.4 MB at 4 workers
  (results-load, bound 216.7 MB) and 307.9 MB at 14 (bound-load; 303.7 to
  307.9 MB over four runs, lines 98, 166, 201, 224; bound 389.0 MB). Every
  input is under its bound. `results-load` at 14 workers ranged from 213.4 to
  260.5 MB over eight runs of two builds (lines 26 to 221): run-to-run
  variance, all under 389.0 MB.
- Default GC, `zeros-9` at 14 workers: 338.4 MB, in two runs of v0.3.1's own
  binary and two of this branch's (`.data/v032-gate-mem.log`, lines 132-155).
  v0.3.1's logged 286.2 MB did not reproduce (corrected below).
- Speed, `DecompressWith` bzip2 decompress-only, v0.3.1 and this branch
  measured in the same run, `Workers` 4 and 14 under `GOMAXPROCS=4`, one run
  each (`.data/v032-gate-speed.log`): rv2-rib 5.42 s to 5.37 s at 4 (-0.9%)
  and 5.59 s to 5.56 s at 14 (-0.5%); rv6-rib 1.90 s to 1.89 s (-0.5%) and
  1.92 s to 1.92 s (0.0%). All four are inside the 5% gate, and the output
  hashes and route counts are identical. v0.3.1 for this run was a worktree
  of its tag with this branch's `decompbench` and `bzcraft` copied in.
- Equivalence on real data: `DecompressWith` and `Decompress` give identical
  bytes on all 19 files (`.data/realdata-decompress-v032.log`).
- Checks: `scripts/check.sh` with `FUZZTIME=15s` ends `check: ok`, SUCCESS
  (`.data/check-v032.log`); `api/` is unchanged and `apisurface -check`
  passes.
- Consumer: routecheck (pins mrt v0.1.0) still fails to build with the same
  four errors in `observe/session.go`, and the same vet errors, against this
  branch and against v0.2.1 (`.data/consumer-v032.log`). The two error sets
  are identical, so v0.3.2 adds none.

### Not measured

Resident memory (RSS), other machines, worker counts other than 4 and 14,
speed with 14 cores (the 14-worker runs used `GOMAXPROCS=4`), `mrtcut` on
files other than the rrc00 bview gzip, `mrtcut` and `mrtdump` on bzip2 files,
and v0.3.1's `mrtcut` (not re-measured). The cause of the `mrtcut` slowdown
is a reading from the profile and the chunk-size screen (32 KiB 7.41 s, 64 KiB
6.88 s, 128 KiB 6.51 s, v0.2.1 6.40 s; one run each,
`.data/v032-mrtcut-after.log`), not a proof. No input loads the segment term
or the window at their bound at 4 workers (see above), and the full hostile
46.6 MB block is arithmetic.

## [0.3.1] - 2026-10-21

v0.3.1 bounds the memory of `DecompressWith`'s parallel bzip2 reader. Figures
come from commands run on 2026-10-21 with Go 1.27.2 on an Apple M3 Max
(`.data/machine-v0.2.log`), `GOMAXPROCS=4`, and name their log. Heap figures
are sampled every 2 ms from `/memory/classes/heap/objects:bytes`, so they are
estimates, and a short peak can be missed. The API (`api/`) is unchanged.

### Changed

- Speculative decodes stop filling output at a 2 MiB cap. A block whose
  output passes the cap is not kept; the reader decodes it itself when it
  reaches it. The cap is the smallest power of two that is at least twice
  the largest decoded block in the six real bzip2 files (981,320 bytes in
  rv2-updates.20261020.0845; `.data/v031-blocks.log`, `cap: 2097152`).
- The copies of compressed segments that jobs hold are limited to 32 MiB in
  all. A job that would pass it waits.
- The reader drops a block's bytes once `Read` has returned them all, so they
  are not live while the next block is decoded.
- The bound on what the bzip2 reader holds live is about `32 MiB + 16 MiB +
  97.4 MB + Workers × (3.6 MB + 4 × 2 MiB + 5 MiB)`: about 148 MB plus 17 MB
  per worker, 216.7 MB at 4 workers and 389.0 MB at 14. The 97.4 MB is the
  reader's own decode of a hostile level-9 block (46.6 MB): 45 chunks of
  1 MiB, the exact-size copy and its 3.6 MB block table. It is arithmetic
  (`.data/v031-gate-mem.log`, section FINAL FIX), and is documented on
  `DecompressWith`.
- `DecompressWith` documents that it reads the first bytes of `r` and, for
  gzip, the whole header before it returns.

### Fixed

- Cancelling the `Context` no longer starts a job that was queued: a worker
  finishes at most the block it is decoding, so a cancel takes effect after
  that block.

### Measured

- Memory, `testdata/bzip2/zeros-9.bz2` (465 MB out, 11 blocks per
  `.data/zeros-blocks-probe.log`), growth of the heap over its baseline
  (`.data/v031-gate-mem.log`: v0.3.1 in section FINAL FIX, v0.3.0 GOGC=10 in
  FIX ROUND 1, v0.3.0 default GC in the first section):

  | workers | bound | v0.3.1 GOGC=10 | v0.3.0 GOGC=10 | v0.3.1 default GC | v0.3.0 default GC |
  |---|---|---|---|---|---|
  | 4 | 216.7 MB | 130.1 MB | 344.1 MB | 233.7 MB | 534.2 MB |
  | 14 | 389.0 MB | 174.2 MB | 634.7 MB | 286.2 MB (corrected in 0.3.2: 338.4 MB) | 818.0 MB |

  GOGC=10 is the gate: an upper-bound estimate of the live bytes, which v0.3.1
  holds under the bound. At the default GC the peak includes garbage not yet
  collected (about 1.8 times the GOGC=10 figure at 4 workers and 1.6 at 14;
  corrected in 0.3.2: 1.9 at 14),
  so it exceeds the bound at 4 workers, 233.7 vs 216.7 MB. The bound is on
  live bytes, not a cap on heap size. v0.3.0's bound grows with `Workers`
  (v0.3 design §6.2); zeros-9 has 11 blocks, so its held bytes here are
  limited by the fixture. (A file with more blocks would hold more:
  arithmetic from the design, not measured.)
  (Corrected in 0.3.2: the 286.2 MB default-GC figure at 14 workers did not
  reproduce. v0.3.1's own binary measured 338.4 MB in two runs, the same as
  the v0.3.2 branch's two (`.data/v032-gate-mem.log`, "Ruling 8" section).
  Default-GC figures vary between sessions; the GOGC=10 column is the gate.)
- Crafted inputs (an end-magic flood and sparse block magics, 4 and 14
  workers): v0.3.1 stays under the bound on all of them, at most 75.9 MB at
  GOGC=10 and 74.2 MB at the default GC (`.data/v031-gate-mem.log`, section
  FINAL FIX).
- Speed, `DecompressWith` bzip2, `Workers` 4 and 14 under `GOMAXPROCS=4`, one
  run each (`.data/v031-gate-speed.log`): rv2-rib 5.42 s to 5.52 s at 4
  (+1.8%) and 5.55 s to 5.66 s at 14 (+2.0%); rv6-rib 1.88 s to 1.91 s (+1.6%)
  and 1.93 s to 1.99 s (+3.1%). v0.3.1 is 1.6-3.1% slower than v0.3.0 in all
  four pairs, inside the 5% gate. The output hashes are identical.
- Blocks of zeros are slower by design: on zeros-9 v0.3.1 took about 1.8 s
  and v0.3.0 0.60 s at 4 workers, because every zero block is over the cap and
  is decoded serially (`.data/v031-gate-mem.log`).
- `mrtcut -count` on rrc00-bview.20261020.0800.gz, mean of 4 alternating runs:
  v0.2.1 7.253 s, v0.3.1 7.778 s (+7.2%; `.data/v031-mrtcut-gz.log`). The
  v0.3.1 runs alternate between about 8.3 s and 7.26 s (corrected in 0.3.2:
  that alternation is most likely memguard's 1 s wall-time resolution, not
  the tool's speed; v0.3.1 was not re-measured). The outputs are
  identical. Nothing changed in this release (the gzip readahead is
  `DecompressWith`'s, and `mrtcut` uses it). The fix is left open: a format
  check in `mrtcut`, or `WriteTo` on the gzip readahead (corrected in 0.3.2:
  `WriteTo` would not affect `mrtcut`, which reads through `Records`, and
  `Records` reads its input in 4 KiB calls through a `bufio.Reader`; v0.3.2
  fixes the slowdown in the readahead's chunk size, see 0.3.2 above).
- Equivalence on real data: `DecompressWith` and `Decompress` give identical
  bytes on all 19 files (`.data/realdata-decompress-v031.log`).
- Consumer: routecheck (pins mrt v0.1.0) fails to build with the same four
  errors in `observe/session.go`, and the same vet errors, against this branch
  and against v0.2.1 (`.data/consumer-v031.log`). The two error sets are
  identical, so v0.3.1 adds none.

### Not measured

Resident memory (RSS), other machines, worker counts other than 4 and 14,
speed with 14 cores (the 14-worker runs used `GOMAXPROCS=4`), and v0.3.0 on
the crafted inputs (those runs last 0.01 s or less). No measured input loads
the segment-budget or results terms of the bound: zeros-9 loads the reader's
own decode and the per-worker decodes, and the crafted inputs end at their
first block. (Corrected in 0.3.2: `Decompress` uses the standard library's
`compress/bzip2`, which holds no decoded block. Only `internal/bzip2`'s
test-only serial reader has the pattern of keeping a block's bytes live while
it decodes the next one.)

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

[Unreleased]: https://github.com/rkolesnichenko/mrt/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/rkolesnichenko/mrt/compare/v0.5.3...v0.6.0
[0.5.3]: https://github.com/rkolesnichenko/mrt/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/rkolesnichenko/mrt/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/rkolesnichenko/mrt/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/rkolesnichenko/mrt/compare/v0.4.6...v0.5.0
[0.4.6]: https://github.com/rkolesnichenko/mrt/compare/v0.4.5...v0.4.6
[0.4.5]: https://github.com/rkolesnichenko/mrt/compare/v0.4.4...v0.4.5
[0.4.4]: https://github.com/rkolesnichenko/mrt/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/rkolesnichenko/mrt/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/rkolesnichenko/mrt/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/rkolesnichenko/mrt/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/rkolesnichenko/mrt/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/rkolesnichenko/mrt/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/rkolesnichenko/mrt/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/rkolesnichenko/mrt/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/rkolesnichenko/mrt/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/rkolesnichenko/mrt/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/rkolesnichenko/mrt/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/rkolesnichenko/mrt/releases/tag/v0.1.0
