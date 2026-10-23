package bmp

import (
	"bytes"
	"errors"
	"iter"
	"net/netip"
	"reflect"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// walk collects a TLV walk: the TLVs before the first error, and the errors.
func walk[T any](seq iter.Seq2[T, error]) ([]T, []error) {
	var vs []T
	var errs []error
	for v, err := range seq {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		vs = append(vs, v)
	}
	return vs, errs
}

func mustParse[M Message](t *testing.T, b []byte) M {
	t.Helper()
	m, err := ParseMessage(b)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	mm, ok := m.(M)
	if !ok {
		t.Fatalf("%T, want %T", m, mm)
	}
	return mm
}

func wantTLVs(t *testing.T, got []TLV, want ...TLV) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d TLVs %v, want %d", len(got), got, len(want))
	}
	for i := range got {
		if got[i].Type != want[i].Type || !bytes.Equal(got[i].Value, want[i].Value) {
			t.Errorf("TLV %d: %d % x, want %d % x", i, got[i].Type, got[i].Value, want[i].Type, want[i].Value)
		}
		if cap(got[i].Value) != len(got[i].Value) {
			t.Errorf("TLV %d: cap %d, len %d", i, cap(got[i].Value), len(got[i].Value))
		}
	}
}

func wantMalformed(t *testing.T, errs []error) {
	t.Helper()
	if len(errs) != 1 || !errors.Is(errs[0], ErrMalformed) {
		t.Errorf("errors %v, want one wrapping ErrMalformed", errs)
	}
}

func TestPeerUpBody(t *testing.T) {
	sent := openMsg(capParam(capMP, capMP6, capAS4))
	recv := openMsg(capParam(capMP, capAS4))
	local := make([]byte, 16)
	copy(local[12:], []byte{198, 51, 100, 1})
	info := cat(tlv(0, []byte("hello")...), tlv(3, []byte("global")...), tlv(4, []byte("edge")...))
	ports := cat(u16(50000), u16(179))
	peerUp := func(p testPeer, data ...[]byte) []byte { return bmpMsg(3, p.bytes(), cat(data...)) }

	t.Run("two OPENs and TLVs", func(t *testing.T) {
		b := peerUp(defaultPeer(), local, ports, sent, recv, info)
		u := mustParse[PeerUp](t, b)
		body, err := u.Body()
		if err != nil {
			t.Fatalf("Body: %v", err)
		}
		if body.LocalAddr != netip.MustParseAddr("198.51.100.1") || body.LocalPort != 50000 || body.RemotePort != 179 {
			t.Errorf("LocalAddr %v LocalPort %d RemotePort %d", body.LocalAddr, body.LocalPort, body.RemotePort)
		}
		if !bytes.Equal(body.SentOpen, sent) || !bytes.Equal(body.ReceivedOpen, recv) {
			t.Errorf("SentOpen % x\nReceivedOpen % x", body.SentOpen, body.ReceivedOpen)
		}
		if cap(body.SentOpen) != len(body.SentOpen) || cap(body.ReceivedOpen) != len(body.ReceivedOpen) || &body.SentOpen[0] != &b[68] {
			t.Error("the OPENs are not slices of Raw with cap == len")
		}
		if o, d := bgp.ParseOpen(body.SentOpen); len(d) != 0 || o.AS() != 4200000000 {
			t.Errorf("ParseOpen(SentOpen): AS %d, diagnostics %v", o.AS(), d)
		}
		tlvs, errs := walk(body.Info())
		if len(errs) != 0 {
			t.Errorf("Info errors %v", errs)
		}
		wantTLVs(t, tlvs, TLV{0, []byte("hello")}, TLV{TLVPeerUpTableName, []byte("global")}, TLV{TLVPeerUpAdminLabel, []byte("edge")})
		if name, ok := body.TableName(); name != "global" || !ok {
			t.Errorf("TableName (%q, %v)", name, ok)
		}
	})
	t.Run("IPv6 local address", func(t *testing.T) {
		p := defaultPeer()
		p.flags = 0x80
		l6 := netip.MustParseAddr("2001:db8::1").As16()
		body, err := mustParse[PeerUp](t, peerUp(p, l6[:], ports, sent, recv)).Body()
		if err != nil || body.LocalAddr != netip.MustParseAddr("2001:db8::1") {
			t.Errorf("LocalAddr %v, err %v", body.LocalAddr, err)
		}
		if _, ok := body.TableName(); ok {
			t.Error("TableName ok without TLVs")
		}
	})
	t.Run("Loc-RIB local address", func(t *testing.T) {
		p := defaultPeer()
		p.typ = 3
		body, err := mustParse[PeerUp](t, peerUp(p, local, ports, sent, sent)).Body()
		if err != nil || body.LocalAddr != (netip.Addr{}) {
			t.Errorf("LocalAddr %v, err %v", body.LocalAddr, err)
		}
	})
	t.Run("second OPEN past the message", func(t *testing.T) {
		u := mustParse[PeerUp](t, peerUp(defaultPeer(), local, ports, sent, recv[:len(recv)-3]))
		if _, err := u.Body(); !errors.Is(err, ErrMalformed) {
			t.Errorf("Body error %v, want ErrMalformed", err)
		}
	})
	t.Run("broken OPEN headers", func(t *testing.T) {
		badMarker := bytes.Clone(sent)
		badMarker[0] = 0
		for _, data := range [][]byte{
			cat(local, ports[:3]),                   // no room for the ports
			cat(local, ports),                       // no OPEN at all
			cat(local, ports, badMarker, recv),      // bad marker
			cat(local, ports, sent, recv[:18]),      // second OPEN shorter than a header
			cat(local, ports, sent[:len(sent)-1]),   // first OPEN past the message
			cat(local, ports, bgpMsg(1)[:17], recv), // first OPEN's header cut
		} {
			if _, err := mustParse[PeerUp](t, peerUp(defaultPeer(), data)).Body(); !errors.Is(err, ErrMalformed) {
				t.Errorf("data % x: Body error %v, want ErrMalformed", data, err)
			}
		}
	})
	t.Run("TLV cut short", func(t *testing.T) {
		cut := cat(tlv(0, []byte("a")...), tlv(3, []byte("global")...)[:7])
		body, err := mustParse[PeerUp](t, peerUp(defaultPeer(), local, ports, sent, recv, cut)).Body()
		if err != nil {
			t.Fatalf("Body: %v", err)
		}
		tlvs, errs := walk(body.Info())
		wantTLVs(t, tlvs, TLV{0, []byte("a")})
		wantMalformed(t, errs)
		if _, ok := body.TableName(); ok {
			t.Error("TableName ok from a cut TLV")
		}
	})
}

