package bmp_test

// The opt-in real-data run for BMP (spec §9.5). scripts/fetch-bmp.sh, which
// needs the user's go-ahead and kcat, writes OpenBMP frame streams into
// .data/realdata/bmp and pins them in MANIFEST; nothing here fetches anything.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/bmp"
	"github.com/rkolesnichenko/mrt/bmp/openbmp"
	"github.com/rkolesnichenko/mrt/internal/oracle"
)

// TestRealDataBMP decodes every capture pinned in
// $MRT_REALDATA/realdata/bmp/MANIFEST with bmp (one Session per router, keyed
// by Header.RouterHash) and with bgpkit-bmp, and compares the sorted route
// lines. A line only one side gives passes only when classifyBMP names the
// known divergence (BK1-BK4 of testdata/bmp/divergences.md) that explains it;
// every other one fails the test (the first 20 printed). bgpkit-bmp's whole
// output is held in memory, so run it alone under the memory guard:
//
//	memguard.sh 8 4000 env GOMAXPROCS=4 MRT_REALDATA=$PWD/.data go test -count=1 -run TestRealDataBMP -v ./bmp
//
// (memguard.sh is ~/DEV/rpsl/.superpowers/sdd/tools/memguard.sh, as in
// CLAUDE.md's real-data commands.)
func TestRealDataBMP(t *testing.T) {
	dir := os.Getenv("MRT_REALDATA")
	if dir == "" {
		t.Skip("MRT_REALDATA is not set (a capture needs the user's go-ahead: scripts/fetch-bmp.sh, then MRT_REALDATA=$PWD/.data)")
	}
	dir = filepath.Join(dir, "realdata", "bmp")
	manifest := filepath.Join(dir, "MANIFEST")
	if _, err := os.Stat(manifest); err != nil {
		t.Skipf("no BMP capture is pinned: %v", err)
	}
	bin, err := exec.LookPath("bgpkit-bmp")
	if err != nil {
		t.Fatalf("MRT_REALDATA is set but bgpkit-bmp is not on PATH (scripts/build-oracles.sh): %v", err)
	}
	for _, f := range readBMPManifest(t, manifest) {
		t.Run(f.name, func(t *testing.T) {
			path := filepath.Join(dir, f.name)
			if err := checkBMPPinned(path, f); err != nil {
				t.Fatal(err)
			}
			realDataBMPFile(t, bin, path, f.count)
		})
	}
}

// bmpManifestFile is one line of MANIFEST: name broker topic partition offset
// count size sha256.
type bmpManifestFile struct {
	name, broker, topic, sha256 string
	partition, count            int
	offset, size                int64
}

func readBMPManifest(t *testing.T, path string) []bmpManifestFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fs []bmpManifestFile
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if len(f) != 8 {
			t.Fatalf("%s: bad line %q", path, l)
		}
		partition, err1 := strconv.Atoi(f[3])
		offset, err2 := strconv.ParseInt(f[4], 10, 64)
		count, err3 := strconv.Atoi(f[5])
		size, err4 := strconv.ParseInt(f[6], 10, 64)
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			t.Fatalf("%s: bad number in %q: %v", path, l, err)
		}
		fs = append(fs, bmpManifestFile{name: f[0], broker: f[1], topic: f[2], partition: partition, offset: offset, count: count, size: size, sha256: f[7]})
	}
	return fs
}

