package mrt

import (
	"errors"
	"fmt"
	"iter"
	"net/netip"
	"time"

	"github.com/rkolesnichenko/mrt/bgp"
)

// Kind says where a Route came from.
type Kind uint8

// Route kinds.
const (
	KindRIB      Kind = 0 // an entry of a RIB dump
	KindAnnounce Kind = 1 // announced by an UPDATE
	KindWithdraw Kind = 2 // withdrawn by an UPDATE
)

// String returns "B", "A" or "W".
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

// Routes turns records into routes: RIB entries (TABLE_DUMP_V2 and legacy
// TABLE_DUMP) and the announcements and withdrawals of BGP4MP UPDATEs, IPv4
// and IPv6 unicast and multicast only. Other records yield nothing. Paths are
// decoded once per UPDATE or RIB entry and shared; callers must not modify
// them. Errors from records pass through; an entry whose peer index is not
// in the latest PEER_INDEX_TABLE yields a *RecordError (Offset -1) and the
// others continue. Routes stops when yield returns false.
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
			if !yield(Route{}, err) {
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
	t, err := m.Type()
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
		diags = append(append(diags, d1...), d2...)
	}
	base := Route{Time: m.Header().Time, Peer: Peer{Addr: m.PeerAddr, AS: m.PeerAS}, Diagnostics: diags}
	emit := func(kind Kind, fam bgp.Family, ns []bgp.NLRI) bool {
		for _, n := range ns {
			rt := base
			rt.Kind, rt.Family = kind, fam
			rt.Prefix, rt.PathID, rt.HasPathID = n.Prefix, n.PathID, n.HasPathID
			if kind != KindWithdraw {
				rt.Path = &p
			}
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
	if mp := p.MPReach; mp != nil && mp.Family.HasPrefixes() {
		return emit(KindAnnounce, mp.Family, mp.NLRI)
	}
	return true
}
