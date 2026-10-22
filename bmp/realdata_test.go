package bmp_test

// The opt-in real-data run for BMP (spec §9.5). scripts/fetch-bmp.sh, which
// needs the user's go-ahead and kcat, writes OpenBMP frame streams into
// .data/realdata/bmp and pins them in MANIFEST; nothing here fetches anything.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
// every other one fails the test (the first 20 printed). Run it alone:
//
//	MRT_REALDATA=$PWD/.data go test -count=1 -run TestRealDataBMP -v ./bmp
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
			realDataBMPFile(t, bin, path)
		})
	}
}

// bmpManifestFile is one line of MANIFEST: name broker topic count size sha256.
type bmpManifestFile struct {
	name, broker, topic, sha256 string
	count                       int
	size                        int64
}

func readBMPManifest(t *testing.T, path string) []bmpManifestFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fs []bmpManifestFile
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		if len(f) != 6 {
			t.Fatalf("%s: bad line %q", path, l)
		}
		count, err1 := strconv.Atoi(f[3])
		size, err2 := strconv.ParseInt(f[4], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: bad count or size in %q", path, l)
		}
		fs = append(fs, bmpManifestFile{name: f[0], broker: f[1], topic: f[2], count: count, size: size, sha256: f[5]})
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

func realDataBMPFile(t *testing.T, bin, path string) {
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

// pathIDDivergence names the ADD-PATH guess case that explains a line pair
// that differs only in path_id: BK3 (ours has an ID of 2^24 or more, bgpkit
// none) or BK4 (ours has none, bgpkit an ID below 2^24). "" for no case.
func pathIDDivergence(ours, theirs map[string]any) (id string, ok bool) {
	po, hasO := pathIDOf(ours)
	pt, hasT := pathIDOf(theirs)
	switch {
	case !hasO && !hasT:
		return "", true
	case hasO && hasT:
		return "", po == pt
	case hasO && po >= 1<<24:
		return "BK3", true
	case hasT && pt < 1<<24:
		return "BK4", true
	}
	return "", false
}

// guessJunk are the prefixes bgpkit-parser makes up when it reads an ADD-PATH
// NLRI list as plain (BK3): the path identifier's leading bytes parsed as
// prefix lengths of 0 and 1.
var guessJunk = []string{"0.0.0.0/0", "0.0.0.0/1", "::/0", "::/1"}

// classifyBMP names the known divergences (testdata/bmp/divergences.md,
// section bgpkit-parser) that explain the route lines only mrt gives
// (onlyOurs) and only bgpkit-bmp gives (onlyTheirs). It returns the number of
// lines explained per ID and the lines nothing explains, prefixed "mrt: " and
// "bgpkit: ". An ours line is paired with a theirs line when applying BK1
// and/or BK2 to it, and ignoring path_id, makes them equal, and the path_id
// difference is BK3 or BK4:
//
//   - BK1: bgpkit's peer_as is the low 16 bits of ours (A flag, 16-bit AS).
//   - BK2: ours has no peer address ("invalid IP"), bgpkit's is 0.0.0.0.
//   - BK3: ours has a path_id of 2^24 or more, bgpkit none; a line bgpkit
//     gives alone for 0.0.0.0/0, 0.0.0.0/1, ::/0 or ::/1 of a peer with a BK3
//     pair is one the guess made up.
//   - BK4: ours has no path_id, bgpkit one below 2^24; a line only ours gives
//     for a peer with a BK4 pair is one the guess swallowed.
//
// The last two clauses are a heuristic: they cannot tell a made-up or lost
// line from a genuine one of that peer.
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
	peers3, peers4 := map[any]bool{}, map[any]bool{}
	for oi, o := range ours {
		if o.m == nil {
			continue
		}
		for mask := 0; mask < 4 && !usedO[oi]; mask++ {
			fixed := maps.Clone(o.m)
			var ids []string
			if mask&1 != 0 {
				if o.m["peer"] != "invalid IP" {
					continue
				}
				fixed["peer"] = "0.0.0.0"
				ids = append(ids, "BK2")
			}
			if mask&2 != 0 {
				as, _ := o.m["peer_as"].(float64)
				if as <= 0xffff {
					continue
				}
				fixed["peer_as"] = float64(uint32(as) & 0xffff)
				ids = append(ids, "BK1")
			}
			for _, ti := range index[canonLine(fixed, "path_id")] {
				if usedT[ti] {
					continue
				}
				pid, ok := pathIDDivergence(o.m, theirs[ti].m)
				if !ok || (len(ids) == 0 && pid == "") {
					continue // equal lines are not in a difference; other path_id pairs are not known
				}
				usedT[ti], usedO[oi] = true, true
				if pid != "" {
					ids = append(ids, pid)
					peers := peers3
					if pid == "BK4" {
						peers = peers4
					}
					peers[o.m["peer"]] = true
				}
				for _, id := range ids {
					known[id]++
				}
				break
			}
		}
	}
	for oi, o := range ours {
		switch {
		case usedO[oi]:
		case o.m != nil && peers4[o.m["peer"]]:
			known["BK4"]++
		default:
			unclassified = append(unclassified, "mrt: "+o.raw)
		}
	}
	for ti, l := range theirs {
		switch {
		case usedT[ti]:
		case l.m != nil && peers3[l.m["peer"]] && slices.Contains(guessJunk, fmt.Sprint(l.m["prefix"])):
			known["BK3"]++
		default:
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
