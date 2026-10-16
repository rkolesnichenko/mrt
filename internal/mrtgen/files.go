package mrtgen

import (
	"net/netip"
	"slices"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// File is one synthetic fixture.
type File struct {
	Name string
	Data []byte
}

var (
	collector = netip.MustParseAddr("192.0.2.1")
	peerA     = mrt.Peer{BGPID: netip.MustParseAddr("192.0.2.10"), Addr: netip.MustParseAddr("192.0.2.10"), AS: 64500}
	peerB     = mrt.Peer{BGPID: netip.MustParseAddr("192.0.2.11"), Addr: netip.MustParseAddr("2001:db8::11"), AS: 4200000001}
	peerC     = mrt.Peer{BGPID: netip.MustParseAddr("192.0.2.99"), Addr: netip.MustParseAddr("192.0.2.99"), AS: 64999}
	local4    = netip.MustParseAddr("192.0.2.1")
	local6    = netip.MustParseAddr("2001:db8::1")
	epoch     = time.Unix(1700000000, 0).UTC()

	v4u = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}
	v6u = bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func nlri(s string) bgp.NLRI { return bgp.NLRI{Prefix: pfx(s)} }

func nlriID(s string, id uint32) bgp.NLRI {
	return bgp.NLRI{Prefix: pfx(s), PathID: id, HasPathID: true}
}

// at is the n-th second after the fixed epoch.
func at(n int) time.Time { return epoch.Add(time.Duration(n) * time.Second) }

func catRecords(rs ...[]byte) []byte { return slices.Concat(rs...) }

func pit(ts time.Time, peers ...mrt.Peer) []byte {
	return Header(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, ts, PeerIndexTable(collector, "synthetic", peers))
}

func rib(st mrt.Subtype, ts time.Time, seq uint32, p string, entries ...Entry) []byte {
	return Header(mrt.TypeTableDumpV2, st, ts, RIB(st, seq, pfx(p), entries))
}

// Synthetic returns the fixtures in a fixed order. The bytes are deterministic.
func Synthetic() []File {
	return []File{
		{"addpath-rib-v4v6.mrt", addpathRIB()},
		{"addpath-updates.mrt", addpathUpdates()},
		{"rib-generic.mrt", ribGeneric()},
		{"nh32.mrt", nh32()},
		{"as4-merge.mrt", as4Merge()},
		{"attrs-malformed.mrt", attrsMalformed()},
		{"otc-large.mrt", otcLarge()},
		{"concat-peer-tables.mrt", concatTables()},
	}
}

func addpathRIB() []byte {
	t := at(0)
	a4 := func(path ...bgp.ASN) []byte {
		return Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(path...)), NextHop(peerA.Addr))
	}
	a6 := func(path ...bgp.ASN) []byte {
		return Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(path...)), MPReachAbbrev(netip.MustParseAddr("2001:db8::11")))
	}
	return catRecords(
		pit(t, peerA, peerB),
		rib(mrt.SubRIBIPv4UnicastAddPath, t, 0, "198.51.100.0/24",
			Entry{0, t, 1, a4(64500, 64501)},
			Entry{0, t, 2, a4(64500, 64502, 64501)}),
		rib(mrt.SubRIBIPv6UnicastAddPath, t, 1, "2001:db8:100::/48",
			Entry{1, t, 1, a6(4200000001, 64501)},
			Entry{1, t, 2, a6(4200000001, 64502, 64501)}),
	)
}

func addpathUpdates() []byte {
	v6nh := netip.MustParseAddr("2001:db8::11")
	attrs4 := Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(64500, 64501)), NextHop(peerA.Addr))
	attrs6 := Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(4200000001, 64501)),
		MPReach(v6u, []netip.Addr{v6nh}, []bgp.NLRI{nlriID("2001:db8:200::/48", 7), nlriID("2001:db8:200::/48", 8)}, true))
	m4 := Update(nil, []bgp.NLRI{nlriID("203.0.113.0/24", 1), nlriID("203.0.113.0/24", 2)}, attrs4, true)
	m6 := Update(nil, nil, attrs6, true)
	w := Update([]bgp.NLRI{nlriID("203.0.113.0/24", 1)}, nil, nil, true)
	return catRecords(
		Header(mrt.TypeBGP4MP, mrt.SubMessageAS4AddPath, at(1), BGP4MP(mrt.SubMessageAS4AddPath, peerA.AS, 64496, peerA.Addr, local4, m4)),
		Header(mrt.TypeBGP4MPET, mrt.SubMessageAS4AddPath, at(2).Add(123456*time.Microsecond),
			BGP4MP(mrt.SubMessageAS4AddPath, peerB.AS, 64496, peerB.Addr, local6, m6)),
		Header(mrt.TypeBGP4MP, mrt.SubMessageAS4AddPath, at(3), BGP4MP(mrt.SubMessageAS4AddPath, peerA.AS, 64496, peerA.Addr, local4, w)),
	)
}

