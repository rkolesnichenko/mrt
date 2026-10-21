package mrt_test

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rkolesnichenko/mrt"
	"github.com/rkolesnichenko/mrt/internal/mrtdump"
)

// TestRealData is the opt-in real-data run (spec §7.5): for every file
// scripts/fetch-mrt.sh pinned in $MRT_REALDATA/realdata/MANIFEST, smallest
// first, `mrtdump -m -l -u` and `TZ=UTC bgpdump -m -l -u` are streamed side
// by side through two pipes and compared line by line, neither output held.
// A differing line pair passes only when classify names the known divergence
// that explains it; every other one fails the test (the first 20 printed).
// Run it alone, under the memory guard (Task 14's report has the command):
//
//	MRT_REALDATA=$PWD/.data go test -count=1 -timeout 60m -run TestRealData -v .
//
// One file: -run 'TestRealData/rv6-rib.20261016.1400.bz2'.
func TestRealData(t *testing.T) {
	dir := os.Getenv("MRT_REALDATA")
	if dir == "" {
		t.Skip("MRT_REALDATA is not set (scripts/fetch-mrt.sh, then MRT_REALDATA=$PWD/.data)")
	}
	bgpdump, err := exec.LookPath("bgpdump")
	if err != nil {
		t.Fatalf("MRT_REALDATA is set but bgpdump is not on PATH (scripts/build-oracles.sh): %v", err)
	}
	dir = filepath.Join(dir, "realdata")
	files := readManifest(t, filepath.Join(dir, "MANIFEST"))
	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			path := filepath.Join(dir, f.name)
			if err := checkPinned(path, f); err != nil {
				t.Fatalf("%v (rerun scripts/fetch-mrt.sh)", err)
			}
			realDataFile(t, bgpdump, path)
		})
	}
	// decompress-with/<file>: DecompressWith (4 workers) yields the bytes
	// Decompress does. One file at a time: -run 'TestRealData/decompress-with'.
	t.Run("decompress-with", func(t *testing.T) {
		for _, f := range files {
			t.Run(f.name, func(t *testing.T) {
				path := filepath.Join(dir, f.name)
				if err := checkPinned(path, f); err != nil {
					t.Fatalf("%v (rerun scripts/fetch-mrt.sh)", err)
				}
				want, wn := decompressedSum(t, path, false)
				got, gn := decompressedSum(t, path, true)
				if got != want || gn != wn {
					t.Errorf("DecompressWith: %d bytes, sha256 %x; Decompress: %d bytes, sha256 %x", gn, got, wn, want)
				}
			})
		}
	})
}

// decompressedSum returns the SHA-256 and length of the decompressed file,
// through DecompressWith (4 workers) when with is set, else Decompress.
func decompressedSum(t *testing.T, path string, with bool) ([sha256.Size]byte, int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var r io.Reader
	if with {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, err = mrt.DecompressWith(f, mrt.DecompressOptions{Workers: 4, Context: ctx})
	} else {
		r, err = mrt.Decompress(f)
	}
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		t.Fatalf("reading %s (with=%v): %v", path, with, err)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, n
}

// manifestFile is one line of MANIFEST: name, URL, size, sha256.
type manifestFile struct {
	name, url, sha256 string
	size              int64
}

// checkPinned errors unless the file at path has the size and sha256 that
// MANIFEST pins for it, so a replaced file cannot stand in for the pinned set.
func checkPinned(path string, f manifestFile) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
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

// readManifest reads MANIFEST, smallest file first.
func readManifest(t *testing.T, path string) []manifestFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run scripts/fetch-mrt.sh)", err)
	}
	var fs []manifestFile
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		if len(f) != 4 {
			t.Fatalf("%s: bad line %q", path, l)
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			t.Fatalf("%s: bad size in %q", path, l)
		}
		fs = append(fs, manifestFile{name: f[0], url: f[1], sha256: f[3], size: size})
	}
	slices.SortStableFunc(fs, func(a, b manifestFile) int { return cmp.Compare(a.size, b.size) })
	return fs
}

