package oracle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
)

// A Pair is one documented difference: the line mrt gives and the line the
// oracle gives, either of them "" when that side gives none.
type Pair struct {
	Entry, Fixture string
	Ours, Theirs   string
}

// Labels returns the names a pair block of section gives its two lines:
// "mrtdump" and "bgpdump" in the bgpdump section, else "mrt" and the section
// name less a "-parser" suffix ("bgpkit" in bgpkit-parser, "gobgp" in gobgp).
func Labels(section string) (ours, theirs string) {
	if section == "bgpdump" {
		return "mrtdump", "bgpdump"
	}
	return "mrt", strings.TrimSuffix(section, "-parser")
}

// ParseDivergences reads a divergences file (testdata/bgpdump/divergences.md,
// testdata/bmp/divergences.md) and returns the pairs of each of the named
// "## SECTION" sections, by section. A pair is a fenced block
//
//	```pair
//	fixture: NAME
//	OURS: LINE
//	THEIRS: LINE
//	```
//
// under an entry heading "### ID. title", with the labels Labels gives; (none)
// stands for no line. A pair outside those sections or outside an entry, a
// pair with no fixture or no line, an unknown line in a pair, an unterminated
// pair, and an entry of those sections that holds no pair are errors: a
// difference no fixture shows belongs in another section.
func ParseDivergences(path string, sections ...string) (map[string][]Pair, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string][]Pair{}
	var section, entry string
	var cur *Pair
	var entries []string        // the entries of the named sections, as "section/entry", in order
	pairsOf := map[string]int{} // their pair counts
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		l := sc.Text()
		bad := func(why string) error { return fmt.Errorf("%s:%d: %s: %q", path, n, why, l) }
		switch {
		case cur != nil && l == "```":
			if cur.Fixture == "" || (cur.Ours == "" && cur.Theirs == "") {
				return nil, bad("a pair needs a fixture and at least one line")
			}
			out[section] = append(out[section], *cur)
			pairsOf[section+"/"+entry]++
			cur = nil
		case cur != nil:
			ours, theirs := Labels(section)
			val := func(s string) string {
				if s == "(none)" {
					return ""
				}
				return s
			}
			switch {
			case strings.HasPrefix(l, "fixture: "):
				cur.Fixture = strings.TrimPrefix(l, "fixture: ")
			case strings.HasPrefix(l, ours+": "):
				cur.Ours = val(strings.TrimPrefix(l, ours+": "))
			case strings.HasPrefix(l, theirs+": "):
				cur.Theirs = val(strings.TrimPrefix(l, theirs+": "))
			default:
				return nil, bad("unknown line in a pair")
			}
		case strings.HasPrefix(l, "## "):
			section, entry = strings.TrimPrefix(l, "## "), ""
		case strings.HasPrefix(l, "### "):
			entry = strings.TrimPrefix(l, "### ")
			if slices.Contains(sections, section) {
				entries = append(entries, section+"/"+entry)
			}
		case l == "```pair":
			if !slices.Contains(sections, section) {
				return nil, bad(fmt.Sprintf("a pair outside the %s sections", strings.Join(sections, " and ")))
			}
			if entry == "" {
				return nil, bad("a pair outside an entry")
			}
			cur = &Pair{Entry: entry}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cur != nil {
		return nil, fmt.Errorf("%s: unterminated pair", path)
	}
	for _, e := range entries {
		if pairsOf[e] == 0 {
			return nil, fmt.Errorf("%s: entry %q has no pair block", path, e)
		}
	}
	return out, nil
}

// Documented returns how many times each line of fixture is documented in
// ps, on each side: every pair stands for one occurrence.
func Documented(ps []Pair, fixture string) (ours, theirs map[string]int) {
	ours, theirs = map[string]int{}, map[string]int{}
	for _, p := range ps {
		if p.Fixture != fixture {
			continue
		}
		if p.Ours != "" {
			ours[p.Ours]++
		}
		if p.Theirs != "" {
			theirs[p.Theirs]++
		}
	}
	return ours, theirs
}

// Without returns ls less the first drop[l] occurrences of each line l.
func Without(ls []string, drop map[string]int) []string {
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

// StillDiffers returns, sorted, the documented lines of one side (doc) that
// the side does not hold as many more times than the other side as they are
// documented.
func StillDiffers(side, other []string, doc map[string]int) []string {
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

// Diff returns the lines of a not in b and of b not in a, as multisets, in
// the order they come.
func Diff(a, b []string) (onlyA, onlyB []string) {
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

// FirstDifference describes where two line lists first differ.
func FirstDifference(ours, theirs []string) string {
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

// PairBlocks writes undocumented lines as pair blocks, pairing a line of
// ours with the first unused line of theirs that has the same key, trying
// each of key's keys in turn (most specific first).
func PairBlocks(fixture, oursName, theirsName string, ours, theirs []string, key func(string) []string) string {
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

// LineKey is a normalized line's kind, peer, prefix and path ID, then the
// same without the path ID: the keys PairBlocks pairs normalized lines by.
func LineKey(l string) []string {
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