func ribGeneric() []byte {
	t := at(0)
	a := Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(64500, 64501)), NextHop(peerA.Addr))
	gen := func(st mrt.Subtype, seq uint32, fam bgp.Family, nlri []byte, es ...Entry) []byte {
		return Header(mrt.TypeTableDumpV2, st, t, RIBGeneric(st, seq, fam, nlri, es))
	}
	// A VPNv4 NLRI: 112 bits = 3-byte label stack + 8-byte route distinguisher + 10.1.1/24.
	vpn := []byte{112, 0x00, 0x01, 0x01, 0, 0, 0xFB, 0xF4, 0, 0, 0, 1, 10, 1, 1}
	return catRecords(
		pit(t, peerA, peerB),
		gen(mrt.SubRIBGeneric, 0, v4u, PrefixBytes(pfx("192.0.2.0/25"), 0, false), Entry{0, t, 0, a}),
		gen(mrt.SubRIBGeneric, 1, bgp.Family{AFI: 1, SAFI: 128}, vpn, Entry{0, t, 0, a}),
		gen(mrt.SubRIBGenericAddPath, 2, v6u, PrefixBytes(pfx("2001:db8:300::/48"), 5, true),
			Entry{1, t, 0, Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(4200000001)), MPReachAbbrev(netip.MustParseAddr("2001:db8::11")))}),
	)
}

func nh32() []byte {
	t := at(0)
	global := netip.MustParseAddr("2001:db8::11")
	ll := netip.MustParseAddr("fe80::11")
	path := ASPath(true, Seq(4200000001, 64501))
	entry := Entry{1, t, 0, Attrs(Origin(bgp.OriginIGP), path, MPReachAbbrev(global, ll))}
	upd := Update(nil, nil, Attrs(Origin(bgp.OriginIGP), path,
		MPReach(v6u, []netip.Addr{global, ll}, []bgp.NLRI{nlri("2001:db8:400::/48")}, false)), false)
	return catRecords(
		pit(t, peerA, peerB),
		rib(mrt.SubRIBIPv6Unicast, t, 0, "2001:db8:400::/48", entry),
		Header(mrt.TypeBGP4MP, mrt.SubMessageAS4, at(1), BGP4MP(mrt.SubMessageAS4, peerB.AS, 64496, peerB.Addr, local6, upd)),
	)
}

func as4Merge() []byte {
	nh := NextHop(peerA.Addr)
	org := Origin(bgp.OriginIGP)
	agg := netip.MustParseAddr("192.0.2.77")
	cases := []struct {
		prefix string
		attrs  []byte
	}{
		// AS4_PATH merged: AS_PATH has 3 ASes, AS4_PATH 2.
		{"198.51.100.0/24", Attrs(org, ASPath(false, Seq(64500, bgp.ASTrans, bgp.ASTrans)), nh,
			AS4PathAttr(Seq(196608, 196609)))},
		// AS4_PATH longer than AS_PATH: ignored.
		{"198.51.101.0/24", Attrs(org, ASPath(false, Seq(64500, bgp.ASTrans)), nh,
			AS4PathAttr(Seq(64500, 196608, 196609)))},
		// AGGREGATOR is not AS_TRANS: AS4_AGGREGATOR and AS4_PATH are ignored.
		{"198.51.102.0/24", Attrs(org, ASPath(false, Seq(64500, bgp.ASTrans)), nh,
			AggregatorAttr(false, 64501, agg), AS4PathAttr(Seq(196608)), AS4AggregatorAttr(196610, agg))},
		// AGGREGATOR is AS_TRANS: AS4_AGGREGATOR replaces it and AS4_PATH merges.
		{"198.51.103.0/24", Attrs(org, ASPath(false, Seq(64500, bgp.ASTrans)), nh,
			AggregatorAttr(false, bgp.ASTrans, agg), AS4PathAttr(Seq(196608)), AS4AggregatorAttr(196610, agg))},
		// AS_SET in both paths.
		{"198.51.104.0/24", Attrs(org, ASPath(false, Seq(64500, bgp.ASTrans), Set(bgp.ASTrans, 64502)), nh,
			AS4PathAttr(Seq(196608), Set(196609, 64502)))},
	}
	var recs [][]byte
	for i, c := range cases {
		msg := Update(nil, []bgp.NLRI{nlri(c.prefix)}, c.attrs, false)
		recs = append(recs, Header(mrt.TypeBGP4MP, mrt.SubMessage, at(i), BGP4MP(mrt.SubMessage, peerA.AS, 64496, peerA.Addr, local4, msg)))
	}
	return catRecords(recs...)
}

