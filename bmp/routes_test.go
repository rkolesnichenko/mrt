package bmp_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

// Every input of these tests is built by bmpgen (and mrtgen for the UPDATEs).

var (
	fam4  = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}
	fam6  = bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}
	famLS = bgp.Family{AFI: 25, SAFI: 70} // a family without prefixes

	router   = netip.MustParseAddr("198.18.0.1") // RoutesOptions.Router
	routerID = netip.MustParseAddr("192.0.2.1")
	local4   = netip.MustParseAddr("192.0.2.1")
	local6   = netip.MustParseAddr("2001:db8::1")
	nh4      = netip.MustParseAddr("192.0.2.254")
	nh6      = netip.MustParseAddr("2001:db8::fe")

	epoch = time.Unix(1700000000, 0).UTC()
)

const routerAS = 65000

// Peer header flags.
const (
	flagV = 0x80
	flagL = 0x40
	flagA = 0x20
	flagO = 0x10
)

// peerHdr is a per-peer header; addr "" is none (Loc-RIB). Its time is epoch.
func peerHdr(typ mrt.PeerType, flags uint8, dist uint64, addr string, as bgp.ASN, id string) bmp.PeerHeader {
	h := bmp.PeerHeader{Type: typ, Flags: flags, Distinguisher: dist, AS: as, BGPID: netip.MustParseAddr(id), Time: epoch}
	if addr != "" {
		h.Addr = netip.MustParseAddr(addr)
	}
	return h
}

// global4 is a global-instance IPv4 peer 192.0.2.n of AS 64500+n.
func global4(n int) bmp.PeerHeader {
	a := fmt.Sprintf("192.0.2.%d", n)
	return peerHdr(mrt.PeerGlobal, 0, 0, a, bgp.ASN(64500+n), a)
}

// locRIB is a Loc-RIB instance peer with distinguisher dist and BGP ID id.
func locRIB(dist uint64, id string) bmp.PeerHeader {
	return peerHdr(mrt.PeerLocRIB, 0, dist, "", routerAS, id)
}

// at is h with flags set and its time sec seconds after epoch.
func at(h bmp.PeerHeader, flags uint8, sec int) bmp.PeerHeader {
	h.Flags |= flags
	h.Time = epoch.Add(time.Duration(sec) * time.Second)
	return h
}

func ap(f bgp.Family, mode uint8) bgp.AddPathFamily { return bgp.AddPathFamily{Family: f, Mode: mode} }

// basicCaps are Multiprotocol IPv4 and IPv6 unicast and the AS capability.
func basicCaps(as bgp.ASN, more ...[]byte) [][]byte {
	return append([][]byte{bmpgen.CapMP(fam4), bmpgen.CapMP(fam6), bmpgen.CapAS4(as)}, more...)
}

// peerUp is a Peer Up for h: the router's OPEN has sent, the peer's recv.
func peerUp(h bmp.PeerHeader, sent, recv [][]byte) []byte {
	local := local4
	if h.Addr.Is6() {
		local = local6
	} else if !h.Addr.IsValid() {
		local = netip.Addr{}
	}
	return bmpgen.PeerUp(h, local, 179, 40000, bmpgen.Open(routerAS, routerID, sent...), bmpgen.Open(h.AS, h.BGPID, recv...))
}

// plainUp is a Peer Up with the basic capabilities and no ADD-PATH.
func plainUp(h bmp.PeerHeader) []byte { return peerUp(h, basicCaps(routerAS), basicCaps(h.AS)) }

// locUp is a Loc-RIB Peer Up whose two (equal) OPENs carry caps.
func locUp(h bmp.PeerHeader, caps ...[]byte) []byte {
	o := bmpgen.Open(routerAS, h.BGPID, caps...)
	return bmpgen.PeerUp(h, netip.Addr{}, 0, 0, o, o)
}

// as4Of is the AS width h says its PDUs use.
func as4Of(h bmp.PeerHeader) bool {
	as4, known := h.AS4()
	return as4 || !known
}

func nlri(p string, withID bool, id uint32) bgp.NLRI {
	return bgp.NLRI{Prefix: netip.MustParsePrefix(p), PathID: id, HasPathID: withID}
}

// announce is an UPDATE from h announcing p (IPv4 in the NLRI field, IPv6 in
// MP_REACH), with path ID id when withID.
func announce(h bmp.PeerHeader, p string, withID bool, id uint32) []byte {
	n := []bgp.NLRI{nlri(p, withID, id)}
	path := mrtgen.ASPath(as4Of(h), mrtgen.Seq(64501, 64502))
	if netip.MustParsePrefix(p).Addr().Is6() {
		return mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), path,
			mrtgen.MPReach(fam6, []netip.Addr{nh6}, n, withID)), false)
	}
	return mrtgen.Update(nil, n, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), path, mrtgen.NextHop(nh4)), withID)
}

