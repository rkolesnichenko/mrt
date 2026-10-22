package bmp

import (
	"fmt"
	"iter"
	"net/netip"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// Diagnostic codes of Routes. They start with "bmp/", so they never collide
// with bgp's.
const (
	// DiagAddPathUnknown is a Warning, at offset 0, on every route of a Route
	// Monitoring whose peer's ADD-PATH state is not known: no Peer Up was
	// seen, the last one was damaged, or, for Loc-RIB, no Peer Up has set a
	// family the UPDATE holds. The NLRI were read without path IDs.
	DiagAddPathUnknown = "bmp/addpath-unknown"
	// DiagPeerUpBody is an Error on a Peer Up's KindPeerState route when
	// PeerUp.Body fails, so the OPENs could not be read.
	DiagPeerUpBody = "bmp/peer-up-body"
)

// RoutesOptions are the options of RoutesWith and NewSession.
type RoutesOptions struct {
	// Router is copied into every Route.Router: the monitored router, which
	// a BMP message does not name.
	Router netip.Addr
	// PeerStates yields a KindPeerState Route for every Peer Up (OldState 0,
	// NewState StateEstablished, the two OPENs' diagnostics) and Peer Down
	// (StateEstablished to StateIdle), in stream order among the routes.
	PeerStates bool
	// EndOfRIB yields a KindEndOfRIB Route for every End-of-RIB marker (RFC
	// 4724 section 2). Without it a marker yields nothing, as in mrt.Routes.
	EndOfRIB bool
	// RequirePeerUp refuses a Route Monitoring that would carry
	// DiagAddPathUnknown: it yields one *MessageError (Offset -1, wrapping
	// ErrNoPeerUp) and no route.
	RequirePeerUp bool
	// Interner, when set, decodes path attributes through it, as
	// mrt.RoutesOptions.Interner does for UPDATEs: each UPDATE's path is its
	// own, shares AS paths and community lists with equal ones decoded
	// before, and holds no message bytes. The routes are deeply equal to
	// those decoded without it.
	Interner *bgp.Interner
}

// decode decodes an attribute block, through the interner when there is one.
func (o RoutesOptions) decode(a bgp.Attrs) (*bgp.Path, []bgp.Diagnostic) {
	if o.Interner != nil {
		return o.Interner.Decode(a)
	}
	p, d := a.Decode()
	return &p, d
}

// Routes turns BMP messages from one monitored router into routes: the
// announcements and withdrawals of Route Monitoring UPDATEs, IPv4 and IPv6
// unicast and multicast only, as mrt.Routes does for BGP4MP UPDATEs. Each
// route's Time is the per-peer header's, Peer is PeerHeader.Peer and View is
// PeerHeader.View. ADD-PATH comes from each peer's Peer Up (see Session).
// Paths are decoded once per UPDATE and shared; callers must not modify
// them. Errors from msgs pass through. These yield a *MessageError (Offset
// -1, wrapping ErrMalformed) and the others continue: a Route Monitoring of
// an unknown peer type, one whose BGP header is broken or whose message is
// not an UPDATE, and an UPDATE that yields no route but has a Warning or
// Error diagnostic from bgp (the first one is named). Statistics, Initiation,
// Termination, Route Mirroring and Other yield nothing.
//
// Routes stops when yield returns false. Ranging over it again after a
// break goes on with the next message from msgs and the same session state;
// the routes left in the message it broke in are not yielded. It must not be
// ranged over by two goroutines at once. Routes is RoutesWith(msgs,
// RoutesOptions{}).
//
// Without an Interner a path is decoded in place, so the values of its
// Unknown() attributes, MPReach.RawNLRI and MPUnreach().RawNLRI alias the
// message's bytes: holding the path holds the message. To hold paths, decode
// through RoutesOptions.Interner, or keep a Path.Clone.
func Routes(msgs iter.Seq2[Message, error]) iter.Seq2[mrt.Route, error] {
	return RoutesWith(msgs, RoutesOptions{})
}

// RoutesWith is Routes with options; see Routes for what it yields and
// RoutesOptions for what each option adds. It is NewSession(opts) and a loop
// over msgs feeding Session.Routes.
func RoutesWith(msgs iter.Seq2[Message, error], opts RoutesOptions) iter.Seq2[mrt.Route, error] {
	s := NewSession(opts)
	return func(yield func(mrt.Route, error) bool) {
		for m, err := range msgs {
			if !s.emit(s.apply(m, err), yield) {
				return
			}
		}
	}
}

// Routes takes what ParseMessage returns, so that
// s.Routes(bmp.ParseMessage(b)) is the whole step for one Kafka message, and
// yields the message's routes as RoutesWith does. A non-nil err is yielded
// unchanged, once. The message's effect on the session (a Peer Up or Peer
// Down) is applied when Routes is called, whether or not the sequence is
// ranged over, and a Route Monitoring is decoded with the state as it is
// then; ranging over the sequence again yields the same routes again.
//
// Without an Interner the paths alias m's bytes, as Routes describes: a
// caller that reuses the buffer it gave ParseMessage sets
// RoutesOptions.Interner, or clones the paths it keeps.
func (s *Session) Routes(m Message, err error) iter.Seq2[mrt.Route, error] {
	st := s.apply(m, err)
	return func(yield func(mrt.Route, error) bool) { s.emit(st, yield) }
}

// step is what one message yields, fixed when the message is applied to the
// session.
type step struct {
	err            error         // yielded as is
	event          mrt.Route     // a KindPeerState route, when its Kind is set
	m              Message       // a RouteMonitoring to decode, or nil
	addPath, known bgp.FamilySet // for m: Session.lookup's answer
}

// apply applies msg to the session and returns what it yields.
func (s *Session) apply(msg Message, err error) step {
	if err != nil {
		return step{err: err}
	}
	var ev mrt.Route
	switch m := msg.(type) {
	case RouteMonitoring:
		a, k := s.lookup(m.Peer)
		return step{m: msg, addPath: a, known: k}
	case PeerUp:
		ev = s.peerUp(m)
	case PeerDown:
		ev = s.peerDown(m)
	default:
		return step{}
	}
	if !s.opts.PeerStates {
		return step{}
	}
	return step{event: ev}
}

// emit yields what st holds. It returns false when yield did.
func (s *Session) emit(st step, yield func(mrt.Route, error) bool) bool {
	switch {
	case st.err != nil:
		return yield(mrt.Route{}, st.err)
	case st.event.Kind != 0:
		return yield(st.event, nil)
	case st.m != nil:
		return s.monitoring(st.m.(RouteMonitoring), st.addPath, st.known, yield)
	}
	return true
}

var v4 = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}

