# mrt and mrtdump against the oracles: every known difference

The oracles are bgpdump (RIPE-NCC/bgpdump master at
`caf2cb07bc3cf06f1720c4cb274811599e9bda6a`) and bgpkit-parser 0.22.0, both
installed by `scripts/build-oracles.sh`. Their output on every fixture is in
`testdata/golden` (see `golden_test.go`).

Each entry has an ID, the reason for the difference, and which side is right
("Decides"). Where bgpdump is wrong, bgpkit-parser decides; where they
disagree with each other or both differ from mrt, the RFC decides. The
`pair` blocks are checked by `TestDivergencesDocumented` in both directions:
every line on which mrt and an oracle differ must be in a block, and every
line in a block must still differ. A block names the fixture (its file name
without `.gz` or `.mrt`), the line mrt gives and the line the oracle gives;
`(none)` means that side gives no line. Sections other than the two oracle
sections hold differences no fixture shows; nothing checks them.

## bgpdump

`mrtdump -m -l -u` against `TZ=UTC bgpdump -m -l -u` (`TestGoldens`).

### B1. A MULTI_EXIT_DISC of the wrong length

Reason: the MED in `attrs-malformed` record 1 is 3 bytes long. bgpdump reads 4
bytes anyway and prints 256 (`00 00 01` and the next byte); the value is not
in the data. mrt treats the attribute as malformed (RFC 7606 section 7.5:
attribute discard), reports `bgp/attr-length` and leaves the MED absent;
mrtdump exits 1.

Decides: bgpkit-parser, which also drops the MED (its golden has med 0 and the
attribute among its unknown attributes).

```pair
fixture: attrs-malformed
mrtdump: BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/32|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
bgpdump: BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/32|64500 64501|IGP|192.0.2.10|0|256|||NAG|||
```

### B2. A duplicate attribute aborts bgpdump

Reason: `attrs-malformed` record 2 carries ORIGIN twice. bgpdump stops on
`Assertion failed: (attr->origin == -1), function process_one_attr, file
bgpdump_lib.c, line 1124` (SIGABRT) and prints nothing for this record or the
two after it. Any duplicate of an attribute bgpdump decodes (codes 1-10, 14,
15, 17, 18, 32) aborts it the same way. mrt keeps the first copy (RFC 7606
section 3 (g): "all the occurrences of the attribute other than the first one
SHALL be discarded and the UPDATE message will continue to be processed"),
reports `bgp/attr-duplicate` and goes on.

Decides: bgpkit-parser prints all four records; for record 2 it keeps the
last ORIGIN instead (K3), so RFC 7606 decides that line.

```pair
fixture: attrs-malformed
mrtdump: BGP4MP|1700000001|A|192.0.2.10|64500|198.51.100.1/32|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
bgpdump: (none)
```

```pair
fixture: attrs-malformed
mrtdump: BGP4MP|1700000002|A|192.0.2.10|64500|198.51.100.2/32||IGP|192.0.2.10|0|0|||NAG|||
bgpdump: (none)
```

```pair
fixture: attrs-malformed
mrtdump: BGP4MP|1700000003|A|192.0.2.10|64500|198.51.100.3/32|64500 64501|IGP|192.0.2.10|0|50|||NAG|||
bgpdump: (none)
```

### B3. RIB_GENERIC is not decoded by bgpdump

Reason: bgpdump's RIB_GENERIC case is commented out (`bgpdump_lib.c`
lines 553-555 at the pinned commit); it prints nothing for RIB_GENERIC and
RIB_GENERIC_ADDPATH records. mrt decodes those whose family has prefixes
(IPv4/IPv6 unicast and multicast, Ruling R11); the AFI 1/SAFI 128 entry has no
prefix and gives no line in either.

Decides: neither oracle reads RIB_GENERIC (bgpkit-parser refuses it too, K4),
so RFC 6396 section 4.3.3 and RFC 8050 section 4.2 decide; the bytes are
pinned by `internal/mrtgen` (`TestGenRIBGeneric`).

```pair
fixture: rib-generic
mrtdump: TABLE_DUMP2|1700000000|B|192.0.2.10|64500|192.0.2.0/25|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
bgpdump: (none)
```

```pair
fixture: rib-generic
mrtdump: TABLE_DUMP2_AP|1700000000|B|2001:db8::11|4200000001|2001:db8:300::/48|5|4200000001|IGP|2001:db8::11|0|0|||NAG|||
bgpdump: (none)
```