func TestPeerDownReasons(t *testing.T) {
	notif := bgpMsg(3, 6, 2) // Cease, administrative shutdown
	down := func(data ...[]byte) PeerDown {
		return mustParse[PeerDown](t, bmpMsg(2, defaultPeer().bytes(), cat(data...)))
	}
	for _, c := range []struct {
		name   string
		d      PeerDown
		notif  []byte // nil: not ok
		fsm    uint16
		fsmOK  bool
		info   []TLV
		reason PeerDownReason
	}{
		{"1 local notification", down([]byte{1}, notif), notif, 0, false, nil, PeerDownLocalNotification},
		{"2 local FSM event", down([]byte{2}, u16(5)), nil, 5, true, nil, PeerDownLocalFSMEvent},
		{"2 with 1 byte", down([]byte{2, 5}), nil, 0, false, nil, PeerDownLocalFSMEvent},
		{"3 remote notification", down([]byte{3}, notif), notif, 0, false, nil, PeerDownRemoteNotification},
		{"4 remote no data", down([]byte{4}), nil, 0, false, nil, PeerDownRemoteNoData},
		{"5 deconfigured", down([]byte{5}), nil, 0, false, nil, PeerDownDeconfigured},
		{"6 Loc-RIB TLVs", down([]byte{6}, tlv(TLVPeerDownTableName, []byte("vrf-a")...)), nil, 0, false, []TLV{{TLVPeerDownTableName, []byte("vrf-a")}}, PeerDownLocalTLV},
		{"no reason", down(), nil, 0, false, nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.d.Reason != c.reason {
				t.Errorf("Reason %d, want %d", c.d.Reason, c.reason)
			}
			n, ok := c.d.Notification()
			if ok != (c.notif != nil) || !bytes.Equal(n, c.notif) {
				t.Errorf("Notification (% x, %v)", n, ok)
			}
			if ok && (cap(n) != len(n) || &n[0] != &c.d.Raw()[49]) {
				t.Error("Notification is not a slice of Raw with cap == len")
			}
			if ev, ok := c.d.FSMEvent(); ev != c.fsm || ok != c.fsmOK {
				t.Errorf("FSMEvent (%d, %v), want (%d, %v)", ev, ok, c.fsm, c.fsmOK)
			}
			tlvs, errs := walk(c.d.Info())
			if len(errs) != 0 {
				t.Errorf("Info errors %v", errs)
			}
			wantTLVs(t, tlvs, c.info...)
		})
	}
	if d := down(); d.Data != nil {
		t.Errorf("no reason: Data % x, want nil", d.Data)
	}
}

