# compare: mrt against gobgp over one MRT file

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

- the **release gate**: `mrt.Routes` (pass 4) allocates less than gobgp
  (pass 2) on the same file; exit status 1 when it does not;
- `mrt.Records`' bytes allocated per decompressed byte (aim ≤ 1.25; reported,
  not a gate);
- decompression's share of the full pass's wall time (pass 1 / pass 5).

`go vet ./...` and `go test ./...` in the root do not enter this module;
run `go vet ./...` here.
