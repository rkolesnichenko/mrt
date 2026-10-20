package mrtgen_test

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

var (
	t0    = time.Unix(1700000000, 0).UTC()
	a4    = netip.MustParseAddr("192.0.2.10")
	a6    = netip.MustParseAddr("2001:db8::11")
	ll    = netip.MustParseAddr("fe80::11")
	local = netip.MustParseAddr("192.0.2.1")
)

func records(t *testing.T, b []byte) []mrt.Record {
	t.Helper()
	var out []mrt.Record
	for r, err := range mrt.Records(bytes.NewReader(b)) {
		if err != nil {
			t.Fatalf("record error: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestGenDecodes(t *testing.T) {
	peers := []mrt.Peer{
		{BGPID: a4, Addr: a4, AS: 64500},
		{BGPID: netip.MustParseAddr("192.0.2.11"), Addr: a6, AS: 4200000001},
	}
	attrs := mrtgen.Attrs(mrtgen.Origin(bgp.OriginEGP), mrtgen.ASPath(true, mrtgen.Seq(64500, 64501)), mrtgen.NextHop(a4))
	v6attrs := mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.MPReachAbbrev(a6, ll))
	stream := bytes.Join([][]byte{
		mrtgen.Header(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, t0, mrtgen.PeerIndexTable(local, "v", peers)),
		mrtgen.Header(mrt.TypeTableDumpV2, mrt.SubRIBIPv4Unicast, t0, mrtgen.RIB(mrt.SubRIBIPv4Unicast, 5,
			netip.MustParsePrefix("198.51.100.0/24"), []mrtgen.Entry{{0, t0, 99, attrs}})),
		mrtgen.Header(mrt.TypeTableDumpV2, mrt.SubRIBIPv6UnicastAddPath, t0, mrtgen.RIB(mrt.SubRIBIPv6UnicastAddPath, 6,
			netip.MustParsePrefix("2001:db8::/32"), []mrtgen.Entry{{1, t0, 3, v6attrs}, {1, t0, 4, v6attrs}})),
	}, nil)
	recs := records(t, stream)
	if len(recs) != 3 {
		t.Fatalf("%d records", len(recs))
	}
	tab := recs[0].(mrt.PeerIndexTable)
	if tab.CollectorID != local || tab.ViewName != "v" || len(tab.Peers) != 2 || tab.Peers[0] != peers[0] || tab.Peers[1] != peers[1] {
		t.Errorf("table = %+v", tab)
	}
	r4 := recs[1].(mrt.RIB)
	if r4.Sequence != 5 || r4.Prefix.String() != "198.51.100.0/24" || r4.EntryCount() != 1 || r4.AddPath {
		t.Errorf("rib4 = %+v", r4)
	}
	for e, err := range r4.Entries() {
		if err != nil || e.PeerIndex != 0 || !e.Originated.Equal(t0) || e.PathID != 0 || !bytes.Equal(e.Attrs.Raw(), attrs) {
			t.Errorf("entry = %+v, %v", e, err)
		}
	}
	r6 := recs[2].(mrt.RIB)
	var ids []uint32
	for e, err := range r6.Entries() {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.PathID)
	}
	if !r6.AddPath || len(ids) != 2 || ids[0] != 3 || ids[1] != 4 {
		t.Errorf("rib6 add-path = %v ids %v", r6.AddPath, ids)
	}

	// Routes sees the same values, abbreviated next hop included.
	var got []mrt.Route
	for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(stream))) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rt)
	}
	if len(got) != 3 {
		t.Fatalf("%d routes", len(got))
	}
	if got[0].Peer != peers[0] || got[0].Path.ReceivedASPath().String() != "64500 64501" || got[0].Path.NextHop() != a4 || got[0].Path.Origin != bgp.OriginEGP {
		t.Errorf("route 0 = %+v", got[0])
	}
	mp := got[1].Path.MPReach
	if got[1].Peer != peers[1] || got[1].PathID != 3 || !got[1].HasPathID || mp == nil || !mp.Abbreviated || mp.NextHop != a6 || mp.LinkLocal != ll {
		t.Errorf("route 1 = %+v mp %+v", got[1], mp)
	}
}

