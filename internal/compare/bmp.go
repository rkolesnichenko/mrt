package main

// gobgp v4.10.0's pkg/packet/bmp as the second BMP oracle and the allocation
// baseline (spec §9.2, §9.6).
//
// Both sides read the same messages: a .bmp file is framed by bmp.Messages
// (the bytes of every message it frames, also of one it refuses, so gobgp
// decides on it itself), an .obmp file by openbmp.Frames and openbmp.Parse,
// one stream per router (Header.RouterHash), as bmp/golden_test.go reads
// them. mrt decodes each stream with one bmp.Session, gobgp with one
// gobgpSession; framing is not compared.
//
// gobgp is called as a caller of ParseBMPMessageWithOptions has to call it:
// the options carry the AS width (Use2ByteAS from the A flag, for every peer
// type but Loc-RIB) and the ADD-PATH families, which the caller keeps from
// each peer's Peer Up. gobgpSession keeps them by spec §7.5's rule, the one
// bmp.Session keeps, from the OPENs as gobgp parses them, so that the
// comparison tests decoding and not the rule. That includes RFC 7911
// section 4's rule that a Send/Receive value outside 1-3 voids the
// capability, which gobgp's own negotiation (open2Cap in pkg/server, whose
// bit tests read 7 as send and receive) does not apply. A peer with no Peer
// Up, or whose Peer Up gobgp does not parse, decodes with no ADD-PATH, as
// mrt's default does (§7.6). Termination and Initiation change nothing.
//
// The lines are oracle.FromRoute's: what gobgp's packet layer decoded,
// rendered field by field and nothing more. gobgp's pkg/packet does not merge
// AS4_PATH into AS_PATH nor AS4_AGGREGATOR into AGGREGATOR (its server does,
// in internal/pkg/table, which no other module can import) and has no
// ONLY_TO_CUSTOMER attribute, so neither is done here. A Route Monitoring
// that gobgp decodes with an error gives no line.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	gbgp "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	gbmp "github.com/osrg/gobgp/v4/pkg/packet/bmp"
	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
	"github.com/rkolesnichenko/mrt/internal/oracle"
)