// statShapeOf is the registered shape of each statistics type, as the test
// expects it (spec §5.1 and §10.4): 'c' counter, 'g' gauge, 'f' per-AFI/SAFI
// gauge, 0 unregistered.
func statShapeOf(typ uint16) byte {
	switch typ {
	case 0, 1, 2, 3, 4, 5, 6, 11, 12, 13:
		return 'c'
	case 7, 8, 14, 15, 18, 20, 29, 31, 33, 39:
		return 'g'
	case 9, 10, 16, 17, 19, 21, 22, 23, 26, 27, 28, 30, 32, 34, 35, 36, 37, 38, 40, 41, 42, 43:
		return 'f'
	}
	return 0
}

func statsMsg(count uint32, tlvs ...[]byte) []byte {
	return bmpMsg(1, defaultPeer().bytes(), u32(count), cat(tlvs...))
}

func TestStats(t *testing.T) {
	t.Run("each registered shape", func(t *testing.T) {
		var tlvs [][]byte
		for typ := uint16(0); typ <= 45; typ++ {
			switch statShapeOf(typ) {
			case 'c':
				tlvs = append(tlvs, tlv(typ, u32(uint32(typ)+1000)...))
			case 'g':
				tlvs = append(tlvs, tlv(typ, u64(uint64(typ)<<40)...))
			default: // per-AFI/SAFI, and the unregistered types in the same 11-byte form
				tlvs = append(tlvs, tlv(typ, cat(u16(2), []byte{1}, u64(uint64(typ)<<40))...))
			}
		}
		stats, errs := walk(mustParse[StatsReport](t, statsMsg(uint32(len(tlvs)), tlvs...)).Stats())
		if len(errs) != 0 || len(stats) != len(tlvs) {
			t.Fatalf("%d stats, errors %v", len(stats), errs)
		}
		for i, s := range stats {
			typ := uint16(i)
			c, cok := s.Counter()
			g, gok := s.Gauge()
			f, fg, fok := s.FamilyGauge()
			if s.Type != StatType(typ) {
				t.Errorf("stat %d: Type %d", i, s.Type)
			}
			switch shape := statShapeOf(typ); {
			case shape == 'c' && cok && !gok && !fok && c == uint32(typ)+1000:
			case shape == 'g' && !cok && gok && !fok && g == uint64(typ)<<40:
			case shape == 'f' && !cok && !gok && fok && f == bgp.Family{AFI: 2, SAFI: 1} && fg == uint64(typ)<<40:
			case shape == 0 && !cok && !gok && !fok:
			default:
				t.Errorf("type %d (shape %q): Counter (%d, %v) Gauge (%d, %v) FamilyGauge (%v, %d, %v)", typ, shape, c, cok, g, gok, f, fg, fok)
			}
		}
	})
	t.Run("wrong lengths fail every accessor", func(t *testing.T) {
		stats, errs := walk(mustParse[StatsReport](t, statsMsg(3, tlv(7, 1, 2, 3, 4), tlv(0, u64(1)...), tlv(9, 0, 1, 1))).Stats())
		if len(errs) != 0 || len(stats) != 3 {
			t.Fatalf("%d stats, errors %v", len(stats), errs)
		}
		for _, s := range stats {
			_, cok := s.Counter()
			_, gok := s.Gauge()
			_, _, fok := s.FamilyGauge()
			if cok || gok || fok {
				t.Errorf("type %d with %d bytes: an accessor is ok", s.Type, len(s.Value))
			}
		}
		if !bytes.Equal(stats[0].Value, []byte{1, 2, 3, 4}) {
			t.Errorf("Value % x not kept", stats[0].Value)
		}
	})
	t.Run("type 24 is unregistered", func(t *testing.T) {
		stats, _ := walk(mustParse[StatsReport](t, statsMsg(1, tlv(24, cat(u16(1), []byte{1}, u64(5))...))).Stats())
		if len(stats) != 1 {
			t.Fatalf("%d stats", len(stats))
		}
		if _, _, ok := stats[0].FamilyGauge(); ok {
			t.Error("FamilyGauge ok for type 24")
		}
	})
	t.Run("Count above the TLVs", func(t *testing.T) {
		stats, errs := walk(mustParse[StatsReport](t, statsMsg(3, tlv(0, u32(1)...), tlv(1, u32(2)...))).Stats())
		if len(stats) != 2 {
			t.Errorf("%d stats, want 2", len(stats))
		}
		wantMalformed(t, errs)
	})
	t.Run("Count below the TLVs", func(t *testing.T) {
		stats, errs := walk(mustParse[StatsReport](t, statsMsg(1, tlv(0, u32(1)...), tlv(1, u32(2)...))).Stats())
		if len(stats) != 2 {
			t.Errorf("%d stats, want 2", len(stats))
		}
		wantMalformed(t, errs)
	})
	t.Run("TLV cut short", func(t *testing.T) {
		stats, errs := walk(mustParse[StatsReport](t, statsMsg(2, tlv(0, u32(1)...), tlv(7, u64(2)...)[:9])).Stats())
		if len(stats) != 1 || stats[0].Type != StatRejectedPrefixes {
			t.Errorf("stats %v, want the first only", stats)
		}
		wantMalformed(t, errs)
	})
	t.Run("TLV header cut short", func(t *testing.T) {
		stats, errs := walk(mustParse[StatsReport](t, statsMsg(2, tlv(0, u32(1)...), []byte{0, 7, 0})).Stats())
		if len(stats) != 1 || stats[0].Type != StatRejectedPrefixes {
			t.Errorf("stats %v, want the first only", stats)
		}
		wantMalformed(t, errs)
	})
	t.Run("break ends the walk", func(t *testing.T) {
		n := 0
		for range mustParse[StatsReport](t, statsMsg(5, tlv(0, u32(1)...), tlv(1, u32(2)...))).Stats() {
			n++
			break
		}
		if n != 1 {
			t.Errorf("%d yields after break", n)
		}
	})
}