func TestGenRIBGeneric(t *testing.T) {
	raw := []byte{112, 0, 1, 1, 0, 0, 0xFB, 0xF4, 0, 0, 0, 1, 10, 1, 1}
	st := mrt.SubRIBGeneric
	stream := append(
		mrtgen.Header(mrt.TypeTableDumpV2, st, t0, mrtgen.RIBGeneric(st, 1, bgp.Family{AFI: 1, SAFI: 1},
			mrtgen.PrefixBytes(netip.MustParsePrefix("192.0.2.0/25"), 0, false), []mrtgen.Entry{{0, t0, 0, []byte{}}})),
		mrtgen.Header(mrt.TypeTableDumpV2, st, t0, mrtgen.RIBGeneric(st, 2, bgp.Family{AFI: 1, SAFI: 128}, raw, nil))...)
	st = mrt.SubRIBGenericAddPath
	stream = append(stream,
		mrtgen.Header(mrt.TypeTableDumpV2, st, t0, mrtgen.RIBGeneric(st, 3, bgp.Family{AFI: 2, SAFI: 1},
			mrtgen.PrefixBytes(netip.MustParsePrefix("2001:db8:300::/48"), 5, true), []mrtgen.Entry{{1, t0, 0, nil}}))...)
	recs := records(t, stream)
	a, b, c := recs[0].(mrt.RIB), recs[1].(mrt.RIB), recs[2].(mrt.RIB)
	if a.Prefix.String() != "192.0.2.0/25" || a.EntryCount() != 1 {
		t.Errorf("a = %+v", a)
	}
	if b.Family != (bgp.Family{AFI: 1, SAFI: 128}) || b.Prefix.IsValid() || !bytes.HasPrefix(b.RawNLRI, raw) {
		t.Errorf("b = %+v", b)
	}
	if c.Prefix.String() != "2001:db8:300::/48" || c.PathID != 5 || !c.AddPath || c.EntryCount() != 1 {
		t.Errorf("c = %+v", c)
	}
}

