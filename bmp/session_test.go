package bmp_test

import (
	"bytes"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/internal/bmpgen"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

// wantIDs checks each route's path ID: id for a route of a family in ids, none otherwise.
func wantIDs(t *testing.T, rs []mrt.Route, ids map[bgp.Family]uint32) {
	t.Helper()
	if len(rs) == 0 {
		t.Fatal("no routes")
	}
	for i, r := range rs {
		id, on := ids[r.Family]
		if r.HasPathID != on || r.PathID != id {
			t.Errorf("route %d (%v %v): path ID %d/%v, want %d/%v", i, r.Family, r.Prefix, r.PathID, r.HasPathID, id, on)
		}
		if n := hasDiag(r.Diagnostics, bmp.DiagAddPathUnknown); n != 0 {
			t.Errorf("route %d (%v): %d %s, want none", i, r.Prefix, n, bmp.DiagAddPathUnknown)
		}
	}
}

// wantUnknown checks that every route has no path ID and ends with exactly
// one DiagAddPathUnknown Warning at offset 0.
func wantUnknown(t *testing.T, rs []mrt.Route) {
	t.Helper()
	if len(rs) == 0 {
		t.Fatal("no routes")
	}
	for i, r := range rs {
		d := r.Diagnostics
		if r.HasPathID || hasDiag(d, bmp.DiagAddPathUnknown) != 1 {
			t.Fatalf("route %d (%v): path ID %v, diagnostics %+v; want none and one %s", i, r.Prefix, r.HasPathID, d, bmp.DiagAddPathUnknown)
		}
		last := d[len(d)-1]
		if last.Code != bmp.DiagAddPathUnknown || last.Severity != bgp.SeverityWarning || last.Offset != 0 || last.Attr != 0 {
			t.Errorf("route %d: last diagnostic %+v, want the %s Warning at offset 0", i, last, bmp.DiagAddPathUnknown)
		}
	}
}

func TestSessionAddPath(t *testing.T) {
	v4id, v6id := uint32(7), uint32(8)
	mixed := func(h bmp.PeerHeader) []byte {
		// The router receives IPv4 IDs and sends IPv6 ones; the peer the reverse.
		return peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1), ap(fam6, 3))),
			basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 2), ap(fam6, 1))))
	}
	both := func(h bmp.PeerHeader, flags uint8) [][]byte {
		g := at(h, flags, 2)
		in4 := flags&flagO == 0 // IPv4 IDs in the Adj-RIB-In views, IPv6 IDs in the Adj-RIB-Out views
		return [][]byte{
			rm(g, announce(g, "198.51.100.0/24", in4, v4id)),
			rm(g, announce(g, "2001:db8:1::/48", !in4, v6id)),
		}
	}
	t.Run("in", func(t *testing.T) {
		h := global4(1)
		wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, slices.Concat([][]byte{mixed(h)}, both(h, 0), both(h, flagL))...)),
			map[bgp.Family]uint32{fam4: v4id})
		// Both families both ways.
		all := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 3), ap(fam6, 3))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 3), ap(fam6, 3))))
		wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, all, rm(h, announce(h, "198.51.100.0/24", true, v4id)), rm(h, announce(h, "2001:db8:1::/48", true, v6id)))),
			map[bgp.Family]uint32{fam4: v4id, fam6: v6id})
	})
	t.Run("out", func(t *testing.T) {
		h := global4(2)
		wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, slices.Concat([][]byte{mixed(h)}, both(h, flagO), both(h, flagO|flagL))...)),
			map[bgp.Family]uint32{fam6: v6id})
	})
	t.Run("loc-rib-intent-ignored", func(t *testing.T) {
		for _, mode := range []uint8{0, 1, 2, 3, 7} {
			l := locRIB(9, "192.0.2.56")
			msgs := [][]byte{
				locUp(l, bmpgen.CapMP(fam4), bmpgen.CapAS4(routerAS), bmpgen.CapAddPath(ap(fam4, mode))),
				rm(l, announce(l, "198.51.100.0/24", true, v4id)),
			}
			wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, msgs...)), map[bgp.Family]uint32{fam4: v4id})
		}
		// Only the Mode's Warning is excused: any other damage still makes the Loc-RIB unknown.
		l := locRIB(9, "192.0.2.56")
		damaged := locUp(l, bmpgen.CapMP(fam4), bmpgen.Capability(1, []byte{0, 1, 1}), bmpgen.CapAS4(routerAS), bmpgen.CapAddPath(ap(fam4, 0)))
		wantUnknown(t, routesOf(t, feed(bmp.RoutesOptions{}, damaged, rm(l, announce(l, "198.51.100.0/24", false, 0)))))
	})
	t.Run("loc-rib-emulated-peers", func(t *testing.T) {
		l := locRIB(5, "192.0.2.160")
		up4 := locUp(l, bmpgen.CapMP(fam4), bmpgen.CapAS4(routerAS), bmpgen.CapAddPath(ap(fam4, 3)))
		up6 := locUp(l, bmpgen.CapMP(fam6), bmpgen.CapAS4(routerAS))
		a4 := rm(l, announce(l, "198.51.100.0/24", true, v4id))
		a6 := rm(l, announce(l, "2001:db8:1::/48", false, 0))
		// Before the IPv6 Peer Up, IPv6 is a family no Loc-RIB Peer Up has set.
		rs := routesOf(t, feed(bmp.RoutesOptions{}, up4, a4, a6))
		wantIDs(t, rs[:1], map[bgp.Family]uint32{fam4: v4id})
		wantUnknown(t, rs[1:])
		wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, up4, up6, a4, a6)), map[bgp.Family]uint32{fam4: v4id})
		// The IPv6 Peer Up leaves IPv4 alone in either order.
		wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, up6, up4, a4, a6)), map[bgp.Family]uint32{fam4: v4id})
		// A Peer Up with no Multiprotocol capability sets IPv4 unicast, here off.
		off4 := locUp(l, bmpgen.CapAS4(routerAS))
		wantIDs(t, routesOf(t, feed(bmp.RoutesOptions{}, up4, up6, off4, rm(l, announce(l, "198.51.100.0/24", false, 0)), a6)), nil)
		// The Peer Down removes the whole Loc-RIB, and so does a damaged Peer Up.
		down := bmpgen.PeerDown(l, uint8(bmp.PeerDownLocalTLV), nil)
		damaged := bmpgen.Message(bmp.TypePeerUp, append(bmpgen.PeerHeader(l), 0, 0, 0))
		for _, gone := range [][]byte{down, damaged} {
			wantUnknown(t, routesOf(t, feed(bmp.RoutesOptions{}, up4, up6, gone, rm(l, announce(l, "198.51.100.0/24", false, 0)), a6)))
		}
		// Another distinguisher or BGP ID is another Loc-RIB.
		for _, other := range []bmp.PeerHeader{locRIB(6, "192.0.2.160"), locRIB(5, "192.0.2.161")} {
			wantUnknown(t, routesOf(t, feed(bmp.RoutesOptions{}, up4, up6, rm(other, announce(other, "198.51.100.0/24", false, 0)))))
		}
	})
	t.Run("re-up", func(t *testing.T) {
		h := global4(3)
		on := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 2))))
		rs := routesOf(t, feed(bmp.RoutesOptions{}, on, rm(h, announce(h, "198.51.100.0/24", true, v4id)),
			plainUp(h), rm(h, announce(h, "198.51.100.0/24", false, 0)),
			on, rm(h, announce(h, "198.51.100.0/24", true, v4id+1))))
		if len(rs) != 3 {
			t.Fatalf("%d routes, want 3", len(rs))
		}
		wantIDs(t, rs[:1], map[bgp.Family]uint32{fam4: v4id})
		wantIDs(t, rs[1:2], nil)
		wantIDs(t, rs[2:], map[bgp.Family]uint32{fam4: v4id + 1})
	})
	t.Run("peer-down", func(t *testing.T) {
		h := global4(4)
		on := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 2))))
		rs := routesOf(t, feed(bmp.RoutesOptions{}, on, bmpgen.PeerDown(h, uint8(bmp.PeerDownRemoteNoData), nil),
			rm(h, announce(h, "198.51.100.0/24", false, 0))))
		wantUnknown(t, rs)
	})
}

