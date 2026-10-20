// Command heldsize is a measurement tool, not a public API: it loads full RIBs
// the way a table keeper would, through mrt.RoutesWith with a bgp.Interner,
// and reports the memory the decoded paths retain (v0.2 design §6.4, §6.4a).
//
//	go run ./internal/heldsize FILE...
//
// It first prints its environment (Go version, GOOS/GOARCH, GOMAXPROCS, memory
// limit). Every route of every file is held in a 16-byte stand-in for the
// table keeper (path pointer, peer index, prefix id). After the load it prints
// the routes, the interner's whole-path count (Len), and five heap readings,
// each after two collections, HeapAlloc (live bytes, the gate metric) first
// and HeapInuse (bytes in heap spans holding any object; not RSS) beside it:
//
//  0. everything live: stand-in table, paths, whole-path index, components;
//  1. stand-in table dropped (paths, whole-path index, component maps);
//  2. after DropPathIndex (paths and component maps: the §6.4a gate);
//  3. interner dropped (paths and the arrays they share);
//  4. paths dropped too (baseline).
//
// Readings 1-3 hold the distinct paths in a slice of pointers, whose bytes are
// subtracted. The whole-path index is reading 1 minus reading 2; the component
// maps (keys and tables, not the shared arrays the paths still use) are
// reading 2 minus reading 3.
package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"
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
	in *bgp.Interner

	table  [][]standIn
	routes uint64

	recErrors uint64
	fresh     uint64 // routes whose block has a Warning or Error: decoded afresh, not interned
	infoOnly  uint64 // routes whose diagnostics are all Info
	other     uint64 // routes of other kinds (none expected in a RIB)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: heldsize FILE...")
		os.Exit(2)
	}
	fmt.Printf("env: %s %s/%s GOMAXPROCS=%d memory limit=%d bytes (GOMEMLIMIT=%q)\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0),
		debug.SetMemoryLimit(-1), os.Getenv("GOMEMLIMIT"))
	s := &state{in: bgp.NewInterner()}
	start := time.Now()
	for _, name := range os.Args[1:] {
		t := time.Now()
		if err := s.file(name); err != nil {
			fmt.Fprintf(os.Stderr, "heldsize: %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("after %s: routes=%d interner_len=%d (%.1f s)\n", name, s.routes, s.in.Len(), time.Since(t).Seconds())
	}
	fmt.Printf("load wall time: %.1f s\n", time.Since(start).Seconds())
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
	for rt, err := range mrt.RoutesWith(mrt.Records(r), mrt.RoutesOptions{Interner: s.in}) {
		if err != nil {
			s.recErrors++
			continue
		}
		if rt.Kind != mrt.KindRIB || rt.Path == nil {
			s.other++
			continue
		}
		info := false
		for _, d := range rt.Diagnostics {
			if d.Severity >= bgp.SeverityWarning {
				s.fresh++
				info = false
				break
			}
			info = true
		}
		if info {
			s.infoOnly++
		}
		s.hold(rt.Path, rt.Peer.AS)
	}
	return nil
}

func (s *state) hold(p *bgp.Path, peer bgp.ASN) {
	if len(s.table) == 0 || len(s.table[len(s.table)-1]) == chunkSize {
		s.table = append(s.table, make([]standIn, 0, chunkSize))
	}
	last := len(s.table) - 1
	s.table[last] = append(s.table[last], standIn{path: p, peer: uint32(peer), prefix: uint32(s.routes)})
	s.routes++
}

// classes are the allocator's size classes (MemStats.BySize), set by readMem.
var classes []uint64

// readMem collects garbage twice and returns HeapAlloc (live bytes) and
// HeapInuse (spans in use).
func readMem() (alloc, inuse uint64) {
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
	return m.HeapAlloc, m.HeapInuse
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

// rareSize is unsafe.Sizeof of bgp's unexported pathRare (144 bytes on 64-bit
// platforms), which this package cannot name. It only feeds the computed
// lines; the readings are measured.
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

func line(label string, alloc, inuse uint64) {
	fmt.Printf("%s: HeapAlloc %d, HeapInuse %d\n", label, alloc, inuse)
}

func (s *state) report() {
	fmt.Printf("record errors: %d\n", s.recErrors)
	fmt.Printf("routes held: %d (other kinds skipped: %d)\n", s.routes, s.other)
	fmt.Printf("routes with a Warning/Error diagnostic (fresh path each): %d; with Info only: %d\n", s.fresh, s.infoOnly)
	fmt.Printf("interner Len before DropPathIndex (distinct whole paths): %d\n", s.in.Len())
	fmt.Printf("sizeof bgp.Path: %d, MPReach: %d, stand-in: %d\n",
		unsafe.Sizeof(bgp.Path{}), unsafe.Sizeof(bgp.MPReach{}), unsafe.Sizeof(standIn{}))
	fmt.Printf("stand-in table bytes (routes*16): %d\n", s.routes*16)

	a0, i0 := readMem()
	line("reading 0, everything live", a0, i0)

	keep := s.distinct()
	keepBytes := class(uint64(cap(keep)) * 8)
	fmt.Printf("keep slice bytes (subtracted from readings 1-3): %d\n", keepBytes)

	s.table = nil
	a1, i1 := readMem()
	line("reading 1, stand-in table dropped (paths + whole-path index + component maps)", a1-keepBytes, i1-keepBytes)

	n := s.in.Len()
	s.in.DropPathIndex()
	a2, i2 := readMem()
	line("reading 2, after DropPathIndex (paths + component maps) [GATE: HeapAlloc <= 4e9]", a2-keepBytes, i2-keepBytes)
	line(fmt.Sprintf("whole-path index (%d paths), reading 1 minus reading 2", n), a1-a2, i1-i2)

	s.in = nil
	a3, i3 := readMem()
	line("reading 3, interner dropped (paths + the arrays they share)", a3-keepBytes, i3-keepBytes)
	line("component maps, reading 2 minus reading 3", a2-a3, i2-i3)

	runtime.KeepAlive(keep)
	keep = nil
	a4, i4 := readMem()
	line("reading 4, all dropped (baseline)", a4, i4)
}

// distinct returns the distinct paths of the table, by pointer, and prints
// what they share and what they are made of.
func (s *state) distinct() []*bgp.Path {
	seen := make(map[*bgp.Path]struct{})
	asArrays := make(map[*bgp.Segment]struct{})
	commArrays := make(map[*bgp.Community]struct{})
	var keep []*bgp.Path
	var nRare, nMP uint64
	for _, chunk := range s.table {
		for _, r := range chunk {
			p := r.path
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			keep = append(keep, p)
			if len(p.ASPath.Segments) > 0 {
				asArrays[&p.ASPath.Segments[0]] = struct{}{}
			}
			if len(p.Communities) > 0 {
				commArrays[&p.Communities[0]] = struct{}{}
			}
			if hasRare(p) {
				nRare++
			}
			if p.MPReach != nil {
				nMP++
			}
		}
	}
	fmt.Printf("distinct paths held (by pointer): %d\n", len(keep))
	fmt.Printf("distinct ASPath arrays (by identity): %d; distinct community arrays: %d\n", len(asArrays), len(commArrays))
	fmt.Printf("paths with a rare part: %d; with MPReach: %d\n", nRare, nMP)
	fmt.Printf("computed: Path structs %d, rare parts (%d-byte part assumed) %d, MPReach structs %d\n",
		uint64(len(keep))*class(uint64(unsafe.Sizeof(bgp.Path{}))), rareSize, nRare*class(rareSize),
		nMP*class(uint64(unsafe.Sizeof(bgp.MPReach{}))))
	return keep
}