// checkBMPPinned errors unless the file has the size and sha256 MANIFEST pins.
func checkBMPPinned(path string, f bmpManifestFile) error {
	fh, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%v (rerun scripts/fetch-bmp.sh)", err)
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return err
	}
	if n != f.size {
		return fmt.Errorf("%s: %d bytes, MANIFEST pins %d", path, n, f.size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != f.sha256 {
		return fmt.Errorf("%s: sha256 %s, MANIFEST pins %s", path, sum, f.sha256)
	}
	return nil
}

// bmpCounts is what one pass of bmp over a capture finds.
type bmpCounts struct {
	frames, frameErrors, headerErrors int
	messages, messageErrors           int
	routes, routeErrors               int
	addPathUnknown                    int
	types                             map[bmp.Type]int
}

// bmpCaptureLines decodes the capture frame by frame (openbmp.Frames and
// Parse, ParseMessage, one Session per router) and returns the announcement
// and withdrawal lines, normalized and sorted.
func bmpCaptureLines(t *testing.T, path string) ([]string, bmpCounts) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	c := bmpCounts{types: map[bmp.Type]int{}}
	var lines []string
	sessions := map[[16]byte]*bmp.Session{}
	for frame, err := range openbmp.Frames(file) {
		if err != nil {
			c.frameErrors++
			t.Logf("frame %d: %v", c.frames+1, err)
			continue
		}
		c.frames++
		h, body, err := openbmp.Parse(frame)
		if err != nil {
			c.headerErrors++
			continue
		}
		s := sessions[h.RouterHash]
		if s == nil {
			s = bmp.NewSession(bmp.RoutesOptions{Router: h.RouterIP})
			sessions[h.RouterHash] = s
		}
		m, err := bmp.ParseMessage(body)
		if err != nil {
			c.messageErrors++
		} else {
			c.messages++
			c.types[m.Header().Type]++
		}
		for r, err := range s.Routes(m, err) {
			if err != nil {
				c.routeErrors++
				continue
			}
			c.routes++
			for _, d := range r.Diagnostics {
				if d.Code == bmp.DiagAddPathUnknown {
					c.addPathUnknown++
					break
				}
			}
			if r.Kind == mrt.KindAnnounce || r.Kind == mrt.KindWithdraw {
				lines = append(lines, oracle.FromRoute(r))
			}
		}
	}
	return oracle.Sorted(lines), c
}

func realDataBMPFile(t *testing.T, bin, path string, count int) {
	t.Helper()
	ours, c := bmpCaptureLines(t, path)
	out, err := oracle.BgpkitBMP(bin, path)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := oracle.NormalizeBgpkit(out)
	if err != nil {
		t.Fatal(err)
	}
	theirs = oracle.Sorted(theirs)

	name := filepath.Base(path)
	t.Logf("%s: frames %d (frame errors %d, header errors %d), messages %d (errors %d), routes %d (errors %d, DiagAddPathUnknown %d)",
		name, c.frames, c.frameErrors, c.headerErrors, c.messages, c.messageErrors, c.routes, c.routeErrors, c.addPathUnknown)
	for _, ty := range slices.Sorted(maps.Keys(c.types)) {
		t.Logf("%s: message type %d: %d", name, ty, c.types[ty])
	}
	if c.frameErrors > 0 {
		t.Errorf("%d frame errors (logged above)", c.frameErrors)
	}
	if c.frames != count {
		t.Errorf("%d frames, MANIFEST asked for %d (a short capture)", c.frames, count)
	}
	onlyOurs, onlyTheirs := oracle.Diff(ours, theirs)
	known, left := classifyBMP(onlyOurs, onlyTheirs)
	t.Logf("%s: %d route lines (bgpkit-bmp %d); known divergences %v, unclassified %d", name, len(ours), len(theirs), known, len(left))
	if len(left) > 0 {
		t.Errorf("%d line differences no known divergence explains; the first %d:\n%s",
			len(left), min(len(left), 20), strings.Join(left[:min(len(left), 20)], "\n"))
	}
}

// bmpLine is a normalized route line (oracle.line) parsed to fields.
type bmpLine struct {
	raw string
	m   map[string]any
}

func parseBMPLines(ls []string) []bmpLine {
	out := make([]bmpLine, len(ls))
	for i, l := range ls {
		out[i].raw = l
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out[i].m = m
		}
	}
	return out
}

