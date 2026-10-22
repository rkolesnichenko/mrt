# package bmp against the oracles: every known difference

The oracle is bgpkit-parser 0.22.0's library, run by the harness
`internal/oracle/bgpkit-bmp` (its command line reads MRT only), which
`scripts/build-oracles.sh` installs as `bgpkit-bmp`. Its output on every
fixture of `testdata/bmp` is in `testdata/golden/NAME.bgpkit-bmp.json` (see
`bmp/golden_test.go`).

Each entry has an ID, the reason for the difference, and which side is right
("Decides"). The `pair` blocks are checked by `TestBMPDivergencesDocumented`
in both directions: every line on which mrt and an oracle differ must be in a
block, and every line in a block must still differ. A block names the fixture
(its file name without `.bmp` or `.obmp`), the line mrt gives and the line the
oracle gives; `(none)` means that side gives no line.

## bgpkit-parser

`bmp.RoutesWith` (or, for `.obmp`, `openbmp.Parse` and one `bmp.Session` per
router), its announcements and withdrawals normalized by `oracle.FromRoute`,
against the elems of `bgpkit-bmp FILE` normalized by `oracle.NormalizeBgpkit`
(`TestBMPMatchesBgpkit`).

### BK1. A 2-byte-AS_PATH peer (A flag set) keeps only 16 bits of its AS

Reason: in `bmp-as2` the peer 192.0.2.70 has the A flag set (its UPDATEs carry
2-byte AS_PATHs, with AS4_PATH beside them) and the Peer AS field holds
196618 (0x0003000a). bgpkit-parser's `parse_per_peer_header`
(`headers.rs`) skips the field's first two bytes when the A flag is set and
reads 10. mrt reads the 4-byte field whatever the flag. RFC 7854 section 4.2:
"Peer AS: The Autonomous System number of the peer from which the
encapsulated PDU was received. If a 16-bit AS number is stored in this field
[RFC6793], it should be padded with zeroes in the 16 most significant bits."
The A flag "indicates that the message is formatted using the legacy 2-byte
AS_PATH format": it is about the AS_PATH, not the Peer AS field. The routes'
AS paths agree (both merge AS4_PATH).

Decides: RFC 7854, for mrt. The peer at 192.0.2.72 (A flag, AS 64500) gives
the same line on both sides.

```pair
fixture: bmp-as2
mrt: {"kind":"A","peer":"192.0.2.70","peer_as":196618,"prefix":"198.51.100.0/24","as_path":"64500 196618 4200000001","origin":"IGP","next_hop":"192.0.2.254","aggregator":"196618 192.0.2.71"}
bgpkit: {"kind":"A","peer":"192.0.2.70","peer_as":10,"prefix":"198.51.100.0/24","as_path":"64500 196618 4200000001","origin":"IGP","next_hop":"192.0.2.254","aggregator":"196618 192.0.2.71"}
```

### BK2. A Loc-RIB route has no peer address

Reason: a Loc-RIB Instance Peer (peer type 3) has a zero-filled Peer Address.
RFC 9069 section 5.1: "Peer Address: Zero-filled. The remote peer address is
not applicable." mrt's `PeerHeader.Addr` and so `Route.Peer.Addr` is then the
zero `netip.Addr`, whatever the bytes (spec §7.1), which `oracle.FromRoute`
prints as `invalid IP`; bgpkit-parser sets `peer_ip` to 0.0.0.0 (and warns
when the bytes are not zero). Both say the field holds no address; only the
rendering differs. Every Loc-RIB route in the fixtures shows it.

Decides: RFC 9069, for mrt: there is no peer address, and 0.0.0.0 would read
as an IPv4 peer. (`invalid IP` is how the normalized line renders the zero
`netip.Addr`; mrt yields no such string.)

```pair
fixture: bmp-addpath
mrt: {"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","path_id":7,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","path_id":7,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-locrib-multi
mrt: {"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-locrib-multi
mrt: {"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-locrib-multi
mrt: {"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"2001:db8:160::/48","as_path":"64501 64502","origin":"IGP","next_hop":"2001:db8::fe"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"2001:db8:160::/48","as_path":"64501 64502","origin":"IGP","next_hop":"2001:db8::fe"}
```

```pair
fixture: bmp-locrib-multi
mrt: {"kind":"W","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1}
bgpkit: {"kind":"W","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1}
```