// realDataFile compares mrtdump and bgpdump over one file and logs the
// figures.
func realDataFile(t *testing.T, bgpdump, path string) {
	start := time.Now()

	counted := make(chan recordCounts, 1)
	go func() { counted <- countRecords(path) }()

	pr, pw := io.Pipe()
	defer pr.Close()
	ourErr := &headWriter{prefix: "mrtdump: "}
	ourStatus := make(chan int, 1)
	go func() {
		st := mrtdump.Run([]string{"-m", "-l", "-u", path}, nil, pw, ourErr)
		pw.Close()
		ourStatus <- st
	}()

	cmd := exec.Command(bgpdump, "-m", "-l", "-u", path)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	theirErr := &headWriter{}
	cmd.Stderr = theirErr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill() // a no-op once it has exited

	ours, theirs := newLineReader(pr), newLineReader(out)
	s := compareSources(ours.next, theirs.next)
	theirWait := cmd.Wait()
	st := <-ourStatus
	c := <-counted
	wall := time.Since(start)

	for _, r := range []*lineReader{ours, theirs} {
		if r.err != nil {
			t.Errorf("reading output: %v", r.err)
		}
	}
	if c.err != nil {
		t.Errorf("counting records: %v", c.err)
	}
	name := filepath.Base(path)
	t.Logf("%s: records %d (record errors %d; RIB entries %d, BGP4MP messages %d)", name, c.records, c.recordErrors, c.ribEntries, c.messages)
	t.Logf("%s: mrtdump: exit %d, %d lines (B %d, A %d, W %d, STATE %d), %d problems reported on stderr",
		name, st, s.ourLines, s.ourKinds["B"], s.ourKinds["A"], s.ourKinds["W"], s.ourKinds["STATE"], ourErr.lines)
	t.Logf("%s: bgpdump: %v, %d lines, %d stderr lines", name, exitText(theirWait), s.theirLines, theirErr.lines)
	t.Logf("%s: compared %d line pairs: known divergences %v, unclassified %d; wall %s", name, s.lines, s.known, s.unclassified, wall.Round(time.Millisecond))
	for _, id := range slices.Sorted(maps.Keys(s.examples)) {
		t.Logf("%s: first %s:\n%s", name, id, s.examples[id])
	}
	if ourErr.lines > 0 {
		t.Logf("mrtdump's first problems:\n%s", ourErr.head())
	}
	if theirErr.lines > 0 {
		t.Logf("bgpdump's first stderr lines:\n%s", theirErr.head())
	}
	if st > 1 {
		t.Errorf("mrtdump exits %d", st)
	}
	if theirWait != nil {
		t.Errorf("bgpdump: %v", theirWait)
	}
	if s.unclassified > 0 {
		t.Errorf("%d mismatches no known divergence explains; the first %d:\n%s", s.unclassified, len(s.first), strings.Join(s.first, "\n"))
	}
}

func exitText(err error) string {
	if err == nil {
		return "exit 0"
	}
	return err.Error()
}

// recordCounts is what one streaming pass of Records over a file finds.
type recordCounts struct {
	records, recordErrors, ribEntries, messages int
	err                                         error // other than a *RecordError
}

func countRecords(path string) (c recordCounts) {
	f, err := os.Open(path)
	if err != nil {
		return recordCounts{err: err}
	}
	defer f.Close()
	r, err := mrt.Decompress(f)
	if err != nil {
		return recordCounts{err: err}
	}
	for rec, err := range mrt.Records(r) {
		c.records++
		if err != nil {
			var re *mrt.RecordError
			if !errors.As(err, &re) {
				c.err = err
				return c
			}
			c.recordErrors++
			continue
		}
		switch r := rec.(type) {
		case mrt.RIB:
			c.ribEntries += int(r.EntryCount())
		case mrt.BGP4MPMessage:
			c.messages++
		}
	}
	return c
}

// headWriter counts the lines written to it (those starting with prefix, if
// set) and keeps the first 20.
type headWriter struct {
	prefix  string
	mu      sync.Mutex
	partial []byte
	first   []string
	lines   int
}

func (w *headWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		l := string(w.partial[:i])
		w.partial = w.partial[i+1:]
		if !strings.HasPrefix(l, w.prefix) {
			continue
		}
		w.lines++
		if len(w.first) < 20 {
			w.first = append(w.first, l)
		}
	}
	if len(w.partial) == 0 {
		w.partial = nil // do not keep a large backing array
	}
	return len(p), nil
}

func (w *headWriter) head() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.first, "\n")
}

// lineReader reads lines without their newline; err is the first read error
// other than EOF.
type lineReader struct {
	r   *bufio.Reader
	err error
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: bufio.NewReaderSize(r, 1<<20)} }

