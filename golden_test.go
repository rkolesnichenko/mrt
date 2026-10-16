package mrt_test

// The differential against the two oracles (spec §7.2). Every fixture in
// testdata/mrt (real, RouteViews) and testdata/synthetic (internal/mrtgen)
// has two goldens in testdata/golden, both written by the pinned oracles:
//
//	NAME.bgpdump[.gz]      TZ=UTC bgpdump -m -l -u FILE
//	NAME.bgpkit.json[.gz]  bgpkit-parser --json FILE, normalized and sorted
//	                       by internal/oracle
//
// TestGoldens and TestMatchesBgpkit need no oracle installed.
// TestMatchesBgpdump and TestMatchesBgpkitLive run the oracles when they are
// on PATH; MRT_GOLDEN_UPDATE=1 makes them rewrite the goldens instead (review
// the diff). Every difference between mrt and an oracle is listed in
// testdata/bgpdump/divergences.md, which TestDivergencesDocumented holds in
// both directions.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/internal/mrtdump"
	"github.com/rkolesnichenko/mrt/internal/oracle"
)

const (
	goldenDir       = "testdata/golden"
	divergencesFile = "testdata/bgpdump/divergences.md"
	// bgpdump builds its community fields in a BUFSIZ buffer (1024 bytes on
	// macOS, 8192 with glibc), so a longer field would make the goldens
	// depend on the platform that wrote them (Ruling R16).
	maxCommunityField = 1000
)

// fixture is one MRT file the goldens cover.
type fixture struct {
	name string // the file name without .gz, .bz2 and .mrt
	path string
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	var fs []fixture
	for _, pat := range []string{"testdata/mrt/*", "testdata/synthetic/*.mrt"} {
		paths, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			name := filepath.Base(p)
			for _, ext := range []string{".gz", ".bz2", ".mrt"} {
				name = strings.TrimSuffix(name, ext)
			}
			fs = append(fs, fixture{name, p})
		}
	}
	if len(fs) == 0 {
		t.Fatal("no fixtures")
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if seen[f.name] {
			t.Fatalf("two fixtures are named %s", f.name)
		}
		seen[f.name] = true
	}
	return fs
}

func (f fixture) golden(kind string) string { return filepath.Join(goldenDir, f.name+kind) }

func update() bool { return os.Getenv("MRT_GOLDEN_UPDATE") == "1" }

// lines splits text into lines without their newlines.
func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

var (
	cacheMu   sync.Mutex
	dumpCache = map[string][]string{}
	mrtCache  = map[string][]string{}
)

// mrtdumpLines is `mrtdump -m -l -u` of f, line by line.
func mrtdumpLines(t *testing.T, f fixture) []string {
	t.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if l, ok := dumpCache[f.path]; ok {
		return l
	}
	var out, errs bytes.Buffer
	if st := mrtdump.Run([]string{"-m", "-l", "-u", f.path}, nil, &out, &errs); st > 1 {
		t.Fatalf("mrtdump %s: status %d: %s", f.path, st, errs.String())
	}
	dumpCache[f.path] = lines(out.Bytes())
	return dumpCache[f.path]
}

// routeLines is mrt.Routes over f, normalized and sorted. Errors yield no line.
func routeLines(t *testing.T, f fixture) []string {
	t.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if l, ok := mrtCache[f.path]; ok {
		return l
	}
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	r, err := mrt.Decompress(file)
	if err != nil {
		t.Fatal(err)
	}
	var ls []string
	for rt, err := range mrt.Routes(mrt.Records(r)) {
		if err == nil {
			ls = append(ls, oracle.FromRoute(rt))
		}
	}
	mrtCache[f.path] = oracle.Sorted(ls)
	return mrtCache[f.path]
}

func readGolden(t *testing.T, path string) []string {
	t.Helper()
	b, err := oracle.ReadGolden(path)
	if err != nil {
		t.Fatalf("%v (MRT_GOLDEN_UPDATE=1 go test -run 'TestMatches' . writes the goldens; it needs the oracles)", err)
	}
	return lines(b)
}

