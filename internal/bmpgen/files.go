package bmpgen

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

// File is one synthetic fixture. Its name has no extension; the file is
// testdata/bmp/<Name>.bmp, or <Name>.obmp for an OpenBMP frame stream.
type File struct {
	Name string
	Data []byte
}

// Ext is the file extension: ".obmp" for the OpenBMP fixtures, else ".bmp".
func (f File) Ext() string {
	if strings.HasPrefix(f.Name, "obmp-") {
		return ".obmp"
	}
	return ".bmp"
}

// Peer header flags (RFC 7854 section 4.2, RFC 9069 section 4.2).
const (
	flagV = 0x80
	flagL = 0x40
	flagA = 0x20
	flagO = 0x10
	flagF = 0x80 // Loc-RIB: filtered
)

var (
	fam4  = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}
	fam4m = bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIMulticast}
	fam6  = bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}

	routerID = netip.MustParseAddr("192.0.2.1")
	local4   = netip.MustParseAddr("192.0.2.1")
	local6   = netip.MustParseAddr("2001:db8::1")
	nh4      = netip.MustParseAddr("192.0.2.254")
	nh6      = netip.MustParseAddr("2001:db8::fe")
	nh6ll    = netip.MustParseAddr("fe80::fe")

	epoch = time.Unix(1700000000, 0).UTC()
)

const routerAS = 65000

// ap builds an ADD-PATH tuple: mode 1 receive, 2 send, 3 both.
func ap(f bgp.Family, mode uint8) bgp.AddPathFamily { return bgp.AddPathFamily{Family: f, Mode: mode} }

// stream is a fixture under construction. Every per-peer header it hands out
// carries the next second after the fixed epoch.
type stream struct {
	b []byte
	n int
}

// h stamps base with the next fixed time.
func (s *stream) h(base bmp.PeerHeader) bmp.PeerHeader {
	base.Time = epoch.Add(time.Duration(s.n) * time.Second)
	s.n++
	return base
}

func (s *stream) add(msgs ...[]byte) { s.b = append(s.b, slices.Concat(msgs...)...) }

func (s *stream) file(name string) File { return File{name, s.b} }

// peer builds a per-peer header. addr "" is no address (Loc-RIB).
func peer(typ mrt.PeerType, flags uint8, dist uint64, addr string, as bgp.ASN, id string) bmp.PeerHeader {
	h := bmp.PeerHeader{Type: typ, Flags: flags, Distinguisher: dist, AS: as, BGPID: netip.MustParseAddr(id)}
	if addr != "" {
		h.Addr = netip.MustParseAddr(addr)
	}
	return h
}

// global is a global-instance peer 192.0.2.<n> (or 2001:db8::<n> when v6).
func global(n int, v6 bool) bmp.PeerHeader {
	id := fmt.Sprintf("192.0.2.%d", n)
	if v6 {
		return peer(mrt.PeerGlobal, flagV, 0, fmt.Sprintf("2001:db8::%x", n), bgp.ASN(64500+n), id)
	}
	return peer(mrt.PeerGlobal, 0, 0, id, bgp.ASN(64500+n), id)
}

func local(h bmp.PeerHeader) netip.Addr {
	if h.Addr.Is6() {
		return local6
	}
	if h.Addr.Is4() {
		return local4
	}
	return netip.Addr{}
}

// routerOpen is the OPEN the monitored router sent.
func routerOpen(caps ...[]byte) []byte { return Open(routerAS, routerID, caps...) }

// peerOpen is the OPEN the peer sent.
func peerOpen(h bmp.PeerHeader, caps ...[]byte) []byte { return Open(h.AS, h.BGPID, caps...) }

// basicCaps are Multiprotocol for IPv4 and IPv6 unicast and the AS capability.
func basicCaps(as bgp.ASN, more ...[]byte) [][]byte {
	return append([][]byte{CapMP(fam4), CapMP(fam6), CapAS4(as)}, more...)
}

// up is a Peer Up for h with the given capabilities on each side.
func (s *stream) up(h bmp.PeerHeader, sent, recv [][]byte) {
	port := uint16(40000 + s.n) // before s.h advances n: argument order is unspecified
	s.add(PeerUp(s.h(h), local(h), 179, port, routerOpen(sent...), peerOpen(h, recv...)))
}

