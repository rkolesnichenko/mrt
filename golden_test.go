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
// on PATH (and fail without them under MRT_ORACLES_REQUIRED=1, as CI sets); MRT_GOLDEN_UPDATE=1 makes them rewrite the goldens instead (review
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
	"maps"
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

// dumpResult is what mrtdump gives for one fixture.
type dumpResult struct {
	lines  []string
	status int
	stderr string
}

// routesResult is what Routes gives for one fixture.
type routesResult struct {
	lines  []string // normalized and sorted
	errors int
}

var (
	cacheMu     sync.Mutex
	dumpCache   = map[string]dumpResult{}
	routesCache = map[string]routesResult{}
)

// runMrtdump runs `mrtdump -m -l -u` over f once.
func runMrtdump(t *testing.T, f fixture) dumpResult {
	t.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if r, ok := dumpCache[f.path]; ok {
		return r
	}
	var out, errs bytes.Buffer
	st := mrtdump.Run([]string{"-m", "-l", "-u", f.path}, nil, &out, &errs)
	if st > 1 {
		t.Fatalf("mrtdump %s: status %d: %s", f.path, st, errs.String())
	}
	dumpCache[f.path] = dumpResult{lines(out.Bytes()), st, errs.String()}
	return dumpCache[f.path]
}

// mrtdumpLines is `mrtdump -m -l -u` of f, line by line.
func mrtdumpLines(t *testing.T, f fixture) []string { return runMrtdump(t, f).lines }

// mrtdumpStatus is the exit status and standard error of mrtdump over f.
func mrtdumpStatus(t *testing.T, f fixture) (int, string) {
	r := runMrtdump(t, f)
	return r.status, r.stderr
}