// A pair is one documented difference: the line mrt gives and the line the
// oracle gives, either of them "" when that side gives none.
type pair struct {
	entry, fixture string
	ours, theirs   string
}

// divergences reads divergences.md: the pairs of its "## bgpdump" section and
// of its "## bgpkit-parser" section. A pair is a fenced block
//
//	```pair
//	fixture: NAME
//	mrtdump: LINE         (mrt: in the bgpkit-parser section)
//	bgpdump: LINE         (bgpkit: in the bgpkit-parser section)
//	```
//
// under an entry heading "### ID. title"; (none) stands for no line. Every
// entry of those two sections must hold at least one pair.
func divergences(t *testing.T) (bgpdump, bgpkit []pair) {
	t.Helper()
	b, err := os.ReadFile(divergencesFile)
	if err != nil {
		t.Fatal(err)
	}
	var section, entry string
	var cur *pair
	pairsOf := map[string]int{} // entries of the oracle sections, by "section/entry"
	oracleSection := func() bool { return section == "bgpdump" || section == "bgpkit-parser" }
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		l := sc.Text()
		bad := func(why string) { t.Fatalf("%s:%d: %s: %q", divergencesFile, n, why, l) }
		switch {
		case cur != nil && l == "```":
			if cur.fixture == "" || (cur.ours == "" && cur.theirs == "") {
				bad("a pair needs a fixture and at least one line")
			}
			if section == "bgpdump" {
				bgpdump = append(bgpdump, *cur)
			} else {
				bgpkit = append(bgpkit, *cur)
			}
			pairsOf[section+"/"+entry]++
			cur = nil
		case cur != nil:
			ours, theirs := "mrtdump: ", "bgpdump: "
			if section == "bgpkit-parser" {
				ours, theirs = "mrt: ", "bgpkit: "
			}
			val := func(s string) string {
				if s == "(none)" {
					return ""
				}
				return s
			}
			switch {
			case strings.HasPrefix(l, "fixture: "):
				cur.fixture = strings.TrimPrefix(l, "fixture: ")
			case strings.HasPrefix(l, ours):
				cur.ours = val(strings.TrimPrefix(l, ours))
			case strings.HasPrefix(l, theirs):
				cur.theirs = val(strings.TrimPrefix(l, theirs))
			default:
				bad("unknown line in a pair")
			}
		case strings.HasPrefix(l, "## "):
			section, entry = strings.TrimPrefix(l, "## "), ""
		case strings.HasPrefix(l, "### "):
			entry = strings.TrimPrefix(l, "### ")
			if oracleSection() {
				pairsOf[section+"/"+entry] += 0
			}
		case l == "```pair":
			if !oracleSection() {
				bad("a pair outside the bgpdump and bgpkit-parser sections")
			}
			if entry == "" {
				bad("a pair outside an entry")
			}
			cur = &pair{entry: entry}
		}
	}
	if cur != nil {
		t.Fatalf("%s: unterminated pair", divergencesFile)
	}
	// An entry of an oracle section names at least one fixture line; a
	// difference no fixture shows belongs in another section.
	for e, n := range pairsOf {
		if n == 0 {
			t.Errorf("%s: entry %q has no pair block", divergencesFile, e)
		}
	}
	return bgpdump, bgpkit
}

// documented returns the set of documented lines of fixture on each side.
func documented(ps []pair, fixture string) (ours, theirs map[string]bool) {
	ours, theirs = map[string]bool{}, map[string]bool{}
	for _, p := range ps {
		if p.fixture != fixture {
			continue
		}
		if p.ours != "" {
			ours[p.ours] = true
		}
		if p.theirs != "" {
			theirs[p.theirs] = true
		}
	}
	return ours, theirs
}

func without(ls []string, drop map[string]bool) []string {
	var out []string
	for _, l := range ls {
		if !drop[l] {
			out = append(out, l)
		}
	}
	return out
}