// plainUp is a Peer Up with the basic capabilities and no ADD-PATH.
func (s *stream) plainUp(h bmp.PeerHeader) {
	s.up(h, basicCaps(routerAS), basicCaps(h.AS))
}

func nlri(p string) bgp.NLRI { return bgp.NLRI{Prefix: netip.MustParsePrefix(p)} }

func nlriID(p string, id uint32) bgp.NLRI {
	return bgp.NLRI{Prefix: netip.MustParsePrefix(p), PathID: id, HasPathID: true}
}

// as4Of is the AS width the header says its PDUs use.
func as4Of(h bmp.PeerHeader) bool {
	as4, known := h.AS4()
	return as4 || !known
}

// ann is an UPDATE announcing prefix p (IPv4 in the NLRI field, IPv6 in
// MP_REACH) from h. With withID every NLRI carries path identifier id. The
// AS_PATH is 64501 64502 at the width h says.
func ann(h bmp.PeerHeader, p string, withID bool, id uint32) []byte {
	n := nlri(p)
	if withID {
		n = nlriID(p, id)
	}
	path := mrtgen.ASPath(as4Of(h), mrtgen.Seq(64501, 64502))
	if netip.MustParsePrefix(p).Addr().Is6() {
		return mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), path,
			mrtgen.MPReach(fam6, []netip.Addr{nh6}, []bgp.NLRI{n}, withID)), false)
	}
	return mrtgen.Update(nil, []bgp.NLRI{n}, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), path, mrtgen.NextHop(nh4)), withID)
}

// wd is an UPDATE withdrawing p.
func wd(p string, withID bool, id uint32) []byte {
	n := nlri(p)
	if withID {
		n = nlriID(p, id)
	}
	if netip.MustParsePrefix(p).Addr().Is6() {
		return mrtgen.Update(nil, nil, mrtgen.MPUnreach(fam6, []bgp.NLRI{n}, withID), false)
	}
	return mrtgen.Update([]bgp.NLRI{n}, nil, nil, withID)
}

// notification is a NOTIFICATION message (RFC 4271 section 4.5).
func notification(code, sub byte) []byte {
	return append(slices.Repeat([]byte{0xFF}, 16), 0, 21, 3, code, sub)
}

// Synthetic returns the fixtures in a fixed order. The bytes are deterministic.
func Synthetic() []File {
	return []File{
		types(), peerFlags(), addPath(), as2(), ipv6Peers(), eor(), noPeerUp(), badOpen(),
		reUp(), sessions(), stats(), openExtended(), locRIBMulti(), truncated(), garbage(),
		obmp("obmp-v4-router", netip.MustParseAddr("192.0.2.1")),
		obmp("obmp-v6-router", netip.MustParseAddr("2001:db8::1")),
	}
}

// obmp wraps every message of bmp-types and bmp-addpath in an OBMP frame from
// the router at ip. The collector time advances one second per frame.
func obmp(name string, ip netip.Addr) File {
	h := openbmp.Header{
		CollectorHash:    [16]byte{0xc0, 0x11},
		CollectorAdminID: "collector-1",
		RouterHash:       [16]byte{0xa0, 0x07},
		RouterIP:         ip,
		RouterGroup:      "synthetic",
	}
	var b []byte
	n := 0
	for _, f := range []File{types(), addPath()} {
		for rest := f.Data; len(rest) > 0; {
			l := int(binary.BigEndian.Uint32(rest[1:]))
			h.Time = epoch.Add(time.Duration(n) * time.Second).Add(123456 * time.Microsecond)
			n++
			b = append(b, OBMP(h, rest[:l])...)
			rest = rest[l:]
		}
	}
	return File{name, b}
}