func TestSessionUnknownState(t *testing.T) {
	h := at(global4(40), 0, 5)
	// One UPDATE with an IPv4 and an MP IPv6 route.
	update := mrtgen.Update(nil, []bgp.NLRI{nlri("198.51.100.0/24", false, 0)},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.ASPath(true, mrtgen.Seq(64501)), mrtgen.NextHop(nh4),
			mrtgen.MPReach(fam6, []netip.Addr{nh6}, []bgp.NLRI{nlri("2001:db8:1::/48", false, 0)}, false)), false)
	on := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 2))))
	badBody := bmpgen.Message(bmp.TypePeerUp, append(bmpgen.PeerHeader(h), 0, 0, 0, 0, 0))
	warnOpen := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))),
		[][]byte{bmpgen.Capability(1, []byte{0, 1, 1}), bmpgen.CapAS4(h.AS), bmpgen.CapAddPath(ap(fam4, 2))})
	badMode := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 7))))
	cases := []struct {
		name   string
		before [][]byte
	}{
		{"no-peer-up", nil},
		// Each damaged Peer Up replaces the good state that came before it.
		{"body-fails", [][]byte{on, badBody}},
		{"open-warning", [][]byte{on, warnOpen}},
		{"addpath-mode", [][]byte{on, badMode}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msgs := append(slices.Clone(c.before), rm(h, update))
			rs := routesOf(t, feed(bmp.RoutesOptions{}, msgs...))
			if len(rs) != 2 {
				t.Fatalf("%d routes, want 2", len(rs))
			}
			wantUnknown(t, rs)
			if &rs[0].Diagnostics[0] != &rs[1].Diagnostics[0] {
				t.Error("the routes of one message do not share their diagnostics")
			}

			// RequirePeerUp: one error, no route; a good Peer Up then lets routes through.
			got := feed(bmp.RoutesOptions{RequirePeerUp: true}, append(msgs, on, rm(h, announce(h, "198.51.100.0/24", true, 3)))...)
			var me *bmp.MessageError
			if len(got) != 2 || !errors.As(got[0].err, &me) || me.Offset != -1 || !errors.Is(got[0].err, bmp.ErrNoPeerUp) ||
				!reflect.DeepEqual(got[0].r, mrt.Route{}) || !bytes.Equal(me.Raw, rm(h, update)) {
				t.Fatalf("RequirePeerUp: %+v, want one ErrNoPeerUp at offset -1 and then a route", got)
			}
			wantIDs(t, routesOf(t, got[1:]), map[bgp.Family]uint32{fam4: 3})
		})
	}
}