// monitoring yields the routes of a Route Monitoring, decoded with path IDs
// for the families in addPath; a family outside known makes the message's
// routes carry DiagAddPathUnknown. It follows mrt's messageRoutes.
func (s *Session) monitoring(m RouteMonitoring, addPath, known bgp.FamilySet, yield func(mrt.Route, error) bool) bool {
	fail := func(err error) bool {
		return yield(mrt.Route{}, &MessageError{Offset: -1, Header: m.Header(), Raw: m.Raw(), Err: err})
	}
	as4, ok := m.Peer.AS4()
	if !ok {
		return fail(fmt.Errorf("%w: peer type %d is unknown, and with it the AS width", ErrMalformed, m.Peer.Type))
	}
	t, err := m.MessageType()
	if err != nil {
		return fail(fmt.Errorf("%w: %w", ErrMalformed, err))
	}
	if t != bgp.MessageUpdate {
		return fail(fmt.Errorf("%w: Route Monitoring carries BGP message type %d, not UPDATE", ErrMalformed, t))
	}
	msg := m.Update
	u, d1 := bgp.ParseUpdate(msg, bgp.Context{AS4: as4, AddPathFamilies: addPath})
	p, d2 := s.opts.decode(u.Attrs)
	var diags []bgp.Diagnostic
	if len(d1)+len(d2) > 0 {
		// Decode's offsets count from the attribute block, which starts after
		// the header, the withdrawn routes and the two length fields.
		block := 0
		if len(msg) >= 21 { // d2 is empty unless ParseUpdate read this far
			block = 19 + 2 + (int(msg[19])<<8 | int(msg[20])) + 2
		}
		diags = append(diags, d1...)
		for _, d := range d2 {
			d.Offset += block
			diags = append(diags, d)
		}
	}

	// The routes to come, and the families the UPDATE holds.
	mpu, mpr := p.MPUnreach(), p.MPReach
	n := len(u.Withdrawn) + len(u.Announced)
	var used bgp.FamilySet
	if ipv4Fields(msg) {
		used = used.With(v4)
	}
	if mpu != nil {
		used = used.With(mpu.Family)
		if mpu.Family.HasPrefixes() {
			n += len(mpu.Withdrawn)
		}
	}
	if mpr != nil {
		used = used.With(mpr.Family)
		if mpr.Family.HasPrefixes() {
			n += len(mpr.NLRI)
		}
	}
	if n > 0 && used&^known != 0 {
		if s.opts.RequirePeerUp {
			return fail(ErrNoPeerUp)
		}
		diags = append(diags, bgp.Diagnostic{Severity: bgp.SeverityWarning, Code: DiagAddPathUnknown,
			Message: "no Peer Up seen for this peer and family: NLRI read without ADD-PATH path identifiers"})
	}

	base := mrt.Route{Time: m.Peer.Time, Peer: m.Peer.Peer(), Diagnostics: diags, Router: s.opts.Router, View: m.Peer.View()}
	emit := func(kind mrt.Kind, fam bgp.Family, ns []bgp.NLRI) bool {
		for _, nl := range ns {
			rt := base
			rt.Kind, rt.Family = kind, fam
			rt.Prefix, rt.PathID, rt.HasPathID = nl.Prefix, nl.PathID, nl.HasPathID
			if kind != mrt.KindWithdraw {
				rt.Path = p
			}
			if !yield(rt, nil) {
				return false
			}
		}
		return true
	}
	if !emit(mrt.KindWithdraw, v4, u.Withdrawn) {
		return false
	}
	if mpu != nil && mpu.Family.HasPrefixes() && !emit(mrt.KindWithdraw, mpu.Family, mpu.Withdrawn) {
		return false
	}
	if !emit(mrt.KindAnnounce, v4, u.Announced) {
		return false
	}
	if mpr != nil && mpr.Family.HasPrefixes() && !emit(mrt.KindAnnounce, mpr.Family, mpr.NLRI) {
		return false
	}
	if n > 0 {
		return true
	}
	// No route: a fault that made the UPDATE's routes vanish must not be
	// silent. Only bgp's diagnostics are here: DiagAddPathUnknown needs a route.
	for _, d := range diags {
		if d.Severity >= bgp.SeverityWarning {
			return fail(fmt.Errorf("%w: %s: %s", ErrMalformed, d.Code, d.Message))
		}
	}
	if s.opts.EndOfRIB && len(diags) == 0 {
		if fam, ok := endOfRIB(u, mpu); ok {
			base.Kind, base.Family = mrt.KindEndOfRIB, fam
			return yield(base, nil)
		}
	}
	return true
}