func TestTerminationReason(t *testing.T) {
	m := mustParse[Termination](t, bmpMsg(5, tlv(0, []byte("bye")...), tlv(1, 0, 4)))
	tlvs, errs := walk(m.Info())
	if len(errs) != 0 {
		t.Errorf("Info errors %v", errs)
	}
	wantTLVs(t, tlvs, TLV{TLVTermString, []byte("bye")}, TLV{TLVTermReason, []byte{0, 4}})
	if r, ok := m.Reason(); r != TerminationPermanentlyClosed || !ok {
		t.Errorf("Reason (%d, %v), want (4, true)", r, ok)
	}
	for _, b := range [][]byte{
		bmpMsg(5, tlv(0, []byte("bye")...)),
		bmpMsg(5, tlv(1, 0, 0, 4), tlv(1, 0, 2)), // the first Reason TLV is the wrong length
		bmpMsg(5, tlv(1, 0, 4)[:5]),
	} {
		if r, ok := mustParse[Termination](t, b).Reason(); ok {
			t.Errorf("% x: Reason (%d, true)", b, r)
		}
	}
}

func TestInitiationInfo(t *testing.T) {
	m := mustParse[Initiation](t, bmpMsg(4, tlv(1, []byte("FRR 10.5.1")...), tlv(2, []byte("route-views8")...), tlv(0, []byte("hello")...)))
	tlvs, errs := walk(m.Info())
	if len(errs) != 0 {
		t.Errorf("Info errors %v", errs)
	}
	wantTLVs(t, tlvs, TLV{TLVInitSysDescr, []byte("FRR 10.5.1")}, TLV{TLVInitSysName, []byte("route-views8")}, TLV{TLVInitString, []byte("hello")})
}

func TestRouteMirroringTLVs(t *testing.T) {
	m := mustParse[RouteMirroring](t, bmpMsg(6, defaultPeer().bytes(), tlv(TLVMirrorInformation, u16(uint16(MirrorMessagesLost))...), tlv(0, emptyUpdate...)))
	tlvs, errs := walk(m.TLVs())
	if len(errs) != 0 {
		t.Errorf("TLVs errors %v", errs)
	}
	wantTLVs(t, tlvs, TLV{TLVMirrorInformation, []byte{0, 1}}, TLV{TLVMirrorBGPMessage, emptyUpdate})
}

