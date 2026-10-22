# mrt v0.4: a BMP reader

Status: implemented in v0.4.0 (branch release-0.4.0; unreleased until the user says so). Approved by the user 2026-10-22 ("Approved; take your recommendations
on all five, no capture yet"). Amends the v0.1 design
(`2026-10-16-mrt-design.md`) and the v0.2 design (`2026-10-20-mrt-v0.2-design.md`)
where it says so. CLAUDE.md's principles and rulings hold for everything
below. The user wrote the brief and asked for this spec before any code.

**User's rulings, 2026-10-22** (§10 has the options that were weighed):

1. Route type (§10.1): **option A.** `bmp` imports `mrt` and yields `mrt.Route`.
2. Route Monitoring before Peer Up (§10.2): **option 1.** Decode with no
   ADD-PATH, add a `bmp/addpath-unknown` Warning, and let `RequirePeerUp`
   refuse instead.
3. OpenBMP (§10.3): **sub-package `bmp/openbmp`**, binary v1.7 only, with no
   text form.
4. Statistics (§10.4): **raw TLVs with typed accessors.**
5. Version (§10.5): **v0.4.0**, with the CHANGELOG and README wording of §10.5.
6. **No capture yet.** Nothing is fetched from either broker, topic listing
   included. §9.5's script and test are written; neither runs until the
   user says so. The benchmark (§9.6) runs on the largest synthetic stream,
   and its gate is re-checked on a capture once one exists.

Every other decision not quoted from the brief, an RFC or an earlier design
is the author's call and is marked so.

## 1. Intent

bgp-platform (`~/DEV/ipintelligence/bgp-platform`) consumes RouteViews'
real-time feed as BMP (RFC 7854). The feed is wrapped in an OpenBMP envelope
and read from CAIDA's Kafka broker (ADR-0011,
`docs/adr/0011-bmp-ingest.md`: `bmp.bgpstream.caida.org:9092`). Today it
parses with gobgp v3.37.0's `pkg/packet/bmp`
(`internal/streamer/bmp/parser.go`). bgp-platform is moving its MRT path to
mrt, and this work removes its last gobgp dependency. Per the brief, the BMP
reader must decode what RouteViews and CAIDA actually send, and produce routes
with the same semantics as `mrt.Routes`. Downstream code should not care
whether a route came from an MRT file or a BMP stream.

What exists to build on:

- `bgp` already decodes UPDATEs (`ParseUpdate`), attributes, AS4 merging and
  diagnostics, and shares paths through `Interner`. The v0.1 design (§2)
  kept `bgp` free of MRT "so a later BMP or RIS Live reader can reuse it".
- `mrt` has the framing pattern this needs (bounded reads, `ErrTooLarge`,
  lossless `Raw`, errors as values), and the route model: `Route`, `Kind`,
  `Peer`, `State`, `RoutesOptions{PeerStates, Interner}`.
- `bgp` cannot read OPEN messages or capabilities (v0.1 design §11 put them
  out of scope). ADD-PATH in `bgp.Context` is one bool for every family.

## 2. Goals