### B4. Multicast RIB subtypes are not printed by bgpdump

Reason: bgpdump's `process_mrtd_table_dump_v2` has no case for
RIB_IPV4_MULTICAST, RIB_IPV6_MULTICAST and their add-path forms (subtypes 3,
5, 9, 11); it prints nothing. mrt decodes them like the unicast subtypes.

Decides: bgpkit-parser, which gives the same two routes as mrt.

```pair
fixture: rib-multicast
mrtdump: TABLE_DUMP2|1700000000|B|192.0.2.10|64500|198.51.102.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
bgpdump: (none)
```

```pair
fixture: rib-multicast
mrtdump: TABLE_DUMP2|1700000000|B|2001:db8::11|4200000001|2001:db8:500::/48|4200000001 64501|IGP|2001:db8::11|0|0|||NAG|||
bgpdump: (none)
```

### B5. LOCAL_PREF and MED of 2^31 or more on IPv4 routes in MP_REACH_NLRI

Reason: bgpdump's `table_line_announce_1` (IPv4 prefixes announced in
MP_REACH_NLRI) prints LOCAL_PREF and MED with `%d`; every other printer uses
`%u`. 3000000000 and 4000000000 come out negative. The same values in the
NLRI field (record 2 of the fixture) print alike in both.

Decides: bgpkit-parser (local_pref 3000000000, med 4000000000).

```pair
fixture: mp-ipv4-large-values
mrtdump: BGP4MP|1700000000|A|192.0.2.10|64500|198.51.110.0/24|64500 64501|IGP|192.0.2.20|3000000000|4000000000|||NAG|||
bgpdump: BGP4MP|1700000000|A|192.0.2.10|64500|198.51.110.0/24|64500 64501|IGP|192.0.2.20|-1294967296|-294967296|||NAG|||
```

### B6. AS4 merge: bgpdump counts confederation segments

Reason: RFC 6793 section 4.2.3 counts ASes "using the method specified in
Section 9.1.2.2 of [RFC4271] and in [RFC5065]", where confederation segments
count 0, and says a confederation segment "SHALL be prepended if it is either
the leading path segment or is adjacent to a path segment that is prepended".
In `as4-confed` record 1, AS_PATH `(65001) 64500 23456 23456` has 3 ASes and
AS4_PATH `196608 196609` has 2, so one AS (64500) and the leading confed
segment are prepended. bgpdump counts the confed segment as an AS, takes
`(65001)` twice and loses 64500.

Decides: bgpkit-parser, which gives `(65001) 64500 196608 196609` as mrt does.

```pair
fixture: as4-confed
mrtdump: BGP4MP|1700000000|A|192.0.2.10|64500|198.51.106.0/24|(65001) 64500 196608 196609|IGP|192.0.2.10|0|0|||NAG|||
bgpdump: BGP4MP|1700000000|A|192.0.2.10|64500|198.51.106.0/24|(65001) (65001) 196608 196609|IGP|192.0.2.10|0|0|||NAG|||
```

### B7. AS4_AGGREGATOR without AGGREGATOR

Reason: bgpdump's `process_asn32_trans` uses AS4_AGGREGATOR only when
AGGREGATOR is present as well, and otherwise prints no aggregator. RFC 6793
section 4.2.3 sets aside AS4_AGGREGATOR only "When both of the attributes are
received" and AGGREGATOR is not AS_TRANS; "Otherwise ... the AS4_AGGREGATOR
attribute SHALL be taken as the information about the aggregating node". mrt
takes it (Ruling R5).

Decides: bgpkit-parser (aggregator 196610 192.0.2.77, as mrt).

```pair
fixture: as4-confed
mrtdump: BGP4MP|1700000002|A|192.0.2.10|64500|198.51.108.0/24|64500 196608|IGP|192.0.2.10|0|0|||NAG|196610 192.0.2.77||
bgpdump: BGP4MP|1700000002|A|192.0.2.10|64500|198.51.108.0/24|64500 196608|IGP|192.0.2.10|0|0|||NAG|||
```

### B8. Confederation segments in AS4_PATH