// types holds every message type, and Peer Down for reasons 1-6.
func types() File {
	var s stream
	s.add(Initiation(TLV(bmp.TLVInitString, []byte("synthetic")), TLV(bmp.TLVInitSysDescr, []byte("bmpgen")),
		TLV(bmp.TLVInitSysName, []byte("router1"))))
	p := global(10, false)
	s.plainUp(p)
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	s.add(Stats(s.h(p), 2, TLV(uint16(bmp.StatRejectedPrefixes), u32(3)), TLV(uint16(bmp.StatAdjRIBIn), binary8(100))))
	s.add(Mirroring(s.h(p), TLV(bmp.TLVMirrorInformation, u16(uint16(bmp.MirrorMessagesLost))),
		TLV(bmp.TLVMirrorBGPMessage, ann(p, "198.51.100.0/24", false, 0))))
	data := map[uint8][]byte{
		1: notification(6, 4),
		2: u16(5),
		3: notification(6, 2),
	}
	for r := uint8(1); r <= 6; r++ {
		q := global(20+int(r), false)
		if r == 6 {
			q = peer(mrt.PeerLocRIB, 0, 0, "", 64500, "192.0.2.26")
			s.up(q, [][]byte{CapMP(fam4), CapAS4(routerAS)}, [][]byte{CapMP(fam4), CapAS4(routerAS)})
			s.add(PeerDown(s.h(q), r, TLV(bmp.TLVPeerDownTableName, []byte("vrf-blue"))))
			continue
		}
		s.plainUp(q)
		s.add(PeerDown(s.h(q), r, data[r]))
	}
	s.add(Termination(TLV(bmp.TLVTermString, []byte("bye")), TLV(bmp.TLVTermReason, u16(uint16(bmp.TerminationAdminClosed)))))
	return s.file("bmp-types")
}

func binary8(v uint64) []byte { return append(u32(uint32(v>>32)), u32(uint32(v))...) }

// peerFlags holds peer types 0-3 and, for types 0-2, every combination of
// V, L, A and O, plus F for Loc-RIB. Each combination has a Route Monitoring.
func peerFlags() File {
	var s stream
	for typ := mrt.PeerGlobal; typ <= mrt.PeerLocal; typ++ {
		var dist uint64
		if typ != mrt.PeerGlobal {
			dist = 0x0001000000000000 | uint64(typ)
		}
		for _, v := range []uint8{0, flagV} {
			addr, id := fmt.Sprintf("192.0.2.%d", 30+int(typ)), fmt.Sprintf("192.0.2.%d", 30+int(typ))
			if v != 0 {
				addr = fmt.Sprintf("2001:db8::%x", 30+int(typ))
			}
			base := peer(typ, v, dist, addr, bgp.ASN(64530+int(typ)), id)
			s.plainUp(base)
			for fl := uint8(0); fl < 8; fl++ {
				h := base
				h.Flags = v | (fl << 4) // L, A, O
				p := fmt.Sprintf("10.%d.%d.0/24", int(typ), fl)
				if v != 0 {
					p = fmt.Sprintf("2001:db8:%x:%x::/64", int(typ), fl)
				}
				s.add(RouteMonitoring(s.h(h), ann(h, p, false, 0)))
			}
		}
	}
	for i, f := range []uint8{0, flagF} {
		h := peer(mrt.PeerLocRIB, f, uint64(100+i), "", 64540, fmt.Sprintf("192.0.2.%d", 40+i))
		s.up(h, [][]byte{CapMP(fam4), CapAS4(64540)}, [][]byte{CapMP(fam4), CapAS4(64540)})
		s.add(RouteMonitoring(s.h(h), ann(h, fmt.Sprintf("10.3.%d.0/24", i), false, 0)))
	}
	return s.file("bmp-peer-flags")
}