// ipv4Fields reports whether the UPDATE msg, whose header is valid, has a
// byte in its own Withdrawn Routes or NLRI field (IPv4 unicast), whether or
// not those bytes parse.
func ipv4Fields(msg []byte) bool {
	if len(msg) < 21 {
		return false
	}
	if msg[19] != 0 || msg[20] != 0 {
		return true
	}
	end := min(len(msg), int(msg[16])<<8|int(msg[17])) // ParseUpdate reads no further
	if end < 23 {
		return false
	}
	return end > 23+(int(msg[21])<<8|int(msg[22]))
}

// endOfRIB reports whether an UPDATE that parsed without diagnostics and
// yields no route is an End-of-RIB marker (RFC 4724 section 2), and for which
// family: no withdrawn routes, attributes or NLRI (IPv4 unicast), or only an
// MP_UNREACH_NLRI attribute that withdraws nothing (its family, which may be
// one without prefixes).
func endOfRIB(u bgp.Update, mpu *bgp.MPUnreach) (bgp.Family, bool) {
	if len(u.Attrs.Raw()) == 0 {
		return v4, true
	}
	if mpu == nil || len(mpu.Withdrawn) > 0 || len(mpu.RawNLRI) > 0 {
		return bgp.Family{}, false
	}
	n := 0
	for a, err := range u.Attrs.All() {
		if err != nil || a.Code != bgp.AttrMPUnreach {
			return bgp.Family{}, false
		}
		n++
	}
	return mpu.Family, n == 1
}