// TestWalkAllocations: a TLV walk allocates nothing per TLV.
func TestWalkAllocations(t *testing.T) {
	allocs := func(n int) float64 {
		var tlvs [][]byte
		for range n {
			tlvs = append(tlvs, tlv(0, []byte("x")...))
		}
		m := mustParse[Initiation](t, bmpMsg(4, tlvs...))
		return testing.AllocsPerRun(100, func() {
			for range m.Info() {
			}
		})
	}
	if one, many := allocs(1), allocs(100); one != many {
		t.Errorf("allocations: %v for 1 TLV, %v for 100", one, many)
	}
}

// TestMessagesAreValues: a message is a plain value; two parses of the same
// bytes are deeply equal.
func TestMessagesAreValues(t *testing.T) {
	b := bmpMsg(0, defaultPeer().bytes(), emptyUpdate)
	m1, err1 := ParseMessage(b)
	m2, err2 := ParseMessage(b)
	if err1 != nil || err2 != nil {
		t.Fatalf("ParseMessage: %v, %v", err1, err2)
	}
	if !reflect.DeepEqual(m1, m2) {
		t.Error("two parses differ")
	}
}

// TestZeroValues: every accessor of every exported type works on its zero
// value, without a panic: nothing reads Raw at a fixed offset.
func TestZeroValues(t *testing.T) {
	var (
		rm  RouteMonitoring
		sr  StatsReport
		pd  PeerDown
		pu  PeerUp
		pub PeerUpBody
		in  Initiation
		te  Termination
		mi  RouteMirroring
		ot  Other
		ph  PeerHeader
		st  Stat
	)
	for _, m := range []Message{rm, sr, pd, pu, in, te, mi, ot} {
		if m.Header() != (Header{}) || m.Raw() != nil {
			t.Errorf("%T: Header %+v Raw % x", m, m.Header(), m.Raw())
		}
	}
	if _, err := rm.MessageType(); err == nil {
		t.Error("RouteMonitoring.MessageType: no error")
	}
	if stats, errs := walk(sr.Stats()); len(stats) != 0 || len(errs) != 0 {
		t.Errorf("StatsReport.Stats: %v, %v", stats, errs)
	}
	if _, ok := pd.Notification(); ok {
		t.Error("PeerDown.Notification ok")
	}
	if _, ok := pd.FSMEvent(); ok {
		t.Error("PeerDown.FSMEvent ok")
	}
	if _, err := pu.Body(); !errors.Is(err, ErrMalformed) {
		t.Errorf("PeerUp.Body: error %v", err)
	}
	if _, ok := pub.TableName(); ok {
		t.Error("PeerUpBody.TableName ok")
	}
	if _, ok := te.Reason(); ok {
		t.Error("Termination.Reason ok")
	}
	for name, seq := range map[string]iter.Seq2[TLV, error]{
		"PeerDown.Info": pd.Info(), "PeerUpBody.Info": pub.Info(), "Initiation.Info": in.Info(),
		"Termination.Info": te.Info(), "RouteMirroring.TLVs": mi.TLVs(),
	} {
		if tlvs, errs := walk(seq); len(tlvs) != 0 || len(errs) != 0 {
			t.Errorf("%s: %v, %v", name, tlvs, errs)
		}
	}
	if as4, known := ph.AS4(); !as4 || !known || ph.View() != mrt.ViewAdjRIBInPre || ph.Filtered() || ph.Peer() != (mrt.Peer{}) {
		t.Errorf("PeerHeader: AS4 (%v, %v) View %v Filtered %v Peer %+v", as4, known, ph.View(), ph.Filtered(), ph.Peer())
	}
	_, cok := st.Counter()
	_, gok := st.Gauge()
	_, _, fok := st.FamilyGauge()
	if cok || gok || fok { // type 0 is a counter, and 0 bytes are not its 4
		t.Errorf("Stat: Counter %v Gauge %v FamilyGauge %v", cok, gok, fok)
	}
	var e MessageError
	if e.Error() == "" || e.Unwrap() != nil {
		t.Errorf("MessageError: %q, %v", e.Error(), e.Unwrap())
	}
}