Reason: RFC 6793 section 6: "the path segment types AS_CONFED_SEQUENCE and
AS_CONFED_SET [RFC5065] MUST NOT be carried in the AS4_PATH attribute ... A
NEW BGP speaker that receives these path segment types in the AS4_PATH
attribute ... MUST discard these path segments, adjust the relevant attribute
fields accordingly, and continue processing". mrt discards them (Ruling R6,
Warning `bgp/attr-value`), so AS4_PATH `(65001) 196608 196609` merges as
`196608 196609`. bgpdump merges the confed segment through, counts it as an
AS and loses 64500.

Decides: RFC 6793 section 6. bgpkit-parser differs too (K5).

```pair
fixture: as4-confed
mrtdump: BGP4MP|1700000003|A|192.0.2.10|64500|198.51.109.0/24|64500 196608 196609|IGP|192.0.2.10|0|0|||NAG|||
bgpdump: BGP4MP|1700000003|A|192.0.2.10|64500|198.51.109.0/24|(65001) 196608 196609|IGP|192.0.2.10|0|0|||NAG|||
```

### B9. A BGP message shorter than its own header

Reason: the BGP header of `short-message` records 2 and 3 claims 3 and 1
bytes more than the MRT record holds (record 3 ends inside its last prefix).
bgpdump decodes whatever prefixes are whole. mrt refuses a message shorter
than its header (`bgp.ErrShortMessage`; `Routes` yields a `RecordError`,
Ruling R12) and mrtdump reports it and exits 1: a message cut short is not
an UPDATE, and the prefixes in the part that is there are a guess.

Real data has this: route-views2's `updates.20030602.1652.bz2` (records 8076
and 8077, 4090- and 4093-byte UPDATEs cut to the 4080 bytes the collector
wrote). Measured on a 12-record slice around them: bgpdump prints 2040
announcements from the two messages; bgpkit-parser and mrt print none. No
real slice is a fixture: 2040 lines are too many to pin one by one here.

Decides: neither oracle (bgpkit-parser decodes a message cut after a whole
prefix and drops one cut inside a prefix, K6); mrt keeps Ruling R12.

```pair
fixture: short-message
mrtdump: (none)
bgpdump: BGP4MP|1700000001|A|192.0.2.10|64500|198.51.103.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
```

```pair
fixture: short-message
mrtdump: (none)
bgpdump: BGP4MP|1700000001|A|192.0.2.10|64500|198.51.104.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
```

```pair
fixture: short-message
mrtdump: (none)
bgpdump: BGP4MP|1700000001|A|192.0.2.10|64500|198.51.105.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
```

```pair
fixture: short-message
mrtdump: (none)
bgpdump: BGP4MP|1700000002|A|192.0.2.10|64500|198.51.106.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
```

```pair
fixture: short-message
mrtdump: (none)
bgpdump: BGP4MP|1700000002|A|192.0.2.10|64500|198.51.107.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||
```

## bgpkit-parser

`Routes` against `bgpkit-parser --json`, both normalized by
`internal/oracle` (`TestMatchesBgpkit`). Its package comment defines the
normalized line; it compares what both can express (bgpkit-parser does not
tell an absent MED or LOCAL_PREF from 0, nor a RIB entry from an
announcement).

### K1. Add-path RIB entries lose their path identifier

Reason: bgpkit-parser 0.22.0 decodes RIB_IPV4_UNICAST_ADDPATH and
RIB_IPV6_UNICAST_ADDPATH entries but its elems carry no path identifier (the
prefix is a plain string), so two paths for one prefix look the same.

Decides: bgpdump, whose `TABLE_DUMP2_AP` lines carry path IDs 1 and 2 as mrt
does (the `addpath-rib-v4v6` bgpdump golden matches mrtdump).

```pair
fixture: addpath-rib-v4v6
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1,"as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
```

```pair
fixture: addpath-rib-v4v6
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","path_id":2,"as_path":"64500 64502 64501","origin":"IGP","next_hop":"192.0.2.10"}
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.0/24","as_path":"64500 64502 64501","origin":"IGP","next_hop":"192.0.2.10"}
```

```pair
fixture: addpath-rib-v4v6
mrt: {"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:100::/48","path_id":1,"as_path":"4200000001 64501","origin":"IGP","next_hop":"2001:db8::11"}
bgpkit: {"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:100::/48","as_path":"4200000001 64501","origin":"IGP","next_hop":"2001:db8::11"}
```