func TestGenBGP4MP(t *testing.T) {
	attrs := mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.ASPath(false, mrtgen.Seq(64500)), mrtgen.NextHop(a4))
	wd := []bgp.NLRI{{Prefix: netip.MustParsePrefix("10.0.0.0/8"), PathID: 9, HasPathID: true}}
	ann := []bgp.NLRI{{Prefix: netip.MustParsePrefix("203.0.113.0/24"), PathID: 1, HasPathID: true}}
	msg := mrtgen.Update(wd, ann, attrs, true)
	et := t0.Add(654321 * time.Microsecond)
	stream := bytes.Join([][]byte{
		mrtgen.Header(mrt.TypeBGP4MP, mrt.SubMessageAddPath, t0, mrtgen.BGP4MP(mrt.SubMessageAddPath, 64500, 64496, a4, local, msg)),
		mrtgen.Header(mrt.TypeBGP4MPET, mrt.SubMessageAS4AddPath, et, mrtgen.BGP4MP(mrt.SubMessageAS4AddPath, 4200000001, 64496, a6, netip.MustParseAddr("2001:db8::1"), msg)),
		mrtgen.Header(mrt.TypeBGP4MP, mrt.SubStateChangeAS4, t0, mrtgen.StateChange(mrt.SubStateChangeAS4, 4200000001, 64496, a4, local, mrt.StateActive, mrt.StateEstablished)),
		mrtgen.Header(mrt.TypeBGP4MPET, mrt.SubStateChange, et, mrtgen.StateChange(mrt.SubStateChange, 64500, 64496, a4, local, mrt.StateIdle, mrt.StateConnect)),
	}, nil)
	recs := records(t, stream)
	if len(recs) != 4 {
		t.Fatalf("%d records", len(recs))
	}
	m := recs[0].(mrt.BGP4MPMessage)
	if m.AS4 || !m.AddPath || m.PeerAS != 64500 || m.LocalAS != 64496 || m.PeerAddr != a4 || m.LocalAddr != local || !bytes.Equal(m.Message, msg) {
		t.Errorf("m = %+v", m)
	}
	u, diags := bgp.ParseUpdate(m.Message, m.Context())
	if len(diags) != 0 || len(u.Withdrawn) != 1 || u.Withdrawn[0] != wd[0] || len(u.Announced) != 1 || u.Announced[0] != ann[0] || !bytes.Equal(u.Attrs.Raw(), attrs) {
		t.Errorf("update = %+v %v", u, diags)
	}
	e := recs[1].(mrt.BGP4MPMessage)
	if !e.AS4 || e.PeerAS != 4200000001 || e.PeerAddr != a6 || !e.Header().Time.Equal(et) || e.Header().Type != mrt.TypeBGP4MPET {
		t.Errorf("e = %+v", e)
	}
	s := recs[2].(mrt.BGP4MPStateChange)
	if !s.AS4 || s.PeerAS != 4200000001 || s.Old != mrt.StateActive || s.New != mrt.StateEstablished {
		t.Errorf("s = %+v", s)
	}
	s2 := recs[3].(mrt.BGP4MPStateChange)
	if s2.AS4 || s2.PeerAS != 64500 || s2.Old != mrt.StateIdle || !s2.Header().Time.Equal(et) {
		t.Errorf("s2 = %+v", s2)
	}

	// Routes: withdrawal, announcement, with path IDs.
	var kinds []mrt.Kind
	for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(stream[:len(recs[0].Raw())]))) {
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, rt.Kind)
		if rt.PathID == 0 || !rt.HasPathID {
			t.Errorf("route %+v lacks its path ID", rt)
		}
	}
	if len(kinds) != 2 {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestGenAttributes(t *testing.T) {
	agg := netip.MustParseAddr("192.0.2.77")
	rt := bgp.ExtCommunity{0, 2, 0xFB, 0xF4, 0, 0, 0, 100}
	lc := bgp.LargeCommunity{Global: 64500, Local1: 1, Local2: 4294967295}
	v6 := []bgp.NLRI{{Prefix: netip.MustParsePrefix("2001:db8::/32")}}
	block := mrtgen.Attrs(
		mrtgen.Origin(bgp.OriginIncomplete),
		mrtgen.ASPath(true, mrtgen.Seq(1, 2), mrtgen.Set(3, 4)),
		mrtgen.NextHop(a4),
		mrtgen.MED(7), mrtgen.LocalPref(200),
		mrtgen.AggregatorAttr(true, 4200000001, agg),
		mrtgen.Communities(0xFFFF0001, 64500<<16|9),
		mrtgen.ExtCommunities(rt),
		mrtgen.Large(lc),
		mrtgen.OTC(64501),
		mrtgen.MPReach(bgp.Family{AFI: 2, SAFI: 1}, []netip.Addr{a6, ll}, v6, false),
		mrtgen.MPUnreach(bgp.Family{AFI: 2, SAFI: 1}, v6, false),
	)
	p, diags := bgp.NewAttrs(block, bgp.Context{AS4: true}).Decode()
	if len(diags) != 0 {
		t.Fatalf("diags %v", diags)
	}
	if gotAgg, _ := p.Aggregator(); p.Origin != bgp.OriginIncomplete || p.ReceivedASPath().String() != "1 2 {3,4}" || p.NextHop() != a4 ||
		p.MED != 7 || p.LocalPref != 200 || gotAgg != (bgp.Aggregator{AS: 4200000001, Addr: agg}) ||
		len(p.Communities) != 2 || p.Communities[0] != 0xFFFF0001 || len(p.ExtCommunities()) != 1 || p.ExtCommunities()[0] != rt ||
		len(p.LargeCommunities) != 1 || p.LargeCommunities[0] != lc || p.OnlyToCustomer != 64501 {
		t.Errorf("path = %+v", p)
	}
	if mp := p.MPReach; mp == nil || mp.NextHop != a6 || mp.LinkLocal != ll || len(mp.NLRI) != 1 || mp.NLRI[0].Prefix != v6[0].Prefix {
		t.Errorf("mpreach = %+v", p.MPReach)
	}
	if mu := p.MPUnreach(); mu == nil || len(mu.Withdrawn) != 1 || mu.Withdrawn[0].Prefix != v6[0].Prefix {
		t.Errorf("mpunreach = %+v", mu)
	}

	// 2-byte path with AS4_PATH and AS4_AGGREGATOR: the merge applies.
	b2 := mrtgen.Attrs(
		mrtgen.ASPath(false, mrtgen.Seq(64500, bgp.ASTrans, bgp.ASTrans)),
		mrtgen.AggregatorAttr(false, bgp.ASTrans, agg),
		mrtgen.AS4PathAttr(mrtgen.Seq(196608, 196609)),
		mrtgen.AS4AggregatorAttr(196610, agg))
	p, diags = bgp.NewAttrs(b2, bgp.Context{}).Decode()
	if gotAgg, _ := p.Aggregator(); p.ASPath.String() != "64500 196608 196609" || gotAgg.AS != 196610 {
		t.Errorf("merged = %v agg %v diags %v", p.ASPath, gotAgg, diags)
	}

	// Raw with a long value gets the extended length flag; RawLen lies.
	long := mrtgen.Raw(mrtgen.FlagOptional|mrtgen.FlagTransitive, 99, make([]byte, 300))
	for at, err := range bgp.NewAttrs(long, bgp.Context{}).All() {
		if err != nil || at.Code != 99 || len(at.Value) != 300 || at.Flags&mrtgen.FlagExtLen == 0 {
			t.Errorf("long = %+v %v", at, err)
		}
	}
	var sawErr bool
	for _, err := range bgp.NewAttrs(mrtgen.RawLen(0x40, 99, 20, []byte{1}), bgp.Context{}).All() {
		sawErr = err != nil
	}
	if !sawErr {
		t.Error("RawLen with a short value should break framing")
	}
}

// dir is where the generator writes.
const dir = "../../testdata/synthetic"

func TestGeneratedFiles(t *testing.T) {
	files := mrtgen.Synthetic()
	if len(files) != 13 {
		t.Fatalf("%d files, want 13", len(files))
	}
	again := mrtgen.Synthetic()
	for i, f := range files {
		if !bytes.Equal(f.Data, again[i].Data) {
			t.Errorf("%s is not deterministic", f.Name)
		}
		t.Run(f.Name, func(t *testing.T) {
			disk, err := os.ReadFile(filepath.Join(dir, f.Name))
			if err != nil {
				t.Fatalf("run go run ./internal/mrtgen/cmd/mrtgen -out testdata/synthetic: %v", err)
			}
			if !bytes.Equal(disk, f.Data) {
				t.Fatal("file on disk differs from the generator's output; regenerate")
			}
			n, routes := 0, 0
			for _, err := range mrt.Records(bytes.NewReader(disk)) {
				if err != nil {
					t.Fatalf("record error: %v", err)
				}
				n++
			}
			var diags int
			for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(disk))) {
				if err != nil {
					t.Fatalf("route error: %v", err)
				}
				routes++
				diags += len(rt.Diagnostics)
			}
			if n == 0 || routes == 0 {
				t.Errorf("%d records, %d routes", n, routes)
			}
			malformed := f.Name == "attrs-malformed.mrt" || f.Name == "short-message.mrt"
			if malformed && diags == 0 {
				t.Errorf("%s should produce diagnostics", f.Name)
			}
			if !malformed && f.Name != "as4-merge.mrt" && f.Name != "as4-confed.mrt" && diags != 0 {
				t.Errorf("%d unexpected diagnostics", diags)
			}
		})
	}
}