func (l *lineReader) next() (string, bool) {
	s, err := l.r.ReadString('\n')
	if err != nil {
		if err != io.EOF {
			l.err = err
		}
		if s == "" {
			return "", false
		}
	}
	return strings.TrimSuffix(s, "\n"), true
}

// resyncWindow bounds how far ahead compareSources looks on each side for
// the line the other side gives next (a RIB record prints one line per
// peer: a whole record printed by one side only stays within it).
const resyncWindow = 4096

// lineQueue holds the lines read ahead of the comparison.
type lineQueue struct {
	next func() (string, bool)
	buf  []string
	done bool
}

func (q *lineQueue) peek(i int) (string, bool) {
	for len(q.buf) <= i && !q.done {
		l, ok := q.next()
		if !ok {
			q.done = true
			break
		}
		q.buf = append(q.buf, l)
	}
	if i < len(q.buf) {
		return q.buf[i], true
	}
	return "", false
}

func (q *lineQueue) pop() {
	q.buf[0] = ""
	q.buf = q.buf[1:]
}

// find is the index (1 to resyncWindow) of the first line ahead whose key is
// key, or -1.
func (q *lineQueue) find(key string) int {
	for i := 1; i <= resyncWindow; i++ {
		l, ok := q.peek(i)
		if !ok {
			return -1
		}
		if lineKey(l) == key {
			return i
		}
	}
	return -1
}

// lineKey is a bgpdump -m line's type, time, kind, peer, AS and prefix, and
// the path ID on an add-path line.
func lineKey(l string) string {
	n := 7
	if f, _, _ := strings.Cut(l, "|"); strings.HasSuffix(f, "_AP") {
		n = 8
	}
	f := strings.SplitN(l, "|", n)
	return strings.Join(f[:min(n-1, len(f))], "|")
}

// compareStats is what a comparison found.
type compareStats struct {
	lines                int // steps: a line pair, or a line only one side gives
	ourLines, theirLines int
	ourKinds             map[string]int    // mrtdump lines by kind (B, A, W, STATE)
	known                map[string]int    // mismatches by divergence ID
	examples             map[string]string // the first mismatch of each known ID
	unclassified         int
	first                []string // the first 20 unclassified mismatches
}

func (s *compareStats) mismatch(ours, theirs string) {
	if ids, ok := classify(ours, theirs); ok {
		for _, id := range ids {
			s.known[id]++
			if _, ok := s.examples[id]; !ok {
				s.examples[id] = fmt.Sprintf("mrtdump (%d bytes): %.300s\nbgpdump (%d bytes): %.300s", len(ours), ours, len(theirs), theirs)
			}
		}
		return
	}
	s.unclassified++
	if len(s.first) < 20 {
		if ours == "" {
			ours = "(none)"
		}
		if theirs == "" {
			theirs = "(none)"
		}
		s.first = append(s.first, "mrtdump: "+ours+"\nbgpdump: "+theirs)
	}
}

// compareLines compares two slices of lines (for tests).
func compareLines(ours, theirs []string) compareStats {
	from := func(ls []string) func() (string, bool) {
		return func() (string, bool) {
			if len(ls) == 0 {
				return "", false
			}
			l := ls[0]
			ls = ls[1:]
			return l, true
		}
	}
	return compareSources(from(ours), from(theirs))
}

// compareSources compares mrtdump's lines with bgpdump's in step. Lines
// with the same key are a pair; when the keys differ, the side whose next
// line the other gives soonest (within resyncWindow) has a line the other
// does not, which is a mismatch on its own.
func compareSources(oursNext, theirsNext func() (string, bool)) compareStats {
	s := compareStats{ourKinds: map[string]int{}, known: map[string]int{}, examples: map[string]string{}}
	countOurs := func() (string, bool) {
		l, ok := oursNext()
		if ok {
			s.ourLines++
			if f := strings.SplitN(l, "|", 4); len(f) > 2 {
				s.ourKinds[f[2]]++
			}
		}
		return l, ok
	}
	countTheirs := func() (string, bool) {
		l, ok := theirsNext()
		if ok {
			s.theirLines++
		}
		return l, ok
	}
	ours, theirs := &lineQueue{next: countOurs}, &lineQueue{next: countTheirs}
	for {
		a, okA := ours.peek(0)
		b, okB := theirs.peek(0)
		if !okA && !okB {
			return s
		}
		s.lines++
		switch {
		case okA && okB && a == b:
			ours.pop()
			theirs.pop()
		case !okB:
			s.mismatch(a, "")
			ours.pop()
		case !okA:
			s.mismatch("", b)
			theirs.pop()
		case lineKey(a) == lineKey(b):
			s.mismatch(a, b)
			ours.pop()
			theirs.pop()
		default:
			i, j := ours.find(lineKey(b)), theirs.find(lineKey(a))
			switch {
			case i > 0 && (j < 0 || i <= j):
				s.mismatch(a, "")
				ours.pop()
			case j > 0:
				s.mismatch("", b)
				theirs.pop()
			default:
				s.mismatch(a, b)
				ours.pop()
				theirs.pop()
			}
		}
	}
}