func rm(h bmp.PeerHeader, update []byte) []byte { return bmpgen.RouteMonitoring(h, update) }

type result struct {
	r   mrt.Route
	err error
}

// feed passes each message, through ParseMessage, to one Session.
func feed(opts bmp.RoutesOptions, msgs ...[]byte) []result {
	s := bmp.NewSession(opts)
	var out []result
	for _, b := range msgs {
		for r, err := range s.Routes(bmp.ParseMessage(b)) {
			out = append(out, result{r, err})
		}
	}
	return out
}

// routesOf fails on any error and returns the routes.
func routesOf(t *testing.T, rs []result) []mrt.Route {
	t.Helper()
	var out []mrt.Route
	for _, r := range rs {
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
		out = append(out, r.r)
	}
	return out
}

// hasDiag counts the diagnostics with code c.
func hasDiag(ds []bgp.Diagnostic, c string) int {
	n := 0
	for _, d := range ds {
		if d.Code == c {
			n++
		}
	}
	return n
}

// mrtRoutes decodes update as the body of an MRT BGP4MP message of subtype st from h's peer.
func mrtRoutes(t *testing.T, st mrt.Subtype, h bmp.PeerHeader, update []byte) []mrt.Route {
	t.Helper()
	rec := mrtgen.Header(mrt.TypeBGP4MP, st, epoch, mrtgen.BGP4MP(st, h.AS, routerAS, h.Addr, local4, update))
	var out []mrt.Route
	for r, err := range mrt.Routes(mrt.Records(bytes.NewReader(rec))) {
		if err != nil {
			t.Fatalf("mrt: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// sameAsMRT checks that the UPDATE fields of got are those of want.
func sameAsMRT(t *testing.T, got, want []mrt.Route) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d routes, mrt gives %d", len(got), len(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Kind != w.Kind || g.Family != w.Family || g.Prefix != w.Prefix || g.PathID != w.PathID || g.HasPathID != w.HasPathID {
			t.Errorf("route %d: %v %v %v %d/%v, mrt %v %v %v %d/%v", i, g.Kind, g.Family, g.Prefix, g.PathID, g.HasPathID,
				w.Kind, w.Family, w.Prefix, w.PathID, w.HasPathID)
		}
		if !reflect.DeepEqual(g.Path, w.Path) {
			t.Errorf("route %d: path differs from mrt's:\n%+v\n%+v", i, g.Path, w.Path)
		}
		if !reflect.DeepEqual(g.Diagnostics, w.Diagnostics) {
			t.Errorf("route %d: diagnostics %+v, mrt %+v", i, g.Diagnostics, w.Diagnostics)
		}
	}
}

func TestRoutesAnnounceWithdraw(t *testing.T) {
	h := at(global4(10), flagL, 7)
	update := mrtgen.Update(
		[]bgp.NLRI{nlri("198.51.100.0/24", false, 0)},
		[]bgp.NLRI{nlri("203.0.113.0/24", false, 0), nlri("203.0.114.0/24", false, 0)},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.Origin(bgp.OriginEGP), // a duplicate: a Warning inside the block
			mrtgen.ASPath(true, mrtgen.Seq(64510, 64511)), mrtgen.NextHop(nh4),
			mrtgen.MPUnreach(fam6, []bgp.NLRI{nlri("2001:db8:1::/48", false, 0)}, false),
			mrtgen.MPReach(fam6, []netip.Addr{nh6}, []bgp.NLRI{nlri("2001:db8:2::/48", false, 0), nlri("2001:db8:3::/48", false, 0)}, false)),
		false)
	got := routesOf(t, feed(bmp.RoutesOptions{Router: router}, plainUp(at(global4(10), 0, 1)), rm(h, update)))

	want := []struct {
		kind   mrt.Kind
		fam    bgp.Family
		prefix string
	}{
		{mrt.KindWithdraw, fam4, "198.51.100.0/24"},
		{mrt.KindWithdraw, fam6, "2001:db8:1::/48"},
		{mrt.KindAnnounce, fam4, "203.0.113.0/24"},
		{mrt.KindAnnounce, fam4, "203.0.114.0/24"},
		{mrt.KindAnnounce, fam6, "2001:db8:2::/48"},
		{mrt.KindAnnounce, fam6, "2001:db8:3::/48"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d routes, want %d", len(got), len(want))
	}
	var path *bgp.Path
	for i, w := range want {
		r := got[i]
		if r.Kind != w.kind || r.Family != w.fam || r.Prefix != netip.MustParsePrefix(w.prefix) || r.HasPathID {
			t.Errorf("route %d: %v %v %v (path ID %v), want %v %v %v", i, r.Kind, r.Family, r.Prefix, r.HasPathID, w.kind, w.fam, w.prefix)
		}
		if r.Router != router || r.View != mrt.ViewAdjRIBInPost || r.Peer != h.Peer() || !r.Time.Equal(h.Time) {
			t.Errorf("route %d: router %v view %v peer %+v time %v", i, r.Router, r.View, r.Peer, r.Time)
		}
		switch {
		case r.Kind == mrt.KindWithdraw && r.Path != nil:
			t.Errorf("withdrawal %d has a path", i)
		case r.Kind == mrt.KindAnnounce && path == nil:
			path = r.Path
		case r.Kind == mrt.KindAnnounce && r.Path != path:
			t.Errorf("announcement %d does not share the first one's *Path", i)
		}
		if len(r.Diagnostics) == 0 || &r.Diagnostics[0] != &got[0].Diagnostics[0] {
			t.Errorf("route %d does not share the message's diagnostics", i)
		}
	}
	// The duplicate ORIGIN's Warning is rebased onto the BGP message: past
	// the header, the withdrawn routes and the two length fields.
	d := got[0].Diagnostics
	if hasDiag(d, bgp.DiagAttrDuplicate) != 1 || d[0].Offset < 19+2+4+2 || hasDiag(d, bmp.DiagAddPathUnknown) != 0 {
		t.Errorf("diagnostics %+v, want one rebased %s and no %s", d, bgp.DiagAttrDuplicate, bmp.DiagAddPathUnknown)
	}
	sameAsMRT(t, got, mrtRoutes(t, mrt.SubMessageAS4, h, update))
}

func TestRoutesAS2(t *testing.T) {
	trans := bgp.ASN(bgp.ASTrans)
	agg := netip.MustParseAddr("192.0.2.71")
	h := at(peerHdr(mrt.PeerGlobal, flagA, 0, "192.0.2.70", 196618, "192.0.2.70"), 0, 3)
	update := mrtgen.Update(nil, []bgp.NLRI{nlri("198.51.100.0/24", false, 0)},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP),
			mrtgen.ASPath(false, mrtgen.Seq(64500, trans, trans)),
			mrtgen.NextHop(nh4),
			mrtgen.AggregatorAttr(false, trans, agg),
			mrtgen.AS4PathAttr(mrtgen.Seq(196618, 4200000001)),
			mrtgen.AS4AggregatorAttr(196618, agg)), false)
	got := routesOf(t, feed(bmp.RoutesOptions{}, plainUp(h), rm(h, update)))
	if len(got) != 1 {
		t.Fatalf("%d routes, want 1", len(got))
	}
	u, _ := bgp.ParseUpdate(update, bgp.Context{AS4: false})
	want, _ := u.Attrs.Decode()
	if !reflect.DeepEqual(*got[0].Path, want) {
		t.Errorf("path %+v, want %+v", *got[0].Path, want)
	}
	if s := got[0].Path.ASPath.String(); s != "64500 196618 4200000001" {
		t.Errorf("AS path %q, want the AS4_PATH merge", s)
	}
	if a, ok := got[0].Path.Aggregator(); !ok || a.AS != 196618 {
		t.Errorf("aggregator %v %v, want AS4_AGGREGATOR's", a, ok)
	}
	sameAsMRT(t, got, mrtRoutes(t, mrt.SubMessage, h, update))
}