// TestStateChanges pins state-changes.mrt: a session cycle with 2-byte and
// 4-byte state changes, IPv4 and IPv6, BGP4MP and BGP4MP_ET.
func TestStateChanges(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(dir, "state-changes.mrt"))
	if err != nil {
		t.Fatal(err)
	}
	type sc struct {
		typ      mrt.Type
		sub      mrt.Subtype
		peer     string
		as       bgp.ASN
		old, new mrt.State
	}
	want := []sc{
		{mrt.TypeBGP4MP, mrt.SubStateChange, "192.0.2.10", 64500, mrt.StateIdle, mrt.StateConnect},
		{mrt.TypeBGP4MP, mrt.SubStateChange, "192.0.2.10", 64500, mrt.StateConnect, mrt.StateOpenSent},
		{mrt.TypeBGP4MP, mrt.SubStateChange, "192.0.2.10", 64500, mrt.StateOpenSent, mrt.StateOpenConfirm},
		{mrt.TypeBGP4MP, mrt.SubStateChange, "192.0.2.10", 64500, mrt.StateOpenConfirm, mrt.StateEstablished},
		{mrt.TypeBGP4MP, mrt.SubStateChangeAS4, "2001:db8::11", 4200000001, mrt.StateActive, mrt.StateEstablished},
		{mrt.TypeBGP4MPET, mrt.SubStateChangeAS4, "2001:db8::11", 4200000001, mrt.StateEstablished, mrt.StateIdle},
		{mrt.TypeBGP4MPET, mrt.SubStateChange, "192.0.2.10", 64500, mrt.StateEstablished, mrt.StateIdle},
	}
	var got []sc
	updates := 0
	for rec, err := range mrt.Records(bytes.NewReader(b)) {
		if err != nil {
			t.Fatal(err)
		}
		switch r := rec.(type) {
		case mrt.BGP4MPStateChange:
			h := r.Header()
			got = append(got, sc{h.Type, h.Subtype, r.PeerAddr.String(), r.PeerAS, r.Old, r.New})
		case mrt.BGP4MPMessage:
			updates++
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("state changes:\n got %v\nwant %v", got, want)
	}
	if updates != 1 {
		t.Errorf("%d messages, want 1 UPDATE between Established and Idle", updates)
	}
}