func TestSessionEndOfRIBBeforePeerUp(t *testing.T) {
	h := at(global4(50), flagO, 9)
	l := at(locRIB(3, "192.0.2.51"), 0, 10)
	eors := []struct {
		h      bmp.PeerHeader
		fam    bgp.Family
		update []byte
	}{
		{h, fam4, mrtgen.Update(nil, nil, nil, false)},
		{h, fam6, mrtgen.Update(nil, nil, mrtgen.MPUnreach(fam6, nil, false), false)},
		{h, famLS, mrtgen.Update(nil, nil, mrtgen.MPUnreach(famLS, nil, false), false)},
		{l, fam6, mrtgen.Update(nil, nil, mrtgen.MPUnreach(fam6, nil, false), false)},
	}
	// Neither End-of-RIB nor an UPDATE with attributes and no NLRI.
	notEOR := [][]byte{
		rm(h, mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.ASPath(true, mrtgen.Seq(64501)), mrtgen.NextHop(nh4)), false)),
		rm(h, mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.MPUnreach(fam6, nil, false)), false)),
	}
	for _, o := range optionSets() {
		o.PeerStates = false
		for _, e := range eors {
			got := routesOf(t, feed(o, rm(e.h, e.update)))
			if !o.EndOfRIB {
				if len(got) != 0 {
					t.Errorf("%+v %v: %d routes without EndOfRIB, want none", o, e.fam, len(got))
				}
				continue
			}
			want := mrt.Route{Time: e.h.Time, Kind: mrt.KindEndOfRIB, Peer: e.h.Peer(), Family: e.fam, Router: router, View: e.h.View()}
			if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Errorf("%+v %v: %+v, want %+v", o, e.fam, got, want)
			}
		}
		if got := feed(o, notEOR...); len(got) != 0 {
			t.Errorf("%+v: an UPDATE with no NLRI yields %+v, want nothing", o, got)
		}
	}
	// A known peer's End-of-RIB is the same.
	got := routesOf(t, feed(bmp.RoutesOptions{EndOfRIB: true}, plainUp(h), rm(h, eors[1].update)))
	if len(got) != 1 || got[0].Kind != mrt.KindEndOfRIB || got[0].Family != fam6 || got[0].Diagnostics != nil {
		t.Errorf("known peer's End-of-RIB: %+v", got)
	}
}

