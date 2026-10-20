// Command heldsize is a throwaway measurement tool, not a public API: it reads
// full RIBs and reports how much memory the decoded paths take when identical
// ones are shared (v0.2 design §5, §6.1, §6.4).
//
//	go run ./internal/heldsize FILE...
//
// Every RIB entry of every file (TABLE_DUMP_V2 and TABLE_DUMP) is keyed by its
// context flags and attribute block, with the NLRI of a full-form MP_REACH
// removed (R20: the record supplies the prefix) and its length normalised.
// One bgp.Path clone is kept per key; AS paths and community lists are
// canonicalised so equal ones share a slice. A 16-byte stand-in per route
// (path pointer, peer index, prefix id) plays the table keeper.
package main

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bgp"
)

// standIn is what the table keeper would hold per route.
type standIn struct {
	path   *bgp.Path
	peer   uint32
	prefix uint32
}

const chunkSize = 1 << 20

type state struct {
	paths   map[string]*bgp.Path // nil value: a block that gets no entry
	asPaths map[string][]bgp.Segment
	comms   map[string][]bgp.Community
	exts    map[string][]bgp.ExtCommunity
	larges  map[string][]bgp.LargeCommunity

	table  [][]standIn
	routes uint64
	nextID uint32

	keyBytes   uint64
	infoOnly   uint64 // distinct blocks whose decode has Info diagnostics only
	skipped    uint64 // routes whose block has a Warning or Error
	skippedKey uint64 // distinct such blocks
	recErrors  uint64

	key []byte
	tmp []byte
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: heldsize FILE...")
		os.Exit(2)
	}
	s := &state{
		paths:   make(map[string]*bgp.Path),
		asPaths: make(map[string][]bgp.Segment),
		comms:   make(map[string][]bgp.Community),
		exts:    make(map[string][]bgp.ExtCommunity),
		larges:  make(map[string][]bgp.LargeCommunity),
	}
	for _, name := range os.Args[1:] {
		if err := s.file(name); err != nil {
			fmt.Fprintf(os.Stderr, "heldsize: %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("after %s: routes=%d distinct_paths=%d\n", name, s.routes, len(s.paths))
	}
	s.report()
}

func (s *state) file(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := mrt.Decompress(f)
	if err != nil {
		return err
	}
	for rec, err := range mrt.Records(r) {
		if err != nil {
			s.recErrors++
			continue
		}
		switch r := rec.(type) {
		case mrt.RIB:
			for e, err := range r.Entries() {
				if err != nil {
					s.recErrors++
					continue
				}
				s.add(e.Attrs, true, uint32(e.PeerIndex))
			}
		case mrt.TableDump:
			s.add(r.Attrs, false, uint32(r.Peer.AS))
		}
	}
	return nil
}

func (s *state) add(a bgp.Attrs, as4 bool, peer uint32) {
	s.key = appendKey(s.key[:0], a.Raw(), as4)
	p, ok := s.paths[string(s.key)]
	if !ok {
		k := string(s.key)
		s.keyBytes += uint64(len(k))
		p = s.decode(a)
		s.paths[k] = p
	}
	if p == nil {
		s.skipped++
		return
	}
	if len(s.table) == 0 || len(s.table[len(s.table)-1]) == chunkSize {
		s.table = append(s.table, make([]standIn, 0, chunkSize))
	}
	last := len(s.table) - 1
	s.table[last] = append(s.table[last], standIn{path: p, peer: peer, prefix: s.nextID})
	s.nextID++
	s.routes++
}

// decode returns the shared path for a block, or nil when the block has a
// Warning or Error. Info-only blocks are kept (and counted).
func (s *state) decode(a bgp.Attrs) *bgp.Path {
	path, diags := a.Decode()
	info := false
	for _, d := range diags {
		if d.Severity >= bgp.SeverityWarning {
			s.skippedKey++
			return nil
		}
		info = true
	}
	if info {
		s.infoOnly++
	}
	c := path.Clone()
	s.share(&c.ASPath.Segments)
	s.share(&c.ReceivedASPath.Segments)
	s.share(&c.AS4Path.Segments)
	if len(c.Communities) > 0 {
		s.tmp = s.tmp[:0]
		for _, v := range c.Communities {
			s.tmp = append(s.tmp, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		}
		if q, ok := s.comms[string(s.tmp)]; ok {
			c.Communities = q
		} else {
			s.comms[string(s.tmp)] = c.Communities
		}
	}
	if len(c.ExtCommunities) > 0 {
		s.tmp = s.tmp[:0]
		for _, v := range c.ExtCommunities {
			s.tmp = append(s.tmp, v[:]...)
		}
		if q, ok := s.exts[string(s.tmp)]; ok {
			c.ExtCommunities = q
		} else {
			s.exts[string(s.tmp)] = c.ExtCommunities
		}
	}
	if len(c.LargeCommunities) > 0 {
		s.tmp = s.tmp[:0]
		for _, v := range c.LargeCommunities {
			for _, w := range [3]uint32{v.Global, v.Local1, v.Local2} {
				s.tmp = append(s.tmp, byte(w>>24), byte(w>>16), byte(w>>8), byte(w))
			}
		}
		if q, ok := s.larges[string(s.tmp)]; ok {
			c.LargeCommunities = q
		} else {
			s.larges[string(s.tmp)] = c.LargeCommunities
		}
	}
	return &c
}

func (s *state) share(segs *[]bgp.Segment) {
	if len(*segs) == 0 {
		return
	}
	s.tmp = s.tmp[:0]
	for _, g := range *segs {
		s.tmp = append(s.tmp, byte(g.Type), byte(len(g.ASNs)>>8), byte(len(g.ASNs)))
		for _, n := range g.ASNs {
			s.tmp = append(s.tmp, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		}
	}
	if q, ok := s.asPaths[string(s.tmp)]; ok {
		*segs = q
		return
	}
	s.asPaths[string(s.tmp)] = *segs
}

// appendKey appends the context flags and the attribute block to dst; a
// full-form MP_REACH (first value byte zero) loses its NLRI and gets a
// normalised header (extended length, two-byte length).
func appendKey(dst, raw []byte, as4 bool) []byte {
	if as4 {
		dst = append(dst, 1)
	} else {
		dst = append(dst, 0)
	}
	dst = append(dst, 1) // RIB
	for off := 0; off < len(raw); {
		start := off
		if len(raw)-off < 3 {
			return append(dst, raw[start:]...)
		}
		flags, code := raw[off], raw[off+1]
		var alen, hdr int
		if flags&0x10 != 0 {
			if len(raw)-off < 4 {
				return append(dst, raw[start:]...)
			}
			alen, hdr = int(raw[off+2])<<8|int(raw[off+3]), 4
		} else {
			alen, hdr = int(raw[off+2]), 3
		}
		if len(raw)-off-hdr < alen {
			return append(dst, raw[start:]...)
		}
		val := raw[off+hdr : off+hdr+alen]
		off += hdr + alen
		if code == 14 && len(val) >= 5 && val[0] == 0 && len(val) >= 5+int(val[3]) {
			keep := val[:5+int(val[3])]
			dst = append(dst, flags|0x10, code, byte(len(keep)>>8), byte(len(keep)))
			dst = append(dst, keep...)
			continue
		}
		dst = append(dst, raw[start:off]...)
	}
	return dst
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

func (s *state) report() {
	distinct := uint64(0)
	for _, p := range s.paths {
		if p != nil {
			distinct++
		}
	}
	fmt.Printf("record errors: %d\n", s.recErrors)
	fmt.Printf("routes: %d\n", s.routes)
	fmt.Printf("routes skipped (block with Warning/Error): %d, distinct such blocks: %d\n", s.skipped, s.skippedKey)
	fmt.Printf("distinct paths: %d (map entries incl. skipped: %d)\n", distinct, len(s.paths))
	fmt.Printf("distinct AS paths (ASPath, ReceivedASPath, AS4Path together): %d\n", len(s.asPaths))
	fmt.Printf("distinct community lists: %d, extended: %d, large: %d\n", len(s.comms), len(s.exts), len(s.larges))
	fmt.Printf("Info-only blocks (distinct): %d\n", s.infoOnly)
	fmt.Printf("summed key bytes: %d\n", s.keyBytes)
	fmt.Printf("sizeof bgp.Path: %d, stand-in: %d\n", unsafe.Sizeof(bgp.Path{}), unsafe.Sizeof(standIn{}))
	fmt.Printf("stand-in table bytes (routes*16): %d\n", s.routes*16)

	fmt.Printf("HeapInuse, everything live: %d\n", heap())

	// A slice of the distinct paths (8 bytes each) keeps them reachable once the map goes.
	keep := make([]*bgp.Path, 0, distinct)
	for _, p := range s.paths {
		if p != nil {
			keep = append(keep, p)
		}
	}
	keepBytes := uint64(len(keep)) * 8
	fmt.Printf("keep slice bytes (subtract below): %d\n", keepBytes)

	// Reading 2: table dropped; map, canonical maps and paths remain (what mrt would retain).
	s.table = nil
	h2 := heap()
	fmt.Printf("HeapInuse, stand-in table dropped (shared paths + map + canonical maps, incl. keep slice): %d\n", h2)

	// Reading 3: path map dropped as well; paths and canonical maps remain, held by keep.
	s.paths = nil
	h3 := heap()
	fmt.Printf("HeapInuse, path map dropped (paths + canonical maps, incl. keep slice): %d, without keep slice: %d\n", h3, h3-keepBytes)

	// Reading 4: everything dropped; the baseline.
	runtime.KeepAlive(keep)
	keep = nil
	s.asPaths, s.comms, s.exts, s.larges = nil, nil, nil, nil
	fmt.Printf("HeapInuse, all dropped (baseline): %d\n", heap())
}