// canonLine renders m without the keys in drop, in a form that does not
// depend on the key order.
func canonLine(m map[string]any, drop ...string) string {
	c := make(map[string]any, len(m))
	for k, v := range m {
		if !slices.Contains(drop, k) {
			c[k] = v
		}
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// pathIDOf is the line's path ID; ok is false when it has none.
func pathIDOf(m map[string]any) (id uint32, ok bool) {
	v, ok := m["path_id"].(float64)
	return uint32(v), ok
}

// plainNLRI reads the four bytes of a path ID as a list of plain NLRI of the
// family of ipv6 (a length byte, then ceil(length/8) address bytes), the way
// bgpkit-parser reads them when it guesses wrong. ok is false unless the
// entries consume exactly the four bytes.
func plainNLRI(id uint32, ipv6 bool) (prefixes []string, ok bool) {
	b := binary.BigEndian.AppendUint32(nil, id)
	maxLen, size := 32, 4
	if ipv6 {
		maxLen, size = 128, 16
	}
	for len(b) > 0 {
		l := int(b[0])
		n := (l + 7) / 8
		if l > maxLen || 1+n > len(b) {
			return nil, false
		}
		addr := make([]byte, size)
		copy(addr, b[1:1+n])
		a, _ := netip.AddrFromSlice(addr)
		prefixes = append(prefixes, netip.PrefixFrom(a, l).String())
		b = b[1+n:]
	}
	return prefixes, true
}

// classifyBMP names the known divergences (testdata/bmp/divergences.md,
// section bgpkit-parser) that explain the route lines only mrt gives
// (onlyOurs) and only bgpkit-bmp gives (onlyTheirs). It returns the number of
// lines explained per ID and the lines nothing explains, prefixed "mrt: " and
// "bgpkit: ". An ours line is paired with a theirs line when applying BK1
// and/or BK2 to it, and ignoring path_id, makes them equal; lines are keyed on
// peer, peer_as and the attributes (the lines carry no router). The path_id
// difference must then be explained:
//
//   - BK1: bgpkit's peer_as is the low 16 bits of ours (A flag, 16-bit AS).
//   - BK2: ours has no peer address ("invalid IP"), bgpkit's is 0.0.0.0.
//   - BK3: ours has a path_id P of 2^24 or more, bgpkit none. bgpkit read
//     P's four bytes as plain NLRI first; P must parse to exactly four bytes,
//     and bgpkit must give, besides the pair, one line with the pair's key
//     for each prefix P parses to. All of them are consumed, else the pair
//     stays unclassified.
//   - BK4: ours has no path_id, bgpkit one, Q, below 2^24. Q's four bytes
//     must parse as plain NLRI to exactly four bytes, and ours must give one
//     line with the pair's key for each prefix they parse to (the routes
//     bgpkit lost), all consumed; else the pair stays unclassified (a Q of 1
//     is an mrt bug, not BK4).
func classifyBMP(onlyOurs, onlyTheirs []string) (known map[string]int, unclassified []string) {
	known = map[string]int{}
	ours, theirs := parseBMPLines(onlyOurs), parseBMPLines(onlyTheirs)
	index := map[string][]int{}
	for i, l := range theirs {
		if l.m != nil {
			k := canonLine(l.m, "path_id")
			index[k] = append(index[k], i)
		}
	}
	usedT := make([]bool, len(theirs))
	usedO := make([]bool, len(ours))

	// take finds, for each prefix, an unused line of ls (marked in used and
	// tmp) with the attributes of base, after fix; nil when one is missing.
	take := func(ls []bmpLine, used []bool, fix func(map[string]any) map[string]any, base map[string]any, prefixes []string) []int {
		var got []int
		taken := map[int]bool{}
		for _, p := range prefixes {
			want := maps.Clone(base)
			want["prefix"] = p
			found := -1
			for i, l := range ls {
				if l.m != nil && !used[i] && !taken[i] && canonLine(fix(l.m), "path_id") == canonLine(want, "path_id") {
					found = i
					break
				}
			}
			if found < 0 {
				return nil
			}
			taken[found] = true
			got = append(got, found)
		}
		return got
	}

	for oi, o := range ours {
		if o.m == nil {
			continue
		}
		for mask := 0; mask < 4 && !usedO[oi]; mask++ {
			fix := func(m map[string]any) map[string]any {
				f := maps.Clone(m)
				if mask&1 != 0 {
					f["peer"] = "0.0.0.0"
				}
				if mask&2 != 0 {
					as, _ := m["peer_as"].(float64)
					f["peer_as"] = float64(uint32(as) & 0xffff)
				}
				return f
			}
			var ids []string
			if mask&1 != 0 {
				if o.m["peer"] != "invalid IP" {
					continue
				}
				ids = append(ids, "BK2")
			}
			if mask&2 != 0 {
				if as, _ := o.m["peer_as"].(float64); as <= 0xffff {
					continue
				}
				ids = append(ids, "BK1")
			}
			fixed := fix(o.m)
			for _, ti := range index[canonLine(fixed, "path_id")] {
				if usedT[ti] {
					continue
				}
				po, hasO := pathIDOf(o.m)
				pt, hasT := pathIDOf(theirs[ti].m)
				ipv6 := strings.Contains(fmt.Sprint(fixed["prefix"]), ":")
				var extraO, extraT []int
				switch {
				case hasO == hasT:
					// Equal path IDs (or none): a difference needs BK1 or BK2;
					// path IDs that differ are not a known case.
					if len(ids) == 0 || po != pt {
						continue
					}
				case hasO:
					prefixes, ok := plainNLRI(po, ipv6)
					if po < 1<<24 || !ok {
						continue
					}
					if extraT = take(theirs, usedT, func(m map[string]any) map[string]any { return m }, theirs[ti].m, prefixes); extraT == nil {
						continue
					}
					ids = append(ids, "BK3")
					for range extraT {
						ids = append(ids, "BK3")
					}
				default:
					prefixes, ok := plainNLRI(pt, ipv6)
					if pt >= 1<<24 || !ok {
						continue
					}
					if extraO = take(ours, usedO, fix, fixed, prefixes); extraO == nil {
						continue
					}
					ids = append(ids, "BK4")
					for range extraO {
						ids = append(ids, "BK4")
					}
				}
				usedT[ti], usedO[oi] = true, true
				for _, i := range extraT {
					usedT[i] = true
				}
				for _, i := range extraO {
					usedO[i] = true
				}
				for _, id := range ids {
					known[id]++
				}
				break
			}
		}
	}
	for oi, o := range ours {
		if !usedO[oi] {
			unclassified = append(unclassified, "mrt: "+o.raw)
		}
	}
	for ti, l := range theirs {
		if !usedT[ti] {
			unclassified = append(unclassified, "bgpkit: "+l.raw)
		}
	}
	return known, unclassified
}

func TestClassifyBMP(t *testing.T) {
	const (
		as2Mrt = `{"kind":"A","peer":"192.0.2.70","peer_as":196618,"prefix":"198.51.100.0/24","as_path":"64500 196618 4200000001","origin":"IGP","next_hop":"192.0.2.254"}`
		as2Bk  = `{"kind":"A","peer":"192.0.2.70","peer_as":10,"prefix":"198.51.100.0/24","as_path":"64500 196618 4200000001","origin":"IGP","next_hop":"192.0.2.254"}`
		locMrt = `{"kind":"A","peer":"invalid IP","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		locBk  = `{"kind":"A","peer":"0.0.0.0","peer_as":64500,"prefix":"198.51.100.0/24","path_id":1,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		bk3Mrt = `{"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"198.51.100.0/24","path_id":16777216,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		bk3Bk  = `{"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"198.51.100.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		junk1  = `{"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"0.0.0.0/1","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		junk0  = `{"kind":"A","peer":"192.0.2.190","peer_as":64690,"prefix":"0.0.0.0/0","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		bk4A   = `{"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"0.0.0.0/0","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		bk4B   = `{"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"10.11.0.0/16","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		bk4Mrt = `{"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"198.51.100.0/24","as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
		bk4Bk  = `{"kind":"A","peer":"192.0.2.191","peer_as":64691,"prefix":"198.51.100.0/24","path_id":1051147,"as_path":"64501 64502","origin":"IGP","next_hop":"192.0.2.254"}`
	)
	other := `{"kind":"A","peer":"192.0.2.9","peer_as":64509,"prefix":"203.0.113.0/24","as_path":"64501","origin":"IGP","next_hop":"192.0.2.254"}`
	withAS := func(l string) string { return strings.Replace(l, `"peer_as":64500`, `"peer_as":64501`, 1) }
	tests := []struct {
		name         string
		ours, theirs []string
		known        map[string]int
		unclassified []string
	}{
		{"nothing", nil, nil, map[string]int{}, nil},
		{"BK1", []string{as2Mrt}, []string{as2Bk}, map[string]int{"BK1": 1}, nil},
		{"BK1 needs the low 16 bits", []string{as2Mrt}, []string{strings.Replace(as2Bk, `"peer_as":10`, `"peer_as":11`, 1)}, map[string]int{},
			[]string{"mrt: " + as2Mrt, "bgpkit: " + strings.Replace(as2Bk, `"peer_as":10`, `"peer_as":11`, 1)}},
		{"BK2", []string{locMrt}, []string{locBk}, map[string]int{"BK2": 1}, nil},
		{"BK2 with another field differing", []string{locMrt}, []string{withAS(locBk)}, map[string]int{},
			[]string{"mrt: " + locMrt, "bgpkit: " + withAS(locBk)}},
		{"BK1 and BK2", []string{strings.Replace(locMrt, `"peer_as":64500`, `"peer_as":196618`, 1)}, []string{strings.Replace(locBk, `"peer_as":64500`, `"peer_as":10`, 1)},
			map[string]int{"BK1": 1, "BK2": 1}, nil},
		{"BK3 with its made-up lines", []string{bk3Mrt}, []string{bk3Bk, junk1, junk0, junk0}, map[string]int{"BK3": 4}, nil},
		{"made-up line without a BK3 pair", nil, []string{junk0}, map[string]int{}, []string{"bgpkit: " + junk0}},
		{"BK3 needs an ID of 2^24 or more", []string{strings.Replace(bk3Mrt, "16777216", "16777215", 1)}, []string{bk3Bk}, map[string]int{},
			[]string{"mrt: " + strings.Replace(bk3Mrt, "16777216", "16777215", 1), "bgpkit: " + bk3Bk}},
		{"BK4 with its swallowed lines", []string{bk4A, bk4B, bk4Mrt}, []string{bk4Bk}, map[string]int{"BK4": 3}, nil},
		{"swallowed line without a BK4 pair", []string{bk4A}, nil, map[string]int{}, []string{"mrt: " + bk4A}},
		{"BK4 needs an ID below 2^24", []string{bk4Mrt}, []string{strings.Replace(bk4Bk, "1051147", "16777216", 1)}, map[string]int{},
			[]string{"mrt: " + bk4Mrt, "bgpkit: " + strings.Replace(bk4Bk, "1051147", "16777216", 1)}},
		{"two path IDs that differ", []string{bk3Mrt}, []string{strings.Replace(bk3Mrt, "16777216", "5", 1)}, map[string]int{},
			[]string{"mrt: " + bk3Mrt, "bgpkit: " + strings.Replace(bk3Mrt, "16777216", "5", 1)}},
		{"an unrelated line beside a known one", []string{as2Mrt, other}, []string{as2Bk}, map[string]int{"BK1": 1}, []string{"mrt: " + other}},
		{"a line that is not JSON", []string{"garbage"}, nil, map[string]int{}, []string{"mrt: garbage"}},
		{"made-up line with other attributes", []string{bk3Mrt}, []string{bk3Bk, junk1, junk0, strings.Replace(junk0, "192.0.2.254", "192.0.2.253", 1)}, map[string]int{},
			[]string{"mrt: " + bk3Mrt, "bgpkit: " + bk3Bk, "bgpkit: " + junk1, "bgpkit: " + junk0, "bgpkit: " + strings.Replace(junk0, "192.0.2.254", "192.0.2.253", 1)}},
		{"a /0 beyond what the path ID explains", []string{bk3Mrt}, []string{bk3Bk, junk1, junk0, junk0, junk0}, map[string]int{"BK3": 4}, []string{"bgpkit: " + junk0}},
		{"BK3 path ID that is not plain NLRI", []string{strings.Replace(bk3Mrt, "16777216", "4278190080", 1)}, []string{bk3Bk}, map[string]int{},
			[]string{"mrt: " + strings.Replace(bk3Mrt, "16777216", "4278190080", 1), "bgpkit: " + bk3Bk}},
		{"swallowed prefix the path ID does not explain", []string{bk4A, strings.Replace(bk4B, "10.11.0.0", "10.12.0.0", 1), bk4Mrt}, []string{bk4Bk}, map[string]int{},
			[]string{"mrt: " + bk4A, "mrt: " + strings.Replace(bk4B, "10.11.0.0", "10.12.0.0", 1), "mrt: " + bk4Mrt, "bgpkit: " + bk4Bk}},
		{"BK4-looking pair with a path ID of 1", []string{bk4Mrt}, []string{strings.Replace(bk4Bk, "1051147", "1", 1)}, map[string]int{},
			[]string{"mrt: " + bk4Mrt, "bgpkit: " + strings.Replace(bk4Bk, "1051147", "1", 1)}},
	}
	for _, tc := range tests {
		known, left := classifyBMP(tc.ours, tc.theirs)
		if !maps.Equal(known, tc.known) || !slices.Equal(left, tc.unclassified) {
			t.Errorf("%s: known %v, unclassified %q; want %v, %q", tc.name, known, left, tc.known, tc.unclassified)
		}
	}
}

func TestCheckBMPPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sum = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // sha256("abc")
	for _, tc := range []struct {
		f     bmpManifestFile
		wantE string
	}{
		{bmpManifestFile{size: 3, sha256: sum}, ""},
		{bmpManifestFile{size: 4, sha256: sum}, "3 bytes, MANIFEST pins 4"},
		{bmpManifestFile{size: 3, sha256: strings.Repeat("0", 64)}, "sha256 " + sum},
	} {
		err := checkBMPPinned(path, tc.f)
		switch {
		case tc.wantE == "" && err != nil:
			t.Errorf("%+v: %v", tc.f, err)
		case tc.wantE != "" && (err == nil || !strings.Contains(err.Error(), tc.wantE)):
			t.Errorf("%+v: err = %v, want it to contain %q", tc.f, err, tc.wantE)
		}
	}
}