1. **Read BMP v3** (the brief's scope): Route Monitoring, Statistics Report,
   Peer Down, Peer Up, Initiation, Termination and Route Mirroring, with the
   per-peer header, Information TLVs (VRF/Table Name included), statistics,
   and Peer Down reasons with their NOTIFICATION PDU or FSM event code.
   Read from a byte stream (`Messages`) and from one message in a buffer
   (`ParseMessage`, for Kafka).
2. **Routes with MRT semantics:** one `mrt.Route` per (router, peer, prefix,
   path ID) in each view. The route says whether it is pre- or post-policy,
   and whether it is from the Adj-RIB-In, the Adj-RIB-Out or the Loc-RIB.
   BMP multiplexes those views on one session (RFC 7854 §5), so a table
   keeper's key is (router, peer, view, prefix, path ID). Peer Down and
   End-of-RIB are reported. `bgp.Interner` works, and interning changes
   nothing.
3. **Decoding context from the protocol, never from the bytes:**
   - AS width comes from the A flag, or from the Loc-RIB peer type
     (principle 5).
   - ADD-PATH comes from the capabilities both sides sent in that peer's
     Peer Up, per address family and direction.
   - Unknown ADD-PATH state is a diagnostic and a documented rule (§6.6).
4. **The OPEN capabilities that decoding needs, in `bgp`:** multiprotocol (1),
   4-byte AS (65) and ADD-PATH (69), per the brief, plus ADD-PATH per family
   in `bgp.Context` (§6.2), without which a Peer Up's ADD-PATH cannot be
   applied (§5.3).
5. **The same guarantees as MRT:** bounded memory, no allocation sized by an
   unchecked count, `ErrTooLarge` for oversized messages, never a panic, and
   lossless `Raw`.

## 3. Non-goals

- **Kafka.** No Kafka client, in the root module or anywhere in the
  library. The caller pulls frames; bgp-platform's `FrameSource` already has
  that shape (ADR-0011 §2).
- **Running a BMP station** (listening on TCP, sending anything). No package
  opens a socket (v0.1 design §2).
- **Keeping the table.** As with MRT, `mrt` reports events and the table
  keeper decides what they mean (v0.2 design §3). A Peer Down yields one
  event, not a withdrawal per route (§7.4).
- **Decoding Route Mirroring's BGP messages into routes.** They are verbatim
  copies, not table state (RFC 7854 §6). They stay readable as messages.
- **BMP drafts v1 and v2, and the BMP TLV draft** (`draft-ietf-grow-bmp-tlv`).
  Only version 3 is read.
- **Families without prefixes** in routes, as for MRT (R9).
- **Writing BMP**, except the test-only generator (§9.1).
- **RFC 7606 actions** (treat-as-withdraw), as for MRT.

## 4. Anti-goals (a failure even if it works)

- **A guess.** An AS width read off the bytes, or an ADD-PATH setting
  inferred from whether the NLRI parse.
- **A silent change for existing MRT callers.** `mrt.Routes` and
  `mrt.RoutesWith` keep yielding exactly what they yield in v0.3.2. New
  route fields are zero on MRT routes, and new kinds are opt-in.
- **A broken consumer.** routecheck (pinned at mrt v0.3.2; it uses
  `mrt.Route`, `mrt.Peer` with keyed literals, `mrt.Kind*`, `mrt.State*`)
  builds and passes its tests against the branch unchanged. The branch is
  built against it in a scratch copy before merge (memory
  `no-breaking-consumers`).
- **Weakening an existing gate:** `TestLossless`, the interner invariant,
  the fuzz properties, the gobgp release gate, `TestPathSize`, no `net`,
  no `require`, and the Go 1.23 floor.
- **A route that depends on message order beyond what BMP defines.** The
  only state a route depends on is its peer's last Peer Up in the same
  session (§6.6).

## 5. Facts this design rests on (checked 2026-10-22)

### 5.1 BMP (RFC 7854, 8671, 9069, 9736, 9972; IANA registry updated 2026-05-22)

Texts fetched from rfc-editor.org into the session scratchpad. The registry
is `iana.org/assignments/bmp-parameters`.

- **Common header** (RFC 7854 §4.1): version (1 byte, "set to '3'"; 1 and 2
  were drafts), message length (4 bytes, "including headers, data, and
  encapsulated messages"), message type (1 byte). Types 0-6 as in §2;
  "A BMP implementation MUST ignore unrecognized message types upon
  receipt."
- **Per-peer header** (§4.2), 42 bytes: peer type (1), peer flags (1),
  peer distinguisher (8), peer address (16; IPv4 in the last 4 bytes, "the
  12 most significant bytes zero-filled"), peer AS (4; a 2-byte AS padded),
  peer BGP ID (4), timestamp seconds (4), microseconds (4). "If zero, the
  time is unavailable." It follows the common header in every message type
  except Initiation and Termination.
- **Peer types:** 0 Global Instance, 1 RD Instance, 2 Local Instance,
  3 Loc-RIB Instance (RFC 9069 §4.1). "The combination of the Peer Type and
  Peer Distinguisher is sufficient to disambiguate peers for which other
  identifying information might overlap" (RFC 7854 §4.2).
- **Peer flags for types 0-2:** V (bit 0, 0x80) IPv6 address; L (0x40)
  post-policy; A (0x20) "the message is formatted using the legacy 2-byte
  AS_PATH format", 0 for 4-byte; O (0x10, RFC 8671 §4) Adj-RIB-Out. Bit 4
  is the deprecated F flag (RFC 9069 §8.5). Reserved bits "MUST be ignored
  on receipt". L and A have "no significance" for Route Mirroring.
- **Peer flags for type 3** are a separate registry: only F (bit 0, 0x80),
  "the Loc-RIB is filtered" (RFC 9069 §4.2). V does not apply: the peer
  address is zero-filled (§5.1). Loc-RIB Route Monitoring "MUST use a 4-byte
  ASN encoding" (§5.4.1).
- **Peer Up** (§4.10): local address (16), local port (2), remote port (2),
  sent OPEN, received OPEN, then optional Information TLVs. Each OPEN is a
  whole BGP message, so its own header gives its length. RFC 9736 (March
  2025) gives Peer Up its own TLV registry: 0 String, 3 VRF/Table Name
  (1-255 bytes), 4 Admin Label (RFC 8671 §6.3.1); 1 and 2 reserved. For
  Loc-RIB the sent OPEN is fabricated and the received OPEN repeats it
  (RFC 9069 §5.2).
- **Peer Down** (§4.9; RFC 9069 §5.3): reason (1) and data.
  - 1: local close, a NOTIFICATION PDU follows.
  - 2: local close, a 2-byte FSM event code follows (0 means none).
  - 3: remote close, a NOTIFICATION PDU follows.
  - 4: remote close, no data.
  - 5: peer de-configured.
  - 6: Loc-RIB, TLVs follow (type 3 VRF/Table Name).

  "A Peer Down message implicitly withdraws all routes that were associated
  with the peer in question."
- **Statistics Report** (§4.8): a 4-byte count, then that many (type 2, len
  2, value) TLVs. Unknown types and "unexpected data" MUST be ignored. The
  registry has types 0-43:
  - 32-bit counters: 0-6, 11-13.
  - 64-bit gauges: 7, 8, 14, 15, and the RFC 9972 global types.
  - 2-byte AFI, 1-byte SAFI and a 64-bit gauge, length 11: 9, 10, 16, 17,
    and the RFC 9972 per-AFI/SAFI types (§4: "The 'Stat Len' MUST be set to
    11").
  - Types 24 and 25 are temporary registrations whose term ended on
    2026-10-18.
- **Initiation** (§4.3): TLVs only, no per-peer header; sysDescr (1) and
  sysName (2) MUST be sent, String (0) repeats. **Termination** (§4.5):
  TLVs, String (0) and Reason (1; 2-byte codes 0-4).
- **Route Mirroring** (§4.7): per-peer header, then TLVs: BGP Message (0;
  "MUST occur last"), Information (1; 2-byte codes 0 Errored PDU, 1
  Messages Lost). RFC 9069 §5.5: for Loc-RIB, "SHOULD be ignored".
- **Lifecycle** (§3.3): Initiation first, then a Peer Up for each
  established peer, then the table dump, then End-of-RIB per peer. A
  session is one TCP connection to one router. Only the Initiation names the
  router (its sysName TLV, which "MUST be sent", §4.3). No other message
  does, so a consumer that joins a stream after the Initiation (every Kafka
  consumer) learns the router only from outside BMP.
- **Withdrawals carry the L flag of the announcement** (§5): a route
  announced both pre- and post-policy is withdrawn twice.
- **Peer Up/Down and the O flag:** "BMP receiver implementations SHOULD
  ignore the O flag in Peer Up and Down Notifications" (RFC 8671 §6.3).
  Stats MUST have O = 0 (§6.2).

### 5.2 BGP OPEN and capabilities

- OPEN (RFC 4271 §4.2): version, My AS (2), hold time, BGP ID, optional
  parameters length (1), optional parameters (type 1, length 1, value).
  Capabilities are parameter type 2 (RFC 5492), each a (code 1, length 1,
  value) list.
- **Extended optional parameters** (RFC 9072 §2): a non-zero one-byte
  length followed by type 255 means a 2-byte total length follows, and every
  parameter's length is 2 bytes.
- Multiprotocol (1, RFC 4760): AFI 2, reserved 1, SAFI 1. 4-byte AS (65,
  RFC 6793): the 4-byte AS; My AS is then AS_TRANS when it does not fit.
- **ADD-PATH (69, RFC 7911 §4):** a list of (AFI 2, SAFI 1, Send/Receive 1)
  tuples. 1 means able to receive, 2 able to send, 3 both. "If any other
  value is received, then the capability SHOULD be treated as not
  understood and ignored." Multiple families "MUST" be in a single instance.
- **Negotiation (RFC 7911 §5):** a speaker sends path identifiers for a
  family only if it advertised 2 or 3 for it and received 1 or 3 from the
  peer.
- RFC 8654 (extended messages) lifts the 4,096-byte limit for every message
  except OPEN and KEEPALIVE. A Route Monitoring UPDATE can be 65,535 bytes.

### 5.3 What `bgp` does today (read in the code)

- `bgp.Context{AS4, AddPath, RIB}`. `ParseUpdate` reads the IPv4 withdrawn
  and NLRI fields with `ctx.AddPath`. `decodeMPReach` and `decodeMPUnreach`
  read MP NLRI with the same bool (`bgp/mp.go:90`, `:112`). A Peer Up can
  negotiate ADD-PATH for IPv6 unicast and not IPv4 unicast. One UPDATE can
  carry IPv4 NLRI in its own fields and IPv6 in MP_REACH. Such an UPDATE
  cannot be read correctly with one bool.
- `Interner` keys hold the context's flags (`bgp/interner.go:148-154`). Any
  new context field must enter the key.

### 5.4 OpenBMP and the feed

- **Two envelope formats exist.** OpenBMP's published Message Bus API
  (`openbmp.org/api/kafka_message_schema.html`, "Current Version 1.7", and
  `docs/MESSAGE_BUS_API.md` in `OpenBMP/obmp-collector`) defines only a
  *text* header for `bmp_raw`: `V: 1.1`, `C_HASH_ID`, `R_HASH_ID`, `R_IP`
  and `L` lines, then `\n\n`, then the BMP bytes. obmp-collector `main`
  writes that text form (`Server/src/kafka/MsgBusImpl_kafka.cpp:1623`).
- **The binary `OBMP` header** that CAIDA and RouteViews send has no
  published spec that this session found. Four independent implementations
  agree on its layout:
  - bgpkit-parser 0.22.0 `src/parser/bmp/openbmp.rs` (reads it);
  - CAIDA libbgpstream `lib/formats/bs_format_bmp.c` (reads it, and the
    text form, and raw BMP);
  - sbezverk/gobmp `pkg/message/raw.go` (writes it);
  - `scripts/rv-kafka-to-bmp.py` in `OpenBMP/obmp-collector`, by the
    OpenBMP author, which mirrors RouteViews' stream (reads it).

  The layout:

  | Field | Size |
  |---|---|
  | magic `OBMP` | 4 |
  | major, minor version (1, 7) | 1 + 1 |
  | header length | 2 |
  | BMP message length | 4 |
  | flags (0x80 router message, 0x40 router IP is IPv6) | 1 |
  | object type (12 = BMP raw) | 1 |
  | collector timestamp, seconds and microseconds | 4 + 4 |
  | collector hash | 16 |
  | collector admin ID length, admin ID | 2 + n |
  | router hash | 16 |
  | router IP (IPv4 in the first 4 bytes: gobmp writes it there, bgpkit and libbgpstream read it there) | 16 |
  | router group length, router group | 2 + m |
  | row count (always 1) | 4 |

  The BMP message follows. OpenBMP's doc: "Each Kafka message is a complete
  BMP message".
- **Topics.** Neither name below has been checked against a live broker:
  - CAIDA: ARTEMIS's documentation configures
    `^openbmp\.router--.+\.peer-as--.+\.bmp_raw`.
  - RouteViews also runs a broker, `stream.routeviews.org:9092`. bgpkit's
    example uses `routeviews.amsix.ams.61955.bmp_raw`; the obmp-collector
    script uses `bmp.rv.routeviews.<router>`.
- **RouteViews is changing its stack.** Its 2026 slides (NZNOG 2026, "rv
  nznog2026 260327") say RouteViews runs FRR 10.5.1 collectors, and that its
  "Bimper" processor "replaces OpenBMP and Bimper messages are compatible
  with OpenBMP's raw bmp". That compatibility is unverified.
- **Licence terms for the stream are unknown** (the brief). Nothing is
  captured or stored until the user rules (§9.5).

### 5.5 The oracles (read in their sources)

- **bgpkit-parser 0.22.0's CLI reads MRT only.** `--help` describes
  `<FILE>` as "File path to a MRT file", and `src/bin/main.rs` never
  mentions BMP. The *library* reads BMP and OpenBMP:
  - `parse_bmp_msg` and `parse_openbmp_header`;
  - `Elementor::bgp_to_elems` turns an UPDATE into elems.

  So the oracle is a small Rust harness against the pinned crate (§9.2).
  Known limits, read in the source:
  - It passes ADD-PATH off for every Route Monitoring UPDATE
    (`route_monitoring.rs`: `parse_bgp_message(data, false, asn_len)`), but
    then guesses from the bytes: `parse_nlri_list` (`src/parser/utils.rs`
    :283-313) reads a list as ADD-PATH when its first byte is 0, and retries
    without it if that fails. (Corrected 2026-10-22, during Task 9: the first
    draft said only "ADD-PATH off".) Once on, the guess holds for the rest
    of the list until an entry fails to parse. So bgpkit gets path IDs below
    2^24 right, misreads a path ID of 2^24 or more on a peer that negotiated
    ADD-PATH (its first byte is non-zero), and misreads a plain /0 only when
    the bytes after it happen to parse as an ADD-PATH entry.
  - With the A flag set it keeps only the low 16 bits of the peer AS
    (`headers.rs`).
  - Peer types other than 0-3 are an error.
  - It accepts OpenBMP 1.7 only, router messages only, object type 12 only,
    row count 1 only.
- **gobgp v4.10.0 `pkg/packet/bmp`** (`internal/compare` pins v4.10.0;
  bgp-platform uses v3.37.0):
  - `ParseBMPMessage(data)` parses with no options.
  - `ParseBMPMessageWithOptions(data, func(BMPPeerHeader)
    []*bgp.MarshallingOption)` is how a caller passes AS width and
    ADD-PATH.
  - It `recover`s panics into an error.
  - Unknown message types are an error.

## 6. Design: package `bgp` additions

### 6.1 OPEN and capabilities

```go
// Open is a parsed BGP OPEN message (RFC 4271 §4.2, RFC 9072).
type Open struct {
	Version      uint8
	MyAS         ASN // the 2-byte field; AS_TRANS when the speaker's AS needs 4 bytes
	HoldTime     uint16
	BGPID        netip.Addr
	Capabilities []Capability // every capability, in order, raw
}

type CapabilityCode uint8

const (
	CapMultiprotocol CapabilityCode = 1
	CapAS4           CapabilityCode = 65
	CapAddPath       CapabilityCode = 69
)

type Capability struct {
	Code  CapabilityCode
	Value []byte
}

func ParseOpen(msg []byte) (Open, []Diagnostic) // msg: whole message, marker included

func (o Open) AS() ASN                      // capability 65's AS, else MyAS
func (o Open) AS4() bool                    // capability 65 present and valid
func (o Open) Multiprotocol() []Family      // capability 1, in order
func (o Open) AddPath() []AddPathFamily     // capability 69's tuples, as sent

type AddPathFamily struct {
	Family Family
	Mode   uint8 // Send/Receive as on the wire
}

func (f AddPathFamily) Receive() bool // Mode 1 or 3
func (f AddPathFamily) Send() bool    // Mode 2 or 3

// AddPathFamilies is the set of families for which sender sends path
// identifiers to receiver (RFC 7911 §5).
func AddPathFamilies(sender, receiver Open) FamilySet
```

Rules:

- `ParseOpen` follows C2. A bad header or a type other than OPEN gives one
  Error (`DiagMessageHeader`, `bgp/not-open`). Fields that run past the
  message give an Error (`bgp/open-length`), and what parsed before them is
  kept. A capability whose value has the wrong length for its code gives a
  Warning (`bgp/capability-value`); its raw entry stays in `Capabilities`,
  and the accessors skip it.
- Optional parameters other than type 2 are skipped (parameter type 1,
  authentication, is deprecated by RFC 4271 Appendix A). RFC 9072's extended
  form is read.
- **ADD-PATH tuples:** `AddPath()` returns every tuple of a well-formed
  capability (a length that is a multiple of 4) with its Mode as sent.
  - **Negotiation applies RFC 7911 §4:** a tuple whose Mode is not 1-3 makes
    `AddPathFamilies` ignore that speaker's whole capability, with a Warning
    from `ParseOpen` (`DiagCapabilityValue`). The tuples stay readable,
    because Loc-RIB ignores the intent (RFC 9069 §5.2) and needs only the
    families (§7.5).
  - A second ADD-PATH capability is ignored with a Warning, so the first
    instance wins (author's call, like duplicate attributes; RFC 7911 says
    one instance "MUST" carry every family).
  - A family listed twice within the one instance: the first tuple wins,
    with a Warning (author's call).
- Allocation: `Capabilities` is sized by a counting pass over the bytes
  present, never by a length field.
- **Also added (author's call):** `ParseNotification(msg) (Notification,
  []Diagnostic)`, with `Notification{Code, Subcode uint8; Data []byte}`. The
  brief asks for Peer Down reasons "with their NOTIFICATION PDU"; code and
  subcode are what a reader of a Peer Down wants, and the parse is a few
  lines.

### 6.2 ADD-PATH per family in `Context`

```go
// FamilySet is a set of the families whose NLRI are prefixes
// (Family.HasPrefixes): AFI 1 and 2 with SAFI 1 and 2.
type FamilySet uint8

func (s FamilySet) Has(f Family) bool
func (s FamilySet) With(f Family) FamilySet // f without prefixes: s unchanged

type Context struct {
	AS4     bool
	AddPath bool      // every NLRI has a path identifier (MRT ADDPATH subtypes)
	AddPathFamilies FamilySet // NLRI of these families have one (BMP: from Peer Up)
	RIB     bool
}
```

- NLRI of family f are read with path identifiers when
  `ctx.AddPath || ctx.AddPathFamilies.Has(f)`. That covers the UPDATE's own
  withdrawn and NLRI fields (IPv4 unicast) and MP_REACH and MP_UNREACH
  (their family). Every existing caller sets only `AddPath` and sees no
  change.
- A bitmask, so `Context` stays comparable and small (author's call). Only
  prefix families matter: NLRI of other families are kept raw and never
  parsed (R9), so their ADD-PATH setting cannot change a decode.
- The interner's key holds `AddPathFamilies` (§5.3). Today only RIB blocks
  are keyed whole (`bgp/interner.go:56`, `:66`), and BMP blocks never are, so
  this is defensive: it keeps the key correct if a later caller sets
  families on a RIB context. The extended interner invariant exercises
  component sharing.
- **Alternative rejected:** keep one bool, and have the BMP reader refuse
  (or flag) any UPDATE whose families disagree. It is cheaper, but it leaves
  Peer Ups with mixed ADD-PATH unreadable, and the user's brief requires
  ADD-PATH "per AFI/SAFI".

### 6.3 Diagnostic codes added to `bgp`

`DiagNotOpen = "bgp/not-open"`, `DiagOpenLength = "bgp/open-length"`,
`DiagCapabilityValue = "bgp/capability-value"`,
`DiagNotNotification = "bgp/not-notification"`. Names are the builder's
within C3. The test that every `Diagnostic.Code` built under `bgp/` is a
constant (v0.2 §6.6) covers them.

## 7. Design: package `bmp`

`github.com/rkolesnichenko/mrt/bmp`. It imports `bgp` and (per Q-R, §10.1,
recommended option) `mrt`. Import direction `cmd → bmp → mrt → bgp`, and
`bgp` imports neither.

### 7.1 Messages

```go
type Type uint8 // TypeRouteMonitoring = 0 … TypeRouteMirroring = 6

type Header struct {
	Version uint8
	Length  uint32 // as on the wire: the whole message, common header included
	Type    Type
}

// Message is sealed: the types below are its only implementations.
type Message interface {
	Header() Header
	Raw() []byte // the whole message, exactly as read
	isMessage()
}

type PeerHeader struct {
	Type          mrt.PeerType
	Flags         uint8 // as on the wire
	Distinguisher uint64
	Addr          netip.Addr // types 0-2: IPv4 or IPv6 by the V flag; zero for Loc-RIB and unknown types
	AS            bgp.ASN
	BGPID         netip.Addr
	Time          time.Time // zero when the header's timestamp is 0 ("unavailable")
}

func (h PeerHeader) View() mrt.View // from the type and the L and O flags; zero for unknown types
func (h PeerHeader) AS4() (bool, bool) // (AS4, known): !A for types 0-2, true for Loc-RIB, unknown otherwise
func (h PeerHeader) Filtered() bool // Loc-RIB F flag
func (h PeerHeader) Peer() mrt.Peer

type RouteMonitoring struct {
	message
	Peer   PeerHeader
	Update []byte // the BGP message, marker included; a slice of Raw
}
func (m RouteMonitoring) MessageType() (bgp.MessageType, error)

type StatsReport struct {
	message
	Peer  PeerHeader
	Count uint32 // as stated
}
func (m StatsReport) Stats() iter.Seq2[Stat, error]

type Stat struct {
	Type  StatType
	Value []byte // as on the wire
}
func (s Stat) Counter() (uint32, bool)                   // a registered 32-bit counter with 4 bytes
func (s Stat) Gauge() (uint64, bool)                     // a registered 64-bit gauge with 8 bytes
func (s Stat) FamilyGauge() (bgp.Family, uint64, bool)   // a registered per-AFI/SAFI gauge with 11 bytes

type PeerDown struct {
	message
	Peer   PeerHeader
	Reason PeerDownReason // 0 (reserved) when the message ends after the per-peer header
	Data   []byte         // everything after the reason
}
func (m PeerDown) Notification() ([]byte, bool) // reasons 1 and 3: the NOTIFICATION PDU
func (m PeerDown) FSMEvent() (uint16, bool)     // reason 2 with its 2 bytes
func (m PeerDown) Info() iter.Seq2[TLV, error]  // reason 6

type PeerUp struct {
	message
	Peer PeerHeader
	Data []byte // everything after the per-peer header
}
func (m PeerUp) Body() (PeerUpBody, error) // wraps ErrMalformed when the OPENs do not frame

type PeerUpBody struct {
	LocalAddr              netip.Addr
	LocalPort, RemotePort  uint16
	SentOpen, ReceivedOpen []byte // whole OPEN messages, slices of Raw
	// info: the Information TLV bytes
}
func (b PeerUpBody) Info() iter.Seq2[TLV, error]
func (b PeerUpBody) TableName() (string, bool) // the first VRF/Table Name TLV

type Initiation struct{ message }
func (m Initiation) Info() iter.Seq2[TLV, error]

type Termination struct{ message }
func (m Termination) Info() iter.Seq2[TLV, error]
func (m Termination) Reason() (TerminationReason, bool)

type RouteMirroring struct {
	message
	Peer PeerHeader
}
func (m RouteMirroring) TLVs() iter.Seq2[TLV, error]

// Other is every message whose type BMP v3 does not define (7-255). Kept raw,
// never an error (RFC 7854 §4.1: "MUST ignore").
type Other struct{ message }

type TLV struct {
	Type  uint16
	Value []byte // a slice of Raw
}
```

The constant sets for `StatType`, `PeerDownReason`, `TerminationReason`, the
TLV types and the Route Mirroring information codes are the builder's, within
C3, from the registry in §5.1.

**Decoding at read time, as for MRT** (v0.1 §5.2, "framing never depends on
the body"):

- Only fixed fields are decoded when a message is read: the common header,
  the per-peer header, Peer Down's reason byte and the stats count.
- **A message whose fixed fields do not fit is malformed.** Each yields a
  `*MessageError` wrapping `ErrMalformed`, and reading goes on:
  - a message too short for its per-peer header;
  - a Statistics Report without its count;
  - a version other than 3 (author's call: reading continues by the length
    field, and the message is an error, not an `Other`).
- **A Peer Up or Peer Down whose per-peer header reads is never malformed**
  (author's call). Its peer is known, so its effect on the session state
  (§7.5) must not be lost to damage further in. A broken OPEN, a missing
  reason, or a reason 2 without its 2 bytes is found by `Body`, `FSMEvent`
  and the other accessors, and §7.5 says what the session does with it. A
  Peer Down or Peer Up too short for its per-peer header names no peer, so
  the error changes no state.
- TLV lists, statistics and BGP PDUs are walked on demand
  (`Info`, `Stats`, `TLVs`, `bgp.ParseUpdate`, `bgp.ParseOpen`), like
  `Attrs.All` and `RIB.Entries`: a TLV that runs past its bytes ends the walk
  with one error, and the TLVs before it are already yielded. A stats walk
  whose TLV count differs from `Count` ends with an error after the last
  whole TLV (author's call: `Count` is checked, never used to size anything).
- Peer Up's Information TLVs are optional. Their damage does not fail
  `Body`, so the OPENs still reach §7.5.
- **Strings stay bytes** in `TLV.Value`. `TableName` converts without
  validating UTF-8 (author's call; the RFCs say UTF-8, and a bad byte is
  better kept than dropped).
- **Peer address for Loc-RIB:** the zero `netip.Addr` (RFC 9069 §5.1: "not
  applicable"), whatever the bytes. The bytes stay in `Raw`.
- **Peer types 4-255** are read (the per-peer header's layout does not
  depend on the type). `View()` is zero and `AS4()` is unknown, so their
  Route Monitoring yields an error in `Routes` (§7.4), never a guess.

### 7.2 Reading

```go
const DefaultMaxMessageLen = 1 << 20

type Options struct {
	MaxMessageLen uint32 // 0 means DefaultMaxMessageLen
}

func Messages(r io.Reader) iter.Seq2[Message, error]
func MessagesWith(r io.Reader, opts Options) iter.Seq2[Message, error]

// ParseMessage reads the one BMP message b holds (a Kafka frame's payload).
func ParseMessage(b []byte) (Message, error)

type MessageError struct {
	Offset int64  // of the message's first byte in the stream; -1 when not known
	Header Header // zero when the header was not read completely
	Raw    []byte // nil when over MaxMessageLen and skipped, or truncated
	Err    error
}

var (
	ErrTruncated = errors.New("bmp: truncated message")
	ErrMalformed = errors.New("bmp: malformed message")
	ErrTooLarge  = errors.New("bmp: message longer than MaxMessageLen")
	ErrNoPeerUp  = errors.New("bmp: route monitoring before its peer's peer up")
)
```

- **Framing:** read the 6-byte common header, then `Length - 6` bytes.
  - A length below 6 cannot hold its own header. The 6 bytes are one
    malformed message, and reading continues after them (author's call). The
    stream goes on, as the brief requires, and lossless `Raw` holds.
  - A length above `MaxMessageLen` is discarded without buffering
    (`ErrTooLarge`), and reading continues.
  - A body over 64 KiB is read through a growing buffer (R10's rule, the
    same code path as `mrt.readRecord`, shared or copied by the builder's
    choice).
  - A stream that ends mid-message, or a read error, yields one error
    (`ErrTruncated` or the reader's own) and ends.
- **`DefaultMaxMessageLen` is 1 MiB** (author's call). The largest legitimate
  message is a Route Monitoring of 6 + 42 + 65,535 bytes (RFC 8654), or a
  Peer Up of two 4,096-byte OPENs and TLVs. The MRT default is 16 MiB because
  an MRT RIB record holds every entry for a prefix. No BMP message does.
- **`ParseMessage`** requires `len(b) == Length`: shorter is `ErrTruncated`
  and longer is `ErrMalformed`. A Kafka frame holds one message, and trailing
  bytes mean the envelope or the producer is wrong. The message *aliases*
  `b` (author's call: Kafka clients hand over a fresh buffer per message,
  and the caller copies if it reuses one); `Messages` gives each message its
  own buffer, as `Records` does. `MaxMessageLen` does not apply: the bytes
  are already in memory.
- **Lossless:** concatenating, in yield order, `Raw()` of every message and
  `MessageError.Raw` of every `ErrMalformed` reproduces the input byte for
  byte, unless an `ErrTooLarge` or `ErrTruncated` was yielded. `TestLossless`
  gets a BMP sibling, and the fuzz target checks the property.
- **Allocation:** the message buffer, and a constant number of small values
  per message. Nothing per TLV until walked; TLV walks allocate nothing, as
  they yield values.

### 7.3 Where the route type lives

The brief asks for the options and their API cost; the user decides. The
recommendation and the full comparison are §10.1. The design below is written
for the recommended option (A): `bmp` imports `mrt` and yields `mrt.Route`,
which gains these fields and constants. Every one of them is zero on an MRT
route:

```go
type Route struct {
	// … every v0.3.2 field, unchanged …
	Router netip.Addr // the monitored router (BMP); zero for MRT
	View   View       // which RIB the route is in (BMP); zero for MRT
}

type Peer struct {
	BGPID netip.Addr
	Addr  netip.Addr
	AS    bgp.ASN
	Type          PeerType // BMP peer type; zero (global instance) for MRT
	Distinguisher uint64   // BMP peer distinguisher; zero for MRT
}

type PeerType uint8 // PeerGlobal = 0, PeerRD = 1, PeerLocal = 2, PeerLocRIB = 3

type View uint8

const (
	ViewAdjRIBInPre   View = 1 // O = 0, L = 0
	ViewAdjRIBInPost  View = 2 // O = 0, L = 1
	ViewAdjRIBOutPre  View = 3 // O = 1, L = 0
	ViewAdjRIBOutPost View = 4 // O = 1, L = 1
	ViewLocRIB        View = 5 // peer type 3
)

const KindEndOfRIB Kind = 5 // Kind.String() "E"
```

- **Zero View means "not stated"**, which is what an MRT file says (author's
  call). An MRT BGP4MP message is what the collector received, which is
  close to Adj-RIB-In pre-policy, but the file does not say so, and setting
  it would change every MRT route (anti-goal 2).
- **Zero `Peer.Type` is the global instance**, which is what every MRT peer
  is. So the new `Peer` fields are true for MRT routes as well as zero.
- **The Loc-RIB F flag is not on the route** (author's call). It is constant
  for a Loc-RIB instance between its Peer Up and Peer Down, it is on every
  message (`PeerHeader.Filtered`), and the brief lists only pre/post,
  in/out and Loc-RIB.
- **Table names are not on the route.** Route Monitoring does not carry
  them; only Peer Up and Peer Down do. A consumer that needs them maps
  (router, peer) to the name from `PeerUpBody.TableName`.
- **The same peer does not compare equal across MRT and BMP.** An MRT
  BGP4MP route's `Peer` has no BGP ID (`routes.go`: `Peer{Addr, AS}`). A BMP
  route's has one, and so does an MRT RIB route's. The fields mean the same
  thing in both, which is what the brief asks for. A consumer that joins a
  RouteViews updates file with the same collector's BMP feed matches on
  (Addr, AS), as it already must between BGP4MP and RIB routes.

### 7.4 Routes

```go
type RoutesOptions struct {
	Router        netip.Addr // copied into every Route.Router
	PeerStates    bool       // yield KindPeerState for Peer Up and Peer Down
	EndOfRIB      bool       // yield KindEndOfRIB
	RequirePeerUp bool       // §7.6: refuse, instead of flagging, a peer with no Peer Up
	Interner      *bgp.Interner
}

// Session is the state Routes keeps for one BMP session (one monitored
// router): each peer's ADD-PATH families from its last Peer Up.
type Session struct{ /* … */ }

func NewSession(opts RoutesOptions) *Session

// Routes takes what ParseMessage returns, so that s.Routes(bmp.ParseMessage(b))
// is the whole Kafka step; a non-nil err is yielded unchanged.
func (s *Session) Routes(m Message, err error) iter.Seq2[mrt.Route, error]

func Routes(msgs iter.Seq2[Message, error]) iter.Seq2[mrt.Route, error]
func RoutesWith(msgs iter.Seq2[Message, error], opts RoutesOptions) iter.Seq2[mrt.Route, error]
```

- **Two entry points, because the inputs differ.**
  - A TCP capture or a file is one session from one router.
    `RoutesWith(Messages(r), opts)` covers it, with `opts.Router` naming the
    router.
  - A Kafka topic interleaves routers, and one router's peers may be spread
    over several topics (CAIDA's pattern names a router and a peer AS,
    §5.4). The router is known only from the OpenBMP envelope (§8). The
    caller keeps one `*Session` per router (keyed by the envelope's router
    hash) and feeds each message to its router's `Session.Routes`. The
    session state is per peer (§7.5). So the only order a route depends on
    is that of its own peer's messages, which a per-peer topic keeps.
    Messages of different topics can interleave in any order.

  `RoutesWith` is `NewSession(opts)` and a loop. A `Session` is not safe for
  concurrent use (author's call; one per router means no sharing is needed).
- **Route Monitoring → routes.**
  - The UPDATE is decoded with `bgp.ParseUpdate(m.Update, ctx)` and the
    attributes through the interner when one is set. The UPDATE handling is
    `mrt`'s `messageRoutes`: withdrawals before announcements, IPv4 fields
    then MP, prefix families only. Diagnostics are rebased onto the BGP
    message.
  - The context:
    - `ctx.AS4` is from `PeerHeader.AS4()`.
    - `ctx.AddPathFamilies` is from the session state (§7.5).
    - `ctx.RIB` is false.
  - The route:
    - `Time` is the per-peer timestamp.
    - `Peer` is `PeerHeader.Peer()`.
    - `View` is `PeerHeader.View()`.
    - `Router` is from `opts.Router`.
    - Kind, Family, Prefix, PathID, Path and Diagnostics are as for an MRT
      UPDATE.
- **Errors, like R12/R13/R22.** Each of these is a
  `*MessageError{Offset: -1}` wrapping `ErrMalformed`, and the next message
  goes on:
  - a Route Monitoring whose BGP header is broken;
  - one whose message is not an UPDATE (RFC 7854 §4.6 says it carries an
    UPDATE; author's call that this is an error, where in MRT non-UPDATEs are
    skipped);
  - one whose peer type is unknown, or whose AS width is unknown (§7.1);
  - an UPDATE that yields no route but has a Warning or Error diagnostic.
- **End-of-RIB** (RFC 4724 §2) is an UPDATE with no withdrawn routes, no
  attributes and no NLRI (IPv4 unicast), or one whose only attribute is
  MP_UNREACH_NLRI with no withdrawn routes (that family). With
  `opts.EndOfRIB` it yields one `KindEndOfRIB` route. Only Time, Router,
  Peer, View and Family are set, and Family may be one without prefixes.
  Without the option it yields nothing, as MRT's End-of-RIB does (R13).
- **Peer Up and Peer Down → state events.** With `opts.PeerStates`:
  - A Peer Up yields one `KindPeerState` route with `NewState` Established
    and `OldState` zero (BMP does not say what came before). Its
    `Diagnostics` are the two OPENs' (sent first, offsets from each OPEN),
    and a `bmp/peer-up-body` Error when `Body` fails. A consumer can then
    see why the peer's ADD-PATH state is unknown.
  - A Peer Down yields `OldState` Established and `NewState` Idle, whatever
    its body holds.
  - Time, Router and Peer are set. View is zero, because the event covers
    every view of the peer (RFC 8671 §6.3), except for a Loc-RIB peer, where
    it is `ViewLocRIB`.

  As in MRT, the event is reported and the table keeper drops the peer's
  routes. No withdrawals are generated (the brief: "the way PeerStates does
  for MRT"; v0.2 §6.2). The Peer Down reason stays on the message.
- **Everything else yields nothing:** Statistics, Initiation, Route
  Mirroring, `Other` and Termination. Errors from `msgs` pass through
  unchanged.
- **Interning.** `opts.Interner` decodes UPDATE attributes exactly as
  `mrt.RoutesWith` does (UPDATE blocks are never interned whole, v0.2 §6.4;
  components are shared). The invariant holds: with and without an interner,
  routes and diagnostics are deeply equal (`TestBMPRoutesInternerSameRoutes`,
  `FuzzBMPRoutes`).
- **Ranging again after a break** goes on with the next message, as
  `mrt.Routes` does. The session state persists in the `Session`.

### 7.5 Session state: ADD-PATH from Peer Up

- **Key:** for peer types 0-2, (peer type, distinguisher, peer address).
  For Loc-RIB, (distinguisher, BGP ID) (RFC 9069 §6.1.1: "The BMP receiver
  identifies the Loc-RIB by the peer header distinguisher and BGP ID"). The
  L and O flags are not in the key: the Peer Up covers every view of the
  peer. AS and BGP ID are not in the key for types 0-2 (author's call: the
  RFC names type, distinguisher and address as the disambiguator).
- **On Peer Up**, `Body` frames the OPENs and `bgp.ParseOpen` parses both.
  The peer's state becomes *unknown* (§7.6) when `Body` fails, or when either
  OPEN has a Warning or Error diagnostic. That is author's call, and
  conservative: a damaged ADD-PATH capability must not read as "no
  ADD-PATH". The one exception is for Loc-RIB: the Warning for an ADD-PATH
  Mode outside 1-3 does not count, because Loc-RIB ignores the Mode (the
  Loc-RIB bullet below; ruled during implementation, 2026-10-22). Unknown
  replaces whatever the key held. Otherwise:
  - Adj-RIB-In views (O = 0): `AddPathFamilies(received, sent)`. The peer
    sends and the router receives.
  - Adj-RIB-Out views (O = 1): `AddPathFamilies(sent, received)`.
  - A Peer Up for a key of types 0-2 that is already held replaces it.
  - **Loc-RIB:** RFC 9069 §5.2 says "the capability intent of advertise,
    receive, or both can be ignored", so every family in the sent OPEN's
    ADD-PATH tuples counts, whatever its Mode (`Open.AddPath()`, §6.1).
    - One Loc-RIB may have several emulated peers, one per group of
      families (RFC 9069 §6.1.1). Their Peer Ups share a key. Each Peer Up
      therefore sets the state of the families in its Multiprotocol
      capability, or of IPv4 unicast when it has none (RFC 4760 §8), on or
      off, and leaves the other families alone. A family the Session has
      never seen in a Loc-RIB Peer Up is unknown.
    - **Limits** (author's calls):
      - A Loc-RIB Peer Down cannot tell its emulated peer from the others
        (Route Monitoring and Peer Down share the key), so it removes the
        whole key.
      - Filtered views of one Loc-RIB that differ only by table name
        (RFC 9069 §6.1.2) share a key and a `Route.Peer`.

      Whether RouteViews sends Loc-RIB at all is unknown (§11).
- **The same families apply to pre- and post-policy.** RFC 7854 says nothing
  more specific, and the A flag's wording ("the message is formatted
  using …") treats a peer's messages as one format. It is an open fact to
  check against FRR's output (§11).
- **On Peer Down** the key is removed, whatever the body holds.
- **Termination and Initiation change nothing** (author's call).
  - Clearing on Termination would make every peer's routes depend on where
    a peer-less message falls among other peers' messages, which differs
    by topic on Kafka (anti-goal 5).
  - Clearing is also not needed for correctness. A new session sends a Peer
    Up for each peer before that peer's routes (RFC 7854 §3.3), and the
    Peer Up replaces the state.
  - RFC 7854 §4.3 allows an Initiation at any time.
- **Memory:** one small entry per peer with a live Peer Up, removed on Peer
  Down. There is no cap (author's call). A stream that adds peers without
  removing them grows by one entry per Peer Up message, and each such
  message is at least 48 bytes of input.

### 7.6 A Route Monitoring before its peer's Peer Up (open question 2)

This is the common case for a Kafka consumer. A consumer sees a peer's Peer
Up only if its first read starts before that Peer Up. A cold start at the
latest offset misses it, and so does a restart after the Peer Up has aged
out of the broker's retention. bgp-platform resumes at its consumer group's
offset (ADR-0011 §5), and has no offset policy for a first start
(`internal/streamer/bmp/config.go`). Whether Peer Ups are re-sent later is
unknown (§11). The recommendation goes to the user (§10.2). The design text below
assumes the recommendation:

- **Default:** the UPDATE is decoded with no ADD-PATH, the RFC 4271 encoding
  a session uses unless both sides negotiate otherwise. Every route from it
  carries a Warning diagnostic, `bmp/addpath-unknown`, at offset 0, saying
  that the peer's capabilities were not seen. The AS width does not depend
  on Peer Up (it is the A flag), so only ADD-PATH is assumed.
- **`RequirePeerUp`:** such a message yields one
  `*MessageError{Offset: -1, Err: ErrNoPeerUp}` and no routes.
- **Priming:** a consumer that kept the Peer Ups it saw earlier (or reads
  the topic from an earlier offset) passes them to `Session.Routes` first.
  No extra API.
- The diagnostic is not added to End-of-RIB (it has no NLRI to misread).
  Under R22 it does not turn a route-less UPDATE into an error by itself:
  the Warning threshold counts only `bgp`'s own diagnostics (author's call,
  so that an EoR or an empty UPDATE before Peer Up stays silent).
- The diagnostic constants live in `bmp` (`DiagAddPathUnknown`,
  `DiagPeerUpBody`). Their values start with `bmp/`, so they cannot collide
  with `bgp`'s.

## 8. Package `bmp/openbmp` (if the user agrees, §10.3)

```go
type Header struct {
	Major, Minor     uint8
	Flags            uint8
	ObjectType       uint8
	Time             time.Time // the collector's timestamp
	CollectorHash    [16]byte
	CollectorAdminID string
	RouterHash       [16]byte
	RouterIP         netip.Addr
	RouterGroup      string
	RowCount         uint32
	Length           int // header length, as stated and checked
	MessageLen       int // BMP message length, as stated and checked
}

var (
	ErrNotOpenBMP = errors.New("openbmp: no OBMP header")
	ErrVersion    = errors.New("openbmp: unsupported header version")
	ErrMalformed  = errors.New("openbmp: malformed header")
)

// Parse reads one Kafka frame: the header and the BMP message after it.
func Parse(frame []byte) (Header, []byte, error)

// Frames reads concatenated frames from r (a capture file); each frame is
// self-delimiting by its two length fields.
func Frames(r io.Reader) iter.Seq2[[]byte, error]
```

- **Parse field by field and check the stated header length against the
  parsed one** (author's call):
  - A mismatch is `ErrMalformed`. Skipping by the header length alone, as
    bgp-platform does, cannot catch a header that lies.
  - The message length must equal the bytes left. Shorter is `ErrMalformed`
    wrapping truncation; longer is `ErrMalformed`.
- **Version 1.7 only** (`ErrVersion` otherwise); flags 0x80 (router message)
  and object type 12 (BMP raw) required; row count must be 1. Those are
  bgpkit's and libbgpstream's checks.
- **Router IP:** IPv6 when flag 0x40 is set, otherwise the first 4 bytes.
- **The text form** (`V: …\n\n`) is out of scope unless the user wants it
  (§10.3).
- The returned BMP bytes alias `frame`; pass them to `bmp.ParseMessage`.
- `openbmp` imports nothing from `bmp` (author's call): a consumer composes
  `Parse` and `bmp.ParseMessage`. A Route's router is
  `Header.RouterIP`, given through the router's `Session`.

## 9. Tests

### 9.1 Synthetic fixtures

`internal/bmpgen` is a new test-only package beside `internal/mrtgen`, and
reuses mrtgen's UPDATE and attribute builders (`mrtgen.Update`, `MPReach`,
…). It writes BMP messages and OpenBMP frames. Author's call: a sibling,
because mrtgen's `Header` is MRT-specific and its `Synthetic()` list drives
the MRT golden suite. Fixtures go to `testdata/bmp/`, each one a raw BMP
stream (`*.bmp`) or an OpenBMP frame stream (`*.obmp`), deterministic, and
regenerated and checked by a test as in mrtgen:

- every message type, with Initiation, Termination and Peer Down for every
  reason (with a NOTIFICATION, an FSM code, TLVs);
- every peer type, and every combination of V, L, A and O for types 0-2,
  plus F for Loc-RIB;
- ADD-PATH on and off, mixed per family (IPv4 on, IPv6 off and the
  reverse), Adj-RIB-In and Adj-RIB-Out directions, and Loc-RIB intent
  ignored;
- 2- and 4-byte AS (AS_PATH and AS4_PATH merge under A = 1), and IPv6 peers;
- End-of-RIB for IPv4 and MP families;
- a Route Monitoring before any Peer Up, a Peer Up with bad OPENs, a Peer
  Down then a re-Up with different capabilities, and Termination then a new
  session;
- statistics of every registered shape, unknown types, and wrong lengths;
- RFC 9072 extended optional parameters;
- truncation at every field boundary, length < 6, a version other than 3,
  oversize, garbage between messages, and unknown message and peer types.

### 9.2 Oracles

- **bgpkit-parser 0.22.0, field by field for routes.** A Rust harness,
  `internal/oracle/bgpkit-bmp/`, holds a Cargo project pinned to
  `bgpkit-parser = "=0.22.0"` with a committed `Cargo.lock`.
  `scripts/build-oracles.sh` builds it into `$HOME/.local/bin/bgpkit-bmp`
  next to the other two. It reads a `.bmp` or `.obmp` file and prints, per
  message, the elems of each Route Monitoring (from `Elementor::bgp_to_elems`
  with the per-peer timestamp, address and AS), one JSON line each, in
  bgpkit-parser's `--json` elem form. Peer Up, Peer Down and the other
  messages print nothing (amended during Task 9). Their parsing is not
  compared with an oracle; the unit tests and fuzz targets cover it, and
  the ADD-PATH it feeds is compared through the routes.

  Goldens are `testdata/golden/<fixture>.bgpkit-bmp.json`, recorded from the
  live harness (`MRT_GOLDEN_UPDATE=1`), never edited by hand.
  - `TestBMPMatchesBgpkit` needs no oracle installed.
  - `TestBMPMatchesBgpkitLive` skips without it, unless
    `MRT_ORACLES_REQUIRED=1`.
  - The CLI cannot be the oracle (§5.5).
- **gobgp v4.10.0 in `internal/compare` only**, the second oracle and the
  allocation baseline. It parses through `ParseBMPMessageWithOptions`, so
  the A flag and the Peer Up's ADD-PATH reach it, and its routes are
  compared with `bmp.Routes` on every fixture and on the real capture.
  `go vet` and `go test` there stay separate from the root's.
- **Divergences:** `testdata/bmp/divergences.md` is two-directional, like
  `testdata/bgpdump/divergences.md`, and is held by a sibling of
  `TestDivergencesDocumented`. It is keyed by fixture and exact lines, one
  section per oracle. Known in advance (§5.5): bgpkit has no ADD-PATH in BMP,
  keeps a 16-bit peer AS under the A flag, and rejects unknown peer types.
  Where bgpkit and gobgp disagree, or both differ from mrt, the RFC decides
  (CLAUDE.md's rule).

### 9.3 Unit and property tests

- `ParseOpen`: one test per rule of §6.1, including RFC 9072 and each
  ADD-PATH rule. `AddPathFamilies`: the 3 × 3 Send/Receive table per
  direction.
- `Context.AddPathFamilies`: a mixed-family UPDATE decodes correctly; the
  existing `AddPath` callers and every MRT golden are unchanged.
- The session state of §7.5, rule by rule, with Peer Ups out of order:
  - a damaged Peer Up makes the peer unknown;
  - a damaged Peer Down still removes the peer and yields its event;
  - Loc-RIB Peer Ups per family;
  - two peers' messages interleaved in every order give the same routes.
- `TestBMPLossless` over every `.bmp` fixture, and the same for `.obmp`
  frames through `openbmp.Frames`.
- The interner invariant over every BMP fixture.
- A test that MRT routes are unchanged: `TestMatchesBgpkit` and the goldens
  pass with no re-recording.

### 9.4 Fuzz

Added to `scripts/fuzz.sh`, which then runs eleven targets (seven today, ten
without `bmp/openbmp`), sharded as now:
- `FuzzMessages`: no panic, lossless, bounded allocation as in
  `TestReadRecordBound`.
- `FuzzBMPRoutes`: with and without an interner, deeply equal; every
  option combination.
- `FuzzParseOpen`: in `bgp`; plus the `AddPathFamilies` symmetry property.
- `FuzzOpenBMP`: `Parse` never panics, and a frame that parses re-frames
  to the same bytes.

### 9.5 Real data (opt-in, never committed)

- **A capture needs the user's go-ahead first** (the brief: licence terms
  unknown). Nothing is fetched in this work until then.
- **The capture tool** (author's call, for the user to confirm) is `kcat`,
  run by a script (`scripts/fetch-bmp.sh`) that writes N frames of a topic
  into `.data/realdata/bmp/` and pins topic, broker, offsets, size and
  sha256 in the manifest. OpenBMP frames are self-delimiting (§8), so the
  file is a frame stream. That keeps any Kafka client out of every Go module.
  The alternative, a nested Go module with a Kafka client, adds a dependency
  that CLAUDE.md allows only for gobgp.
- **`TestRealData/bmp-*`** decodes the capture with `bmp`, the bgpkit
  harness and gobgp, and fails on any mismatch that `divergences.md` does not
  name. It runs alone under memguard, like the MRT real-data runs.

### 9.6 Benchmark

- `internal/compare` gets a BMP benchmark: `bmp.Routes` against gobgp's
  `ParseBMPMessageWithOptions` and its UPDATE walk, over the same capture
  (or, before one exists, the largest synthetic stream).
- It reports bytes and allocations per message from `testing.B`.
- The run is logged under `.data/` and the log is named wherever a figure
  is quoted. No figure is estimated.
- **Gate (author's call, matching the MRT gate):** `bmp.Routes` allocates
  less than gobgp per message on the capture.

### 9.7 Gates before "done"

- `memguard.sh 8 3600 env GOMAXPROCS=4 FUZZTIME=15s scripts/check.sh` ends
  `SUCCESS` and `check: ok`.
- `api/bgp.txt`, `api/mrt.txt` and the new `api/bmp.txt` (and
  `api/openbmp.txt`) regenerated with `MRT_API_UPDATE=1`. Their diff is the
  API change list in the CHANGELOG.
- `go list -deps ./... | grep -x net` prints nothing, and the root `go.mod`
  has no `require`.
- routecheck built and tested against the branch in a scratch copy (§4).
- An independent reviewer reads the whole diff, and its findings are
  resolved.

## 10. Questions for the user

### 10.1 Q-R: where the shared Route type lives (the brief: "I decide")

| Option | What changes | API cost | routecheck |
|---|---|---|---|
| **A. `bmp` imports `mrt`, yields `mrt.Route`** (recommended) | `mrt.Route` gains `Router`, `View`; `mrt.Peer` gains `Type`, `Distinguisher`; new `mrt.View`, `mrt.PeerType`, `KindEndOfRIB` | `api/mrt.txt`: about 15 added lines, none changed or removed. `bmp` depends on `mrt` (same module, so no new dependency) | Builds unchanged: keyed `Peer` literals, `Peer` stays comparable |
| B. New package `route` (`mrt/route`) with Route, Kind, Peer, State, View, PeerType; `mrt` keeps aliases (`type Route = route.Route`, constants re-declared) | Route model moves; `mrt` and `bmp` both import `route` | A third public package. Every `mrt.Route`/`Peer`/`Kind`/`State` line in `api/mrt.txt` becomes an alias line (apisurface already handles aliases), and godoc for `mrt.Route` points elsewhere | Builds unchanged (aliases are the same types) |
| C. As B, but the model moves into `bgp` | `bgp.Route`, `bgp.Peer`, … | As B, and `bgp` takes on collector concepts (RIB views, router, originated time) it has stayed free of | Builds unchanged |
| D. A separate `bmp.Route` that shares only `*bgp.Path` | Nothing in `mrt` | Smallest diff in `mrt`, but downstream handles two route types, which the brief rules out ("should not care") | Untouched |

Recommendation: **A**. It is the smallest API change that gives one type.
It has no aliases and no relocation, the new fields are truthful zeros on
MRT routes (§7.3), and `bmp → mrt → bgp` keeps `bgp` clean. Its cost is that
`mrt` hosts two BMP-flavoured types (`View`, `PeerType`). B is the choice if
the user wants `mrt` to stay strictly about MRT files.

### 10.2 Q2: Route Monitoring before its peer's Peer Up

1. **Decode with no ADD-PATH and flag every route** with
   `bmp/addpath-unknown` (a Warning). `RequirePeerUp` refuses instead
   (recommended, §7.6). A Kafka consumer gets routes from its first message,
   and the assumption is stated on each one. The risk is that the peer did
   negotiate ADD-PATH. The prefixes are then misread, and the Warning is the
   only signal (`bgp`'s NLRI diagnostics usually fire too, but not always).
2. **Refuse by default** (`ErrNoPeerUp` per message), with an option to
   assume no ADD-PATH. This is the strictest reading of "never a silent
   guess". In practice bgp-platform gets nothing until each peer re-ups,
   which may be never for a long session.
3. **Hold the messages until a Peer Up arrives.** It is unbounded, and is
   ruled out by the bounded-memory principle.

Which ADD-PATH settings RouteViews' peers actually negotiate is unknown until
a capture exists (§11). If none negotiate it, option 1's risk is
theoretical for this feed.

### 10.3 Q1: the OpenBMP envelope

1. **A small sub-package `bmp/openbmp`** (recommended, §8). bgp-platform's
   envelope code shrinks to one call. The field-by-field check catches a
   header that lies. It is fuzzed and tested like the rest, and it is where
   the router identity for multi-router streams comes from. Its cost is a
   fourth public package that tracks a format with no published spec (§5.4).
2. **Stay in bgp-platform.** mrt is then format-pure, but every consumer of
   CAIDA's stream rewrites the same 40 lines, and bgp-platform's current
   code skips by the header length unchecked.
3. **Inside `bmp`.** The same code, without a separate package name. It
   mixes a Kafka framing convention into the protocol package.

Sub-question: should the **text form** (`V: 1.x … \n\n`, the only one
OpenBMP documents) be read too? Recommended: no, until a capture shows it on
either broker. libbgpstream reads it, so it may exist in the wild.

### 10.4 Q3: statistics

Recommended: **raw TLVs with typed accessors** (`Counter`, `Gauge`,
`FamilyGauge`, §7.1), the `ExtCommunity` pattern of v0.2 §6.5:

- The registry grew from 14 types (RFC 7854) to 44 (RFC 9972, 2026), so a
  struct with one field per counter would rot.
- An accessor answers `ok` only for a registered type with its registered
  length. Anything else stays bytes, never guessed from its length (RFC 7854:
  "MUST ignore unexpected data").
- Types 24 and 25 (temporary, expired 2026-10-18) are treated as
  unregistered.

The alternative, a `Stats` struct with named fields, is friendlier for the
14 classic counters and wrong for the rest.

### 10.5 Q4: version and wording

Recommended: **v0.4.0**. It adds packages and API, changes no existing
signature, and adds a field to `bgp.Context`. That is additive for keyed
literals, but it changes `Context`'s size and its comparison, so it is a
minor, not a patch.

- **CHANGELOG:** an `Added` section (`bmp`, `bmp/openbmp`, `bgp.ParseOpen`,
  `ParseNotification`, `FamilySet`, `Context.AddPathFamilies`, the new
  `mrt.Route` fields and `KindEndOfRIB`), a `Changed` section (none for
  MRT callers, stated as such), and the measured benchmark with its log.
- **README:** one new section, "BMP", after "Holding a table":
  - a Kafka-style loop: `openbmp.Parse`, a `Session` per router hash,
    `s.Routes(bmp.ParseMessage(b))`;
  - a byte-stream loop;
  - one sentence on §7.6's rule, and one saying a table's key includes
    `View`.
- The first paragraph's "reads MRT" gains "and BMP".

## 11. Open facts (to settle during planning or the capture)

- **The live topic names** on CAIDA's and RouteViews' brokers, and whether
  either is the other's mirror (§5.4). Listing topics fetches no route data,
  but it does contact the brokers, so it waits for the same go-ahead as the
  capture.
- Whether Bimper's frames are byte-compatible with the OpenBMP 1.7 binary
  header.
- Whether Peer Ups appear in the stream at all after a consumer joins
  (re-sent by Bimper, or only at session start), and how long the brokers
  retain messages. These decide how often §7.6's rule fires.
- Which ADD-PATH families RouteViews' peers negotiate, and whether FRR
  encodes post-policy and Loc-RIB UPDATEs with the same path-ID setting as
  pre-policy (§7.5).
- Whether RouteViews sends Adj-RIB-Out or Loc-RIB at all, and whether it
  sets the A flag (FRR may reformat to 4-byte).
- Whether the per-peer timestamp is ever zero in the feed. bgp-platform
  falls back to the receive time, and bgpkit's example to the OpenBMP
  timestamp. With `Route.Time` zero, a consumer may want the envelope's
  `Time`.

## 12. Left to the builder

- Constant names and the `String` methods of the new types, within C3.
- Whether `mrt` and `bmp` share the growing-buffer reader through an
  internal package, or `bmp` copies it.
- The harness's exact JSON and normalizer, as long as every route field of
  §7.4 that bgpkit has is compared.
- How `internal/bmpgen` lays out its fixture list, mirroring
  `mrtgen.Synthetic()`.

## 13. Builder check

A builder (a fresh subagent, not the author) read the first draft against
the code and the RFC texts. What they raised, and where it is answered now:

- **A malformed Peer Up or Peer Down lost its effect on the session.** In
  the draft, a broken OPEN or a short reason-2 Peer Down made the whole
  message a `MessageError`, which `Session` never saw. A peer could then keep
  stale ADD-PATH state, or never be reported down. Now, once the per-peer
  header reads, they are messages (§7.1). Damage is found by `Body` and the
  accessors, a damaged Peer Up makes the peer unknown, and a Peer Down always
  removes the peer and yields its event (§7.4, §7.5). Warnings in the OPENs
  also make the state unknown, and the OPEN diagnostics ride on the Peer Up
  event. `Session.Routes` takes `(Message, error)`, so errors reach it in the
  Kafka loop as in `RoutesWith`.
- **Loc-RIB with several emulated peers.** A Peer Up now sets only its
  Multiprotocol families. A Loc-RIB Peer Down still removes the whole key,
  and filtered views by table name collide. Both are stated as limits
  (§7.5).
- **Loc-RIB needs the ADD-PATH tuples even when their mode is invalid.** The
  RFC 7911 "ignore the capability" rule moved from the accessor into
  `AddPathFamilies`, and `AddPathFamily` keeps the raw `Mode` (§6.1).
- **Kafka topics are per router and peer AS, so peer-less messages have no
  order against other peers'.** Termination no longer clears state (§7.5),
  and the session state is per peer. A route then depends only on its own
  peer's messages (§7.4).
- **Peer identity across MRT and BMP.** It is not equal (BGP4MP routes have
  no BGP ID); that is stated, with the match to use (§7.3).
- **Corrected statements:**
  - fuzz.sh would run eleven targets, not ten;
  - bgp-platform's starting offset is unverified, and §7.6 now says what is
    known;
  - the Initiation does name the router (sysName);
  - a table key needs `View` (§2);
  - parameter type 1's deprecation is in RFC 4271 Appendix A;
  - the interner key change is defensive, since BMP blocks are never keyed
    whole (§6.2).