// optionSets is every combination of PeerStates, EndOfRIB and RequirePeerUp.
func optionSets() []bmp.RoutesOptions {
	var out []bmp.RoutesOptions
	for i := range 8 {
		out = append(out, bmp.RoutesOptions{Router: router, PeerStates: i&1 != 0, EndOfRIB: i&2 != 0, RequirePeerUp: i&4 != 0})
	}
	return out
}

// fixtures returns every testdata/bmp file's bytes by name.
func fixtures(t *testing.T) map[string][]byte {
	t.Helper()
	names, err := filepath.Glob("../testdata/bmp/*.bmp")
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	out := make(map[string][]byte)
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(n)] = b
	}
	return out
}

// outcome is a route or an error's text: what two runs must agree on.
type outcome struct {
	r   mrt.Route
	err string
}

func collect(seq func(func(mrt.Route, error) bool)) []outcome {
	var out []outcome
	for r, err := range seq {
		o := outcome{r: r}
		if err != nil {
			o.err = err.Error()
		}
		out = append(out, o)
	}
	return out
}

func TestBMPRoutesInternerSameRoutes(t *testing.T) {
	routes := 0
	for name, data := range fixtures(t) {
		for _, o := range optionSets() {
			want := collect(bmp.RoutesWith(bmp.Messages(bytes.NewReader(data)), o))
			o.Interner = bgp.NewInterner()
			got := collect(bmp.RoutesWith(bmp.Messages(bytes.NewReader(data)), o))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %+v: routes with an interner differ from those without", name, o)
			}
			for _, w := range want {
				if w.r.Path != nil {
					routes++
				}
			}
		}
	}
	if routes == 0 {
		t.Fatal("no fixture yielded an announcement")
	}
}