// classify names the known divergences (testdata/bgpdump/divergences.md)
// that explain every field in which bgpdump's line theirs differs from
// mrtdump's line ours, in field order; ok is false when one field is not
// explained. Only announcement and RIB lines that both sides print are
// classified:
//
//   - B5: LOCAL_PREF or MED printed with %d, negative, the same value modulo 2^32.
//   - S16: a community or large-community field cut at bgpdump's BUFSIZ:
//     a prefix of mrtdump's of exactly BUFSIZ-2 characters (communityCuts).
//   - S17: an AS path cut with "..." appended right after the ASN that
//     passed 7900 characters (aspathCutAfter, aspathCutMax).
func classify(ours, theirs string) (ids []string, ok bool) {
	if ours == "" || theirs == "" {
		return nil, false
	}
	o, th := strings.Split(ours, "|"), strings.Split(theirs, "|")
	base := 6 // the AS path column
	if strings.HasSuffix(o[0], "_AP") {
		base = 7
	}
	if len(o) != len(th) || len(o) < base+10 || (o[2] != "A" && o[2] != "B") {
		return nil, false
	}
	for i := range o {
		if o[i] == th[i] {
			continue
		}
		var id string
		switch {
		case i == base && cutPath(o[i], th[i]):
			id = "S17"
		case (i == base+3 || i == base+4) && signedValue(o[i], th[i]):
			id = "B5"
		case (i == base+5 || i == base+6) && cutCommunities(o[i], th[i]):
			id = "S16"
		default:
			return nil, false
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, len(ids) > 0
}

// signedValue: theirs is ours (a uint32) printed as an int32 (B5).
func signedValue(ours, theirs string) bool {
	u, err1 := strconv.ParseUint(ours, 10, 32)
	v, err2 := strconv.ParseInt(theirs, 10, 32)
	return err1 == nil && err2 == nil && v < 0 && uint32(int32(v)) == uint32(u)
}

// bgpdump builds a community or large-community field in char buf[BUFSIZ],
// each value with a leading space, snprintf/strlcat filling it to BUFSIZ-1,
// and prints it from the second character (bgpdump_lib.c
// process_attr_community_string, process_attr_lcommunity_string; bgpdump.c
// `community->str+1`): a cut field is exactly BUFSIZ-2 characters, 1022 on
// macOS (BUFSIZ 1024) and 8190 with glibc (8192).
var communityCuts = []int{1024 - 2, 8192 - 2}

// cutCommunities: theirs is ours cut at bgpdump's buffer (S16).
func cutCommunities(ours, theirs string) bool {
	return slices.Contains(communityCuts, len(theirs)) && len(theirs) < len(ours) && strings.HasPrefix(ours, theirs)
}

// bgpdump writes an AS path into MAX_ASPATH_LEN (8000) bytes and, right
// after the ASN that takes the text past MAX_ASPATH_LEN-100, appends "..."
// and stops (bgpdump_lib.c process_attr_aspath_string). Before that ASN the
// text was at most 7900 characters; a segment's closing and opening
// characters, a separator and at most 10 digits follow, so the part kept is
// 7901 to 7913 characters and ends with a digit.
const (
	aspathCutAfter = 8000 - 100
	aspathCutMax   = aspathCutAfter + 3 + 10
)

// cutPath: theirs is ours cut with "..." appended (S17). The ASN that crossed
// the limit may have been the last, so the kept part may be all of ours.
func cutPath(ours, theirs string) bool {
	p, ok := strings.CutSuffix(theirs, "...")
	if !ok || len(p) <= aspathCutAfter || len(p) > aspathCutMax || !strings.HasPrefix(ours, p) {
		return false
	}
	isDigit := func(c byte) bool { return '0' <= c && c <= '9' }
	return isDigit(p[len(p)-1]) && (len(p) == len(ours) || !isDigit(ours[len(p)]))
}

func TestClassifyRealDataMismatch(t *testing.T) {
	long := strings.Repeat("65000:1 ", 200) // 1600 characters
	cut := long[:1022]
	longer := strings.Repeat("65000:1 ", 1100) // 8800 characters
	path1500 := strings.TrimSpace(strings.Repeat("64500 ", 1500))
	set7900 := "{" + strings.Repeat("64500,", 1316) + "645" // 7900 characters
	rib := func(path string) string {
		return "TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|" + path + "|IGP|192.0.2.20|0|0|||NAG|||"
	}
	for _, tc := range []struct {
		name, ours, theirs string
		want               string // "" for unclassified
	}{
		{"B5 from the fixture",
			"BGP4MP|1700000000|A|192.0.2.10|64500|198.51.110.0/24|64500 64501|IGP|192.0.2.20|3000000000|4000000000|||NAG|||",
			"BGP4MP|1700000000|A|192.0.2.10|64500|198.51.110.0/24|64500 64501|IGP|192.0.2.20|-1294967296|-294967296|||NAG|||",
			"B5"},
		{"B5 MED only, add-path columns",
			"BGP4MP_ET_AP|1.000001|A|192.0.2.10|64500|198.51.110.0/24|7|64500|IGP|192.0.2.20|0|2147483648|||NAG|||",
			"BGP4MP_ET_AP|1.000001|A|192.0.2.10|64500|198.51.110.0/24|7|64500|IGP|192.0.2.20|0|-2147483648|||NAG|||",
			"B5"},
		{"B5 needs the same value modulo 2^32",
			"BGP4MP|1|A|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|3000000000|0|||NAG|||",
			"BGP4MP|1|A|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|-1|0|||NAG|||",
			""},
		{"S16 communities cut at BUFSIZ",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + long + "||NAG|||",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + cut + "||NAG|||",
			"S16"},
		{"S16 large communities cut",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0||" + long + "|NAG|||",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0||" + cut + "|NAG|||",
			"S16"},
		{"S16 communities cut at glibc's BUFSIZ",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + longer + "||NAG|||",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + longer[:8190] + "||NAG|||",
			"S16"},
		{"a 1010-character prefix is not bgpdump's cut",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + long + "||NAG|||",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + long[:1010] + "||NAG|||",
			""},
		{"a 1022-character field that is not a prefix",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + long + "||NAG|||",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|" + "7" + cut[1:] + "||NAG|||",
			""},
		{"a short community field cut is not S16",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|65000:1 65000:2||NAG|||",
			"TABLE_DUMP2|1|B|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|0|0|65000:1||NAG|||",
			""},
		// bgpdump appends "..." as soon as the text passes 7900 characters
		// right after an ASN: with 6-character steps ASN 1317 ends at 7901.
		{"S17 AS path cut with ...", rib(path1500), rib(path1500[:7901] + "..."), "S17"},
		{"S17: the ASN that crossed 7900 was the last", rib(path1500[:7901]), rib(path1500[:7901] + "..."), "S17"},
		{"S17: a kept part of 7895, not past 7900, is not bgpdump's", rib(path1500), rib(path1500[:7895] + "..."), ""},
		{"S17: cut past 7913 is not bgpdump's", rib(path1500), rib(path1500[:7919] + "..."), ""},
		// The longest kept part: an AS_SET's text reaches exactly 7900, then
		// "} {" and a 10-digit ASN in the next set: 7913.
		{"S17 at 7913", rib(set7900 + "} {4200000000}"), rib(set7900 + "} {4200000000..."), "S17"},
		{"S17: 7914 is past bgpdump's longest", rib(set7900 + "} {42000000001}"), rib(set7900 + "} {42000000001..."), ""},
		{"S17: cut inside an ASN is not bgpdump's", rib(path1500), rib(path1500[:7903] + "..."), ""},
		{"S17: theirs not a prefix", rib(path1500), rib("1" + path1500[1:7901] + "..."), ""},
		{"B5 and S16 on one line",
			"BGP4MP|1|A|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|3000000000|0|" + long + "||NAG|||",
			"BGP4MP|1|A|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|-1294967296|0|" + cut + "||NAG|||",
			"B5+S16"},
		{"another field differs too",
			"BGP4MP|1|A|192.0.2.10|64500|198.51.110.0/24|64500|IGP|192.0.2.20|3000000000|0|||NAG|||",
			"BGP4MP|1|A|192.0.2.10|64500|198.51.110.0/24|64501|IGP|192.0.2.20|-1294967296|0|||NAG|||",
			""},
		{"a withdrawal is never classified",
			"BGP4MP|1|W|192.0.2.10|64500|198.51.110.0/24",
			"BGP4MP|1|W|192.0.2.10|64500|198.51.111.0/24",
			""},
	} {
		ids, ok := classify(tc.ours, tc.theirs)
		got := strings.Join(ids, "+")
		if !ok {
			got = ""
		}
		if got != tc.want {
			t.Errorf("%s: classify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCompareLinesResyncs: a line only one side prints is one unclassified
// mismatch, not a cascade over every line after it.
func TestCompareLinesResyncs(t *testing.T) {
	a := "BGP4MP|1|A|192.0.2.1|1|192.0.2.0/24|1|IGP|192.0.2.1|0|0|||NAG|||"
	b := "BGP4MP|1|A|192.0.2.1|1|192.0.2.1/32|1|IGP|192.0.2.1|0|0|||NAG|||"
	c := "BGP4MP|1|A|192.0.2.1|1|192.0.2.2/32|1|IGP|192.0.2.1|0|0|||NAG|||"
	x := "BGP4MP|1|STATE|192.0.2.1|1|1|2"
	b5ours := "BGP4MP|2|A|192.0.2.1|1|192.0.2.3/32|1|IGP|192.0.2.1|3000000000|0|||NAG|||"
	b5theirs := "BGP4MP|2|A|192.0.2.1|1|192.0.2.3/32|1|IGP|192.0.2.1|-1294967296|0|||NAG|||"
	for _, tc := range []struct {
		name          string
		ours, theirs  []string
		lines, uncl   int
		known         map[string]int
		firstContains string
	}{
		{"equal", []string{a, b, c}, []string{a, b, c}, 3, 0, map[string]int{}, ""},
		{"ours has one more", []string{a, x, b, c}, []string{a, b, c}, 4, 1, map[string]int{}, "mrtdump: " + x},
		{"theirs has one more", []string{a, b, c}, []string{a, b, x, c}, 4, 1, map[string]int{}, "bgpdump: " + x},
		{"theirs ends early", []string{a, b, c}, []string{a}, 3, 2, map[string]int{}, "mrtdump: " + b},
		{"known value divergence", []string{a, b5ours, c}, []string{a, b5theirs, c}, 3, 0, map[string]int{"B5": 1}, ""},
	} {
		s := compareLines(tc.ours, tc.theirs)
		if s.lines != tc.lines || s.unclassified != tc.uncl || len(s.known) != len(tc.known) {
			t.Errorf("%s: %d lines, %d unclassified, known %v; want %d, %d, %v", tc.name, s.lines, s.unclassified, s.known, tc.lines, tc.uncl, tc.known)
			continue
		}
		for id, n := range tc.known {
			if s.known[id] != n {
				t.Errorf("%s: known %v, want %v", tc.name, s.known, tc.known)
			}
		}
		if tc.firstContains != "" && !strings.Contains(strings.Join(s.first, "\n"), tc.firstContains) {
			t.Errorf("%s: first mismatches %q lack %q", tc.name, s.first, tc.firstContains)
		}
	}
}

func TestCheckPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sum = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // sha256("abc")
	tests := []struct {
		f     manifestFile
		wantE string // "" for no error, else a substring of it
	}{
		{manifestFile{name: "f", size: 3, sha256: sum}, ""},
		{manifestFile{name: "f", size: 4, sha256: sum}, "3 bytes, MANIFEST pins 4"},
		{manifestFile{name: "f", size: 3, sha256: strings.Repeat("0", 64)}, "sha256 " + sum},
	}
	for _, tc := range tests {
		err := checkPinned(path, tc.f)
		switch {
		case tc.wantE == "" && err != nil:
			t.Errorf("%+v: %v", tc.f, err)
		case tc.wantE != "" && (err == nil || !strings.Contains(err.Error(), tc.wantE)):
			t.Errorf("%+v: err = %v, want it to contain %q", tc.f, err, tc.wantE)
		}
	}
}