func TestSessionPeerStates(t *testing.T) {
	h := at(peerHdr(mrt.PeerRD, flagO|flagL, 0x0001000000000007, "192.0.2.60", 64560, "192.0.2.60"), 0, 11)
	sent := bmpgen.Open(routerAS, routerID, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 7)))...)
	recv := bmpgen.Open(h.AS, h.BGPID, bmpgen.Capability(1, []byte{0, 1, 1}), bmpgen.CapAS4(h.AS))
	up := bmpgen.PeerUp(h, local4, 179, 40000, sent, recv)
	_, ds := bgp.ParseOpen(sent)
	_, dr := bgp.ParseOpen(recv)
	if len(ds) != 1 || len(dr) != 1 || ds[0].Message == dr[0].Message {
		t.Fatalf("the OPENs should have one distinct diagnostic each: %+v %+v", ds, dr)
	}
	ev := func(h bmp.PeerHeader, from, to mrt.State, diags []bgp.Diagnostic) mrt.Route {
		r := mrt.Route{Time: h.Time, Kind: mrt.KindPeerState, Peer: h.Peer(), Router: router, OldState: from, NewState: to, Diagnostics: diags}
		if h.Type == mrt.PeerLocRIB {
			r.View = mrt.ViewLocRIB
		}
		return r
	}
	opts := bmp.RoutesOptions{Router: router, PeerStates: true}
	check := func(name string, msg []byte, want mrt.Route) {
		t.Helper()
		got := routesOf(t, feed(opts, msg))
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("%s: %+v, want %+v", name, got, want)
		}
		if got := feed(bmp.RoutesOptions{Router: router}, msg); len(got) != 0 {
			t.Errorf("%s without PeerStates: %+v, want nothing", name, got)
		}
	}
	check("peer up", up, ev(h, 0, mrt.StateEstablished, append(slices.Clone(ds), dr...)))
	check("clean peer up", plainUp(h), ev(h, 0, mrt.StateEstablished, nil))

	bad := bmpgen.Message(bmp.TypePeerUp, append(bmpgen.PeerHeader(h), 0, 0, 0, 0, 0))
	got := routesOf(t, feed(opts, bad))
	if len(got) != 1 || len(got[0].Diagnostics) != 1 {
		t.Fatalf("peer up with a broken body: %+v", got)
	}
	if d := got[0].Diagnostics[0]; d.Code != bmp.DiagPeerUpBody || d.Severity != bgp.SeverityError || d.Offset != 0 || d.Attr != 0 ||
		!strings.Contains(d.Message, "local address") {
		t.Errorf("peer up with a broken body: diagnostic %+v", d)
	}
	got[0].Diagnostics = nil
	if !reflect.DeepEqual(got[0], ev(h, 0, mrt.StateEstablished, nil)) {
		t.Errorf("peer up with a broken body: %+v", got[0])
	}

	downs := map[string][]byte{
		"reason 0 (no reason byte)": bmpgen.Message(bmp.TypePeerDown, bmpgen.PeerHeader(h)),
		"reason 1":                  bmpgen.PeerDown(h, 1, append(bytes.Repeat([]byte{0xFF}, 16), 0, 21, 3, 6, 4)),
		"reason 2 short":            bmpgen.PeerDown(h, 2, []byte{5}),
		"reason 3 empty":            bmpgen.PeerDown(h, 3, nil),
		"reason 4":                  bmpgen.PeerDown(h, 4, nil),
		"reason 5":                  bmpgen.PeerDown(h, 5, nil),
		"reason 6":                  bmpgen.PeerDown(h, 6, bmpgen.TLV(bmp.TLVPeerDownTableName, []byte("x"))),
		"reason 99":                 bmpgen.PeerDown(h, 99, []byte{1, 2, 3}),
	}
	for name, m := range downs {
		check("peer down "+name, m, ev(h, mrt.StateEstablished, mrt.StateIdle, nil))
	}

	l := at(locRIB(4, "192.0.2.61"), 0x80, 12) // F set
	check("loc-rib peer up", locUp(l, bmpgen.CapMP(fam4), bmpgen.CapAS4(routerAS)), ev(l, 0, mrt.StateEstablished, nil))
	check("loc-rib peer down", bmpgen.PeerDown(l, 6, nil), ev(l, mrt.StateEstablished, mrt.StateIdle, nil))
}