// diff returns the lines of a not in b and of b not in a, as multisets, in
// the order they come.
func diff(a, b []string) (onlyA, onlyB []string) {
	count := map[string]int{}
	for _, l := range b {
		count[l]++
	}
	for _, l := range a {
		if count[l] > 0 {
			count[l]--
		} else {
			onlyA = append(onlyA, l)
		}
	}
	count = map[string]int{}
	for _, l := range a {
		count[l]++
	}
	for _, l := range b {
		if count[l] > 0 {
			count[l]--
		} else {
			onlyB = append(onlyB, l)
		}
	}
	return onlyA, onlyB
}

// firstDifference describes where two line lists first differ.
func firstDifference(ours, theirs []string) string {
	for i := range max(len(ours), len(theirs)) {
		var o, g string
		if i < len(ours) {
			o = ours[i]
		}
		if i < len(theirs) {
			g = theirs[i]
		}
		if o != g {
			return fmt.Sprintf("line %d (documented lines left out):\n mrt:    %s\n oracle: %s", i+1, o, g)
		}
	}
	return ""
}

func TestGoldens(t *testing.T) {
	bgpdumpPairs, _ := divergences(t)
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			ours, theirs := documented(bgpdumpPairs, f.name)
			got := without(mrtdumpLines(t, f), ours)
			want := without(readGolden(t, f.golden(".bgpdump")), theirs)
			if !slices.Equal(got, want) {
				t.Errorf("mrtdump -m -l -u differs from bgpdump's golden at %s\n(TestDivergencesDocumented lists the lines)",
					firstDifference(got, want))
			}
		})
	}
}

func TestMatchesBgpkit(t *testing.T) {
	_, bgpkitPairs := divergences(t)
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			ours, theirs := documented(bgpkitPairs, f.name)
			got := without(routeLines(t, f), ours)
			want := without(readGolden(t, f.golden(".bgpkit.json")), theirs)
			if !slices.Equal(got, want) {
				t.Errorf("Routes differs from bgpkit-parser's golden at %s\n(TestDivergencesDocumented lists the lines)",
					firstDifference(got, want))
			}
		})
	}
}

// TestDivergencesDocumented holds divergences.md to the goldens in both
// directions: every line on which mrt and an oracle differ is documented, and
// every documented line still differs. A failure prints the undocumented
// lines as pair blocks.
func TestDivergencesDocumented(t *testing.T) {
	bgpdumpPairs, bgpkitPairs := divergences(t)
	names := map[string]bool{}
	for _, f := range fixtures(t) {
		names[f.name] = true
	}
	for _, side := range []struct {
		section, ours, theirs string
		pairs                 []pair
		key                   func(string) []string
		mrt, oracle           func(*testing.T, fixture) []string
	}{
		{"bgpdump", "mrtdump", "bgpdump", bgpdumpPairs, bgpdumpKey,
			mrtdumpLines, func(t *testing.T, f fixture) []string { return readGolden(t, f.golden(".bgpdump")) }},
		{"bgpkit-parser", "mrt", "bgpkit", bgpkitPairs, bgpkitKey,
			routeLines, func(t *testing.T, f fixture) []string { return readGolden(t, f.golden(".bgpkit.json")) }},
	} {
		t.Run(side.section, func(t *testing.T) {
			for _, p := range side.pairs {
				if !names[p.fixture] {
					t.Errorf("%s: no fixture named %q", p.entry, p.fixture)
				}
			}
			for _, f := range fixtures(t) {
				ours, theirs := side.mrt(t, f), side.oracle(t, f)
				inOurs, inTheirs := map[string]bool{}, map[string]bool{}
				for _, l := range ours {
					inOurs[l] = true
				}
				for _, l := range theirs {
					inTheirs[l] = true
				}
				// Every documented pair still differs.
				for _, p := range side.pairs {
					if p.fixture != f.name {
						continue
					}
					if p.ours != "" && (!inOurs[p.ours] || inTheirs[p.ours]) {
						t.Errorf("%s: %s: the %s line is no longer a difference: %s", p.entry, f.name, side.ours, p.ours)
					}
					if p.theirs != "" && (!inTheirs[p.theirs] || inOurs[p.theirs]) {
						t.Errorf("%s: %s: the %s line is no longer a difference: %s", p.entry, f.name, side.theirs, p.theirs)
					}
				}
				// Every differing line is documented.
				docOurs, docTheirs := documented(side.pairs, f.name)
				onlyOurs, onlyTheirs := diff(ours, theirs)
				onlyOurs, onlyTheirs = without(onlyOurs, docOurs), without(onlyTheirs, docTheirs)
				if len(onlyOurs)+len(onlyTheirs) > 0 {
					t.Errorf("%s: %d %s and %d %s lines differ and are not in %s:\n%s", f.name,
						len(onlyOurs), side.ours, len(onlyTheirs), side.theirs, divergencesFile,
						pairBlocks(f.name, side.ours, side.theirs, onlyOurs, onlyTheirs, side.key))
				}
			}
		})
	}
}