```pair
fixture: bmp-peer-flags
mrt: {"kind":"A","peer":"invalid IP","peer_as":64540,"prefix":"10.3.0.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64540,"prefix":"10.3.0.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-peer-flags
mrt: {"kind":"A","peer":"invalid IP","peer_as":64540,"prefix":"10.3.1.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64540,"prefix":"10.3.1.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: obmp-v4-router
mrt: {"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","path_id":7,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","path_id":7,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: obmp-v6-router
mrt: {"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","path_id":7,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","path_id":7,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

### BK3. A path identifier of 2^24 or more is read as plain NLRI

Reason: bgpkit-parser 0.22.0 parses every Route Monitoring UPDATE with ADD-PATH
off (`route_monitoring.rs`: `parse_bgp_message(data, false, asn_len)`) and
then guesses from the bytes: `parse_nlri_list` (`src/parser/utils.rs`
lines 283-326, used for NLRI, withdrawn routes and MP_REACH/MP_UNREACH)
reads an entry as ADD-PATH when its first byte is 0. Once on, the guess
holds for the following entries of the list until one fails to parse, which
is then read in the plain form and the guess starts over. mrt takes ADD-PATH
from the peer's Peer Up only (spec §7.5). RFC 7911 section 5: a speaker that
advertised and received the capability for an <AFI, SAFI> "MUST ... use the
extended NLRI encodings specified in this document. The peer SHALL act
accordingly in processing an UPDATE message related to a particular <AFI,
SAFI>"; nothing in the NLRI says which form it is in.

In `bmp-addpath-guess` the peer 192.0.2.190 negotiated IPv4 ADD-PATH in both
directions and announces 198.51.100.0/24 with path identifier 0x01000000
(bytes `01 00 00 00 18 c6 33 64`). The first byte is not 0, so bgpkit reads
the plain form: 0.0.0.0/1 (`01 00`), then two 0.0.0.0/0 (each a failed guess
read plain) and 198.51.100.0/24 without a path identifier. mrt reads the one
route the Peer Up says is there.

Decides: RFC 7911, for mrt.

```pair
fixture: bmp-addpath-guess
mrt: {"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"198.51.100.0/24","path_id":16777216,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"198.51.100.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-addpath-guess
mrt: (none)
bgpkit: {"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"0.0.0.0/1","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-addpath-guess
mrt: (none)
bgpkit: {"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"0.0.0.0/0","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

```pair
fixture: bmp-addpath-guess
mrt: (none)
bgpkit: {"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"0.0.0.0/0","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

### BK4. Plain NLRI that also parse as ADD-PATH are read as ADD-PATH

Reason: the guess of BK3 the other way round. In `bmp-addpath-guess` the peer
192.0.2.191 has no ADD-PATH (its Peer Up has no capability) and announces
0.0.0.0/0, 10.11.0.0/16 and 198.51.100.0/24: bytes `00 10 0a 0b 18 c6 33
64`. The first byte is 0, and the eight bytes also parse as one ADD-PATH
entry: path identifier 0x00100a0b (1051147) and 198.51.100.0/24. bgpkit
loses the first two routes and gives the third a path identifier. A lone
0.0.0.0/0, or one followed by bytes that do not parse as ADD-PATH, falls
back to the plain form and agrees.

Decides: RFC 7911 section 5 (quoted in BK3), for mrt: without the
capability the NLRI are in the plain form.

```pair
fixture: bmp-addpath-guess
mrt: {"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"0.0.0.0/0","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: (none)
```

```pair
fixture: bmp-addpath-guess
mrt: {"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"10.11.0.0/16","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: (none)
```

```pair
fixture: bmp-addpath-guess
mrt: {"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"198.51.100.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
bgpkit: {"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"198.51.100.0/24","path_id":1051147,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}
```

## Known from bgpkit-parser's source, in no differing line

Read in bgpkit-parser 0.22.0's source; the fixtures named give the same
lines on both sides, so nothing checks these.

- BS1. Where the ADD-PATH guess (BK3) agrees with the Peer Up. The other
  ADD-PATH fixtures (`bmp-addpath`, `bmp-reup`, `bmp-no-peer-up`,
  `bmp-open-extended`, `bmp-locrib-multi`, the `.obmp` ones) give the same
  lines on both sides: every path identifier in them is below 2^24 (first
  byte 0, so the guess is ADD-PATH and right), and no plain list in them
  starts with prefix length 0.
- BS2. Peer types other than 0-3 are an error in `parse_per_peer_header`, so
  the message is lost. RFC 7854 section 4.2 defines peer types 0-2 ("Currently,
  three types of peers are identified") and RFC 9069 section 4.1 adds 3
  ("Peer Type = 3: Loc-RIB Instance Peer"); neither says what a type's Route
  Monitoring means beyond those, while the per-peer header's layout does not
  depend on the type. So mrt reads the message and refuses its Route
  Monitoring with a `*MessageError` (spec §7.1, §7.4): it would have to guess
  the AS width and the view. `bmp-garbage`'s peer type 9 gives no line on
  either side.
- BS3. A message bgpkit-parser rejects costs only that message, because the
  harness frames the stream itself (`bgpkit-bmp` prints each to standard
  error as `skip: OFFSET: ERROR`): two Peer Ups of `bmp-bad-open` (offsets
  826 and 1099) and two Statistics Reports of `bmp-stats` (991 and 1119),
  "Corrupted BGP message"; in `bmp-garbage` the length 3, version 2, two
  zero-header, peer type 9 and message type 9 messages. None of them carries
  a route on either side.
- BS4. OpenBMP: `parse_openbmp_header` accepts version 1.7, router messages,
  object type 12 and a row count of 1 only, as `openbmp.Parse` does; it does
  not check the stated header length against the fields it read (the BMP
  message starts after the row count), where `openbmp.Parse` refuses a
  mismatch.
