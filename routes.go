package mrt

import (
	"errors"
	"fmt"
	"iter"
	"net/netip"
	"time"

	"github.com/rkolesnichenko/mrt/bgp"
)

// Kind says where a Route came from. The zero Kind is no route, as on the
// Route that comes with an error.
type Kind uint8

// Route kinds.
const (
	KindRIB      Kind = 1 // an entry of a RIB dump
	KindAnnounce Kind = 2 // announced by an UPDATE
	KindWithdraw Kind = 3 // withdrawn by an UPDATE
)

// String returns "B", "A" or "W", and "?" for the zero Kind or any other value.
func (k Kind) String() string {
	switch k {
	case KindRIB:
		return "B"
	case KindAnnounce:
		return "A"
	case KindWithdraw:
		return "W"
	}
	return "?"
}

// Route is one (peer, prefix, path ID) from a RIB entry or an UPDATE.
type Route struct {
	Time      time.Time // record time; Originated is on the entry for RIB routes
	Kind      Kind
	Peer      Peer
	Family    bgp.Family
	Prefix    netip.Prefix
	PathID    uint32
	HasPathID bool
	Path      *bgp.Path // nil for Withdraw; shared by every route from one UPDATE or RIB entry
	// Diagnostics are from decoding the UPDATE or the entry's attributes,
	// shared the same way. For an UPDATE's routes every Offset counts from
	// the start of the BGP message (attribute diagnostics are rebased onto
	// it); for a RIB or TABLE_DUMP entry's, from the entry's attribute block.
	Diagnostics []bgp.Diagnostic
}

// Routes turns records into routes: RIB entries (TABLE_DUMP_V2 and legacy
// TABLE_DUMP) and the announcements and withdrawals of BGP4MP UPDATEs, IPv4
// and IPv6 unicast and multicast only. Other records yield nothing. Paths are
// decoded once per UPDATE or RIB entry and shared; callers must not modify
// them. Errors from records pass through. These yield a *RecordError
// (Offset -1, wrapping ErrMalformed) and the others continue: a RIB entry
// whose framing is broken or whose peer index is not in the latest
// PEER_INDEX_TABLE, a BGP4MP message whose header is broken, and an UPDATE
// that yields no route but has a Warning or Error diagnostic (the first one
// is named). Routes stops when yield returns false.
func Routes(records iter.Seq2[Record, error]) iter.Seq2[Route, error] {
	return func(yield func(Route, error) bool) {
		var table PeerIndexTable
		for rec, err := range records {
			if err != nil {
				// A broken PEER_INDEX_TABLE must not leave the previous table in force.
				var re *RecordError
				if errors.As(err, &re) && re.Header.Type == TypeTableDumpV2 && re.Header.Subtype == SubPeerIndexTable {
					table = PeerIndexTable{}
				}
				if !yield(Route{}, err) {
					return
				}
				continue
			}
			ok := true
			switch r := rec.(type) {
			case PeerIndexTable:
				table = r
			case RIB:
				ok = ribRoutes(r, table, yield)
			case TableDump:
				ok = tableDumpRoute(r, yield)
			case BGP4MPMessage:
				ok = messageRoutes(r, yield)
			}
			if !ok {
				return
			}
		}
	}
}

func ribRoutes(r RIB, table PeerIndexTable, yield func(Route, error) bool) bool {
	if !r.Prefix.IsValid() {
		return true
	}
	h := r.Header()
	for e, err := range r.Entries() {
		if err != nil {
			if !yield(Route{}, &RecordError{Offset: -1, Header: h, Raw: r.Raw(), Err: err}) {
				return false
			}
			continue
		}
		if int(e.PeerIndex) >= len(table.Peers) {
			err := &RecordError{Offset: -1, Header: h, Raw: r.Raw(),
				Err: fmt.Errorf("%w: peer index %d not in the peer index table (%d peers)", ErrMalformed, e.PeerIndex, len(table.Peers))}
			if !yield(Route{}, err) {
				return false
			}
			continue
		}
		p, d := e.Attrs.Decode()
		rt := Route{
			Time: h.Time, Kind: KindRIB, Peer: table.Peers[e.PeerIndex],
			Family: r.Family, Prefix: r.Prefix,
			PathID: e.PathID, HasPathID: r.AddPath,
			Path: &p, Diagnostics: d,
		}
		if h.Subtype == SubRIBGenericAddPath {
			rt.PathID = r.PathID
		}
		if !yield(rt, nil) {
			return false
		}
	}
	return true
}

