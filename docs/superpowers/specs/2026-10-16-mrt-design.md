# mrt: a streaming MRT reader and BGP UPDATE decoder for Go

Status: draft for review · 2026-10-16 · new repository `github.com/rkolesnichenko/mrt`, first release v0.1.0

This file is written in the rpsl repository because the new repository does
not exist yet. It moves to `mrt/docs/superpowers/specs/` with that
repository's first commit (my call).

## 1. Summary

The RIPE RIS and RouteViews route collectors publish what BGP routers
actually announced, as MRT files (RFC 6396): full table snapshots every few
hours and update files every 5 to 15 minutes. rpsl reads what operators
*registered*. Comparing the two ("which of my peer's real announcements does
my policy reject, and at which clause") needs a reader for those files. rpsl's
design reserves that work for a separate consumer (design §13: AS-path
matching against real paths "belongs in a separate `bgp` consumer").

Go has no MRT reader that gets current collector files right. A research
pass on 2026-10-01 (`research_notes/Go RPSL sibling library landscape/mrt_bgp.md`
in the rpsl repo; figures below are from it, none re-measured in this
session) ran the existing readers over three current files:

- gobgp v4 `pkg/packet/mrt` rejected all 85,572 records of a RouteViews
  updates file (no BGP4MP_ET support). It also failed with "seg length is
  short" on 235 of the 235 2-byte-AS `BGP4MP_MESSAGE` UPDATEs with an AS_PATH
  in a RIS rrc00 file, because it reads their AS_PATH as 4-byte. It allocated
  3,197 MB to decode a 496 MB route-views6 RIB.
- bgptools/fgbgp fails on the first BGP4MP_ET record and uses `net.IP`.
- bgpfix rejects TABLE_DUMP_V2 add-path subtypes and pulls in 22 packages.
- kaorimatz/go-mrt aborts on attribute 35 (OTC) within the first 66 records.
- mdlayher/bgp's MRT reader is `internal/` (re-checked 2026-10-16: still is).

`mrt` is a new, dependency-free Go module that reads every MRT record type
the collectors write today, streams with bounded memory, never panics, keeps
going past a bad record or attribute, and is held byte for byte to bgpdump
and field by field to bgpkit-parser.

### Decisions taken in brainstorming

1. **Separate repository, named `mrt`** (the user's decision).
   `github.com/rkolesnichenko/mrt`, a single Go module. Not a module inside
   rpsl: it imports nothing from rpsl, has its own release cadence, CI and
   fixture data, and its import path should say "MRT", not "RPSL".
2. **First consumer is IRR-vs-BGP reconciliation** (my suggestion; the user
   asked me to suggest). The main output is therefore a stream of routes
   (peer, prefix, path attributes), with full-fidelity records underneath.
3. **No dependency on rpsl**, including `rpsl/types` (the user's decision).
   ASNs are `bgp.ASN` (`uint32`), prefixes `netip.Prefix`. Reconciliation
   converts. Importing `types` would tie `mrt`'s releases to rpsl's.
4. **Not built on mdlayher/bgp or gobgp** (my call). Both need Go ≥ 1.25
   (mdlayher 1.27); `mrt` keeps rpsl's floor, Go 1.23 (the first with
   `iter.Seq2`), and its `go.mod` has no requirements at all.
5. **rpsl's conventions carry over by copy** (my call): API conventions
   C1–C3, the `apisurface` signature golden, `check.sh`/`fuzz.sh`, the
   release script, the CHANGELOG style, a CLAUDE.md operating contract. The
   rpsl tools are `internal/`, so they are copied and trimmed, not imported.
6. **Deterministic, never heuristic** (rpsl's "never guesses"): AS width
   comes from the record type (§4.2), not from sniffing bytes.

### Facts this design rests on (checked 2026-10-16)

From the RFC texts and IANA's MRT registry (last updated 2020-01-08):

- **ET length** (RFC 6396 §3): "The Microsecond Timestamp immediately
  follows the Length field in the MRT Common Header … The Microsecond
  Timestamp is included in the computation of the Length field value." The
  Length field otherwise "does not include the length of the MRT Common
  Header". So an ET record's body is `Length − 4` bytes after a 16-byte
  header.
- **AS width per type:**
  - `BGP4MP_MESSAGE` (§4.4.2): "The AS_PATH contained in these messages
    MUST only consist of 2-byte AS numbers."
  - `BGP4MP_MESSAGE_AS4` (§4.4.3): "The AS_PATH in these messages MUST only
    consist of 4-byte AS numbers."
  - TABLE_DUMP_V2 RIB entries (§4.3.4): "All AS numbers in the AS_PATH
    attribute MUST be encoded as 4-byte AS numbers."
  - TABLE_DUMP (§4.2): "The AS_PATH attribute MUST only consist of 2-byte AS
    numbers."
  - bgpdump (`bgpdump_lib.c`) uses the same rule: `ASN16_LEN` for
    MESSAGE/STATE_CHANGE and their LOCAL/ADDPATH forms, `ASN32_LEN` for the
    AS4 forms, 4 for RIB entries.
- **Abbreviated MP_REACH_NLRI in RIB entries** (§4.3.4): "only the Next Hop
  Address Length and Next Hop Address fields are included. The Reserved
  field is omitted." bgpdump tells the two forms apart by the first byte:
  non-zero is the abbreviated form (a next-hop length), zero is the full RFC
  4760 form (an AFI's high byte).
- **Peer Type bits** (§4.3.1): bit 6 is the peer AS size (0 = 16 bits,
  1 = 32 bits), bit 7 the peer IP family (0 = IPv4, 1 = IPv6). Peer index
  numbering starts at 0.
- **Add-path** (RFC 8050): BGP4MP subtypes 8–11 (MESSAGE_ADDPATH,
  MESSAGE_AS4_ADDPATH, MESSAGE_LOCAL_ADDPATH, MESSAGE_AS4_LOCAL_ADDPATH);
  TABLE_DUMP_V2 subtypes 8–12 (RIB_{IPV4,IPV6}_{UNICAST,MULTICAST}_ADDPATH,
  RIB_GENERIC_ADDPATH). AFI/SAFI-specific add-path RIB entries carry a 4-byte
  Path Identifier between Originated Time and Attribute Length.
  RIB_GENERIC_ADDPATH's entries are not redefined; its NLRI carries the
  path ID.
- **IANA subtypes:** TABLE_DUMP_V2 1–12 assigned (1 PEER_INDEX_TABLE, 2–5
  RIB_IPV{4,6}_{UNICAST,MULTICAST}, 6 RIB_GENERIC, 7 GEO_PEER_TABLE, 8–12
  add-path). BGP4MP 0–11 assigned (2 ENTRY and 3 SNAPSHOT deprecated).
  Types: 11 OSPFv2, 12 TABLE_DUMP, 13 TABLE_DUMP_V2, 16 BGP4MP, 17 BGP4MP_ET,
  32/33 ISIS(_ET), 48/49 OSPFv3(_ET); 0–10 deprecated.
- **AS4_PATH merge** (RFC 6793 §4.2.3): when both AGGREGATOR and
  AS4_AGGREGATOR are present and AGGREGATOR's AS is not AS_TRANS, ignore
  AS4_AGGREGATOR and AS4_PATH (bgpdump's `process_asn32_trans` checks both
  too). Confederation segments in AS4_PATH are discarded and the rest kept
  (RFC 6793 §6). Otherwise count ASes as RFC 4271
  §9.1.2.2 does (an AS_SET counts as 1; confederation segments count 0). If
  AS_PATH has fewer than AS4_PATH, ignore AS4_PATH. Otherwise take leading
  ASes and segments from AS_PATH and prepend them to AS4_PATH until the count
  equals AS_PATH's, prepending a confederation segment that leads or is
  adjacent to a prepended segment.
- **bgpdump, current master** (last code change 2023-11-05, after the
  v1.6.2 tag of 2020-06-07): reads BGP4MP_ET and prints `.%06ld`
  microseconds; reads both add-path forms (line prefixes such as
  `TABLE_DUMP2_AP`, `BGP4MP_ET_AP`); prints large communities only with `-l`
  and unknown attributes only with `-u` (added 2019-02-09). Its attribute
  table stops at 32 (LARGE_COMMUNITY), so OTC (35) is unknown to it. The
  2020-09-20 change (dynamic NLRI allocation, RFC 8654) postdates v1.6.2, so
  the oracle is master at a pinned commit, not the tag.
- **No oracle is installed on this machine**: `bgpdump`, `bgpkit-parser` and
  `bgpreader` are all absent.

## 2. Architecture

One module, `github.com/rkolesnichenko/mrt`, `go 1.23`, no requirements.

```
mrt/                 package mrt: framing, record types, Routes, Decompress
  bgp/               package bgp: BGP messages, path attributes, AS paths, NLRI
  cmd/mrtdump/       bgpdump -m compatible printer (the differential's subject)
  internal/apisurface/   copied from rpsl, trimmed
  internal/mrtgen/       test-only encoder for synthetic fixtures (§7.3)
  internal/oracle/       test-only: run bgpdump / bgpkit-parser, normalize
  internal/compare/      nested module (own go.mod): benchmark against gobgp
  testdata/              fixtures, goldens, divergences.md
  scripts/               check.sh, fuzz.sh, fetch-mrt.sh, release.sh
```

`bgp` knows nothing about MRT except a decoding context (§4.1), so a later
BMP or RIS Live reader can reuse it. `mrt` imports `bgp`. Import direction is
strictly `cmd → mrt → bgp`. No package opens a socket. `Decompress` reads an
`io.Reader`; opening files is the caller's job (and `mrtdump`'s).

`internal/compare` is its own nested module, so gobgp never enters `mrt`'s
`go.mod` and the published module zip excludes it.

## 3. Conventions (copied from rpsl, held by `apisurface -check`)

- **C1** Options: `X(in)` uses the zero options, `XWith(in, XOptions)` takes
  all of them.
- **C2** A whole-value parser returns `(T, []Diagnostic)`; a single value's
  parser `(T, error)`. A trailing `ok bool` only for an expected false.
- **C3** Sentinels use `errors.New`. A type is named after the protocol
  element it is.
- Parsed values (records, paths, routes) are values, not pointers.
  Exception (user's decision 2026-10-20, v0.2 design §6.4):
  `(*bgp.Interner).Decode(Attrs) (*Path, []Diagnostic)` returns a pointer,
  because the path is shared with every caller that decoded an equal block
  and sharing is the interner's purpose (`Route.Path` is already a
  `*bgp.Path`). `Attrs.Decode` still returns a value, and there is no
  `Attrs.DecodeWith`: C1 would require it to return what `Decode` returns.
- `api/<pkg>.txt` is the golden of every exported signature;
  `MRT_API_UPDATE=1` rewrites it.

## 4. Package `bgp`

### 4.1 Types

```go
package bgp

type ASN uint32

const ASTrans ASN = 23456

type AFI uint16  // AFIIPv4 = 1, AFIIPv6 = 2
type SAFI uint8  // SAFIUnicast = 1, SAFIMulticast = 2

type Family struct {
	AFI  AFI
	SAFI SAFI
}

// Context says how to read bytes that do not describe themselves.
type Context struct {
	AS4     bool // AS_PATH and AGGREGATOR carry 4-byte ASNs
	AddPath bool // every NLRI is preceded by a 4-byte path identifier
	RIB     bool // MRT RIB entry: MP_REACH_NLRI may be abbreviated (RFC 6396 §4.3.4)
}

type MessageType uint8 // Open = 1, Update = 2, Notification = 3, Keepalive = 4, RouteRefresh = 5

type NLRI struct {
	Prefix    netip.Prefix
	PathID    uint32
	HasPathID bool
}

type Update struct {
	Withdrawn []NLRI // IPv4 unicast, from the Withdrawn Routes field
	Attrs     Attrs
	Announced []NLRI // IPv4 unicast, from the NLRI field
}

// Attrs is an undecoded block of path attributes and the context to read it with.
type Attrs struct{ /* raw []byte; ctx Context */ }

func NewAttrs(raw []byte, ctx Context) Attrs
func (a Attrs) Raw() []byte
func (a Attrs) All() iter.Seq2[Attribute, error] // walk the raw attributes in order
func (a Attrs) Decode() (Path, []Diagnostic)

type AttrCode uint8 // Origin = 1 … LargeCommunity = 32, OnlyToCustomer = 35

type Attribute struct {
	Flags uint8
	Code  AttrCode
	Value []byte
}

type Origin uint8 // OriginIGP, OriginEGP, OriginIncomplete

type SegmentType uint8 // ASSet = 1, ASSequence = 2, ASConfedSequence = 3, ASConfedSet = 4

type Segment struct {
	Type SegmentType
	ASNs []ASN
}

type ASPath struct{ Segments []Segment }

func (p ASPath) Len() int            // RFC 4271 §9.1.2.2 count: a set is 1, confed segments 0
func (p ASPath) Origin() (ASN, bool) // last ASN of a final AS_SEQUENCE; false if empty or it ends in a set
func (p ASPath) String() string      // bgpdump's notation: "1 2 {3,4}"

type Aggregator struct {
	AS   ASN
	Addr netip.Addr
}

type Community uint32

func (c Community) String() string // "65000:1"

type LargeCommunity struct{ Global, Local1, Local2 uint32 }
type ExtCommunity [8]byte

type MPReach struct {
	Family        Family
	NextHop       netip.Addr // global
	LinkLocal     netip.Addr // second IPv6 next hop, when present
	NLRI          []NLRI     // decoded for IPv4/IPv6 unicast and multicast only
	RawNLRI       []byte     // other families: kept raw
	Abbreviated   bool       // RFC 6396 §4.3.4 form
}

type MPUnreach struct {
	Family    Family
	Withdrawn []NLRI
	RawNLRI   []byte
}

// Path is a decoded attribute block. Presence of an optional scalar is its Has* flag.
type Path struct {
	Origin             Origin
	HasOrigin          bool
	ASPath             ASPath // AS_PATH merged with AS4_PATH (RFC 6793 §4.2.3)
	ReceivedASPath     ASPath // AS_PATH as it was on the wire
	AS4Path            ASPath // AS4_PATH as it was on the wire, if any
	NextHop            netip.Addr
	MED                uint32
	HasMED             bool
	LocalPref          uint32
	HasLocalPref       bool
	AtomicAggregate    bool
	Aggregator         Aggregator // after the AS4_AGGREGATOR rule
	HasAggregator      bool
	Communities        []Community
	ExtCommunities     []ExtCommunity
	LargeCommunities   []LargeCommunity
	OnlyToCustomer     ASN // RFC 9234 OTC
	HasOnlyToCustomer  bool
	OriginatorID       netip.Addr
	ClusterList        []netip.Addr
	MPReach            *MPReach
	MPUnreach          *MPUnreach
	Unknown            []Attribute // every attribute not decoded above, in order, raw
}

func ParseUpdate(msg []byte, ctx Context) (Update, []Diagnostic) // msg: whole message, marker included
type MessageHeader struct {
	Type   MessageType
	Length int // from the header, marker and header included
}

func ParseMessageHeader(msg []byte) (MessageHeader, error)

type Severity uint8 // Info, Warning, Error

type Diagnostic struct {
	Severity Severity
	Code     string   // stable, e.g. "bgp/attr-length", "bgp/as4path-ignored"
	Offset   int      // byte offset in the parsed input
	Attr     AttrCode // 0 when not about one attribute
	Message  string
}
```

### 4.2 Decoding rules

- **AS width is the context's**, never sniffed. A 2-byte AS_PATH that does
  not parse at width 2 is a Warning diagnostic and an empty `ASPath`, with the
  raw attribute in `Unknown`, never re-read at width 4.
- **AS4_PATH/AS4_AGGREGATOR** are merged only when `ctx.AS4` is false, by the
  RFC 6793 §4.2.3 rule quoted in §1. With `ctx.AS4` true they are ignored and
  noted with an Info diagnostic (bgpdump also merges only for 2-byte input).
- **MP_REACH_NLRI with `ctx.RIB`:** first byte non-zero → abbreviated (next
  hop only; the family and NLRI come from the record). First byte zero → full
  RFC 4760 form, with no diagnostic (`Abbreviated` false records the form, and
  route-views6 writes it on every entry, so it is not a fault); its family and
  next hops are decoded and its NLRI is not kept (the record supplies the
  prefix; the bytes stay in `Attrs.Raw()`), amended 2026-10-20 by the user (Ruling R20). Same test as bgpdump. Without
  `ctx.RIB`, always the full form, NLRI parsed.
- **A malformed attribute never stops decoding.** Its raw bytes go to
  `Unknown`, a diagnostic names it, and decoding continues with the next
  attribute when the length framing allows. When the framing itself is
  broken, the rest of the block goes to one `Unknown` entry and decoding
  stops there.
- **Duplicate attributes:** the first wins (RFC 7606 §3.g); later ones go to
  `Unknown` with a Warning.
- **Lengths are checked before allocating.** No count read from input sizes a
  `make` before it is checked against the bytes remaining.
- **Prefix bits beyond the length** are masked, as bgpdump does since
  2020-09-20, with an Info diagnostic.

## 5. Package `mrt`

### 5.1 Records

```go
package mrt

type Type uint16    // the IANA type codes in §1
type Subtype uint16 // TD2*, BGP4MP*, TD* constants

type Header struct {
	Time    time.Time // seconds, plus microseconds for *_ET types
	Type    Type
	Subtype Subtype
	Length  uint32 // as on the wire; for *_ET it includes the 4 microsecond bytes
}

// Record is sealed: the types below are its only implementations.
type Record interface {
	Header() Header
	Raw() []byte // the whole record, header included, exactly as read
	isRecord()
}

type Peer struct {
	BGPID netip.Addr // zero when the record does not carry one
	Addr  netip.Addr
	AS    bgp.ASN
}

type PeerIndexTable struct {
	CollectorID netip.Addr
	ViewName    string
	Peers       []Peer
	// header, raw
}

type RIB struct {
	Sequence uint32
	Family   bgp.Family
	Prefix   netip.Prefix // zero for RIB_GENERIC with a family other than IPv4/IPv6 unicast/multicast
	PathID   uint32       // RIB_GENERIC_ADDPATH: carried in the NLRI
	RawNLRI  []byte       // RIB_GENERIC
	AddPath  bool
	// header, raw, the undecoded entry block
}

// Entries walks the RIB entries lazily, in order. An entry whose framing is
// broken yields one error (wrapping ErrMalformed) and ends the walk; the
// entries before it are already yielded.
func (r RIB) Entries() iter.Seq2[RIBEntry, error]
func (r RIB) EntryCount() uint16 // as the record states it

type RIBEntry struct {
	PeerIndex  uint16
	Originated time.Time
	PathID     uint32 // add-path subtypes only
	Attrs      bgp.Attrs // Context{AS4: true, RIB: true}
}

type TableDump struct { // legacy type 12
	View       uint16
	Sequence   uint16
	Prefix     netip.Prefix
	Status     uint8
	Originated time.Time
	Peer       Peer
	Attrs      bgp.Attrs // Context{AS4: false, RIB: true}
	// header, raw
}

type BGP4MPMessage struct {
	PeerAS, LocalAS     bgp.ASN
	IfIndex             uint16
	PeerAddr, LocalAddr netip.Addr
	AS4, Local, AddPath bool    // from the subtype
	Message             []byte  // whole BGP message, marker included
	// header, raw
}

func (m BGP4MPMessage) Context() bgp.Context
func (m BGP4MPMessage) MessageType() (bgp.MessageType, error)

type BGP4MPStateChange struct {
	PeerAS, LocalAS     bgp.ASN
	IfIndex             uint16
	PeerAddr, LocalAddr netip.Addr
	AS4                 bool
	Old, New            State // Idle = 1 … Established = 6
	// header, raw
}

// Other is every record not decoded above: OSPF, IS-IS, GEO_PEER_TABLE,
// deprecated types and subtypes, and unassigned codes. Kept raw, never an error.
type Other struct{ /* header, raw */ }
```

### 5.2 Reading

```go
func Records(r io.Reader) iter.Seq2[Record, error]
func RecordsWith(r io.Reader, opts Options) iter.Seq2[Record, error]

type Options struct {
	MaxRecordLen uint32 // 0 means DefaultMaxRecordLen (16 MiB)
}

type RecordError struct {
	Offset int64  // of the record's first byte in the stream
	Header Header
	Raw    []byte // nil when the record was over MaxRecordLen and skipped
	Err    error  // ErrMalformed, ErrTooLarge
}

var (
	ErrTruncated = errors.New("mrt: truncated record")
	ErrMalformed = errors.New("mrt: malformed record")
	ErrTooLarge  = errors.New("mrt: record longer than MaxRecordLen")
)

func Decompress(r io.Reader) (io.Reader, error) // gzip by 1f 8b, bzip2 by "BZh", else r unchanged
```

- **Framing never depends on the body.** Each step reads the 12-byte header
  (16 for `*_ET`), then exactly `Length` bytes. Reading decodes only a
  record's fixed fields (my call, so damage stays as local as possible):
  - A record whose fixed fields do not fit (a RIB header, a peer index
    table, a BGP4MP header, a state change) yields
    `(nil, *RecordError{Err: ErrMalformed})`, and iteration continues with
    the next record.
  - A broken RIB entry is found only by `RIB.Entries`. The entries before it
    survive.
  - A broken BGP message inside a `BGP4MPMessage` (bad marker, bad length)
    is found only by `MessageType()` or `bgp.ParseUpdate`, as diagnostics. A record over `MaxRecordLen` is discarded without
  being buffered, yielding `ErrTooLarge`, and iteration continues.
- **A stream that ends mid-record**, or a read error, yields one error
  wrapping `ErrTruncated` (or the reader's error) and iteration ends.
- **Each record owns its bytes**: one buffer per record, every slice in it
  points into that buffer, and it stays valid after the loop moves on. RIB
  entries and attributes are not decoded while reading; `RIB.Entries` and
  `Attrs.Decode` do that on demand. Beyond the buffer, reading costs a
  small constant number of allocations per record (the interface box, a
  peer table's `[]Peer`), never one per entry.
- **Lossless:** concatenating, in yield order, `Raw()` of every record and
  `RecordError.Raw` of every `ErrMalformed` error reproduces the input byte
  for byte, unless an `ErrTooLarge` or `ErrTruncated` was yielded.

### 5.3 Routes

```go
type Kind uint8 // RIBEntry ('B'), Announce ('A'), Withdraw ('W')

type Route struct {
	Time        time.Time // record time; Originated is on the entry for RIB routes
	Kind        Kind
	Peer        Peer
	Family      bgp.Family
	Prefix      netip.Prefix
	PathID      uint32
	HasPathID   bool
	Path        *bgp.Path        // nil for Withdraw; shared by every route from one UPDATE or RIB entry
	Diagnostics []bgp.Diagnostic // from decoding Path; shared the same way
}

func Routes(records iter.Seq2[Record, error]) iter.Seq2[Route, error]
```

- One route per (peer, prefix, path ID): from TABLE_DUMP and TABLE_DUMP_V2
  RIB entries (`RIBEntry`); from BGP4MP(_ET) UPDATEs, the NLRI field and
  MP_REACH (`Announce`), the withdrawn field and MP_UNREACH (`Withdraw`).
- `Path` is decoded once per UPDATE or RIB entry and shared. Callers must not
  modify it.
- IPv4/IPv6 unicast and multicast only. Other families, state changes,
  non-UPDATE messages and `*_LOCAL` messages (the collector's own outbound)
  yield no routes; they stay visible through `Records`.
- A RIB entry whose peer index is missing from the last PEER_INDEX_TABLE
  (or arrives before one) yields a `RecordError` for that entry and the
  others continue. A broken entry yields the error `RIB.Entries` gave; the
  record's earlier entries are already routes.
- Record errors pass through unchanged.

## 6. `mrtdump`

A bgpdump-compatible printer, the subject of the differential, the way
`rpslq` is bgpq4's.

```
mrtdump [-m | -M] [-l] [-u] FILE...
```

- `-m`: one line per entry, Unix timestamps (bgpdump `-m`). `-M`: the same
  with human-readable timestamps. `-l`: large communities after the
  communities field. `-u`: unknown attributes. Any other bgpdump option is
  refused with exit status 2 and a message naming it.
- Reads `.gz`, `.bz2` or raw MRT via `Decompress`; `-` is stdin.
- Output is byte for byte what bgpdump (pinned commit) prints for the same
  file and options, line prefixes (`TABLE_DUMP2`, `TABLE_DUMP2_AP`,
  `BGP4MP_ET`, …) and `.%06d` microseconds included. Every deliberate
  difference is pinned in `testdata/bgpdump/divergences.md` and in a test
  (`TestDivergencesDocumented` checks both directions, as in rpsl).
- Exit status: 0 done, 1 some records or attributes were malformed (each
  reported on stderr, output continues), 2 bad command line, 3 a file could
  not be read.

## 7. Tests

### 7.1 Unit

- Table tests per record type and subtype from hand-built bytes, starting
  with RFC 6396 Appendix A's two encodings.
- AS4 merge: one test per sentence of RFC 6793 §4.2.3 (fewer, equal, more;
  AS_SET counting; confed prepend; AGGREGATOR ≠ AS_TRANS).
- Abbreviated vs full MP_REACH in a RIB entry; 32-byte IPv6 next hop.
- Every `Decompress` branch, multistream gzip included.

### 7.2 Fixtures and goldens

`testdata/mrt/` holds small pinned real files, each with a line in
`testdata/README.md` giving its source URL, download date and sha256:

1. A RouteViews 15-minute updates file (all BGP4MP_ET).
2. A RIS 5-minute updates file containing 2-byte `BGP4MP_MESSAGE` UPDATEs
   and state changes.
3. RIB slices: the PEER_INDEX_TABLE plus the first records of a
   route-views6 RIB and of a RIS bview, cut at a record boundary and
   recompressed (IPv6 abbreviated MP_REACH, both families).
4. A legacy TABLE_DUMP slice from an archive written before TABLE_DUMP_V2.
5. Add-path: a real file if a collector writing RFC 8050 subtypes is found
   (open fact, §10); otherwise synthetic, from `internal/mrtgen` (§7.3).

Budget: under 10 MB in the repository (my call). For every fixture,
`testdata/golden/` holds bgpdump's `-m -l -u` output and bgpkit-parser's
normalized JSON, both produced by the pinned oracles.

- `TestGoldens`: `mrtdump -m -l -u` equals the bgpdump golden. Needs no
  oracle installed.
- `TestMatchesBgpkit`: `Routes` normalized equals the bgpkit golden, field
  by field: peer, prefix, path ID, AS path, origin, next hop, MED,
  local-pref, communities, large communities, OTC, aggregator, atomic
  aggregate. bgpkit is the oracle for OTC and add-path details bgpdump does
  not print.
- `TestMatchesBgpdump` / `TestMatchesBgpkitLive`: when the binaries are on
  PATH, regenerate and compare. `MRT_GOLDEN_UPDATE=1` rewrites the goldens
  (review the diff).
- `TestLossless`: the §5.2 concatenation equals each fixture's decompressed
  bytes.

**Oracle pinning:** bgpdump built from `RIPE-NCC/bgpdump` master at a named
commit; bgpkit-parser at a named release (its text format changed in 0.22.0;
JSON is used, version still pinned). CI installs both; locally they are
optional and the live tests skip. How CI installs them is left to the plan.

### 7.3 Synthetic fixtures

`internal/mrtgen` writes records for cases the real files lack: add-path in
both families, RIB_GENERIC, 32-byte next hops, AS4_PATH merge cases,
duplicate and malformed attributes, OTC. It is test-only and never exported
(writing MRT is out of scope). Its output is not trusted on its own: every
synthetic file is also decoded by both oracles and the result checked into
the goldens, so `mrtgen` and the decoder never only agree with each other.

### 7.4 Fuzz (never panic)

`FuzzRecords`, `FuzzParseUpdate`, `FuzzDecodeAttrs` (all four contexts),
`FuzzRoutes`, `FuzzASPathMerge`. Properties beyond "no panic":

- Lossless (§5.2) whenever no `ErrTooLarge` or `ErrTruncated` was yielded.
- Bounded allocation: no count read from the input sizes an allocation
  before it is checked against the bytes remaining, so a short input
  claiming a huge count allocates little (the plan picks the mechanism).
- `ASPath.Len()` after merge equals the received AS_PATH's `Len()` whenever
  AS4_PATH was used.

Seeds come from the fixtures. `scripts/fuzz.sh` runs them, sharded in CI as
rpsl does.

### 7.5 Real data (opt-in)

`scripts/fetch-mrt.sh` downloads a pinned set into `.data/` (RIS rrc00 bview,
415 MB gzip on 2026-09-30; route-views2 and route-views6 RIBs; one hour of
RIS and RouteViews updates). `MRT_REALDATA=$PWD/.data go test -run TestRealData`
streams `mrtdump` and bgpdump side by side and compares line by line without
holding either output, then reports record counts, malformed counts, wall
time and peak RSS. One record count and its denominator are checked by hand
against the file before calling the release done. These runs go one at a
time, alone, under a memory guard.

### 7.6 Performance

- Benchmarks on the fixtures: `Records` alone, `Routes` with decode.
- `internal/compare` (nested module) measures `TotalAlloc` for gobgp and
  `mrt` over the same route-views6 RIB. Targets (my calls):
  - **Release gate:** `Routes` with decode allocates less than gobgp on the
    same file.
  - **Reported, not a gate:** `Records` alone, measured as bytes allocated
    per decompressed input byte; the aim is at most 1.25 (the record buffers
    plus the per-record constant of §5.2).
  - The research figure (3,197 MB, 2026-10-01) is re-measured before it is
    compared with anything.
- bzip2 decompression time is measured separately. The standard library's
  bzip2 is slow; that is a number to report, not a design change.

## 8. Repository, docs and release

- `README.md`, `LICENSE` (MIT, as rpsl), `CHANGELOG.md`, `CLAUDE.md` (the
  operating contract: principles, the AS-width rule, oracle rules, commands),
  `docs/mrtdump.md`, `testdata/bgpdump/divergences.md`.
- `scripts/check.sh` (gofmt, vet, staticcheck, govulncheck, `-race` tests,
  `apisurface -check`, coverage), `scripts/fuzz.sh`, `scripts/release.sh`
  (one module: tag, wait for the proxy, verify from an empty module cache,
  build `mrtdump` for the same five platforms as rpsl's tools, attach to the
  GitHub release).
- CI: Go 1.23.x and stable, as rpsl; a fuzz job in two shards; the oracles
  installed for the differential.
- Creating the GitHub repository and the first push are outward-facing; they
  wait for the user's go-ahead.
- Releases are 0.x until the API settles, as rpsl's.

## 9. Open questions for the user

None. (Q1, the ASN type: `bgp.ASN`, no rpsl import, decided 2026-10-16.)

## 10. Open facts (to settle during planning)

- Which collector, if any, writes RFC 8050 add-path subtypes today. The
  2026-10-01 samples had none.
- RIS and RouteViews terms for redistributing sample files in a repository;
  attribution goes in `testdata/README.md`.
- Whether bgpdump master handles RIB_GENERIC and multicast RIBs, and what it
  prints for them. Where it does not, bgpkit is the only oracle and
  `divergences.md` says so.
- bgpdump's exact notation for confederation segments and for `-u`
  unknown attributes (taken from its output, not from this document).
- What bgpdump prints for a RIB record with a broken entry, or an UPDATE
  with a broken attribute. Where it differs from §5.2's per-entry rule,
  `divergences.md` records it.

## 11. Out of scope

- BMP and RIS Live (later sub-packages; RIS Live needs a websocket and goes
  in a socket-only sub-package, as rpsl isolates its network backends).
- Writing MRT (beyond the test-only `mrtgen`).
- Decoding OSPF, IS-IS and GEO_PEER_TABLE records (kept raw as `Other`).
- OPEN, NOTIFICATION and capability decoding (exposed raw with their type).
- RFC 7606 error-handling actions (treat-as-withdraw etc.) on diagnostics.
- Matching AS-path regexps against paths, ASPA verification, RPKI: the
  reconciliation library's job.
- Parallel decoding or decompression.

## 12. Risks

- **mdlayher/bgp exports its MRT reader.** It would then overlap, but it
  needs Go 1.27, has no bgpdump/bgpkit differential and no route view.
  Watch the repository; it does not change this design.
- **bgpdump is unmaintained** (last code change 2023-11). Where it is wrong,
  bgpkit decides and `divergences.md` records it, as bgpq4's divergences are
  recorded in rpsl.
- **Fixture rot:** collectors change what they write. The opt-in real-data
  run over fresh files is the guard.