// TestShortMessage pins short-message.mrt: a valid UPDATE, then four whose
// BGP header claims more bytes than the record holds, cut after a whole
// prefix, inside a prefix, inside the withdrawn routes, and inside the path
// attributes after an MP_REACH_NLRI. Each is read as far as its bytes go,
// with one Error bgp/message-truncated (Ruling R17a).
func TestShortMessage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(dir, "short-message.mrt"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rec, err := range mrt.Records(bytes.NewReader(b)) {
		if err != nil {
			t.Fatal(err)
		}
		m := rec.(mrt.BGP4MPMessage)
		h, err := bgp.ParseMessageHeader(m.Message)
		if err != nil {
			t.Fatal(err)
		}
		u, diags := bgp.ParseUpdate(m.Message, m.Context())
		var codes []string
		for _, d := range diags {
			codes = append(codes, d.Code)
		}
		p, pd := u.Attrs.Decode()
		mp := 0
		if p.MPReach != nil {
			mp = len(p.MPReach.NLRI)
		}
		got = append(got, fmt.Sprintf("%d of %d bytes: %v w=%d a=%d mp=%d attr-diags=%d",
			len(m.Message), h.Length, codes, len(u.Withdrawn), len(u.Announced), mp, len(pd)))
	}
	want := []string{
		"55 of 55 bytes: [] w=0 a=3 mp=0 attr-diags=0",
		"55 of 58 bytes: [bgp/message-truncated] w=0 a=3 mp=0 attr-diags=0",
		"54 of 55 bytes: [bgp/message-truncated] w=0 a=2 mp=0 attr-diags=0",
		"31 of 35 bytes: [bgp/message-truncated] w=2 a=0 mp=0 attr-diags=0",
		"71 of 74 bytes: [bgp/message-truncated] w=0 a=0 mp=1 attr-diags=0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("messages:\n got %q\nwant %q", got, want)
	}
}

