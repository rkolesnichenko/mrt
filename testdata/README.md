# Test data

- `mrt/`: slices of real MRT files from RouteViews (below), cut at record
  boundaries and gzip-compressed by `scripts/fetch-fixtures.sh`.
- `synthetic/`: files written by `internal/mrtgen` for cases the real files
  lack (see `synthetic/README.md`).
- `golden/`: for every file in `mrt/` and `synthetic/`, the output of the two
  pinned oracles (`scripts/build-oracles.sh`): `NAME.bgpdump` is
  `TZ=UTC bgpdump -m -l -u FILE`, `NAME.bgpkit.json` is `bgpkit-parser --json
  FILE` normalized and sorted by `internal/oracle`; either is stored as `.gz`
  when larger than 64 KiB. `MRT_GOLDEN_UPDATE=1 go test -run 'TestMatches' .`
  rewrites them; `golden_test.go` says what each test compares.
- `bgpdump/divergences.md`: every difference between mrt and the oracles.
- `bench/rv2-updates-et.20261015.1200.bz2`: the fixture
  `mrt/rv2-updates-et.20261015.1200.gz` decompressed and recompressed with
  `bzip2 -9` (bzip2 1.0.8), for `BenchmarkBzip2` (`bench_test.go`), since no
  committed fixture is bzip2; its decompressed bytes are the fixture's (slice
  sha256 8b74dfbdc55bfbead57e7fde987de4a575cf08a74f5556c977e69e8dc9d9e5ef),
  which the benchmark checks.
- `bzip2/`: inputs for `internal/bzip2`'s tests and `FuzzBzip2`, made on
  2026-10-20 with the `bzip2` command (bzip2 1.0.8), run from `testdata/`:
  - `one-byte.bz2`: `printf A | bzip2 -9 > bzip2/one-byte.bz2`, the single
    byte `A` (sha256 559aead08264d5795d3909718cdd05abd49572e84fe55590eef31a88a08fdffd);
  - `level1.bz2`: `bzip2 -1 -c synthetic/state-changes.mrt > bzip2/level1.bz2`,
    one level-1 block; its decompressed bytes are `state-changes.mrt` as it
    was then (sha256 283d19fb5ad018596120cfdea3d831f513c31db929890c931f83d41c99a55e7f);
  - `multi-stream.bz2`: `cat bzip2/level1.bz2 bzip2/one-byte.bz2 >
    bzip2/multi-stream.bz2`, two streams, of levels 1 and 9 (decompressed
    sha256 e0c6e49113f153edb51973b7abee6d51ed567688639d752188724d033e7bac30).
  - `zeros-1.bz2`: `head -c 60000000 /dev/zero | bzip2 -1 > bzip2/zeros-1.bz2`
    (made 2026-10-21), 388 bytes, 12 level-1 blocks of zeros (11 of 5,099,235
    bytes, one of 3,908,415), for the speculative-cap tests. The tests that
    loop over every bzip2 fixture glob `[^z]*.bz2`, which leaves it out; the
    cap and bound tests of `internal/bzip2` and of `DecompressWith` read it
    by name.
  - `zeros-9.bz2`: `head -c 465000000 /dev/zero | bzip2 -9 > bzip2/zeros-9.bz2`
    (made 2026-10-21), 369 bytes, 11 level-9 blocks of zeros (the largest
    45,899,235 bytes), for the v0.3.1 memory gate only; no `go test` decodes it.

## Real fixtures

From RouteViews' archive, downloaded on 2026-10-16. "Source sha256" is the
sha256 of the file as downloaded; "slice sha256" that of the slice's
decompressed MRT bytes (the `.gz` bytes depend on the Go version's
compressor). `scripts/fetch-fixtures.sh` rebuilds every slice from the same
URLs and prints these lines; on 2026-10-16 a second run gave the same bytes.

- `rv2-updates-et.20261015.1200.gz`: route-views2,
  https://archive.routeviews.org/bgpdata/2026.10/UPDATES/updates.20261015.1200.bz2,
  first 10000 records. Source sha256
  29349915636238ca927a5f0b50f58ba4877791293aafbfe25834220a94fe3569; slice
  sha256 8b74dfbdc55bfbead57e7fde987de4a575cf08a74f5556c977e69e8dc9d9e5ef.
  BGP4MP_ET MESSAGE_AS4 UPDATEs (all 10000), IPv4: 25306 routes with
  COMMUNITIES, LARGE_COMMUNITY (1422 UPDATEs), ONLY_TO_CUSTOMER (572) and
  extended communities (445).
- `rv6-updates-et.20261015.1200.gz`: route-views6,
  https://archive.routeviews.org/route-views6/bgpdata/2026.10/UPDATES/updates.20261015.1200.bz2,
  records 71536-72095 (`-skip 71535 -first 560`). Source sha256
  422905e7919a096dd7ab0b23c56fb11f6ce7b959976f47023ce39ae678fceb88; slice
  sha256 63dbf761a3c458c84bee2df7830654f1a46d5d6fe2ff1ef177f4c18bb2cb7ac6.
  BGP4MP_ET MESSAGE_AS4 UPDATEs with IPv6 in MP_REACH_NLRI and
  MP_UNREACH_NLRI (636 announcements, 391 withdrawals). The window holds only
  three routes whose UPDATE also carries NEXT_HOP (divergence K7); the first
  1607 records would have done, but record 1608 has a community field longer
  than 1000 characters (Ruling R16).
