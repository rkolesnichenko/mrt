# compare: mrt against gobgp (MRT files and BMP streams)

A nested module (its own `go.mod`), so the root module keeps no `require`
lines; it requires gobgp v4 and reads `mrt` from `../..` by a `replace`.
Spec §7.6.

```sh
cd internal/compare
memguard.sh 8 3600 \
  env GOMAXPROCS=4 go run . ../../.data/realdata/rv6-rib.YYYYMMDD.HHMM.bz2
```

Run it alone, under the memory guard: it holds the whole decompressed file in
memory (the route-views6 RIB of 2026-10-16 14:00 is 470,078,858 bytes
decompressed, `bzip2 -dc FILE | wc -c`).

In one process, after a garbage collection each, it measures wall time and the
`runtime.MemStats` `TotalAlloc` and `Mallocs` added by:

1. decompression alone (`mrt.Decompress` to `io.Discard`);
2. gobgp: the loop of its own `gobgp mrt inject` (`ParseHeader` on a 12-byte
   buffer, a body buffer, `ParseBody`, which decodes every RIB entry and path
   attribute), over the decompressed bytes in memory;
3. `mrt.Records` alone, over the same bytes;
4. `mrt.Routes` over `mrt.Records`, every path decoded, over the same bytes;
5. 4 from the compressed file, decompression included.

It prints:

- the **release gate**: `mrt.Routes` (pass 4) yields no error, yields one
  route per RIB entry gobgp decoded (pass 2; so the file must be a RIB), and
  allocates less than gobgp on the same file; the counts are printed, and the
  exit status is 1 when any of the three fails (Ruling R21);
- `mrt.Records`' bytes allocated per decompressed byte (aim ≤ 1.25; reported,
  not a gate);
- decompression's share of the full pass's wall time (pass 1 / pass 5).

## BMP: gobgp's pkg/packet/bmp as the second oracle

`bmp.go` runs gobgp v4.10.0's `pkg/packet/bmp` beside package `bmp`
(spec §9.2, §9.6). Both read the same messages, framed once by
`bmp.Messages` (or, for `.obmp`, `openbmp.Frames` and `openbmp.Parse`, one
stream per router): framing is not compared. gobgp is called through
`ParseBMPMessageWithOptions`, with the A flag as `Use2ByteAS` and the
ADD-PATH families the harness keeps from each peer's Peer Up by spec §7.5's
rule, from the OPENs as gobgp parses them. Its routes are rendered in
`oracle.FromRoute`'s line form, from what gobgp's packet layer decoded and
nothing more (no AS4_PATH merge, which gobgp does only in its server).

- `TestGobgpBMPMatches`: on every fixture of `testdata/bmp`, mrt's
  announcements and withdrawals equal gobgp's but for the `## gobgp`
  section of `testdata/bmp/divergences.md`, checked in both directions.
- `go run . -bmp FILE` (a `.bmp` or `.obmp` file): both sides' decode
  passes with `TotalAlloc` and `Mallocs` per message, both line counts and
  every mismatch (divergences.md is not read; exit 1 on a mismatch).
- `BenchmarkBMP/{mrt,gobgp}`: every `testdata/bmp/*.bmp` message, file after
  file, repeated to at least 50,000 messages, already framed (a Kafka
  consumer's input), through one session per side; it reports `B/msg` and
  `allocs/msg`. The gate (spec §9.6): mrt's `B/msg` is below gobgp's in every
  run. Run it alone:

  ```sh
  cd internal/compare
  MEMGUARD_NORMAL_PRIORITY=1 memguard.sh 8 1200 env GOMAXPROCS=4 \
    go test -run='^$' -bench=BMP -benchmem -count=5 . > ../../.data/bmp-bench-v0.4.log 2>&1
  ```

`go vet ./...` and `go test ./...` in the root do not enter this module;
run `go vet ./...` and `go test ./...` here (CI does).