```pair
fixture: addpath-rib-v4v6
mrt: {"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:100::/48","path_id":2,"as_path":"4200000001 64502 64501","origin":"IGP","next_hop":"2001:db8::11"}
bgpkit: {"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:100::/48","as_path":"4200000001 64502 64501","origin":"IGP","next_hop":"2001:db8::11"}
```

### K2. AGGREGATOR that is not AS_TRANS beside AS4_AGGREGATOR

Reason: RFC 6793 section 4.2.3: "When both of the attributes are received,
if the AS number in the AGGREGATOR attribute is not AS_TRANS, then: the
AS4_AGGREGATOR attribute and the AS4_PATH attribute SHALL be ignored". In
`as4-merge` record 3 AGGREGATOR is AS 64501; bgpkit-parser merges AS4_PATH
anyway and takes whichever aggregator attribute comes last.

Decides: RFC 6793 and bgpdump (its golden line for this record matches
mrtdump: `64500 23456`, aggregator `64501 192.0.2.77`; Ruling R5).

```pair
fixture: as4-merge
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.102.0/24","as_path":"64500 23456","origin":"IGP","next_hop":"192.0.2.10","aggregator":"64501 192.0.2.77"}
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.102.0/24","as_path":"64500 196608","origin":"IGP","next_hop":"192.0.2.10","aggregator":"196610 192.0.2.77"}
```

### K3. A duplicate attribute: the last copy wins in bgpkit-parser

Reason: bgpkit-parser keeps the second ORIGIN of `attrs-malformed` record 2
(EGP). RFC 7606 section 3 (g): "all the occurrences of the attribute other
than the first one SHALL be discarded". mrt keeps the first (IGP).

Decides: RFC 7606 (bgpdump aborts here, B2).

```pair
fixture: attrs-malformed
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.1/32","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.100.1/32","as_path":"64500 64501","origin":"EGP","next_hop":"192.0.2.10"}
```

### K4. RIB_GENERIC is not decoded by bgpkit-parser

Reason: bgpkit-parser 0.22.0 returns `ElemError::UnsupportedRibGeneric` for
RIB_GENERIC records and gives no elems.

Decides: neither oracle; see B3.

```pair
fixture: rib-generic
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"192.0.2.0/25","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
bgpkit: (none)
```

```pair
fixture: rib-generic
mrt: {"kind":"A","peer":"2001:db8::11","peer_as":4200000001,"prefix":"2001:db8:300::/48","path_id":5,"as_path":"4200000001","origin":"IGP","next_hop":"2001:db8::11"}
bgpkit: (none)
```

### K5. AS4 merge: confederation segments

Reason: two cases in `as4-confed`. Record 2: AS_PATH `(65001 65002) 23456`
and AS4_PATH `196608` both count 1, and RFC 6793 section 4.2.3 says the
leading confed segment "SHALL be prepended"; bgpkit-parser drops it. Record 4:
AS4_PATH carries a confed segment, which RFC 6793 section 6 says to discard
(Ruling R6); bgpkit-parser keeps it in the merged path.

Decides: RFC 6793. For record 2 bgpdump agrees with mrt (`(65001 65002)
196608`); for record 4 it differs as well (B8).

```pair
fixture: as4-confed
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.107.0/24","as_path":"(65001 65002) 196608","origin":"IGP","next_hop":"192.0.2.10"}
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.107.0/24","as_path":"196608","origin":"IGP","next_hop":"192.0.2.10"}
```

```pair
fixture: as4-confed
mrt: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.109.0/24","as_path":"64500 196608 196609","origin":"IGP","next_hop":"192.0.2.10"}
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.109.0/24","as_path":"64500 (65001) 196608 196609","origin":"IGP","next_hop":"192.0.2.10"}
```

### K6. A BGP message shorter than its own header

Reason: as B9. bgpkit-parser decodes `short-message` record 2, whose NLRI ends
after a whole prefix, and gives nothing for record 3, cut inside a prefix
(and nothing for the two real messages of route-views2's 2003-06-02 16:52
file). mrt refuses both (Ruling R12).

Decides: neither oracle; mrt keeps Ruling R12.

```pair
fixture: short-message
mrt: (none)
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.103.0/24","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
```

```pair
fixture: short-message
mrt: (none)
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.104.0/24","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
```

```pair
fixture: short-message
mrt: (none)
bgpkit: {"kind":"A","peer":"192.0.2.10","peer_as":64500,"prefix":"198.51.105.0/24","as_path":"64500 64501","origin":"IGP","next_hop":"192.0.2.10"}
```