- `rv2-updates-as2.20110601.1200.gz`: route-views2,
  https://archive.routeviews.org/bgpdata/2011.06/UPDATES/updates.20110601.1200.bz2,
  first 12000 records. Source sha256
  4418aa322f8548a510506de249dce167a8e6dc77d28bcd6c39cf2669e8d95701; slice
  sha256 1e6780aa5f1871d3741ffba724aa5dd326b9f7aa575d1bf57970d9ef32c95708.
  2-byte BGP4MP_MESSAGE UPDATEs (4931, 181 of them with AS4_PATH, so the
  RFC 6793 merge on real paths) beside BGP4MP_MESSAGE_AS4 ones (7069).
- `rv2-rib.20261015.1200.gz`: route-views2,
  https://archive.routeviews.org/bgpdata/2026.10/RIBS/rib.20261015.1200.bz2,
  first 2000 records. Source sha256
  c63b33af7646435471e9f646e3514a6b2cdfb71a7a143e14252086a2630f4d07; slice
  sha256 ba1423bf55b5f05a1fcb4d99312e021b178df9621e1d3b24b9550c2de3e6ec0f.
  TABLE_DUMP_V2: PEER_INDEX_TABLE and 1999 RIB_IPV4_UNICAST records (many
  peers: 31782 entries).
- `rvwide-rib.20261015.1200.gz`: route-views.wide,
  https://archive.routeviews.org/route-views.wide/bgpdata/2026.10/RIBS/rib.20261015.1200.bz2,
  first 5000 records. Source sha256
  1f6ed6d417aa75216b89e71f80b2bc3735c8501481a917127ea8c12d07a7d8e1; slice
  sha256 3845b551ac2035758db4a9282e3233d86f0137b41db403ec90003ad776358789.
  PEER_INDEX_TABLE and 4999 RIB_IPV4_UNICAST records from a collector with
  few peers (12760 entries).
- `rv6-rib.20261015.1200.gz`: route-views6,
  https://archive.routeviews.org/route-views6/bgpdata/2026.10/RIBS/rib.20261015.1200.bz2,
  first 3000 records. Source sha256
  ae612f568f6a51ebed3999d0508c137a486b2bb2d680dd713f9b164838639be8; slice
  sha256 103cc2d7241f07447f5092ae14c0bda0c1bb4a5f6d0def7732c11580646f223a.
  PEER_INDEX_TABLE and 2999 RIB_IPV6_UNICAST records (24870 entries).
  route-views6 writes the full RFC 4760 MP_REACH_NLRI in RIB entries, not RFC
  6396's abbreviated form. Ruling R20 as amended on 2026-10-20: its family
  and next hops are decoded, with `Abbreviated` false, and its NLRI is not
  kept (`RawNLRI` nil; the record supplies the prefix, and the bytes stay in
  `Attrs.Raw()`); the abbreviated form is covered by
  `synthetic/nh32.mrt` and `synthetic/addpath-rib-v4v6.mrt`.
- `rv2-tabledump.20070601.0025.gz`: route-views2,
  https://archive.routeviews.org/bgpdata/2007.06/RIBS/rib.20070601.0025.bz2,
  first 10000 records. Source sha256
  4262f544f70ac6e686902e643528d1dfae35b012855253fde039f621832997db; slice
  sha256 9c826be055f6572acee96b76532f9547359fb1270060852deb31b0e7e7347485.
  Legacy TABLE_DUMP, AFI_IPv4 (written before TABLE_DUMP_V2).
- `rv6-tabledump.20070601.0123.gz`: route-views6,
  https://archive.routeviews.org/route-views6/bgpdata/2007.06/RIBS/rib.20070601.0123.bz2,
  first 5000 records. Source sha256
  a214faa1b6e05a09acc1352805de0726be235967bf700bc73c704b72b98c6779; slice
  sha256 687838862a90330e0a8b14c3a4fd6eb192dfb0fb66c446e794d0901521a6bfcf.
  Legacy TABLE_DUMP, AFI_IPv6.

What no real fixture has, and why:

- BGP4MP state changes: no RouteViews collector logs them. Measured on
  2026-10-16: none in a 15-minute updates file from each of route-views2,
  route-views3, -4, -5, -6, .amsix, .linx, .eqix, .sydney, .wide, .isc, .kixp,
  .saopaulo, .sg, .perth, .soxrs, .napafrica and .nwax (2026-10-15 12:00), nor
  in route-views2's files of 2001-11, 2002-07, 2003-06, 2004-06, 2005-06,
  2007-06, 2008-06, 2009-06 and 2011-06, nor in four other collectors' files
  of 2006-06 and route-views6's of 2008-06. RIS files have them (the first 2 MB of
  rrc00's 2026-10-15 12:00 file: 31 STATE_CHANGE, 16 STATE_CHANGE_AS4) but cannot be
  redistributed (below). `synthetic/state-changes.mrt` covers them.