// pairBlocks writes undocumented lines as pair blocks, pairing a line of
// ours with the first unused line of theirs that has the same key, trying
// each of key's keys in turn (most specific first).
func pairBlocks(fixture, oursName, theirsName string, ours, theirs []string, key func(string) []string) string {
	var b strings.Builder
	block := func(o, g string) {
		if o == "" {
			o = "(none)"
		}
		if g == "" {
			g = "(none)"
		}
		fmt.Fprintf(&b, "```pair\nfixture: %s\n%s: %s\n%s: %s\n```\n", fixture, oursName, o, theirsName, g)
	}
	match := make([]string, len(ours))
	used := make([]bool, len(theirs))
	for level := range len(key("")) {
		for i, o := range ours {
			if match[i] != "" {
				continue
			}
			for j, g := range theirs {
				if !used[j] && key(g)[level] == key(o)[level] {
					used[j], match[i] = true, g
					break
				}
			}
		}
	}
	const limit = 20
	n := 0
	for i, o := range ours {
		if n++; n <= limit {
			block(o, match[i])
		}
	}
	for j, g := range theirs {
		if !used[j] {
			if n++; n <= limit {
				block("", g)
			}
		}
	}
	if n > limit {
		fmt.Fprintf(&b, "... and %d more\n", n-limit)
	}
	return b.String()
}

// bgpdumpKey is a bgpdump line's type, time, kind, peer, AS and prefix.
func bgpdumpKey(l string) []string {
	f := strings.SplitN(l, "|", 7)
	return []string{strings.Join(f[:min(6, len(f))], "|")}
}

// bgpkitKey is a normalized line's kind, peer, prefix and path ID, then the
// same without the path ID.
func bgpkitKey(l string) []string {
	var k struct {
		Kind, Peer, Prefix string
		PathID             *uint32 `json:"path_id"`
	}
	if json.Unmarshal([]byte(l), &k) != nil {
		return []string{l, l}
	}
	id := "-"
	if k.PathID != nil {
		id = fmt.Sprint(*k.PathID)
	}
	return []string{strings.Join([]string{k.Kind, k.Peer, k.Prefix, id}, "|"), strings.Join([]string{k.Kind, k.Peer, k.Prefix}, "|")}
}

// TestCommunityFieldsFitBgpdumpBuffer pins Ruling R16: no fixture route has a
// community or large-community field bgpdump could truncate.
func TestCommunityFieldsFitBgpdumpBuffer(t *testing.T) {
	for _, f := range fixtures(t) {
		check := func(source string, ls []string) {
			for _, l := range ls {
				fs := strings.Split(l, "|")
				if len(fs) < 13 || fs[2] == "STATE" {
					continue
				}
				for _, i := range []int{11, 12} { // communities, large communities (-l)
					if len(fs[i]) >= maxCommunityField {
						t.Errorf("%s (%s): a %d-character field %d: %.80s...", f.name, source, len(fs[i]), i+1, fs[i])
					}
				}
			}
		}
		check("mrtdump", mrtdumpLines(t, f))
		check("golden", readGolden(t, f.golden(".bgpdump")))
	}
}