### K7. NEXT_HOP is taken as the next hop of routes in MP_REACH_NLRI

Reason: when an UPDATE carries both NEXT_HOP and MP_REACH_NLRI,
bgpkit-parser 0.22.0 gives NEXT_HOP as the next hop of every route in it
(`get_relevant_attributes` falls back to MP_REACH_NLRI's next hop only when
NEXT_HOP is absent). RFC 4760 section 3 carries the next hop of the routes in
MP_REACH_NLRI in that attribute; NEXT_HOP belongs to the NLRI field. Peer AS
3130 at route-views6 sends both on IPv6 announcements (in the full file,
6386 of 88544 records carry both, all from AS 3130), so its IPv6 routes get
the IPv4 address 206.81.81.212 as next hop. The fixture window was chosen to hold only three.

Decides: bgpdump, whose lines for these routes carry `2001:418:1::3` as mrt
does (the bgpdump golden matches mrtdump).

```pair
fixture: rv6-updates-et.20261015.1200
mrt: {"kind":"A","peer":"2001:418:1::3","peer_as":3130,"prefix":"2400:7400:11::/48","as_path":"3130 2914 6453 4788 38044 23736","origin":"IGP","next_hop":"2001:418:1::3","communities":["3130:396"],"aggregator":"65401 1.9.246.251"}
bgpkit: {"kind":"A","peer":"2001:418:1::3","peer_as":3130,"prefix":"2400:7400:11::/48","as_path":"3130 2914 6453 4788 38044 23736","origin":"IGP","next_hop":"206.81.81.212","communities":["3130:396"],"aggregator":"65401 1.9.246.251"}
```

```pair
fixture: rv6-updates-et.20261015.1200
mrt: {"kind":"A","peer":"2001:418:1::3","peer_as":3130,"prefix":"2400:7400:43::/48","as_path":"3130 2914 6453 4788 38044 23736","origin":"IGP","next_hop":"2001:418:1::3","communities":["3130:396"],"aggregator":"65401 1.9.246.251"}
bgpkit: {"kind":"A","peer":"2001:418:1::3","peer_as":3130,"prefix":"2400:7400:43::/48","as_path":"3130 2914 6453 4788 38044 23736","origin":"IGP","next_hop":"206.81.81.212","communities":["3130:396"],"aggregator":"65401 1.9.246.251"}
```

```pair
fixture: rv6-updates-et.20261015.1200
mrt: {"kind":"A","peer":"2001:418:1::3","peer_as":3130,"prefix":"2803:9c0::/40","as_path":"3130 6939 212895 207036","origin":"IGP","next_hop":"2001:418:1::3","communities":["3130:376","6939:1000","6939:7525","6939:8840","6939:9001"],"otc":33108}
bgpkit: {"kind":"A","peer":"2001:418:1::3","peer_as":3130,"prefix":"2803:9c0::/40","as_path":"3130 6939 212895 207036","origin":"IGP","next_hop":"206.81.81.212","communities":["3130:376","6939:1000","6939:7525","6939:8840","6939:9001"],"otc":33108}
```

## Known from bgpdump's source or other inputs, in no fixture

Each was found while writing mrtdump (Task 11) by reading `bgpdump_lib.c` and
`bgpdump.c` at the pinned commit, or by running bgpdump on inputs that are
not fixtures. Each is malformed or unusual data; mrt's side is the RFC's.