func attrsMalformed() []byte {
	nh := NextHop(peerA.Addr)
	path := ASPath(true, Seq(64500, 64501))
	attrs := [][]byte{
		// Malformed MED: 3 bytes.
		Attrs(Origin(bgp.OriginIGP), path, nh, Raw(FlagOptional, 4, []byte{0, 0, 1})),
		// Duplicate ORIGIN.
		Attrs(Origin(bgp.OriginIGP), path, nh, Origin(bgp.OriginEGP)),
		// An attribute declaring 20 bytes with 2 left in the block.
		Attrs(Origin(bgp.OriginIGP), nh, RawLen(FlagOptional|FlagTransitive, 99, 20, []byte{1, 2})),
		// A valid UPDATE after them.
		Attrs(Origin(bgp.OriginIGP), path, nh, MED(50)),
	}
	var recs [][]byte
	for i, a := range attrs {
		msg := Update(nil, []bgp.NLRI{nlri("198.51.100." + string(rune('0'+i)) + "/32")}, a, false)
		recs = append(recs, Header(mrt.TypeBGP4MP, mrt.SubMessageAS4, at(i), BGP4MP(mrt.SubMessageAS4, peerA.AS, 64496, peerA.Addr, local4, msg)))
	}
	return catRecords(recs...)
}

func otcLarge() []byte {
	nh := NextHop(peerA.Addr)
	base := Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(64500, 64501)), nh)
	rt := bgp.ExtCommunity{0x00, 0x02, 0xFB, 0xF4, 0, 0, 0, 100}
	soo := bgp.ExtCommunity{0x01, 0x03, 192, 0, 2, 1, 0, 7}
	cases := [][]byte{
		Attrs(base, OTC(64500)),
		Attrs(base, Communities(0xFFFF0001, 64500<<16|100, 0xFFFFFF01),
			Large(bgp.LargeCommunity{Global: 64500, Local1: 1, Local2: 2}, bgp.LargeCommunity{Global: 4200000001, Local1: 0, Local2: 4294967295})),
		Attrs(base, ExtCommunities(rt, soo), OTC(4200000001)),
		Attrs(base, Communities(65000<<16|1), ExtCommunities(rt),
			Large(bgp.LargeCommunity{Global: 64500, Local1: 10, Local2: 20}), OTC(64501)),
	}
	var recs [][]byte
	for i, a := range cases {
		msg := Update(nil, []bgp.NLRI{nlri("203.0.113." + string(rune('0'+i)) + "/32")}, a, false)
		recs = append(recs, Header(mrt.TypeBGP4MP, mrt.SubMessageAS4, at(i), BGP4MP(mrt.SubMessageAS4, peerA.AS, 64496, peerA.Addr, local4, msg)))
	}
	return catRecords(recs...)
}

func concatTables() []byte {
	t := at(0)
	a := func(as bgp.ASN, nh netip.Addr) []byte {
		return Attrs(Origin(bgp.OriginIGP), ASPath(true, Seq(as, 64501)), NextHop(nh))
	}
	return catRecords(
		pit(t, peerA),
		rib(mrt.SubRIBIPv4Unicast, t, 0, "198.51.100.0/24", Entry{0, t, 0, a(peerA.AS, peerA.Addr)}),
		pit(at(1), peerC),
		rib(mrt.SubRIBIPv4Unicast, at(1), 0, "198.51.100.0/24", Entry{0, at(1), 0, a(peerC.AS, peerC.Addr)}),
	)
}