// addPath holds ADD-PATH on and off, mixed per family both ways, the
// Adj-RIB-In and Adj-RIB-Out directions, and a Loc-RIB whose intent is ignored.
func addPath() File {
	var s stream
	// both: the peer sends and the router receives IPv4 and IPv6.
	p1 := global(51, false)
	s.up(p1, basicCaps(routerAS, CapAddPath(ap(fam4, 3), ap(fam6, 3))), basicCaps(p1.AS, CapAddPath(ap(fam4, 3), ap(fam6, 3))))
	s.add(RouteMonitoring(s.h(p1), ann(p1, "198.51.100.0/24", true, 1)))
	s.add(RouteMonitoring(s.h(p1), ann(p1, "2001:db8:1::/48", true, 2)))
	// off: no capability.
	p2 := global(52, false)
	s.plainUp(p2)
	s.add(RouteMonitoring(s.h(p2), ann(p2, "198.51.100.0/24", false, 0)))
	s.add(RouteMonitoring(s.h(p2), ann(p2, "2001:db8:1::/48", false, 0)))
	// IPv4 on, IPv6 off: the router receives only IPv4.
	p3 := global(53, false)
	s.up(p3, basicCaps(routerAS, CapAddPath(ap(fam4, 1))), basicCaps(p3.AS, CapAddPath(ap(fam4, 2), ap(fam6, 2))))
	s.add(RouteMonitoring(s.h(p3), ann(p3, "198.51.100.0/24", true, 3)))
	s.add(RouteMonitoring(s.h(p3), ann(p3, "2001:db8:1::/48", false, 0)))
	// IPv6 on, IPv4 off.
	p4 := global(54, false)
	s.up(p4, basicCaps(routerAS, CapAddPath(ap(fam6, 1))), basicCaps(p4.AS, CapAddPath(ap(fam4, 2), ap(fam6, 2))))
	s.add(RouteMonitoring(s.h(p4), ann(p4, "198.51.100.0/24", false, 0)))
	s.add(RouteMonitoring(s.h(p4), ann(p4, "2001:db8:1::/48", true, 4)))
	// Direction: the router sends IPv4 path identifiers to the peer (Adj-RIB-Out),
	// the peer sends none to the router (Adj-RIB-In).
	p5 := global(55, false)
	s.up(p5, basicCaps(routerAS, CapAddPath(ap(fam4, 2))), basicCaps(p5.AS, CapAddPath(ap(fam4, 1))))
	in, out, outPost := p5, p5, p5
	out.Flags, outPost.Flags = flagO, flagO|flagL
	s.add(RouteMonitoring(s.h(in), ann(in, "198.51.100.0/24", false, 0)))
	s.add(RouteMonitoring(s.h(out), ann(out, "198.51.100.0/24", true, 5)))
	s.add(RouteMonitoring(s.h(outPost), ann(outPost, "198.51.100.0/24", true, 6)))
	// Loc-RIB: the sent OPEN says receive only; the intent is ignored.
	loc := peer(mrt.PeerLocRIB, 0, 9, "", 64500, "192.0.2.56")
	locCaps := [][]byte{CapMP(fam4), CapAS4(64500), CapAddPath(ap(fam4, 1))}
	s.up(loc, locCaps, locCaps)
	s.add(RouteMonitoring(s.h(loc), ann(loc, "198.51.100.0/24", true, 7)))
	return s.file("bmp-addpath")
}

// as2 holds A = 1 peers, with an AS_PATH and AS4_PATH merge, and one whose
// per-peer-header AS is above 65535.
func as2() File {
	var s stream
	wide := peer(mrt.PeerGlobal, flagA, 0, "192.0.2.70", 196618, "192.0.2.70")
	s.up(wide, basicCaps(routerAS), [][]byte{CapMP(fam4), CapAS4(196618)})
	trans := bgp.ASN(bgp.ASTrans)
	agg := netip.MustParseAddr("192.0.2.71")
	s.add(RouteMonitoring(s.h(wide), mrtgen.Update(nil, []bgp.NLRI{nlri("198.51.100.0/24")},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP),
			mrtgen.ASPath(false, mrtgen.Seq(64500, trans, trans)),
			mrtgen.NextHop(nh4),
			mrtgen.AggregatorAttr(false, trans, agg),
			mrtgen.AS4PathAttr(mrtgen.Seq(196618, 4200000001)),
			mrtgen.AS4AggregatorAttr(196618, agg)), false)))
	small := peer(mrt.PeerGlobal, flagA, 0, "192.0.2.72", 64500, "192.0.2.72")
	s.plainUp(small)
	s.add(RouteMonitoring(s.h(small), ann(small, "198.51.100.0/24", false, 0)))
	four := peer(mrt.PeerGlobal, 0, 0, "192.0.2.73", 196619, "192.0.2.73")
	s.plainUp(four)
	s.add(RouteMonitoring(s.h(four), mrtgen.Update(nil, []bgp.NLRI{nlri("203.0.113.0/24")},
		mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP), mrtgen.ASPath(true, mrtgen.Seq(196619, 4200000001)), mrtgen.NextHop(nh4)), false)))
	return s.file("bmp-as2")
}