// runRoutes runs mrt.Routes over f once.
func runRoutes(t *testing.T, f fixture) routesResult {
	t.Helper()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if r, ok := routesCache[f.path]; ok {
		return r
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
	var res routesResult
	for rt, err := range mrt.Routes(mrt.Records(r)) {
		if err != nil {
			res.errors++
			continue
		}
		res.lines = append(res.lines, oracle.FromRoute(rt))
	}
	res.lines = oracle.Sorted(res.lines)
	routesCache[f.path] = res
	return res
}

// routeLines is mrt.Routes over f, normalized and sorted. Errors yield no line.
func routeLines(t *testing.T, f fixture) []string { return runRoutes(t, f).lines }

// routeErrors is how many errors mrt.Routes yields over f.
func routeErrors(t *testing.T, f fixture) int { return runRoutes(t, f).errors }

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

// documented returns how many times each line of fixture is documented, on
// each side: every pair stands for one occurrence.
func documented(ps []pair, fixture string) (ours, theirs map[string]int) {
	ours, theirs = map[string]int{}, map[string]int{}
	for _, p := range ps {
		if p.fixture != fixture {
			continue
		}
		if p.ours != "" {
			ours[p.ours]++
		}
		if p.theirs != "" {
			theirs[p.theirs]++
		}
	}
	return ours, theirs
}

// without returns ls less the first drop[l] occurrences of each line l.
func without(ls []string, drop map[string]int) []string {
	left := maps.Clone(drop)
	var out []string
	for _, l := range ls {
		if left[l] > 0 {
			left[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// stillDiffers returns, sorted, the documented lines of one side (doc) that
// the side does not hold as many more times than the other side as they are
// documented.
func stillDiffers(side, other []string, doc map[string]int) []string {
	n := map[string]int{}
	for _, l := range side {
		n[l]++
	}
	for _, l := range other {
		n[l]--
	}
	var bad []string
	for l, k := range doc {
		if n[l] < k {
			bad = append(bad, l)
		}
	}
	sort.Strings(bad)
	return bad
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
				docOurs, docTheirs := documented(side.pairs, f.name)
				// Every documented line still differs, as many times as it is
				// documented.
				for _, l := range stillDiffers(ours, theirs, docOurs) {
					t.Errorf("%s: the %s line is documented more often than it differs: %s", f.name, side.ours, l)
				}
				for _, l := range stillDiffers(theirs, ours, docTheirs) {
					t.Errorf("%s: the %s line is documented more often than it differs: %s", f.name, side.theirs, l)
				}
				// Every differing line is documented.
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
				comm, large := communityFields(l)
				for _, field := range []string{comm, large} {
					if len(field) >= maxCommunityField {
						t.Errorf("%s (%s): a %d-character community field: %.80s...", f.name, source, len(field), field)
					}
				}
			}
		}
		check("mrtdump", mrtdumpLines(t, f))
		check("golden", readGolden(t, f.golden(".bgpdump")))
	}
}

// communityFields returns the communities and large communities fields of a
// `bgpdump -m -l` line, or "" for a line without them (W, STATE). Add-path
// lines (*_AP) carry the path ID after the prefix, one column more.
func communityFields(l string) (comm, large string) {
	fs := strings.Split(l, "|")
	i := 11
	if len(fs) > 0 && strings.HasSuffix(fs[0], "_AP") {
		i++
	}
	if len(fs) < i+2 || fs[2] == "STATE" || fs[2] == "W" {
		return "", ""
	}
	return fs[i], fs[i+1]
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

// lookPath finds an oracle on PATH or skips the test; with
// MRT_ORACLES_REQUIRED=1 (CI) or MRT_GOLDEN_UPDATE=1 a missing oracle fails it.
func lookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		if update() || os.Getenv("MRT_ORACLES_REQUIRED") == "1" {
			t.Fatalf("MRT_GOLDEN_UPDATE=1 and MRT_ORACLES_REQUIRED=1 need %s on PATH (scripts/build-oracles.sh)", name)
		}
		t.Skipf("%s is not on PATH (scripts/build-oracles.sh installs it)", name)
	}
	return p
}

// checkLive compares an oracle's output with its golden, or rewrites it.
//
// crashed says the oracle died on a signal (bgpdump asserting). Its stdout then
// ends wherever the C library last flushed, which differs by platform: glibc
// buffers a pipe fully and abort() discards the buffer, while on macOS the lines
// printed before the assertion arrive. So a crashed oracle's output need only be a
// prefix of the golden, and an update never shortens a golden to such a prefix.
func checkLive(t *testing.T, path string, out []byte, crashed bool) {
	t.Helper()
	if update() {
		if crashed {
			if old, err := oracle.ReadGolden(path); err == nil && bytes.HasPrefix(old, out) {
				return
			}
		}
		if err := oracle.WriteGolden(path, out); err != nil {
			t.Fatal(err)
		}
		return
	}
	golden, err := oracle.ReadGolden(path)
	if err != nil {
		t.Fatalf("%v (MRT_GOLDEN_UPDATE=1 writes it)", err)
	}
	if crashed && bytes.HasPrefix(golden, out) {
		return
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
				t.Logf("bgpdump exited with status %d (-1: killed by a signal, as when it asserts); its output must be a prefix of the golden", status)
			}
			checkLive(t, f.golden(".bgpdump"), out, status < 0)
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
			checkLive(t, f.golden(".bgpkit.json"), b.Bytes(), false)
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

func TestCommunityFieldsColumns(t *testing.T) {
	for _, tc := range []struct{ line, comm, large string }{
		{"BGP4MP|1|A|192.0.2.1|1|192.0.2.0/24|1|IGP|192.0.2.1|0|0|1:2|3:4:5|NAG|||", "1:2", "3:4:5"},
		// add-path lines carry the path ID after the prefix (Ruling R19)
		{"TABLE_DUMP2_AP|1|B|192.0.2.1|1|192.0.2.0/24|7|1|IGP|192.0.2.1|0|0|1:2|3:4:5|NAG|||", "1:2", "3:4:5"},
		{"BGP4MP_ET_AP|1.000001|A|192.0.2.1|1|192.0.2.0/24|7|1|IGP|192.0.2.1|0|0|1:2|3:4:5|NAG|||", "1:2", "3:4:5"},
		{"BGP4MP|1|W|192.0.2.1|1|192.0.2.0/24", "", ""},
		{"BGP4MP|1|STATE|192.0.2.1|1|1|2", "", ""},
	} {
		comm, large := communityFields(tc.line)
		if comm != tc.comm || large != tc.large {
			t.Errorf("%s: %q %q, want %q %q", tc.line, comm, large, tc.comm, tc.large)
		}
	}
}

func TestWithoutCounts(t *testing.T) {
	ls := []string{"a", "b", "a", "c", "a"}
	if got := without(ls, map[string]int{"a": 2, "c": 1}); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("without = %q, want [b a]", got)
	}
	// "a" differs twice (3 against 1): documenting it twice holds, three times does not.
	ours, theirs := []string{"a", "a", "a", "b"}, []string{"a", "b"}
	if bad := stillDiffers(ours, theirs, map[string]int{"a": 2}); len(bad) != 0 {
		t.Errorf("a documented twice: %q", bad)
	}
	if bad := stillDiffers(ours, theirs, map[string]int{"a": 3, "b": 1}); !slices.Equal(bad, []string{"a", "b"}) {
		t.Errorf("a three times, b once: %q, want [a b]", bad)
	}
}

// TestRealFixturesClean (Ruling R19): the real fixtures hold no malformed
// record, entry or attribute, so mrtdump exits 0 on each and Routes yields
// no error. A difference from an oracle on them is then never hidden behind
// a record mrt refused.
func TestRealFixturesClean(t *testing.T) {
	for _, f := range fixtures(t) {
		if !strings.HasPrefix(f.path, "testdata/mrt/") {
			continue
		}
		if st, stderr := mrtdumpStatus(t, f); st != 0 {
			t.Errorf("%s: mrtdump exits %d:\n%s", f.name, st, stderr)
		}
		if n := routeErrors(t, f); n != 0 {
			t.Errorf("%s: Routes yields %d errors", f.name, n)
		}
	}
}
