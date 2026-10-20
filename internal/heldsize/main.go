// Command heldsize is a throwaway measurement tool, not a public API: it reads
// full RIBs and reports how much memory the decoded paths take when identical
// ones are shared (v0.2 design §5, §6.1, §6.4).
//
//	go run ./internal/heldsize FILE...
//
// Every RIB entry of every file (TABLE_DUMP_V2 and TABLE_DUMP) is keyed by its
// context flags and attribute block, with the NLRI of a full-form MP_REACH
// removed (R20: the record supplies the prefix) and its length normalised.
// One bgp.Path clone is kept per key; AS paths (the ASPath field) and community
// and large-community lists are canonicalised so equal ones share a slice. What
// sits in Path's rare part (AS4_PATH, the received AS_PATH, extended
// communities, ...) cannot be written from outside package bgp and is not shared. A 16-byte stand-in per route
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

// classes are the allocator's size classes (MemStats.BySize), set by readMem.
var classes []uint64

// readMem collects garbage twice and returns HeapInuse (spans in use) and
// HeapAlloc (live bytes).
func readMem() (inuse, alloc uint64) {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if classes == nil {
		for _, b := range m.BySize {
			if b.Size > 0 {
				classes = append(classes, uint64(b.Size))
			}
		}
	}
	return m.HeapInuse, m.HeapAlloc
}

// class is the bytes the allocator spends on an n-byte object: the next size
// class, or whole 8 KiB pages above the largest class.
func class(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	for _, c := range classes {
		if n <= c {
			return c
		}
	}
	return (n + 8191) &^ 8191
}

// measuredClass allocates 1M objects of T, keeps them, and returns the
// measured live bytes per object, to check class(sizeof T).
func measuredClass[T any]() float64 {
	_, before := readMem()
	keep := make([]*T, 1<<20)
	for i := range keep {
		keep[i] = new(T)
	}
	_, after := readMem()
	n := float64(after-before-uint64(len(keep))*8) / float64(len(keep))
	runtime.KeepAlive(keep)
	return n
}

// rareSize is unsafe.Sizeof of bgp's unexported pathRare (144 bytes on 64-bit
// platforms), which this package cannot name. It only feeds the computed
// breakdown; the HeapAlloc readings are measured.
const rareSize = 144

// hasRare reports whether p has a rare part: any of the method-read attributes
// but NextHop is set.
func hasRare(p *bgp.Path) bool {
	_, agg := p.Aggregator()
	return agg || len(p.AS4Path().Segments) > 0 || p.ExtCommunities() != nil || p.OriginatorID().IsValid() ||
		p.ClusterList() != nil || p.MPUnreach() != nil || p.Unknown() != nil || receivedIfMerged(p).Segments != nil
}

// receivedIfMerged is the received AS_PATH when it is held apart from ASPath.
func receivedIfMerged(p *bgp.Path) bgp.ASPath {
	r := p.ReceivedASPath()
	if len(r.Segments) > 0 && len(p.ASPath.Segments) > 0 && &r.Segments[0] == &p.ASPath.Segments[0] {
		return bgp.ASPath{}
	}
	return r
}

func sliceBytes[T any](s []T) uint64 {
	var z T
	return class(uint64(cap(s)) * uint64(unsafe.Sizeof(z)))
}

func (s *state) report() {
	readMem() // fills classes
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
	fmt.Printf("distinct AS paths, ASPath role only (canonical map size): %d\n", len(s.asPaths))
	fmt.Printf("distinct community lists: %d, large: %d (extended: in the rare part, not shared)\n", len(s.comms), len(s.larges))
	fmt.Printf("Info-only blocks (distinct): %d\n", s.infoOnly)
	fmt.Printf("summed key bytes (raw lengths): %d\n", s.keyBytes)
	fmt.Printf("sizeof bgp.Path: %d, MPReach: %d, MPUnreach: %d, stand-in: %d\n", unsafe.Sizeof(bgp.Path{}), unsafe.Sizeof(bgp.MPReach{}), unsafe.Sizeof(bgp.MPUnreach{}), unsafe.Sizeof(standIn{}))
	fmt.Printf("size class of Path: %d (computed), %.1f (measured per object over 1M allocations)\n", class(uint64(unsafe.Sizeof(bgp.Path{}))), measuredClass[bgp.Path]())
	fmt.Printf("stand-in table bytes (routes*16): %d\n", s.routes*16)

	s.breakdown(distinct)

	_, a1 := readMem()
	h1, _ := readMem()
	fmt.Printf("reading 1, everything live: HeapInuse %d, HeapAlloc %d\n", h1, a1)

	// A slice of the distinct paths (8 bytes each) keeps them reachable once the map goes.
	keep := make([]*bgp.Path, 0, distinct)
	for _, p := range s.paths {
		if p != nil {
			keep = append(keep, p)
		}
	}
	keepBytes := class(uint64(cap(keep)) * 8)
	fmt.Printf("keep slice bytes (subtracted below): %d\n", keepBytes)

	s.table = nil
	h2, a2 := readMem()
	fmt.Printf("reading 2, stand-in table dropped (paths + path map + canonical maps; keep slice subtracted): HeapInuse %d, HeapAlloc %d\n", h2-keepBytes, a2-keepBytes)

	s.paths = nil
	h3, a3 := readMem()
	fmt.Printf("reading 3, path map dropped too (paths + canonical maps; keep slice subtracted): HeapInuse %d, HeapAlloc %d\n", h3-keepBytes, a3-keepBytes)
	fmt.Printf("path map cost, reading 2 minus reading 3: HeapInuse %d, HeapAlloc %d\n", h2-h3, a2-a3)

	runtime.KeepAlive(keep)
	keep = nil
	s.asPaths, s.comms, s.larges = nil, nil, nil
	h4, a4 := readMem()
	fmt.Printf("reading 4, all dropped (baseline): HeapInuse %d, HeapAlloc %d\n", h4, a4)
}