// ipv6Peers holds peers with IPv6 addresses.
func ipv6Peers() File {
	var s stream
	a := global(80, true)
	s.plainUp(a)
	s.add(RouteMonitoring(s.h(a), mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP),
		mrtgen.ASPath(true, mrtgen.Seq(a.AS)),
		mrtgen.MPReach(fam6, []netip.Addr{nh6, nh6ll}, []bgp.NLRI{nlri("2001:db8:80::/48")}, false)), false)))
	s.add(RouteMonitoring(s.h(a), ann(a, "198.51.100.0/24", false, 0)))
	s.add(RouteMonitoring(s.h(a), wd("2001:db8:80::/48", false, 0)))
	b := peer(mrt.PeerRD, flagV|flagA, 0x0001000000000081, "2001:db8::81", 64581, "192.0.2.81")
	s.plainUp(b)
	s.add(RouteMonitoring(s.h(b), ann(b, "2001:db8:81::/48", false, 0)))
	return s.file("bmp-ipv6-peers")
}

// eor holds IPv4 and MP End-of-RIB markers.
func eor() File {
	var s stream
	p := global(90, false)
	s.plainUp(p)
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	s.add(RouteMonitoring(s.h(p), mrtgen.Update(nil, nil, nil, false)))
	s.add(RouteMonitoring(s.h(p), ann(p, "2001:db8:90::/48", false, 0)))
	s.add(RouteMonitoring(s.h(p), mrtgen.Update(nil, nil, mrtgen.MPUnreach(fam6, nil, false), false)))
	s.add(RouteMonitoring(s.h(p), mrtgen.Update(nil, nil, mrtgen.MPUnreach(fam4m, nil, false), false)))
	return s.file("bmp-eor")
}

// noPeerUp holds messages for peers whose Peer Up never came.
func noPeerUp() File {
	var s stream
	s.add(Initiation(TLV(bmp.TLVInitSysName, []byte("router2"))))
	p, q := global(100, false), global(101, false)
	// An End-of-RIB and an UPDATE that announces and withdraws nothing, then a route, all before the Peer Up.
	s.add(RouteMonitoring(s.h(p), mrtgen.Update(nil, nil, nil, false)))
	s.add(RouteMonitoring(s.h(p), mrtgen.Update(nil, nil, mrtgen.Attrs(mrtgen.Origin(bgp.OriginIGP),
		mrtgen.ASPath(true, mrtgen.Seq(64501)), mrtgen.NextHop(nh4)), false)))
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	s.add(RouteMonitoring(s.h(q), ann(q, "203.0.113.0/24", false, 0)))
	s.add(Stats(s.h(p), 1, TLV(uint16(bmp.StatRejectedPrefixes), u32(1))))
	// The Peer Up comes late, with IPv4 ADD-PATH negotiated in the in direction.
	s.up(p, basicCaps(routerAS, CapAddPath(ap(fam4, 1))), basicCaps(p.AS, CapAddPath(ap(fam4, 2))))
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", true, 9)))
	s.add(PeerDown(s.h(q), uint8(bmp.PeerDownRemoteNoData), nil))
	return s.file("bmp-no-peer-up")
}

// badOpen holds Peer Ups with a broken OPEN, with a Warning capability and
// with a frame that does not hold.
func badOpen() File {
	var s stream
	route := func(h bmp.PeerHeader) { s.add(RouteMonitoring(s.h(h), ann(h, "198.51.100.0/24", false, 0))) }

	// The optional parameters run past the OPEN: an Error diagnostic.
	p1 := global(110, false)
	broken := peerOpen(p1, basicCaps(p1.AS)...)
	broken[28] = 200
	s.add(PeerUp(s.h(p1), local4, 179, 40001, routerOpen(basicCaps(routerAS)...), broken))
	route(p1)

	// A Multiprotocol capability with a 3-byte value: a Warning.
	p2 := global(111, false)
	s.up(p2, basicCaps(routerAS), [][]byte{Capability(1, []byte{0, 1, 1}), CapAS4(p2.AS)})
	route(p2)

	// An ADD-PATH tuple with Send/Receive 7: a Warning, and the capability is ignored.
	p3 := global(112, false)
	s.up(p3, basicCaps(routerAS, CapAddPath(ap(fam4, 1))), basicCaps(p3.AS, CapAddPath(ap(fam4, 7))))
	route(p3)

	// The sent OPEN's length runs past the Peer Up: its frame does not hold.
	p4 := global(113, false)
	long := routerOpen(basicCaps(routerAS)...)
	long[16] = 0x01 // length 256 and more
	s.add(PeerUp(s.h(p4), local4, 179, 40004, long, peerOpen(p4, basicCaps(p4.AS)...)))
	route(p4)

	// Nothing after the per-peer header but five bytes.
	p5 := global(114, false)
	s.add(Message(bmp.TypePeerUp, append(PeerHeader(s.h(p5)), 0, 0, 0, 0, 0)))
	route(p5)
	return s.file("bmp-bad-open")
}

