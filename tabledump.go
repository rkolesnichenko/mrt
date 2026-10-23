package mrt

import (
	"encoding/binary"
	"fmt"
	"iter"
	"net/netip"
	"time"

	"github.com/rkolesnichenko/mrt/bgp"
)

// Peer is a BGP peer, from a PEER_INDEX_TABLE or a legacy TABLE_DUMP record,
// a BGP4MP record (Addr and AS only), or, in package bmp, a BMP per-peer
// header.
type Peer struct {
	BGPID netip.Addr // zero when the record does not carry one
	Addr  netip.Addr
	AS    bgp.ASN
	// Type is the BMP peer type. BMP only: zero (the global instance) on
	// every MRT peer.
	Type PeerType
	// Distinguisher is the BMP peer distinguisher. BMP only: zero on every
	// MRT peer.
	Distinguisher uint64
}

// PeerIndexTable is TABLE_DUMP_V2 PEER_INDEX_TABLE (RFC 6396 section 4.3.1).
// RIB entries refer to Peers by index.
type PeerIndexTable struct {
	record
	CollectorID netip.Addr
	ViewName    string
	Peers       []Peer
}

// RIB is a TABLE_DUMP_V2 RIB record: one prefix and the entries for it, one
// per peer. The entries stay undecoded until Entries is called.
type RIB struct {
	record
	Sequence uint32
	Family   bgp.Family
	Prefix   netip.Prefix // zero for RIB_GENERIC with a family other than IPv4/IPv6 unicast/multicast
	PathID   uint32       // RIB_GENERIC_ADDPATH: carried in the NLRI
	RawNLRI  []byte       // RIB_GENERIC: the NLRI as written, or every byte after the SAFI for a family without prefixes
	AddPath  bool

	count       uint16
	entries     []byte // everything after the entry count
	entryPathID bool   // each entry carries a path identifier (subtypes 8-11)
}

// RIBEntry is one peer's route to a RIB's prefix.
type RIBEntry struct {
	PeerIndex  uint16
	Originated time.Time
	PathID     uint32    // add-path subtypes only
	Attrs      bgp.Attrs // Context{AS4: true, RIB: true}
}

// TableDump is a legacy TABLE_DUMP record (type 12, RFC 6396 section 4.2).
type TableDump struct {
	record
	View       uint16
	Sequence   uint16
	Prefix     netip.Prefix
	Status     uint8
	Originated time.Time
	Peer       Peer
	Attrs      bgp.Attrs // Context{AS4: false, RIB: true}
}

// EntryCount is the number of entries as the record states it.
func (r RIB) EntryCount() uint16 { return r.count }

// Entries walks the RIB entries lazily, in order, and can be called again.
// An entry whose framing is broken yields one error (wrapping ErrMalformed)
// and ends the walk; the entries before it are already yielded. Bytes after
// the last counted entry are ignored.
func (r RIB) Entries() iter.Seq2[RIBEntry, error] {
	return func(yield func(RIBEntry, error) bool) {
		b := r.entries
		fixed := 8 // peer index, originated time, attribute length
		if r.entryPathID {
			fixed = 12
		}
		for i := 0; i < int(r.count); i++ {
			if len(b) < fixed {
				yield(RIBEntry{}, fmt.Errorf("%w: RIB entry %d of %d runs past the record", ErrMalformed, i+1, r.count))
				return
			}
			e := RIBEntry{
				PeerIndex:  binary.BigEndian.Uint16(b),
				Originated: time.Unix(int64(binary.BigEndian.Uint32(b[2:])), 0).UTC(),
			}
			off := 6
			if r.entryPathID {
				e.PathID = binary.BigEndian.Uint32(b[6:])
				off = 10
			}
			alen := int(binary.BigEndian.Uint16(b[off:]))
			off += 2
			if len(b)-off < alen {
				yield(RIBEntry{}, fmt.Errorf("%w: RIB entry %d attributes (%d bytes) run past the record", ErrMalformed, i+1, alen))
				return
			}
			e.Attrs = bgp.NewAttrs(b[off:off+alen:off+alen], bgp.Context{AS4: true, RIB: true})
			b = b[off+alen:]
			if !yield(e, nil) {
				return
			}
		}
	}
}