// TestLossless: the raw bytes of every record, and of every malformed one,
// in yield order, are the decompressed file (spec §5.2).
func TestLossless(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			file, err := os.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			r, err := mrt.Decompress(file)
			if err != nil {
				t.Fatal(err)
			}
			want, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			var got []byte
			for rec, err := range mrt.Records(bytes.NewReader(want)) {
				var re *mrt.RecordError
				switch {
				case err == nil:
					got = append(got, rec.Raw()...)
				case errors.As(err, &re) && errors.Is(err, mrt.ErrMalformed):
					got = append(got, re.Raw...)
				default:
					t.Fatalf("%v: a fixture must not end in a truncated or oversized record", err)
				}
			}
			if !bytes.Equal(got, want) {
				t.Errorf("records concatenate to %d bytes, the file has %d", len(got), len(want))
			}
		})
	}
}

// lookPath finds an oracle on PATH or skips the test.
func lookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		if update() {
			t.Fatalf("MRT_GOLDEN_UPDATE=1 needs %s on PATH (scripts/build-oracles.sh)", name)
		}
		t.Skipf("%s is not on PATH (scripts/build-oracles.sh installs it)", name)
	}
	return p
}

// checkLive compares an oracle's output with its golden, or rewrites it.
func checkLive(t *testing.T, path string, out []byte) {
	t.Helper()
	if update() {
		if err := oracle.WriteGolden(path, out); err != nil {
			t.Fatal(err)
		}
		return
	}
	golden, err := oracle.ReadGolden(path)
	if err != nil {
		t.Fatalf("%v (MRT_GOLDEN_UPDATE=1 writes it)", err)
	}
	if !bytes.Equal(out, golden) {
		t.Errorf("the oracle's output differs from %s at %s\n(MRT_GOLDEN_UPDATE=1 rewrites it; review the diff)",
			path, firstDifference(lines(golden), lines(out)))
	}
}

func TestMatchesBgpdump(t *testing.T) {
	bin := lookPath(t, "bgpdump")
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			out, status, err := oracle.Bgpdump(bin, f.path)
			if err != nil {
				t.Fatal(err)
			}
			if status != 0 {
				t.Logf("bgpdump exited with status %d (-1: killed by a signal, as when it asserts); its output so far is the golden", status)
			}
			checkLive(t, f.golden(".bgpdump"), out)
		})
	}
}

func TestMatchesBgpkitLive(t *testing.T) {
	bin := lookPath(t, "bgpkit-parser")
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			out, err := oracle.Bgpkit(bin, f.path)
			if err != nil {
				t.Fatal(err)
			}
			ls, err := oracle.NormalizeBgpkit(out)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			for _, l := range oracle.Sorted(ls) {
				b.WriteString(l)
				b.WriteByte('\n')
			}
			checkLive(t, f.golden(".bgpkit.json"), b.Bytes())
		})
	}
}

// TestFixturesListed: every fixture has a line in testdata/README.md or
// testdata/synthetic/README.md, and every golden belongs to a fixture.
func TestFixturesListed(t *testing.T) {
	readme := map[string][]byte{}
	for _, p := range []string{"testdata/README.md", "testdata/synthetic/README.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		readme[p] = b
	}
	names := map[string]bool{}
	for _, f := range fixtures(t) {
		names[f.name] = true
		base := filepath.Base(f.path)
		doc := "testdata/README.md"
		if strings.HasPrefix(f.path, "testdata/synthetic/") {
			doc = "testdata/synthetic/README.md"
		}
		if !bytes.Contains(readme[doc], []byte("`"+base+"`")) {
			t.Errorf("%s has no line in %s", base, doc)
		}
	}
	goldens, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	var orphans []string
	for _, g := range goldens {
		n := strings.TrimSuffix(g.Name(), ".gz")
		n = strings.TrimSuffix(strings.TrimSuffix(n, ".bgpdump"), ".bgpkit.json")
		if !names[n] {
			orphans = append(orphans, g.Name())
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("goldens with no fixture: %v", orphans)
	}
}