// reUp holds a Peer Down and a re-Up of the same peer with different ADD-PATH.
func reUp() File {
	var s stream
	p := global(120, false)
	s.up(p, basicCaps(routerAS, CapAddPath(ap(fam4, 1))), basicCaps(p.AS, CapAddPath(ap(fam4, 2))))
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", true, 7)))
	s.add(PeerDown(s.h(p), uint8(bmp.PeerDownRemoteNotification), notification(6, 4)))
	s.plainUp(p)
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	s.add(PeerDown(s.h(p), uint8(bmp.PeerDownRemoteNoData), nil))
	s.up(p, basicCaps(routerAS, CapAddPath(ap(fam4, 1))), basicCaps(p.AS, CapAddPath(ap(fam4, 2))))
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", true, 8)))
	// A re-Up with no Peer Down between replaces the state too.
	s.plainUp(p)
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	return s.file("bmp-reup")
}

// sessions holds a Termination, then a new Initiation and a new session.
func sessions() File {
	var s stream
	p := global(130, false)
	for i := 0; i < 2; i++ {
		s.add(Initiation(TLV(bmp.TLVInitSysName, []byte(fmt.Sprintf("session-%d", i)))))
		s.plainUp(p)
		s.add(RouteMonitoring(s.h(p), ann(p, fmt.Sprintf("198.51.%d.0/24", 100+i), false, 0)))
		s.add(Termination(TLV(bmp.TLVTermReason, u16(uint16(bmp.TerminationUnspecified)))))
	}
	return s.file("bmp-sessions")
}

// stats holds statistics of every registered shape, unregistered types, wrong
// lengths, and counts that do not match.
func stats() File {
	var s stream
	p := global(140, false)
	s.plainUp(p)
	gauge, family := binary8(42), append(u16(1), append([]byte{1}, binary8(42)...)...)
	counters := []uint16{0, 1, 2, 3, 4, 5, 6, 11, 12, 13}
	gauges := []uint16{7, 8, 14, 15, 18, 20, 29, 31, 33, 39}
	var every [][]byte
	for t := uint16(0); t <= 43; t++ {
		switch {
		case t == 24 || t == 25: // expired temporary registrations
		case slices.Contains(counters, t):
			every = append(every, TLV(t, u32(uint32(t)+1)))
		case slices.Contains(gauges, t):
			every = append(every, TLV(t, gauge))
		default:
			every = append(every, TLV(t, family))
		}
	}
	s.add(Stats(s.h(p), uint32(len(every)), every...))
	s.add(Stats(s.h(p), 3, TLV(0, u32(5)), TLV(7, gauge), TLV(9, family)))
	// Unregistered: far outside the registry, and the expired type 24.
	s.add(Stats(s.h(p), 2, TLV(9999, []byte{1, 2, 3, 4, 5}), TLV(24, u32(1))))
	// Registered types with the wrong length.
	s.add(Stats(s.h(p), 3, TLV(0, []byte{1, 2, 3}), TLV(7, u32(1)), TLV(9, gauge)))
	// The count states 5 and 2 are present; then 0 and 1.
	s.add(Stats(s.h(p), 5, TLV(0, u32(1)), TLV(1, u32(2))))
	s.add(Stats(s.h(p), 0, TLV(0, u32(1))))
	// A TLV that runs past the message.
	s.add(Stats(s.h(p), 1, []byte{0, 0, 0, 200, 1}))
	s.add(Stats(s.h(p), 0))
	return s.file("bmp-stats")
}