func TestSessionErrors(t *testing.T) {
	h := at(global4(70), 0, 13)
	good := func(p string) []byte { return rm(h, announce(h, p, false, 0)) }
	brokenHeader := announce(h, "198.51.100.0/24", false, 0)
	brokenHeader[3] = 0 // the marker
	keepalive := append(bytes.Repeat([]byte{0xFF}, 16), 0, 19, 4)
	nine := peerHdr(9, 0, 0, "192.0.2.71", 64571, "192.0.2.71")
	noRoute := mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.Origin(bgp.OriginIGP)), false)
	bad := map[string]struct {
		msg  []byte
		text string
	}{
		"broken header": {rm(h, brokenHeader), "marker"},
		"keepalive":     {rm(h, keepalive), "not UPDATE"},
		"peer type 9":   {rm(nine, announce(nine, "198.51.100.0/24", false, 0)), "peer type 9"},
		"no route":      {rm(h, noRoute), bgp.DiagAttrDuplicate},
	}
	for name, b := range bad {
		for _, o := range optionSets() {
			got := feed(o, plainUp(h), b.msg, good("203.0.113.0/24"))
			if o.PeerStates {
				got = got[1:]
			}
			var me *bmp.MessageError
			if len(got) != 2 || !errors.As(got[0].err, &me) || me.Offset != -1 || !errors.Is(got[0].err, bmp.ErrMalformed) ||
				!bytes.Equal(me.Raw, b.msg) || me.Header.Type != bmp.TypeRouteMonitoring || !strings.Contains(me.Error(), b.text) {
				t.Fatalf("%s %+v: %+v, want one ErrMalformed at offset -1 naming %q, then the next message's route", name, o, got, b.text)
			}
			if got[1].err != nil || got[1].r.Prefix != netip.MustParsePrefix("203.0.113.0/24") {
				t.Errorf("%s: the next message gives %+v", name, got[1])
			}
		}
	}
}