// TestNewFamilies pins what Routes reads from rib-multicast.mrt and
// mp-ipv4-large-values.mrt.
func TestNewFamilies(t *testing.T) {
	routes := func(name string) []string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(b))) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, fmt.Sprintf("%v %d/%d %v lp=%d med=%d", rt.Kind, rt.Family.AFI, rt.Family.SAFI, rt.Prefix, rt.Path.LocalPref, rt.Path.MED))
		}
		return got
	}
	if got, want := routes("rib-multicast.mrt"), []string{
		"B 1/2 198.51.102.0/24 lp=0 med=0",
		"B 2/2 2001:db8:500::/48 lp=0 med=0",
	}; !slices.Equal(got, want) {
		t.Errorf("rib-multicast.mrt: %q, want %q", got, want)
	}
	if got, want := routes("mp-ipv4-large-values.mrt"), []string{
		"A 1/1 198.51.110.0/24 lp=3000000000 med=4000000000",
		"A 1/1 198.51.111.0/24 lp=3000000000 med=4000000000",
	}; !slices.Equal(got, want) {
		t.Errorf("mp-ipv4-large-values.mrt: %q, want %q", got, want)
	}
}

// TestAS4Confed pins the effective paths and aggregators of as4-confed.mrt
// (RFC 6793 sections 4.2.3 and 6; Rulings R5 and R6).
func TestAS4Confed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(dir, "as4-confed.mrt"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(b))) {
		if err != nil {
			t.Fatal(err)
		}
		agg := "-"
		if a, ok := rt.Path.Aggregator(); ok {
			agg = fmt.Sprint(a.AS)
		}
		var diags []string
		for _, d := range rt.Diagnostics {
			diags = append(diags, fmt.Sprintf("%d/%s/%d", d.Severity, d.Code, d.Attr))
		}
		got = append(got, fmt.Sprintf("%v: %s agg %s diags %v", rt.Prefix, rt.Path.ASPath, agg, diags))
	}
	// Record 4's confed segment in AS4_PATH is discarded with a Warning on
	// AS4_PATH (Ruling R6); the others decode cleanly.
	warnAS4Path := fmt.Sprintf("%d/bgp/attr-value/%d", bgp.SeverityWarning, bgp.AttrAS4Path)
	want := []string{
		"198.51.106.0/24: (65001) 64500 196608 196609 agg - diags []",
		"198.51.107.0/24: (65001 65002) 196608 agg - diags []",
		"198.51.108.0/24: 64500 196608 agg 196610 diags []",
		"198.51.109.0/24: 64500 196608 196609 agg - diags [" + warnAS4Path + "]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("as4-confed.mrt:\n got %q\nwant %q", got, want)
	}
}

func TestMalformedAndMergeDetails(t *testing.T) {
	routes := func(name string) []mrt.Route {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var out []mrt.Route
		for rt, err := range mrt.Routes(mrt.Records(bytes.NewReader(b))) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, rt)
		}
		return out
	}
	codes := func(r mrt.Route) map[string]bool {
		m := map[string]bool{}
		for _, d := range r.Diagnostics {
			m[d.Code] = true
		}
		return m
	}
	mal := routes("attrs-malformed.mrt")
	if len(mal) != 4 {
		t.Fatalf("%d routes", len(mal))
	}
	if !codes(mal[0])["bgp/attr-length"] || !codes(mal[1])["bgp/attr-duplicate"] || !codes(mal[2])["bgp/attr-truncated"] || len(mal[3].Diagnostics) != 0 {
		t.Errorf("diagnostics: %v %v %v %v", mal[0].Diagnostics, mal[1].Diagnostics, mal[2].Diagnostics, mal[3].Diagnostics)
	}
	if !mal[3].Path.HasMED || mal[3].Path.MED != 50 {
		t.Errorf("valid UPDATE = %+v", mal[3].Path)
	}
	m := routes("as4-merge.mrt")
	want := []string{"64500 196608 196609", "64500 23456", "64500 23456", "64500 196608", "64500 196608 {196609,64502}"}
	if len(m) != len(want) {
		t.Fatalf("%d routes", len(m))
	}
	for i, w := range want {
		if got := m[i].Path.ASPath.String(); got != w {
			t.Errorf("as4-merge %d: ASPath = %q, want %q", i, got, w)
		}
	}
}