// breakdown prints the computed live bytes of each component of the retained
// structure (everything but the stand-in table), and their sum.
func (s *state) breakdown(distinct uint64) {
	var nMP, nMPU, nRare, other uint64
	roles := map[*bgp.Segment]struct{}{}
	for _, p := range s.paths {
		if p == nil {
			continue
		}
		if len(p.ASPath.Segments) > 0 {
			roles[&p.ASPath.Segments[0]] = struct{}{}
		}
		if p.MPReach != nil {
			nMP++
			other += sliceBytes(p.MPReach.NLRI) + sliceBytes(p.MPReach.RawNLRI)
		}
		if hasRare(p) {
			nRare++
		}
		if mu := p.MPUnreach(); mu != nil {
			nMPU++
			other += sliceBytes(mu.Withdrawn) + sliceBytes(mu.RawNLRI)
		}
		other += sliceBytes(p.ClusterList()) + sliceBytes(p.Unknown()) + sliceBytes(p.ExtCommunities())
		for _, a := range p.Unknown() {
			other += sliceBytes(a.Value)
		}
		// Unshared AS paths in the rare part (a received AS_PATH exists only
		// when AS4_PATH was merged, which never happens in a 4-byte RIB).
		for _, asp := range [...]bgp.ASPath{p.AS4Path(), receivedIfMerged(p)} {
			other += sliceBytes(asp.Segments)
			for _, g := range asp.Segments {
				other += sliceBytes(g.ASNs)
			}
		}
	}
	fmt.Printf("distinct merged ASPath values (by canonical slice identity): %d\n", len(roles))
	roles = nil

	pathStructs := distinct * class(uint64(unsafe.Sizeof(bgp.Path{})))
	mpStructs := nMP*class(uint64(unsafe.Sizeof(bgp.MPReach{}))) + nMPU*class(uint64(unsafe.Sizeof(bgp.MPUnreach{})))
	rareStructs := nRare * class(rareSize)
	var segArrays, asnArrays uint64
	for k, v := range s.asPaths {
		_ = k
		segArrays += sliceBytes(v)
		for _, g := range v {
			asnArrays += sliceBytes(g.ASNs)
		}
	}
	var commArrays uint64
	for _, v := range s.comms {
		commArrays += sliceBytes(v)
	}
	for _, v := range s.larges {
		commArrays += sliceBytes(v)
	}
	var pathKeys uint64
	for k := range s.paths {
		pathKeys += class(uint64(len(k)))
	}
	var canonKeys uint64
	for k := range s.asPaths {
		canonKeys += class(uint64(len(k)))
	}
	for k := range s.comms {
		canonKeys += class(uint64(len(k)))
	}
	for k := range s.larges {
		canonKeys += class(uint64(len(k)))
	}
	// Map entry overhead estimate: each entry takes a slot of string header
	// (16) + value + 1 control byte; a Swiss map (Go 1.24+) grows by doubling
	// and runs between 7/16 and 7/8 full, so slots = entries / 0.66 (the
	// midpoint). Path map value is 8 (pointer); canonical values are 24
	// (slice header). The estimate ignores table/group headers.
	slot := func(n, val uint64) uint64 { return uint64(float64(n*(16+val+1)) / 0.66) }
	mapOverhead := slot(uint64(len(s.paths)), 8) +
		slot(uint64(len(s.asPaths)), 24) + slot(uint64(len(s.comms)), 24) +
		slot(uint64(len(s.larges)), 24)

	total := pathStructs + rareStructs + mpStructs + other + segArrays + asnArrays + commArrays + pathKeys + canonKeys + mapOverhead
	fmt.Printf("breakdown (computed live bytes, allocator size classes):\n")
	fmt.Printf("  Path structs:                        %d\n", pathStructs)
	fmt.Printf("  rare parts (%d paths, %d-byte part assumed): %d\n", nRare, rareSize, rareStructs)
	fmt.Printf("  MPReach/MPUnreach structs (%d/%d):   %d\n", nMP, nMPU, mpStructs)
	fmt.Printf("  other per-path arrays (NLRI, Unknown, ClusterList, ext communities, rare AS paths): %d\n", other)
	fmt.Printf("  canonical []Segment arrays:          %d\n", segArrays)
	fmt.Printf("  canonical ASN arrays:                %d\n", asnArrays)
	fmt.Printf("  canonical community/ext/large arrays: %d\n", commArrays)
	fmt.Printf("  path-map key bytes:                  %d\n", pathKeys)
	fmt.Printf("  canonical-map key bytes:             %d\n", canonKeys)
	fmt.Printf("  map entry overhead (estimate):       %d\n", mapOverhead)
	fmt.Printf("  sum:                                 %d\n", total)
}
