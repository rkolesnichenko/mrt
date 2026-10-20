package mrtdump

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	g "github.com/rkolesnichenko/mrt/internal/mrtgen"
)

// Inputs the synthetic files lack: state changes, legacy TABLE_DUMP,
// empty and confederation paths, LOCAL messages, withdrawals over
// MP_UNREACH_NLRI, a RIB entry without NEXT_HOP. Every expected line in the
// tests that read them was printed by bgpdump (caf2cb07, TZ=UTC) from exactly
// these bytes.

var (
	tBase   = time.Unix(1700000000, 0).UTC()
	peer4   = netip.MustParseAddr("192.0.2.10")
	peer6   = netip.MustParseAddr("2001:db8::11")
	local4  = netip.MustParseAddr("192.0.2.1")
	local6  = netip.MustParseAddr("2001:db8::1")
	aggAddr = netip.MustParseAddr("192.0.2.77")
	v6u     = bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}
	v4u     = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}
)

func sec(n int) time.Time { return tBase.Add(time.Duration(n) * time.Second) }

func nl(s string) bgp.NLRI { return bgp.NLRI{Prefix: netip.MustParsePrefix(s)} }

// stateFile holds state changes: 2-byte IPv4, 4-byte IPv6, BGP4MP_ET, and a
// state number bgpdump has no name for.
func stateFile() []byte {
	return slices.Concat(
		g.Header(mrt.TypeBGP4MP, mrt.SubStateChange, sec(0),
			g.StateChange(mrt.SubStateChange, 64500, 64496, peer4, local4, mrt.StateIdle, mrt.StateConnect)),
		g.Header(mrt.TypeBGP4MP, mrt.SubStateChangeAS4, sec(1),
			g.StateChange(mrt.SubStateChangeAS4, 4200000001, 64496, peer6, local6, mrt.StateOpenConfirm, mrt.StateEstablished)),
		g.Header(mrt.TypeBGP4MPET, mrt.SubStateChangeAS4, sec(2).Add(5*time.Microsecond),
			g.StateChange(mrt.SubStateChangeAS4, 64500, 64496, peer4, local4, mrt.StateEstablished, mrt.StateIdle)),
		g.Header(mrt.TypeBGP4MP, mrt.SubStateChange, sec(3),
			g.StateChange(mrt.SubStateChange, 64500, 64496, peer4, local4, mrt.StateIdle, 9)),
	)
}

func msgAS4(n int, st mrt.Subtype, peer, local netip.Addr, peerAS bgp.ASN, upd []byte) []byte {
	return g.Header(mrt.TypeBGP4MP, st, sec(n), g.BGP4MP(st, peerAS, 64496, peer, local, upd))
}

// pathsFile holds BGP4MP UPDATEs over paths and attributes.
func pathsFile() []byte {
	nh := g.NextHop(peer4)
	org := g.Origin(bgp.OriginIGP)
	upd := func(attrs []byte, nlri ...string) []byte {
		var ns []bgp.NLRI
		for _, s := range nlri {
			ns = append(ns, nl(s))
		}
		return g.Update(nil, ns, attrs, false)
	}
	confed := bgp.Segment{Type: bgp.ASConfedSequence, ASNs: []bgp.ASN{65001, 65002}}
	confedSet := bgp.Segment{Type: bgp.ASConfedSet, ASNs: []bgp.ASN{65003, 65004}}
	keepalive := append(slices.Repeat([]byte{0xFF}, 16), 0, 19, 4)
	return slices.Concat(
		// 0: empty AS_PATH (iBGP), LOCAL_PREF, MED, ATOMIC_AGGREGATE, AGGREGATOR.
		msgAS4(0, mrt.SubMessageAS4, peer4, local4, 64500, upd(g.Attrs(org, g.ASPath(true), nh,
			g.MED(10), g.LocalPref(100), g.Raw(g.FlagTransitive, 6, nil), g.AggregatorAttr(true, 64500, aggAddr)),
			"198.51.100.0/24")),
		// 1: no AS_PATH attribute at all.
		msgAS4(1, mrt.SubMessageAS4, peer4, local4, 64500, upd(g.Attrs(org, nh), "198.51.100.1/32")),
		// 2: a path ending in an AS_SET.
		msgAS4(2, mrt.SubMessageAS4, peer4, local4, 64500, upd(g.Attrs(org,
			g.ASPath(true, g.Seq(1, 2), g.Set(3, 4)), nh), "198.51.100.2/32")),
		// 3: confederation sequence and set.
		msgAS4(3, mrt.SubMessageAS4, peer4, local4, 64500, upd(g.Attrs(g.Origin(bgp.OriginIncomplete),
			g.ASPath(true, confed, confedSet, g.Seq(64500)), nh), "198.51.100.3/32")),
		// 4: IPv4 over MP_REACH_NLRI, LOCAL_PREF and MED above 2^31.
		msgAS4(4, mrt.SubMessageAS4, peer4, local4, 64500, upd(g.Attrs(g.Origin(bgp.OriginEGP),
			g.ASPath(true, g.Seq(64500)), g.LocalPref(3000000000), g.MED(4000000000),
			g.MPReach(v4u, []netip.Addr{netip.MustParseAddr("192.0.2.20")}, []bgp.NLRI{nl("198.51.100.4/32")}, false)))),
		// 5: classic withdrawals and announcements, and an IPv6 MP_UNREACH_NLRI, in one UPDATE.
		msgAS4(5, mrt.SubMessageAS4, peer4, local4, 64500, g.Update(
			[]bgp.NLRI{nl("198.51.100.5/32"), nl("198.51.100.6/32")},
			[]bgp.NLRI{nl("198.51.100.7/32"), nl("198.51.100.8/32")},
			g.Attrs(org, g.ASPath(true, g.Seq(64500)), nh,
				g.MPUnreach(v6u, []bgp.NLRI{nl("2001:db8:500::/48")}, false)), false)),
		// 6: LOCAL (sent by the collector) announcement over IPv6.
		msgAS4(6, mrt.SubMessageAS4Local, peer6, local6, 4200000001, upd(g.Attrs(org,
			g.ASPath(true, g.Seq(64496)),
			g.MPReach(v6u, []netip.Addr{local6}, []bgp.NLRI{nl("2001:db8:600::/48")}, false)))),
		// 7: 2-byte BGP4MP_MESSAGE over an IPv6 session, IPv6 multicast.
		g.Header(mrt.TypeBGP4MP, mrt.SubMessage, sec(7), g.BGP4MP(mrt.SubMessage, 64500, 64496, peer6, local6,
			upd(g.Attrs(org, g.ASPath(false, g.Seq(64500)),
				g.MPReach(bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIMulticast}, []netip.Addr{peer6},
					[]bgp.NLRI{nl("2001:db8:700::/48")}, false))))),
		// 8: KEEPALIVE: nothing to print.
		msgAS4(8, mrt.SubMessageAS4, peer4, local4, 64500, keepalive),
		// 9: well-known communities.
		msgAS4(9, mrt.SubMessageAS4, peer4, local4, 64500, upd(g.Attrs(org, g.ASPath(true, g.Seq(64500)), nh,
			g.Communities(0xFFFFFF01, 0xFFFFFF02, 0xFFFFFF03, 0xFFFFFF04, 0)), "198.51.100.9/32")),
	)
}