- S1. A truncated NLRI (bgpdump's own `test_data/updates.nlri_mask_trailing_bits.gz`):
  the same lines, but mrtdump exits 1 and reports `bgp/nlri-length` (a /11
  needs 2 bytes and none are left); bgpdump exits 0 silently.
- S2. A known attribute with a wrong length (LOCAL_PREF, MED, NEXT_HOP,
  ORIGIN, AGGREGATOR, COMMUNITIES not a multiple of 4, LARGE_COMMUNITY not a
  multiple of 12): bgpdump prints what its partial read gives (B1 is one case);
  mrt leaves the field absent and reports it.
- S3. An AS_PATH segment of length 0: bgpdump prints a stray space; mrt treats
  the attribute as malformed (Ruling R4) and mrtdump prints `! Error !`.
- S4. An aggregator whose address is 255.255.255.255: bgpdump hides it (its
  "unset" value); mrtdump prints it.
- S5. A second MP_REACH_NLRI or MP_UNREACH_NLRI of another family in one
  UPDATE: bgpdump prints both; mrt keeps the first (duplicate attribute,
  Warning). RFC 7606 section 3 (g) makes the UPDATE itself malformed.
- S6. MP families with SAFI 3 (deprecated unicast+multicast): bgpdump prints
  them; mrt does not (`bgp.Family.HasPrefixes`, Ruling R9).
- S7. An MP_REACH_NLRI next hop whose length is not 4, 16 or 32: bgpdump
  leaves out next hop, LOCAL_PREF and MED (IPv4) or also path and origin
  (IPv6), shifting the columns, and in a RIB entry prints an uninitialised
  buffer; mrtdump keeps every column with an empty next hop, or falls back to
  NEXT_HOP in a RIB entry.
- S8. A LOCAL message over an IPv4 session withdrawing IPv6 prefixes: bgpdump
  formats the IPv4 peer address with `fmt_ipv6` (garbage); mrtdump prints it.
- S9. TABLE_DUMP_V2 prefixes with bits set beyond the length: bgpdump prints
  the bytes as stored; mrt masks the prefix.
- S10. Legacy TABLE_DUMP subtypes 3 and 4 (bgpdump's 32-bit-AS extension, not
  in RFC 6396): bgpdump prints them; mrt keeps them as `Other`.
- S11. A legacy TABLE_DUMP IPv6 record whose MP_REACH_NLRI next hop is 4
  bytes: bgpdump prints it through `fmt_ipv6` (garbage); mrtdump prints the
  IPv4 address.
- S12. A RIB entry whose peer index is not in the peer table: bgpdump aborts
  (assert); mrt reports it and goes on (Ruling R12). A RIB before any peer
  table: neither prints it; mrtdump exits 1.
- S13. Zebra's workarounds (AFI 0xFFFF in BGP4MP messages, 8-byte state
  changes): bgpdump special-cases them; mrt reports a malformed record. An
  8-byte state change prints a STATE line with 0.0.0.0 in bgpdump and nothing
  in mrtdump.
- S14. State changes whose length is not exactly 20/24 (IPv4) or 44/48
  (IPv6): bgpdump drops them with a warning; mrtdump prints them when mrt
  decodes them.
- S15. An empty COMMUNITIES attribute: bgpdump reads a byte past its string
  (undefined output); mrtdump prints an empty field.
- S16. COMMUNITIES and LARGE_COMMUNITY text is built in a `char buf[BUFSIZ]`
  (1024 on macOS, 8192 with glibc): measured on macOS, 150 communities print
  as 1022 characters cut mid-value; mrtdump prints all 150 (1799 characters).
  So no fixture route has a community field of 1000 characters or more
  (Ruling R16, `TestCommunityFieldsFitBgpdumpBuffer`).
- S17. AS_PATH text stops at about 7900 characters with `...` appended
  (MAX_ASPATH_LEN 8000); mrtdump prints the whole path.

## Command line and exit status

Pinned by `internal/mrtdump` tests (named below).

- C1. mrtdump reads several files; bgpdump one. mrtdump has no `-H`
  (bgpdump's default, multi-line); with neither `-m` nor `-M` it prints `-m`.
  bgpdump's `-t -p -O -s -v -q -i -f -o -T` are refused with exit 2
  (`TestMrtdumpRefusesUnknownFlags`, `TestMrtdumpModeFlags`).
- C2. Clustered flags (`-ml`) are refused with exit 2; bgpdump's getopt
  accepts them (`TestMrtdumpCommandLineDivergences`).
- C3. Every argument after the first file name is a file name, so `FILE -l`
  tries to open `-l` (exit 3, FILE still printed). bgpdump with glibc's
  getopt permutes and reads the flag; with BSD getopt (macOS) it prints its
  usage (`TestMrtdumpCommandLineDivergences`).
- C4. Exit status: bgpdump exits 0 on malformed input, or dies with SIGABRT
  where it asserts (B2, S12); mrtdump exits 1 and reports each problem on
  stderr, 3 when a file cannot be read (`TestMrtdumpMalformedExit1`,
  `TestMrtdumpMissingFile`, `TestMrtdumpReadErrors`). bgpdump logs to syslog
  unless given `-v`.
