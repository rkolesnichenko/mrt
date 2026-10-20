package mrt_test

import (
	"net/netip"
	"os"
	"reflect"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

func openFixture(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestRoutesPeerStates(t *testing.T) {
	const path = "testdata/synthetic/state-changes.mrt"
	records := func() func(func(mrt.Record, error) bool) {
		r, err := mrt.Decompress(openFixture(t, path))
		if err != nil {
			t.Fatal(err)
		}
		return mrt.Records(r)
	}
	peerA := mrt.Peer{Addr: netip.MustParseAddr("192.0.2.10"), AS: 64500}
	peerB := mrt.Peer{Addr: netip.MustParseAddr("2001:db8::11"), AS: 4200000001}
	type ev struct {
		kind     mrt.Kind
		peer     mrt.Peer
		old, new mrt.State
		micro    int
	}
	want := []ev{
		{mrt.KindPeerState, peerA, mrt.StateIdle, mrt.StateConnect, 0},
		{mrt.KindPeerState, peerA, mrt.StateConnect, mrt.StateOpenSent, 0},
		{mrt.KindPeerState, peerA, mrt.StateOpenSent, mrt.StateOpenConfirm, 0},
		{mrt.KindPeerState, peerA, mrt.StateOpenConfirm, mrt.StateEstablished, 0},
		{mrt.KindPeerState, peerB, mrt.StateActive, mrt.StateEstablished, 0},
		{mrt.KindAnnounce, peerA, 0, 0, 0},
		{mrt.KindPeerState, peerB, mrt.StateEstablished, mrt.StateIdle, 250},
		{mrt.KindPeerState, peerA, mrt.StateEstablished, mrt.StateIdle, 250},
	}
	var got []mrt.Route
	for rt, err := range mrt.RoutesWith(records(), mrt.RoutesOptions{PeerStates: true}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rt)
	}
	if len(got) != len(want) {
		t.Fatalf("%d routes, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Kind != w.kind || g.Peer.Addr != w.peer.Addr || g.Peer.AS != w.peer.AS {
			t.Errorf("%d: %v peer %+v, want %v %+v", i, g.Kind, g.Peer, w.kind, w.peer)
		}
		if g.Time.Nanosecond() != w.micro*1000 {
			t.Errorf("%d: time %v, want %d µs", i, g.Time, w.micro)
		}
		if w.kind != mrt.KindPeerState {
			continue
		}
		if g.OldState != w.old || g.NewState != w.new {
			t.Errorf("%d: states %d->%d, want %d->%d", i, g.OldState, g.NewState, w.old, w.new)
		}
		if g.Prefix.IsValid() || g.Family != (bgp.Family{}) || g.PathID != 0 || g.HasPathID || g.Path != nil || g.Diagnostics != nil || !g.Originated.IsZero() {
			t.Errorf("%d: event carries route fields: %+v", i, g)
		}
	}

	// Without the option only the announcement remains.
	var only []mrt.Route
	for rt, err := range mrt.Routes(records()) {
		if err != nil {
			t.Fatal(err)
		}
		only = append(only, rt)
	}
	if len(only) != 1 || only[0].Kind != mrt.KindAnnounce || !reflect.DeepEqual(only[0], got[5]) {
		t.Errorf("Routes: %+v", only)
	}
}

type routeOrErr struct {
	r   mrt.Route
	err string
}

func collect(seq func(func(mrt.Route, error) bool)) []routeOrErr {
	var out []routeOrErr
	for r, err := range seq {
		e := ""
		if err != nil {
			e = err.Error()
		}
		out = append(out, routeOrErr{r, e})
	}
	return out
}

func TestRoutesInternerSameRoutes(t *testing.T) {
	in := bgp.NewInterner()
	for _, f := range fixtures(t) {
		open := func() func(func(mrt.Record, error) bool) {
			r, err := mrt.Decompress(openFixture(t, f.path))
			if err != nil {
				t.Fatal(err)
			}
			return mrt.Records(r)
		}
		want := collect(mrt.Routes(open()))
		got := collect(mrt.RoutesWith(open(), mrt.RoutesOptions{Interner: in}))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: interner routes differ (%d vs %d)", f.name, len(got), len(want))
		}
	}
}