func tableDumpRoute(r TableDump, yield func(Route, error) bool) bool {
	h := r.Header()
	fam := bgp.Family{AFI: 1, SAFI: 1}
	if h.Subtype == SubAFIIPv6 {
		fam.AFI = 2
	}
	p, d := r.Attrs.Decode()
	return yield(Route{Time: h.Time, Kind: KindRIB, Peer: r.Peer, Family: fam, Prefix: r.Prefix, Path: &p, Diagnostics: d}, nil)
}

func messageRoutes(m BGP4MPMessage, yield func(Route, error) bool) bool {
	if m.Local {
		return true
	}
	t, err := m.MessageType()
	if err != nil {
		return yield(Route{}, &RecordError{Offset: -1, Header: m.Header(), Raw: m.Raw(),
			Err: fmt.Errorf("%w: %w", ErrMalformed, err)})
	}
	if t != bgp.MessageUpdate {
		return true
	}
	u, d1 := bgp.ParseUpdate(m.Message, m.Context())
	p, d2 := u.Attrs.Decode()
	var diags []bgp.Diagnostic
	if len(d1)+len(d2) > 0 {
		// Decode's offsets count from the attribute block, which starts after
		// the header, the withdrawn routes and the two length fields.
		block := 0
		if len(m.Message) >= 21 { // d2 is empty unless ParseUpdate read this far
			block = 19 + 2 + (int(m.Message[19])<<8 | int(m.Message[20])) + 2
		}
		diags = append(diags, d1...)
		for _, d := range d2 {
			d.Offset += block
			diags = append(diags, d)
		}
	}
	base := Route{Time: m.Header().Time, Peer: Peer{Addr: m.PeerAddr, AS: m.PeerAS}, Diagnostics: diags}
	routes := 0
	emit := func(kind Kind, fam bgp.Family, ns []bgp.NLRI) bool {
		for _, n := range ns {
			rt := base
			rt.Kind, rt.Family = kind, fam
			rt.Prefix, rt.PathID, rt.HasPathID = n.Prefix, n.PathID, n.HasPathID
			if kind != KindWithdraw {
				rt.Path = &p
			}
			routes++
			if !yield(rt, nil) {
				return false
			}
		}
		return true
	}
	v4 := bgp.Family{AFI: 1, SAFI: 1}
	if !emit(KindWithdraw, v4, u.Withdrawn) {
		return false
	}
	if mp := p.MPUnreach; mp != nil && mp.Family.HasPrefixes() && !emit(KindWithdraw, mp.Family, mp.Withdrawn) {
		return false
	}
	if !emit(KindAnnounce, v4, u.Announced) {
		return false
	}
	if mp := p.MPReach; mp != nil && mp.Family.HasPrefixes() && !emit(KindAnnounce, mp.Family, mp.NLRI) {
		return false
	}
	if routes > 0 {
		return true
	}
	// No route: a fault that made the UPDATE's routes vanish must not be silent.
	for _, d := range diags {
		if d.Severity >= bgp.SeverityWarning {
			return yield(Route{}, &RecordError{Offset: -1, Header: m.Header(), Raw: m.Raw(),
				Err: fmt.Errorf("%w: %s: %s", ErrMalformed, d.Code, d.Message)})
		}
	}
	return true
}