// openExtended holds Peer Ups whose OPENs use the RFC 9072 form.
func openExtended() File {
	var s stream
	a := global(150, false)
	s.add(PeerUp(s.h(a), local4, 179, 40001,
		OpenExtended(routerAS, routerID, basicCaps(routerAS, CapAddPath(ap(fam4, 3)))...),
		OpenExtended(a.AS, a.BGPID, basicCaps(a.AS, CapAddPath(ap(fam4, 3)))...)))
	s.add(RouteMonitoring(s.h(a), ann(a, "198.51.100.0/24", true, 1)))
	b := global(151, false)
	s.add(PeerUp(s.h(b), local4, 179, 40002, OpenExtended(routerAS, routerID), OpenExtended(b.AS, b.BGPID)))
	s.add(RouteMonitoring(s.h(b), ann(b, "198.51.100.0/24", false, 0)))
	c := peer(mrt.PeerGlobal, 0, 0, "192.0.2.152", 196618, "192.0.2.152")
	s.add(PeerUp(s.h(c), local4, 179, 40003,
		routerOpen(basicCaps(routerAS, CapAddPath(ap(fam4, 1)))...),
		OpenExtended(c.AS, c.BGPID, basicCaps(c.AS, CapAddPath(ap(fam4, 2)))...)))
	s.add(RouteMonitoring(s.h(c), ann(c, "198.51.100.0/24", true, 2)))
	return s.file("bmp-open-extended")
}

// locRIBMulti holds two emulated Loc-RIB peers that share a key, one per family.
func locRIBMulti() File {
	var s stream
	l := peer(mrt.PeerLocRIB, 0, 5, "", 64500, "192.0.2.160")
	s.add(PeerUp(s.h(l), netip.Addr{}, 0, 0,
		routerOpen(CapMP(fam4), CapAS4(64500), CapAddPath(ap(fam4, 3))),
		routerOpen(CapMP(fam4), CapAS4(64500), CapAddPath(ap(fam4, 3))),
		TLV(bmp.TLVPeerUpTableName, []byte("default"))))
	s.add(PeerUp(s.h(l), netip.Addr{}, 0, 0,
		routerOpen(CapMP(fam6), CapAS4(64500)), routerOpen(CapMP(fam6), CapAS4(64500)),
		TLV(bmp.TLVPeerUpTableName, []byte("default"))))
	s.add(RouteMonitoring(s.h(l), ann(l, "198.51.100.0/24", true, 1)))
	s.add(RouteMonitoring(s.h(l), ann(l, "2001:db8:160::/48", false, 0)))
	s.add(RouteMonitoring(s.h(l), wd("198.51.100.0/24", true, 1)))
	// No Multiprotocol capability: IPv4 unicast (RFC 4760 section 8), here without ADD-PATH.
	s.add(PeerUp(s.h(l), netip.Addr{}, 0, 0, routerOpen(CapAS4(64500)), routerOpen(CapAS4(64500)),
		TLV(bmp.TLVPeerUpTableName, []byte("default"))))
	s.add(RouteMonitoring(s.h(l), ann(l, "198.51.100.0/24", false, 0)))
	return s.file("bmp-locrib-multi")
}

// truncated ends in a Peer Up cut after its sent OPEN, a field boundary.
func truncated() File {
	var s stream
	p := global(170, false)
	s.plainUp(p)
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	q := global(171, false)
	sent := routerOpen(basicCaps(routerAS)...)
	whole := PeerUp(s.h(q), local4, 179, 40002, sent, peerOpen(q, basicCaps(q.AS)...))
	s.add(whole[:6+42+20+len(sent)])
	return s.file("bmp-truncated")
}

// garbage holds a length below 6, a version 2, a message over a 1 KiB limit,
// junk between messages, peer type 9 and message type 9. Read with a 1 KiB
// limit it has five errors: length 3, version 2, oversize, and two zero headers.
func garbage() File {
	var s stream
	p := global(180, false)
	s.add(Initiation(TLV(bmp.TLVInitSysName, []byte("garbage"))))
	s.plainUp(p)
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.100.0/24", false, 0)))
	s.add([]byte{3, 0, 0, 0, 3, 4}) // length 3
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.101.0/24", false, 0)))
	v2 := Initiation(TLV(bmp.TLVInitSysName, []byte("old")))
	v2[0] = 2
	s.add(v2)
	s.add(make([]byte, 12)) // junk: two headers of length 0
	s.add(Initiation(TLV(bmp.TLVInitString, slices.Repeat([]byte("x"), 2000))))
	nine := peer(9, 0, 0, "192.0.2.181", 64581, "192.0.2.181")
	s.add(RouteMonitoring(s.h(nine), ann(nine, "198.51.102.0/24", false, 0)))
	s.add(Message(9, []byte("a message type BMP v3 does not define")))
	s.add(RouteMonitoring(s.h(p), ann(p, "198.51.103.0/24", false, 0)))
	return s.file("bmp-garbage")
}