func TestSessionInternerSurvivesBufferReuse(t *testing.T) {
	h := at(global4(20), 0, 1)
	buf := rm(h, mrtgen.Update(nil, []bgp.NLRI{nlri("198.51.100.0/24", false, 0)},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.ASPath(true, mrtgen.Seq(64501, 64502)), mrtgen.NextHop(nh4),
			mrtgen.Communities(0xFDE80001, 0xFDE80002),
			mrtgen.Raw(mrtgen.FlagOptional|mrtgen.FlagTransitive, 99, []byte{1, 2, 3}),
			mrtgen.MPReach(fam6, []netip.Addr{nh6}, []bgp.NLRI{nlri("2001:db8:2::/48", false, 0)}, false)), false))
	saved := slices.Clone(buf)
	s := bmp.NewSession(bmp.RoutesOptions{Interner: bgp.NewInterner()})
	for range s.Routes(bmp.ParseMessage(plainUp(h))) {
	}
	var got []mrt.Route
	for r, err := range s.Routes(bmp.ParseMessage(buf)) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	for i := range buf {
		buf[i] = 0xEE
	}
	u, _ := bgp.ParseUpdate(saved[6+42:], bgp.Context{AS4: true})
	want, _ := u.Attrs.Decode()
	if len(got) != 2 || len(want.Unknown()) != 1 {
		t.Fatalf("%d routes and %d unknown attributes, want 2 and 1", len(got), len(want.Unknown()))
	}
	for i, r := range got {
		if !reflect.DeepEqual(*r.Path, want) {
			t.Errorf("route %d: after the buffer is overwritten the path is %+v, want %+v", i, *r.Path, want)
		}
	}
}

func TestRoutesWithMatchesSession(t *testing.T) {
	for name, data := range fixtures(t) {
		for _, o := range optionSets() {
			want := collect(bmp.RoutesWith(bmp.Messages(bytes.NewReader(data)), o))
			s := bmp.NewSession(o)
			var got []outcome
			for m, err := range bmp.Messages(bytes.NewReader(data)) {
				got = append(got, collect(s.Routes(m, err))...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %+v: Session.Routes per message differs from RoutesWith", name, o)
			}
		}
		if !reflect.DeepEqual(collect(bmp.Routes(bmp.Messages(bytes.NewReader(data)))),
			collect(bmp.RoutesWith(bmp.Messages(bytes.NewReader(data)), bmp.RoutesOptions{}))) {
			t.Errorf("%s: Routes differs from RoutesWith with no options", name)
		}
	}

	// Ranging again after a break goes on with the next message; the routes
	// left in the broken message are not yielded, and the session persists.
	h := at(global4(30), 0, 1)
	three := mrtgen.Update(nil, []bgp.NLRI{nlri("198.51.100.0/24", true, 1), nlri("198.51.101.0/24", true, 2), nlri("198.51.102.0/24", true, 3)},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.ASPath(true, mrtgen.Seq(64501)), mrtgen.NextHop(nh4)), true)
	stream := slices.Concat(
		peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 2)))),
		rm(h, three), rm(h, announce(h, "203.0.113.0/24", true, 9)))
	seq := bmp.RoutesWith(bmp.Messages(bytes.NewReader(stream)), bmp.RoutesOptions{})
	var first, rest []mrt.Route
	for r, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, r)
		break
	}
	for r, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		rest = append(rest, r)
	}
	if len(first) != 1 || first[0].PathID != 1 || len(rest) != 1 ||
		rest[0].Prefix != netip.MustParsePrefix("203.0.113.0/24") || !rest[0].HasPathID || rest[0].PathID != 9 {
		t.Errorf("break and resume: first %+v, then %+v", first, rest)
	}
}

func TestSessionErrorPassThrough(t *testing.T) {
	for _, e := range []error{errors.New("a reader error"), &bmp.MessageError{Offset: 7, Err: bmp.ErrMalformed}} {
		var got []result
		for r, err := range bmp.NewSession(bmp.RoutesOptions{PeerStates: true, EndOfRIB: true}).Routes(nil, e) {
			got = append(got, result{r, err})
		}
		if len(got) != 1 || got[0].err != e || !reflect.DeepEqual(got[0].r, mrt.Route{}) {
			t.Errorf("Routes(nil, %v) yields %+v, want the error once and unchanged", e, got)
		}
	}
}