func TestSessionTerminationKeepsState(t *testing.T) {
	h := at(global4(80), 0, 14)
	on := peerUp(h, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(h.AS, bmpgen.CapAddPath(ap(fam4, 2))))
	term := bmpgen.Termination(bmpgen.TLV(bmp.TLVTermReason, []byte{0, 1}))
	ini := bmpgen.Initiation(bmpgen.TLV(bmp.TLVInitSysName, []byte("r")))
	rs := routesOf(t, feed(bmp.RoutesOptions{PeerStates: true}, on, term, ini, rm(h, announce(h, "198.51.100.0/24", true, 4))))
	if len(rs) != 2 || rs[0].Kind != mrt.KindPeerState {
		t.Fatalf("%+v, want the Peer Up event and one route", rs)
	}
	wantIDs(t, rs[1:], map[bgp.Family]uint32{fam4: 4})
}

// interleavings returns every merge of a and b that keeps each one's order.
func interleavings(a, b [][]byte) [][][]byte {
	if len(a) == 0 {
		return [][][]byte{slices.Clone(b)}
	}
	if len(b) == 0 {
		return [][][]byte{slices.Clone(a)}
	}
	var out [][][]byte
	for _, rest := range interleavings(a[1:], b) {
		out = append(out, append([][]byte{a[0]}, rest...))
	}
	for _, rest := range interleavings(a, b[1:]) {
		out = append(out, append([][]byte{b[0]}, rest...))
	}
	return out
}

func TestSessionInterleavedPeers(t *testing.T) {
	x, y := at(global4(91), 0, 15), at(global4(92), flagO, 16)
	xs := [][]byte{
		peerUp(x, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam4, 1))), basicCaps(x.AS, bmpgen.CapAddPath(ap(fam4, 2)))),
		rm(x, announce(x, "198.51.100.0/24", true, 1)),
		rm(x, announce(x, "2001:db8:1::/48", false, 0)),
	}
	ys := [][]byte{
		peerUp(y, basicCaps(routerAS, bmpgen.CapAddPath(ap(fam6, 2))), basicCaps(y.AS, bmpgen.CapAddPath(ap(fam6, 1)))),
		rm(y, announce(y, "203.0.113.0/24", false, 0)),
		rm(y, announce(y, "2001:db8:2::/48", true, 2)),
	}
	orders := interleavings(xs, ys)
	if len(orders) != 20 {
		t.Fatalf("%d interleavings, want 20", len(orders))
	}
	byPeer := func(order [][]byte) map[netip.Addr][]mrt.Route {
		out := make(map[netip.Addr][]mrt.Route)
		for _, r := range routesOf(t, feed(bmp.RoutesOptions{PeerStates: true}, order...)) {
			out[r.Peer.Addr] = append(out[r.Peer.Addr], r)
		}
		return out
	}
	want := byPeer(orders[0])
	wantIDs(t, want[x.Addr][1:], map[bgp.Family]uint32{fam4: 1})
	wantIDs(t, want[y.Addr][1:], map[bgp.Family]uint32{fam6: 2})
	for i, o := range orders[1:] {
		if got := byPeer(o); !reflect.DeepEqual(got, want) {
			t.Errorf("interleaving %d gives other routes per peer", i+1)
		}
	}
}

func TestSessionFamilyNotFromVFlag(t *testing.T) {
	v6 := at(peerHdr(mrt.PeerGlobal, flagV, 0, "2001:db8::93", 64593, "192.0.2.93"), 0, 17)
	v4 := at(global4(94), 0, 18)
	for _, c := range []struct {
		h   bmp.PeerHeader
		p   string
		fam bgp.Family
	}{
		{v6, "198.51.100.0/24", fam4},
		{v4, "2001:db8:1::/48", fam6},
	} {
		rs := routesOf(t, feed(bmp.RoutesOptions{}, plainUp(c.h), rm(c.h, announce(c.h, c.p, false, 0))))
		if len(rs) != 1 || rs[0].Family != c.fam || rs[0].Prefix != netip.MustParsePrefix(c.p) {
			t.Errorf("peer %v announcing %s: %+v, want family %v", c.h.Addr, c.p, rs, c.fam)
		}
	}
}
