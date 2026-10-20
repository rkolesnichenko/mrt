# Security policy

`mrt` reads untrusted binary input: MRT files downloaded from public route
collectors, and BGP messages taken from them. Hostile or corrupt input must
never crash a program that uses it or make it exhaust memory.

## Reporting a vulnerability

Please report it privately through GitHub:
**[Report a vulnerability](https://github.com/rkolesnichenko/mrt/security/advisories/new)**
(also on the repository's Security tab). The report stays private between you
and the maintainer until an advisory is published. If you can't use GitHub,
email **rokolg@gmail.com** instead.

Include a minimal reproducer if you have one (the bytes, or a fuzz corpus
entry). Public GitHub issues are not the right channel for an embargoed report;
open an issue only after we've coordinated disclosure.

I'll acknowledge within 72 hours and aim to have a fix candidate within two
weeks for issues affecting decoding safety. Timelines for other classes of
issue are negotiated case by case.

## What's already hardened

These are the invariants the library enforces; treat a regression in any of
them as a security-relevant bug.

- **No panics on any input.** Every decoder that takes untrusted bytes has a
  fuzz target (`FuzzRecords`, `FuzzRoutes`, `FuzzParseUpdate`,
  `FuzzDecodeAttrs`, `FuzzASPathMerge`), run in CI on every push.
- **Lengths are checked before allocating.** No count read from the input sizes
  an allocation before it is checked against the bytes actually present, so a
  short input claiming a huge count allocates little
  (`TestHugeCountsAllocateLittle`).
- **Record size is bounded.** A record longer than `Options.MaxRecordLen`
  (default 16 MiB) is skipped without being buffered and reported as
  `ErrTooLarge`. A body over 64 KiB is read through a buffer that grows only as
  bytes arrive, so a header that lies about its length allocates, in total,
  about four times the bytes actually present, and under five
  (`TestRecordsLyingLengthAllocation`).
- **Errors are values; the stream goes on.** A malformed record becomes a
  `*RecordError` and reading continues with the next one; a malformed attribute
  becomes a diagnostic. Truncation and read errors end the stream with an
  error, never a silent clean end.
- **No network, no dependencies.** The module imports nothing outside the Go
  standard library and no package imports `net`; `check.sh` enforces both.

## Scope

In scope:

- Panics, memory exhaustion or unbounded CPU in `mrt`, `bgp` or `mrtdump` on
  any input.
- A decoder reading or reporting data outside the bytes it was given.

Out of scope:

- Differences from bgpdump or bgpkit-parser that are listed in
  `testdata/bgpdump/divergences.md`.
- Decompression costs inherent in gzip or bzip2 themselves (a small compressed
  file can expand to a large stream; bound it with your own reader if that
  matters to you).