// tableDump returns a legacy TABLE_DUMP record (RFC 6396 section 4.2): AS_PATH
// in 2-byte ASNs.
func tableDump(n int, prefix netip.Prefix, peer netip.Addr, peerAS uint16, attrs []byte) []byte {
	st := mrt.SubAFIIPv4
	if prefix.Addr().Is6() {
		st = mrt.SubAFIIPv6
	}
	b := binary.BigEndian.AppendUint16(nil, 0)      // view
	b = binary.BigEndian.AppendUint16(b, uint16(n)) // sequence
	b = append(b, prefix.Addr().AsSlice()...)
	b = append(b, byte(prefix.Bits()), 1) // prefix length, status
	b = binary.BigEndian.AppendUint32(b, uint32(sec(-100).Unix()))
	b = append(b, peer.AsSlice()...)
	b = binary.BigEndian.AppendUint16(b, peerAS)
	b = binary.BigEndian.AppendUint16(b, uint16(len(attrs)))
	b = append(b, attrs...)
	return g.Header(mrt.TypeTableDump, st, sec(n), b)
}

// tableDumpFile holds legacy TABLE_DUMP records, IPv4 and IPv6.
func tableDumpFile() []byte {
	org := g.Origin(bgp.OriginIGP)
	return slices.Concat(
		tableDump(0, netip.MustParsePrefix("198.51.100.0/24"), peer4, 64500,
			g.Attrs(org, g.ASPath(false, g.Seq(64500, 64501)), g.NextHop(peer4), g.Communities(64500<<16|1))),
		tableDump(1, netip.MustParsePrefix("2001:db8:100::/48"), peer6, 64500,
			g.Attrs(org, g.ASPath(false, g.Seq(64500)), g.MPReachAbbrev(peer6))),
	)
}

// ribMiscFile holds TABLE_DUMP_V2 cases: an empty AS path, no NEXT_HOP, and
// an IPv4 multicast RIB.
func ribMiscFile() []byte {
	t := tBase
	peers := []mrt.Peer{{BGPID: peer4, Addr: peer4, AS: 64500}}
	org := g.Origin(bgp.OriginIGP)
	rib := func(st mrt.Subtype, seq uint32, p string, attrs []byte) []byte {
		return g.Header(mrt.TypeTableDumpV2, st, t,
			g.RIB(st, seq, netip.MustParsePrefix(p), []g.Entry{{PeerIndex: 0, Originated: t, Attrs: attrs}}))
	}
	return slices.Concat(
		g.Header(mrt.TypeTableDumpV2, mrt.SubPeerIndexTable, t, g.PeerIndexTable(local4, "", peers)),
		rib(mrt.SubRIBIPv4Unicast, 0, "198.51.100.0/24", g.Attrs(org, g.ASPath(true), g.NextHop(peer4), g.LocalPref(100))),
		rib(mrt.SubRIBIPv4Unicast, 1, "198.51.101.0/24", g.Attrs(org, g.ASPath(true, g.Seq(64500)))),
		rib(mrt.SubRIBIPv4Multicast, 2, "198.51.102.0/24", g.Attrs(org, g.ASPath(true, g.Seq(64500)), g.NextHop(peer4))),
	)
}

// oddAttrsFile holds one UPDATE with attributes bgpdump still prints from:
// an AS_PATH whose segment claims more ASNs than it holds, a 1-byte
// ATOMIC_AGGREGATE, and an ORIGIN value of 7.
func oddAttrsFile() []byte {
	return msgAS4(0, mrt.SubMessageAS4, peer4, local4, 64500, g.Update(nil, []bgp.NLRI{nl("198.51.100.0/24")},
		g.Attrs(g.Raw(g.FlagTransitive, 1, []byte{7}), g.Raw(g.FlagTransitive, 2, []byte{2, 3, 0, 1}),
			g.NextHop(peer4), g.RawLen(g.FlagTransitive, 6, 1, []byte{0})), false))
}
