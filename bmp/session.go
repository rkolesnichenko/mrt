package bmp

import (
	"net/netip"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// Session is the state Routes keeps for one BMP session (one monitored
// router): each peer's ADD-PATH families from its last Peer Up (spec §7.5).
// A peer is known from a Peer Up whose OPENs frame and parse without a
// Warning until its Peer Down; before that, or after a damaged Peer Up, it is
// unknown and its UPDATEs are decoded without ADD-PATH (§7.6). The one
// exception: a Loc-RIB Peer Up whose only OPEN Warning is for an ADD-PATH
// Mode outside 1-3 stays known, as RFC 9069 section 5.2 says to ignore the
// ADD-PATH intent. Termination and Initiation change nothing. A Session is
// not safe for concurrent use.
type Session struct {
	opts  RoutesOptions
	peers map[peerKey]peerState
}

// peerKey identifies a peer within a session: (type, distinguisher, address)
// for peer types 0-2 and others, (distinguisher, BGP ID) for Loc-RIB (RFC
// 9069 section 6.1.1). The L and O flags are not in it: a Peer Up covers
// every view of its peer.
type peerKey struct {
	typ   mrt.PeerType
	dist  uint64
	addr  netip.Addr // zero for Loc-RIB
	bgpID netip.Addr // zero for every type but Loc-RIB
}

func keyOf(h PeerHeader) peerKey {
	k := peerKey{typ: h.Type, dist: h.Distinguisher}
	if h.Type == mrt.PeerLocRIB {
		k.bgpID = h.BGPID
	} else {
		k.addr = h.Addr
	}
	return k
}

// peerState is what a peer's Peer Ups established. A key in Session.peers is
// the known flag: an unknown peer has no entry.
type peerState struct {
	in, out bgp.FamilySet // types other than Loc-RIB: families with path IDs toward the router (Adj-RIB-In) and from it (Adj-RIB-Out)
	seen    bgp.FamilySet // Loc-RIB: families some Peer Up has set, on or off
	on      bgp.FamilySet // Loc-RIB: the seen families with path IDs
}

// allFamilies is every family: a known peer of types 0-2 knows them all.
const allFamilies = ^bgp.FamilySet(0)

// NewSession returns an empty Session that yields routes with opts.
func NewSession(opts RoutesOptions) *Session {
	return &Session{opts: opts, peers: make(map[peerKey]peerState)}
}

// lookup returns, for a message of h's peer, the families whose NLRI carry
// path IDs and the families whose ADD-PATH state is known.
func (s *Session) lookup(h PeerHeader) (addPath, known bgp.FamilySet) {
	st, ok := s.peers[keyOf(h)]
	switch {
	case h.Type == mrt.PeerLocRIB:
		return st.on, st.seen
	case !ok:
		return 0, 0
	case h.Flags&flagO != 0:
		return st.out, allFamilies
	}
	return st.in, allFamilies
}

// peerUp applies m to the session and returns its KindPeerState event.
func (s *Session) peerUp(m PeerUp) mrt.Route {
	h := m.Peer
	key := keyOf(h)
	ev := s.event(h, 0, mrt.StateEstablished)
	body, err := m.Body()
	if err != nil {
		delete(s.peers, key)
		ev.Diagnostics = []bgp.Diagnostic{{Severity: bgp.SeverityError, Code: DiagPeerUpBody, Message: err.Error()}}
		return ev
	}
	sent, ds := bgp.ParseOpen(body.SentOpen)
	recv, dr := bgp.ParseOpen(body.ReceivedOpen)
	if len(ds)+len(dr) > 0 {
		ev.Diagnostics = append(append(make([]bgp.Diagnostic, 0, len(ds)+len(dr)), ds...), dr...)
	}
	locRIB := h.Type == mrt.PeerLocRIB
	if damaged(ds, locRIB && badMode(sent)) || damaged(dr, locRIB && badMode(recv)) {
		delete(s.peers, key)
		return ev
	}
	if !locRIB {
		s.peers[key] = peerState{in: bgp.AddPathFamilies(recv, sent), out: bgp.AddPathFamilies(sent, recv)}
		return ev
	}
	// Loc-RIB (RFC 9069 section 5.2): the sent OPEN says it all, and the
	// ADD-PATH intent is ignored. This Peer Up sets the families of its
	// Multiprotocol capability, or IPv4 unicast without one (RFC 4760
	// section 8), and leaves the others alone: another emulated peer of the
	// same Loc-RIB may have set them.
	var on bgp.FamilySet
	for _, t := range sent.AddPath() {
		on = on.With(t.Family)
	}
	fams := sent.Multiprotocol()
	if len(fams) == 0 {
		fams = []bgp.Family{{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}}
	}
	st := s.peers[key]
	for _, f := range fams {
		bit := bgp.FamilySet(0).With(f)
		st.seen |= bit
		if on.Has(f) {
			st.on |= bit
		} else {
			st.on &^= bit
		}
	}
	s.peers[key] = st
	return ev
}

// peerDown applies m to the session and returns its KindPeerState event. A
// Loc-RIB Peer Down removes every emulated peer of its Loc-RIB: they share
// the key.
func (s *Session) peerDown(m PeerDown) mrt.Route {
	delete(s.peers, keyOf(m.Peer))
	return s.event(m.Peer, mrt.StateEstablished, mrt.StateIdle)
}

// event is a KindPeerState route for h's peer. Its View is zero, as the event
// covers every view of the peer (RFC 8671 section 6.3), except for Loc-RIB.
func (s *Session) event(h PeerHeader, from, to mrt.State) mrt.Route {
	ev := mrt.Route{Time: h.Time, Kind: mrt.KindPeerState, Peer: h.Peer(), Router: s.opts.Router, OldState: from, NewState: to}
	if h.Type == mrt.PeerLocRIB {
		ev.View = mrt.ViewLocRIB
	}
	return ev
}

// damaged reports whether an OPEN's diagnostics make its peer's state
// unknown: any Warning or Error. With excuseMode, one DiagCapabilityValue
// Warning is not counted: the one a Loc-RIB OPEN draws for an ADD-PATH Mode
// outside 1-3, whose intent RFC 9069 section 5.2 says to ignore. ParseOpen
// gives an ADD-PATH capability at most one Warning, so any other Warning is
// still counted.
func damaged(diags []bgp.Diagnostic, excuseMode bool) bool {
	for _, d := range diags {
		if d.Severity < bgp.SeverityWarning {
			continue
		}
		if excuseMode && d.Code == bgp.DiagCapabilityValue {
			excuseMode = false
			continue
		}
		return true
	}
	return false
}

// badMode reports whether o's ADD-PATH tuples hold a Mode outside 1-3.
func badMode(o bgp.Open) bool {
	for _, t := range o.AddPath() {
		if t.Mode < 1 || t.Mode > 3 {
			return true
		}
	}
	return false
}