func short(what string) error { return fmt.Errorf("%w: %s runs past the record", ErrMalformed, what) }

// addrOf reads a 4-byte or, when v6, 16-byte address from the front of b.
func addrOf(b []byte, v6 bool) (netip.Addr, []byte, bool) {
	n := 4
	if v6 {
		n = 16
	}
	if len(b) < n {
		return netip.Addr{}, b, false
	}
	if v6 {
		return netip.AddrFrom16([16]byte(b[:16])), b[16:], true
	}
	return netip.AddrFrom4([4]byte(b[:4])), b[4:], true
}

// prefixOf builds the masked prefix of plen bits from the bytes of its
// ceil(plen/8) bytes.
func prefixOf(pb []byte, plen int, v6 bool) netip.Prefix {
	var a netip.Addr
	if v6 {
		var x [16]byte
		copy(x[:], pb)
		a = netip.AddrFrom16(x)
	} else {
		var x [4]byte
		copy(x[:], pb)
		a = netip.AddrFrom4(x)
	}
	return netip.PrefixFrom(a, plen).Masked()
}

func decodePeerIndexTable(h Header, raw []byte) (Record, error) {
	b := raw[12:]
	if len(b) < 8 {
		return nil, short("PEER_INDEX_TABLE header")
	}
	t := PeerIndexTable{record: record{hdr: h, raw: raw}}
	t.CollectorID = netip.AddrFrom4([4]byte(b[:4]))
	nameLen := int(binary.BigEndian.Uint16(b[4:]))
	b = b[6:]
	if len(b) < nameLen+2 {
		return nil, short("view name")
	}
	t.ViewName = string(b[:nameLen])
	count := int(binary.BigEndian.Uint16(b[nameLen:]))
	b = b[nameLen+2:]
	// A peer takes at least 11 bytes: type, BGP ID, IPv4 address, 2-byte AS.
	if count*11 > len(b) {
		return nil, fmt.Errorf("%w: %d peers do not fit in %d bytes", ErrMalformed, count, len(b))
	}
	t.Peers = make([]Peer, 0, count)
	for i := 0; i < count; i++ {
		if len(b) < 5 {
			return nil, short("peer entry")
		}
		pt := b[0]
		var p Peer
		p.BGPID = netip.AddrFrom4([4]byte(b[1:5]))
		var ok bool
		if p.Addr, b, ok = addrOf(b[5:], pt&0x01 != 0); !ok {
			return nil, short("peer address")
		}
		if pt&0x02 != 0 {
			if len(b) < 4 {
				return nil, short("peer AS")
			}
			p.AS, b = bgp.ASN(binary.BigEndian.Uint32(b)), b[4:]
		} else {
			if len(b) < 2 {
				return nil, short("peer AS")
			}
			p.AS, b = bgp.ASN(binary.BigEndian.Uint16(b)), b[2:]
		}
		t.Peers = append(t.Peers, p)
	}
	return t, nil
}

func famOfRIBSubtype(s Subtype) (bgp.Family, bool) {
	switch s {
	case SubRIBIPv4Unicast, SubRIBIPv4UnicastAddPath:
		return bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIUnicast}, true
	case SubRIBIPv4Multicast, SubRIBIPv4MulticastAddPath:
		return bgp.Family{AFI: bgp.AFIIPv4, SAFI: bgp.SAFIMulticast}, true
	case SubRIBIPv6Unicast, SubRIBIPv6UnicastAddPath:
		return bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIUnicast}, true
	case SubRIBIPv6Multicast, SubRIBIPv6MulticastAddPath:
		return bgp.Family{AFI: bgp.AFIIPv6, SAFI: bgp.SAFIMulticast}, true
	}
	return bgp.Family{}, false
}