// readBMPStreams reads a .bmp file as one stream of messages, or an .obmp
// file as one stream per router, in the order the routers first appear.
// Frames and messages that cannot be framed are left out.
func readBMPStreams(path string) ([][][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !strings.HasSuffix(path, ".obmp") {
		var stream [][]byte
		for m, err := range bmp.Messages(f) {
			var me *bmp.MessageError
			switch {
			case err == nil:
				stream = append(stream, m.Raw())
			case errors.As(err, &me) && me.Raw != nil:
				stream = append(stream, me.Raw)
			case !errors.As(err, &me):
				return nil, err
			}
		}
		return [][][]byte{stream}, nil
	}
	var streams [][][]byte
	routers := map[[16]byte]int{}
	for frame, err := range openbmp.Frames(f) {
		if err != nil {
			continue
		}
		h, msg, err := openbmp.Parse(frame)
		if err != nil {
			continue
		}
		i, ok := routers[h.RouterHash]
		if !ok {
			i = len(streams)
			routers[h.RouterHash] = i
			streams = append(streams, nil)
		}
		streams[i] = append(streams[i], msg)
	}
	return streams, nil
}

// mrtBMPRoutes is the announcements and withdrawals of one stream through one
// bmp.Session, as oracle.FromRoute lines, sorted. Errors give no line.
func mrtBMPRoutes(stream [][]byte) []string {
	s := bmp.NewSession(bmp.RoutesOptions{})
	var ls []string
	for _, b := range stream {
		for r, err := range s.Routes(bmp.ParseMessage(b)) {
			if err == nil && (r.Kind == mrt.KindAnnounce || r.Kind == mrt.KindWithdraw) {
				ls = append(ls, oracle.FromRoute(r))
			}
		}
	}
	return oracle.Sorted(ls)
}

// gobgpBMPRoutes is the announcements and withdrawals gobgp decodes from one
// stream, rendered as oracle.FromRoute lines, sorted. Its error is a route
// that cannot be rendered.
func gobgpBMPRoutes(stream [][]byte) ([]string, error) {
	s := newGobgpSession()
	var ls []string
	for _, b := range stream {
		msg, err := s.decode(b)
		if err != nil {
			continue
		}
		rm, ok := msg.Body.(*gbmp.BMPRouteMonitoring)
		if !ok || rm.BGPUpdate == nil {
			continue
		}
		u, ok := rm.BGPUpdate.Body.(*gbgp.BGPUpdate)
		if !ok {
			continue
		}
		if ls, err = renderUpdate(ls, msg.PeerHeader, u, s.cur); err != nil {
			return nil, err
		}
	}
	return oracle.Sorted(ls), nil
}

// gobgpKey identifies a peer within a session, as bmp.Session's key does:
// (type, distinguisher, address), or (distinguisher, BGP ID) for Loc-RIB.
type gobgpKey struct {
	typ   uint8
	dist  uint64
	addr  netip.Addr
	bgpID netip.Addr
}

func gobgpKeyOf(h gbmp.BMPPeerHeader) gobgpKey {
	k := gobgpKey{typ: h.PeerType, dist: h.PeerDistinguisher}
	if h.PeerType == gbmp.BMP_PEER_TYPE_LOCAL_RIB {
		k.bgpID = h.PeerBGPID
	} else {
		k.addr = h.PeerAddress
	}
	return k
}

// gobgpPeer is a known peer's ADD-PATH families: fams[0] toward the router
// (Adj-RIB-In, or Loc-RIB's families on and off), fams[1] from it
// (Adj-RIB-Out). opts[view][as2] are the options for them, built once, so that
// a message costs gobgp's decoding and nothing of this harness.
type gobgpPeer struct {
	fams [2]map[gbgp.Family]bool
	opts [2][2][]*gbgp.MarshallingOption
}

func (p *gobgpPeer) options(view, as2 int) []*gbgp.MarshallingOption {
	if p.opts[view][as2] == nil {
		o := &gbgp.MarshallingOption{Use2ByteAS: as2 == 1, AddPath: map[gbgp.Family]gbgp.BGPAddPathMode{}}
		for f, on := range p.fams[view] {
			if on {
				o.AddPath[f] = gbgp.BGP_ADD_PATH_RECEIVE
			}
		}
		// Clipped: BGPUpdate.DecodeFromBytes appends to the options.
		p.opts[view][as2] = slices.Clip([]*gbgp.MarshallingOption{o})
	}
	return p.opts[view][as2]
}

// gobgpSession is what a caller of gobgp's ParseBMPMessageWithOptions keeps
// for one monitored router: each peer's ADD-PATH families from its last Peer
// Up (spec §7.5).
type gobgpSession struct {
	peers   map[gobgpKey]*gobgpPeer
	plain   [2][]*gbgp.MarshallingOption // a peer with no ADD-PATH state: 4-byte, 2-byte AS
	optFunc func(gbmp.BMPPeerHeader) []*gbgp.MarshallingOption

	// Of the message last decoded: its per-peer header, when gobgp read one,
	// and the families whose NLRI it read with path IDs.
	hdr    gbmp.BMPPeerHeader
	hasHdr bool
	cur    map[gbgp.Family]bool
}

func newGobgpSession() *gobgpSession {
	s := &gobgpSession{
		peers: map[gobgpKey]*gobgpPeer{},
		plain: [2][]*gbgp.MarshallingOption{nil, slices.Clip([]*gbgp.MarshallingOption{{Use2ByteAS: true}})},
	}
	s.optFunc = s.options
	return s
}

// options is the callback gobgp calls with the per-peer header before it
// parses the body.
func (s *gobgpSession) options(h gbmp.BMPPeerHeader) []*gbgp.MarshallingOption {
	s.hdr, s.hasHdr = h, true
	as2 := 0
	if h.PeerType != gbmp.BMP_PEER_TYPE_LOCAL_RIB && h.Flags&gbmp.BMP_PEER_FLAG_TWO_AS != 0 {
		as2 = 1
	}
	p := s.peers[gobgpKeyOf(h)]
	if p == nil {
		s.cur = nil
		return s.plain[as2]
	}
	view := 0
	if h.PeerType != gbmp.BMP_PEER_TYPE_LOCAL_RIB && h.IsAdjRIBOut() {
		view = 1
	}
	s.cur = p.fams[view]
	return p.options(view, as2)
}

// decode parses one BMP message with gobgp and applies a Peer Up or Peer Down
// to the session. A Peer Up that gobgp refuses, or whose OPENs are not OPENs,
// makes its peer unknown; a Peer Down removes its peer whatever its body.
func (s *gobgpSession) decode(b []byte) (*gbmp.BMPMessage, error) {
	s.hasHdr, s.cur = false, nil
	msg, err := gbmp.ParseBMPMessageWithOptions(b, s.optFunc)
	if !s.hasHdr {
		return msg, err
	}
	key := gobgpKeyOf(s.hdr)
	switch b[5] {
	case gbmp.BMP_MSG_PEER_DOWN_NOTIFICATION:
		delete(s.peers, key)
	case gbmp.BMP_MSG_PEER_UP_NOTIFICATION:
		var sent, recv *gbgp.BGPOpen
		if err == nil {
			up := msg.Body.(*gbmp.BMPPeerUpNotification)
			sent, _ = up.SentOpenMsg.Body.(*gbgp.BGPOpen)
			recv, _ = up.ReceivedOpenMsg.Body.(*gbgp.BGPOpen)
		}
		if sent == nil || recv == nil {
			delete(s.peers, key)
			break
		}
		s.peerUp(key, s.hdr, sent, recv)
	}
	return msg, err
}

// peerUp sets key's families from the two OPENs of its Peer Up.
func (s *gobgpSession) peerUp(key gobgpKey, h gbmp.BMPPeerHeader, sent, recv *gbgp.BGPOpen) {
	sm, rm := addPathModes(sent), addPathModes(recv)
	if h.PeerType != gbmp.BMP_PEER_TYPE_LOCAL_RIB {
		// RFC 7911 section 5: the sender's Mode has Send, the receiver's
		// Receive. In: the peer sends (received OPEN); out: the router does.
		// A Mode outside 1-3 in either OPEN means no ADD-PATH at all (RFC
		// 7911 section 4, as bgp.AddPathFamilies).
		p := &gobgpPeer{fams: [2]map[gbgp.Family]bool{{}, {}}}
		if badMode(sm) || badMode(rm) {
			s.peers[key] = p
			return
		}
		for f, m := range rm {
			if m&gbgp.BGP_ADD_PATH_SEND != 0 && sm[f]&gbgp.BGP_ADD_PATH_RECEIVE != 0 {
				p.fams[0][f] = true
			}
		}
		for f, m := range sm {
			if m&gbgp.BGP_ADD_PATH_SEND != 0 && rm[f]&gbgp.BGP_ADD_PATH_RECEIVE != 0 {
				p.fams[1][f] = true
			}
		}
		s.peers[key] = p
		return
	}
	// Loc-RIB (RFC 9069 section 5.2): every family of the sent OPEN's
	// ADD-PATH tuples, whatever the Mode, set for the families of its
	// Multiprotocol capabilities (IPv4 unicast without one), the others
	// left as an earlier Peer Up of the same key set them.
	fams := map[gbgp.Family]bool{}
	if old := s.peers[key]; old != nil {
		for f, on := range old.fams[0] {
			fams[f] = on
		}
	}
	mp := mpFamilies(sent)
	if len(mp) == 0 {
		mp = []gbgp.Family{gbgp.RF_IPv4_UC}
	}
	for _, f := range mp {
		_, on := sm[f]
		fams[f] = on
	}
	s.peers[key] = &gobgpPeer{fams: [2]map[gbgp.Family]bool{fams, nil}}
}

// badMode reports whether a Mode in m is outside 1-3: the capability "SHOULD
// be treated as not understood and ignored" (RFC 7911 section 4).
func badMode(m map[gbgp.Family]gbgp.BGPAddPathMode) bool {
	for _, mode := range m {
		if mode < gbgp.BGP_ADD_PATH_RECEIVE || mode > gbgp.BGP_ADD_PATH_BOTH {
			return true
		}
	}
	return false
}

// addPathModes is the Mode of each family in o's ADD-PATH capabilities, all
// of them together, a later tuple for a family replacing an earlier one (as
// gobgp's own open2Cap squashes them).
func addPathModes(o *gbgp.BGPOpen) map[gbgp.Family]gbgp.BGPAddPathMode {
	m := map[gbgp.Family]gbgp.BGPAddPathMode{}
	for _, p := range o.OptParams {
		pc, ok := p.(*gbgp.OptionParameterCapability)
		if !ok {
			continue
		}
		for _, c := range pc.Capability {
			if ap, ok := c.(*gbgp.CapAddPath); ok {
				for _, t := range ap.Tuples {
					m[t.Family] = t.Mode
				}
			}
		}
	}
	return m
}

// mpFamilies is the families of o's Multiprotocol capabilities.
func mpFamilies(o *gbgp.BGPOpen) []gbgp.Family {
	var fs []gbgp.Family
	for _, p := range o.OptParams {
		pc, ok := p.(*gbgp.OptionParameterCapability)
		if !ok {
			continue
		}
		for _, c := range pc.Capability {
			if mp, ok := c.(*gbgp.CapMultiProtocol); ok {
				fs = append(fs, mp.CapValue)
			}
		}
	}
	return fs
}

// gobgpLine is internal/oracle's normalized line (oracle.go, type line): the
// same fields in the same order, left out by the same rules, so that a route
// gobgp decoded and oracle.FromRoute of the same route give the same string.
type gobgpLine struct {
	Kind        string   `json:"kind"`
	Peer        string   `json:"peer"`
	PeerAS      uint32   `json:"peer_as"`
	Prefix      string   `json:"prefix"`
	PathID      *uint32  `json:"path_id,omitempty"`
	ASPath      string   `json:"as_path,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	NextHop     string   `json:"next_hop,omitempty"`
	MED         uint32   `json:"med,omitempty"`
	LocalPref   uint32   `json:"local_pref,omitempty"`
	Communities []string `json:"communities,omitempty"`
	Ext         []string `json:"ext_communities,omitempty"`
	Large       []string `json:"large_communities,omitempty"`
	OTC         *uint32  `json:"otc,omitempty"`
	Aggregator  string   `json:"aggregator,omitempty"`
	Atomic      bool     `json:"atomic,omitempty"`
}

func (l gobgpLine) String() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil {
		panic(err) // strings, numbers and bools only
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// prefixFamily reports whether f is one of the families mrt yields routes
// for: IPv4 and IPv6 unicast and multicast (R9).
func prefixFamily(f gbgp.Family) bool {
	switch f {
	case gbgp.RF_IPv4_UC, gbgp.RF_IPv4_MC, gbgp.RF_IPv6_UC, gbgp.RF_IPv6_MC:
		return true
	}
	return false
}

// gobgpAttrs is the first attribute of each type an UPDATE carries (RFC 7606
// section 3 g, as mrt keeps them; gobgp's ParseBGPMessage keeps them all).
type gobgpAttrs struct {
	origin    *gbgp.PathAttributeOrigin
	asPath    *gbgp.PathAttributeAsPath
	nextHop   *gbgp.PathAttributeNextHop
	med       *gbgp.PathAttributeMultiExitDisc
	localPref *gbgp.PathAttributeLocalPref
	atomic    *gbgp.PathAttributeAtomicAggregate
	agg       *gbgp.PathAttributeAggregator
	comms     *gbgp.PathAttributeCommunities
	ext       *gbgp.PathAttributeExtendedCommunities
	large     *gbgp.PathAttributeLargeCommunities
	reach     *gbgp.PathAttributeMpReachNLRI
	unreach   *gbgp.PathAttributeMpUnreachNLRI
}

func firstAttrs(u *gbgp.BGPUpdate) gobgpAttrs {
	var a gobgpAttrs
	for _, p := range u.PathAttributes {
		switch v := p.(type) {
		case *gbgp.PathAttributeOrigin:
			a.origin = firstOf(a.origin, v)
		case *gbgp.PathAttributeAsPath:
			a.asPath = firstOf(a.asPath, v)
		case *gbgp.PathAttributeNextHop:
			a.nextHop = firstOf(a.nextHop, v)
		case *gbgp.PathAttributeMultiExitDisc:
			a.med = firstOf(a.med, v)
		case *gbgp.PathAttributeLocalPref:
			a.localPref = firstOf(a.localPref, v)
		case *gbgp.PathAttributeAtomicAggregate:
			a.atomic = firstOf(a.atomic, v)
		case *gbgp.PathAttributeAggregator:
			a.agg = firstOf(a.agg, v)
		case *gbgp.PathAttributeCommunities:
			a.comms = firstOf(a.comms, v)
		case *gbgp.PathAttributeExtendedCommunities:
			a.ext = firstOf(a.ext, v)
		case *gbgp.PathAttributeLargeCommunities:
			a.large = firstOf(a.large, v)
		case *gbgp.PathAttributeMpReachNLRI:
			a.reach = firstOf(a.reach, v)
		case *gbgp.PathAttributeMpUnreachNLRI:
			a.unreach = firstOf(a.unreach, v)
		}
	}
	return a
}

// firstOf is old, or v when there is no old: the first one wins.
func firstOf[T any](old, v *T) *T {
	if old != nil {
		return old
	}
	return v
}

var gobgpOrigins = map[uint8]string{0: "IGP", 1: "EGP", 2: "INCOMPLETE"}

// renderUpdate appends the lines of u's routes: withdrawn routes and NLRI of
// IPv4 unicast, then MP_UNREACH_NLRI and MP_REACH_NLRI of the prefix
// families. A route of a family in addPath carries its path ID.
func renderUpdate(ls []string, h gbmp.BMPPeerHeader, u *gbgp.BGPUpdate, addPath map[gbgp.Family]bool) ([]string, error) {
	a := firstAttrs(u)
	route := func(kind string, f gbgp.Family, n gbgp.PathNLRI) (gobgpLine, error) {
		p, ok := n.NLRI.(*gbgp.IPAddrPrefix)
		if !ok {
			return gobgpLine{}, fmt.Errorf("an NLRI of family %v is a %T", f, n.NLRI)
		}
		l := gobgpLine{Kind: kind, Peer: h.PeerAddress.String(), PeerAS: h.PeerAS, Prefix: p.Prefix.String()}
		if addPath[f] {
			id := n.ID
			l.PathID = &id
		}
		return l, nil
	}
	withdraw := func(f gbgp.Family, ns []gbgp.PathNLRI) error {
		for _, n := range ns {
			l, err := route("W", f, n)
			if err != nil {
				return err
			}
			ls = append(ls, l.String())
		}
		return nil
	}
	announce := func(f gbgp.Family, ns []gbgp.PathNLRI) error {
		if len(ns) == 0 {
			return nil
		}
		tmpl, err := pathFields(a, f)
		if err != nil {
			return err
		}
		for _, n := range ns {
			k, err := route("A", f, n)
			if err != nil {
				return err
			}
			l := tmpl
			l.Kind, l.Peer, l.PeerAS, l.Prefix, l.PathID = k.Kind, k.Peer, k.PeerAS, k.Prefix, k.PathID
			ls = append(ls, l.String())
		}
		return nil
	}
	if err := withdraw(gbgp.RF_IPv4_UC, u.WithdrawnRoutes); err != nil {
		return nil, err
	}
	if err := announce(gbgp.RF_IPv4_UC, u.NLRI); err != nil {
		return nil, err
	}
	if m := a.unreach; m != nil {
		if f := gbgp.NewFamily(m.AFI, m.SAFI); prefixFamily(f) {
			if err := withdraw(f, m.Value); err != nil {
				return nil, err
			}
		}
	}
	if m := a.reach; m != nil {
		if f := gbgp.NewFamily(m.AFI, m.SAFI); prefixFamily(f) {
			if err := announce(f, m.Value); err != nil {
				return nil, err
			}
		}
	}
	return ls, nil
}

// pathFields is the attribute fields of an announcement of family f, as
// oracle.FromRoute fills them: the next hop is MP_REACH_NLRI's for its own
// family, else NEXT_HOP.
func pathFields(a gobgpAttrs, f gbgp.Family) (gobgpLine, error) {
	var l gobgpLine
	if a.asPath != nil {
		l.ASPath = renderASPath(a.asPath.Value)
	}
	if a.origin != nil {
		l.Origin = gobgpOrigins[a.origin.Value]
	}
	var nh netip.Addr
	if a.nextHop != nil {
		nh = a.nextHop.Value
	}
	if m := a.reach; m != nil && m.Nexthop.IsValid() && gbgp.NewFamily(m.AFI, m.SAFI) == f {
		nh = m.Nexthop
	}
	if nh.IsValid() {
		l.NextHop = nh.String()
	}
	if a.med != nil {
		l.MED = a.med.Value
	}
	if a.localPref != nil {
		l.LocalPref = a.localPref.Value
	}
	if a.comms != nil {
		for _, c := range a.comms.Value {
			l.Communities = append(l.Communities, fmt.Sprintf("%d:%d", c>>16, c&0xffff))
		}
	}
	if a.ext != nil {
		for _, c := range a.ext.Value {
			// gobgp's own encoding of what it decoded, labeled by mrt's
			// bgp.ExtCommunity.String: the one place mrt touches this side,
			// as a formatter of eight bytes.
			b, err := c.Serialize()
			if err != nil || len(b) != 8 {
				return gobgpLine{}, fmt.Errorf("extended community %v: %d bytes, %v", c, len(b), err)
			}
			l.Ext = append(l.Ext, bgp.ExtCommunity(b).String())
		}
	}
	if a.large != nil {
		for _, c := range a.large.Values {
			l.Large = append(l.Large, fmt.Sprintf("%d:%d:%d", c.ASN, c.LocalData1, c.LocalData2))
		}
	}
	if a.agg != nil {
		l.Aggregator = fmt.Sprintf("%d %s", a.agg.Value.AS, a.agg.Value.Address)
	}
	l.Atomic = a.atomic != nil
	return l, nil
}

// renderASPath renders AS_PATH segments as oracle's line does: a sequence's
// ASNs separated by spaces, a set as {a,b}, a confed sequence as (a b), a
// confed set as [a,b]; two sequences side by side separated by " ; ".
func renderASPath(segs []gbgp.AsPathParamInterface) string {
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			if s.GetType() == gbgp.BGP_ASPATH_ATTR_TYPE_SEQ && segs[i-1].GetType() == gbgp.BGP_ASPATH_ATTR_TYPE_SEQ {
				b.WriteString(" ; ")
			} else {
				b.WriteByte(' ')
			}
		}
		open, sep, close := "", " ", ""
		switch s.GetType() {
		case gbgp.BGP_ASPATH_ATTR_TYPE_SET:
			open, sep, close = "{", ",", "}"
		case gbgp.BGP_ASPATH_ATTR_TYPE_CONFED_SEQ:
			open, close = "(", ")"
		case gbgp.BGP_ASPATH_ATTR_TYPE_CONFED_SET:
			open, sep, close = "[", ",", "]"
		}
		b.WriteString(open)
		for j, a := range s.GetAS() {
			if j > 0 {
				b.WriteString(sep)
			}
			b.WriteString(strconv.FormatUint(uint64(a), 10))
		}
		b.WriteString(close)
	}
	return b.String()
}

// walkGobgpBMP decodes every message of streams with gobgp, one session per
// stream, and counts the routes renderUpdate would render (the measured
// pass: no rendering).
func walkGobgpBMP(streams [][][]byte) (routes, errs int) {
	for _, stream := range streams {
		s := newGobgpSession()
		for _, b := range stream {
			msg, err := s.decode(b)
			if err != nil {
				errs++
				continue
			}
			rm, ok := msg.Body.(*gbmp.BMPRouteMonitoring)
			if !ok || rm.BGPUpdate == nil {
				continue
			}
			u, ok := rm.BGPUpdate.Body.(*gbgp.BGPUpdate)
			if !ok {
				continue
			}
			routes += len(u.WithdrawnRoutes) + len(u.NLRI)
			for _, p := range u.PathAttributes {
				switch v := p.(type) {
				case *gbgp.PathAttributeMpReachNLRI:
					if prefixFamily(gbgp.NewFamily(v.AFI, v.SAFI)) {
						routes += len(v.Value)
					}
				case *gbgp.PathAttributeMpUnreachNLRI:
					if prefixFamily(gbgp.NewFamily(v.AFI, v.SAFI)) {
						routes += len(v.Value)
					}
				}
			}
		}
	}
	return routes, errs
}

// walkMrtBMP decodes every message of streams with bmp, one Session per
// stream, and counts the announcements and withdrawals and the errors.
func walkMrtBMP(streams [][][]byte) (routes, errs int) {
	for _, stream := range streams {
		s := bmp.NewSession(bmp.RoutesOptions{})
		for _, b := range stream {
			for r, err := range s.Routes(bmp.ParseMessage(b)) {
				switch {
				case err != nil:
					errs++
				case r.Kind == mrt.KindAnnounce || r.Kind == mrt.KindWithdraw:
					routes++
				}
			}
		}
	}
	return routes, errs
}