- A BGP message shorter than its own header: route-views2's
  `updates.20030602.1652.bz2` has two (records 8076 and 8077, header 4090 and
  4093 bytes, 4080 in the record). bgpdump and mrt read 2040 routes from them
  (Ruling R17a), bgpkit-parser none: pinning that slice would take about 2040
  pairs in divergences.md (entry K6), so `synthetic/short-message.mrt` covers
  the case instead (cuts after a whole prefix, inside a prefix, inside the
  withdrawn routes and inside the path attributes).

## Open facts settled here (spec §10)

- **Add-path (RFC 8050) in the wild: none found.** On 2026-10-16 the first
  2 MB (or the whole file, when smaller) of the 2026-10-15 12:00 updates file
  of 20 collectors was read with `go run ./internal/mrtcut -count`: RouteViews
  route-views2, route-views3, route-views4, route-views5, route-views6,
  route-views.amsix, route-views.linx, route-views.eqix, route-views.sydney,
  route-views.wide, and RIS rrc00, rrc01, rrc03, rrc04, rrc06, rrc11, rrc12,
  rrc16, rrc21, rrc25. Every RouteViews file held only BGP4MP_ET
  MESSAGE_AS4 (17/4); the RIS files only BGP4MP subtypes 0, 1, 4 and 5. None held
  a BGP4MP subtype 8-11 record. The heads of the route-views2, route-views6
  and route-views.wide RIBs of the same time held TABLE_DUMP_V2 subtypes 1, 2
  and 4 only, no 8-12. Add-path is covered by `synthetic/addpath-*.mrt`.
- **Redistribution terms**: RouteViews permits it (CC BY 4.0); RIPE NCC's
  terms do not permit redistributing RIS files, so none is used. Quotes below.
- **RIB_GENERIC and multicast RIBs**: bgpdump prints nothing for RIB_GENERIC
  (its decoder is commented out) and nothing for the multicast RIB subtypes
  3, 5, 9 and 11; bgpkit-parser refuses RIB_GENERIC and decodes the multicast
  RIBs. See divergences.md B3, B4, K4.
- **bgpdump's notation**: a confederation sequence prints as `(65001 65002)`,
  a confederation set as `[65003,65004]`, an AS_SET as `{3,4}`; `-u` prints
  each attribute it does not decode as `CODE:FLAGS:VALUE` in hex, separated by
  spaces (`23:c0:0000fbf4` is ONLY_TO_CUSTOMER, code 0x23, AS 64500).
- **Broken entries and attributes**: bgpdump prints what a short read gives
  (B1), aborts on a duplicate attribute (B2) or a missing peer index (S12);
  see divergences.md.

## Terms and attribution

Checked on 2026-10-16.

**RouteViews**, https://www.routeviews.org/routeviews/licenses/ :

> Use of the data created and owned by RouteViews ("RouteViews Data") is
> licensed under a Creative Commons Attribution 4.0 International License
> (CC BY 4.0).

and https://www.routeviews.org/routeviews/ :

> This means you are generally free to use RouteViews data for network
> operations and research, provided that you give attribution to RouteViews
> for use of the data.

(That page links the words "Creative Commons Attribution 4.0 International
License" to the BY-SA 4.0 deed, but its own text and the licenses page say BY
4.0 and link https://creativecommons.org/licenses/by/4.0/; the licenses page
is taken as the one that governs.)

Attribution: the files in `testdata/mrt/` utilize data provided by RouteViews
(www.routeviews.org), University of Oregon; use of this data is subject to
the CC BY 4.0 license (https://creativecommons.org/licenses/by/4.0/). RouteViews'
DOI is 10.7264/1y7v-2d90. Changes made: each file is a contiguous run of
whole records from the RouteViews file named above, decompressed from bzip2
and recompressed with gzip; no record is altered. The goldens derived from
them carry the same attribution.

**RIPE NCC (RIS)**: not used. https://www.ripe.net/about-us/legal/terms-of-service/
(Article 1: "The Website – All webpages hosted under the ripe.net domain and
any sub-domains thereof", which includes data.ris.ripe.net):

> 3.5 The User shall not re-package, download, compile, re-distribute or
> re-Use any or all of the data contained on the Website or on any Publicly
> Available RIPE NCC Service, unless this is permitted as part of their Use of
> the Website or the particular Publicly Available RIPE NCC Service.

and https://www.ripe.net/analyse/raw-data-sets/terms-conditions/ :

> 2. The RIPE Data Repository and the Data may only be accessed and used by
> the User. The User must not re-distribute the RIPE Data Repository or the
> Data to anybody else.

No RIS page found grants that permission for RIS raw data: the RIS
documentation's legal page (https://ris.ripe.net/docs/legal/, source
RIPE-NCC/ris-docs `docs/99_legal_information.md`) shows RIPE NCC's general
legal text, and the RIS commercial-use page
(https://www.ripe.net/analyse/internet-measurements/routing-information-service-ris/commercial-use/)
covers services and products built on RIS data, not copies of the files.