func decodeRIB(h Header, raw []byte) (Record, error) {
	b := raw[12:]
	r := RIB{record: record{hdr: h, raw: raw}}
	if len(b) < 4 {
		return nil, short("RIB sequence number")
	}
	r.Sequence, b = binary.BigEndian.Uint32(b), b[4:]
	if h.Subtype == SubRIBGeneric || h.Subtype == SubRIBGenericAddPath {
		r.AddPath = h.Subtype == SubRIBGenericAddPath
		if len(b) < 3 {
			return nil, short("RIB_GENERIC family")
		}
		r.Family = bgp.Family{AFI: bgp.AFI(binary.BigEndian.Uint16(b)), SAFI: bgp.SAFI(b[2])}
		b = b[3:]
		if !r.Family.HasPrefixes() {
			// The NLRI does not describe its own length (Ruling R11).
			r.RawNLRI = b[:len(b):len(b)]
			return r, nil
		}
		skip := 0
		if r.AddPath {
			skip = 4
		}
		if len(b) < skip+1 {
			return nil, short("RIB_GENERIC NLRI")
		}
		plen := int(b[skip])
		if plen > famBits(r.Family) {
			return nil, fmt.Errorf("%w: prefix length %d", ErrMalformed, plen)
		}
		n := skip + 1 + (plen+7)/8
		if len(b) < n {
			return nil, short("RIB_GENERIC NLRI")
		}
		nlri, diags := bgp.ParseNLRI(b[:n], r.Family, r.AddPath)
		for _, d := range diags {
			if d.Severity == bgp.SeverityError {
				return nil, fmt.Errorf("%w: RIB_GENERIC NLRI: %s", ErrMalformed, d.Message)
			}
		}
		if len(nlri) != 1 {
			return nil, fmt.Errorf("%w: RIB_GENERIC NLRI", ErrMalformed)
		}
		r.Prefix, r.PathID = nlri[0].Prefix, nlri[0].PathID
		r.RawNLRI = b[:n:n]
		b = b[n:]
	} else {
		fam, ok := famOfRIBSubtype(h.Subtype)
		if !ok {
			return nil, fmt.Errorf("%w: no RIB layout for subtype %d", ErrMalformed, h.Subtype)
		}
		r.Family = fam
		r.AddPath = h.Subtype >= SubRIBIPv4UnicastAddPath
		r.entryPathID = r.AddPath
		if len(b) < 1 {
			return nil, short("prefix length")
		}
		plen := int(b[0])
		if plen > famBits(fam) {
			return nil, fmt.Errorf("%w: prefix length %d", ErrMalformed, plen)
		}
		nb := (plen + 7) / 8
		if len(b) < 1+nb {
			return nil, short("prefix")
		}
		r.Prefix = prefixOf(b[1:1+nb], plen, fam.AFI == bgp.AFIIPv6)
		b = b[1+nb:]
	}
	if len(b) < 2 {
		return nil, short("entry count")
	}
	r.count, r.entries = binary.BigEndian.Uint16(b), b[2:]
	return r, nil
}

func famBits(f bgp.Family) int {
	if f.AFI == bgp.AFIIPv6 {
		return 128
	}
	return 32
}

func decodeTableDump(h Header, raw []byte) (Record, error) {
	v6 := h.Subtype == SubAFIIPv6
	b := raw[12:]
	if len(b) < 4 {
		return nil, short("TABLE_DUMP view and sequence")
	}
	t := TableDump{record: record{hdr: h, raw: raw}}
	t.View, t.Sequence, b = binary.BigEndian.Uint16(b), binary.BigEndian.Uint16(b[2:]), b[4:]
	pfx, b, ok := addrOf(b, v6)
	if !ok {
		return nil, short("prefix")
	}
	if len(b) < 6 {
		return nil, short("prefix length and status")
	}
	plen := int(b[0])
	if plen > pfx.BitLen() {
		return nil, fmt.Errorf("%w: prefix length %d", ErrMalformed, plen)
	}
	t.Prefix = netip.PrefixFrom(pfx, plen).Masked()
	t.Status = b[1]
	t.Originated = time.Unix(int64(binary.BigEndian.Uint32(b[2:])), 0).UTC()
	b = b[6:]
	if t.Peer.Addr, b, ok = addrOf(b, v6); !ok {
		return nil, short("peer address")
	}
	if len(b) < 4 {
		return nil, short("peer AS and attribute length")
	}
	t.Peer.AS = bgp.ASN(binary.BigEndian.Uint16(b))
	alen := int(binary.BigEndian.Uint16(b[2:]))
	b = b[4:]
	if len(b) < alen {
		return nil, short("attributes")
	}
	t.Attrs = bgp.NewAttrs(b[:alen:alen], bgp.Context{AS4: false, RIB: true})
	return t, nil
}
