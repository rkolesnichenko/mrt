package oracle

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func writeDoc(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "divergences.md")
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const sampleDoc = "# title\n\n## bgpdump\n\n### B1. one\n\n```pair\nfixture: f\nmrtdump: a\nbgpdump: (none)\n```\n\n" +
	"## bgpkit-parser\n\n### K1. two\n\n```pair\nfixture: g\nmrt: (none)\nbgpkit: b\n```\n\n```pair\nfixture: g\nmrt: c\nbgpkit: d\n```\n\n" +
	"## gobgp\n\n### G1. three\n\n```pair\nfixture: h\nmrt: e\ngobgp: x\n```\n\n" +
	"## Other\n\n### X1. no pairs here\n\ntext\n"

func TestParseDivergences(t *testing.T) {
	got, err := ParseDivergences(writeDoc(t, sampleDoc), "bgpdump", "bgpkit-parser", "gobgp")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Pair{
		"bgpdump":       {{Entry: "B1. one", Fixture: "f", Ours: "a"}},
		"bgpkit-parser": {{Entry: "K1. two", Fixture: "g", Theirs: "b"}, {Entry: "K1. two", Fixture: "g", Ours: "c", Theirs: "d"}},
		"gobgp":         {{Entry: "G1. three", Fixture: "h", Ours: "e", Theirs: "x"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseDivergencesRefuses(t *testing.T) {
	for _, tc := range []struct{ name, doc, why string }{
		{"pair outside the sections", "## Other\n\n### X\n\n```pair\nfixture: f\nmrt: a\n```\n", "outside"},
		{"pair outside an entry", "## bgpkit-parser\n\n```pair\nfixture: f\nmrt: a\n```\n", "outside an entry"},
		{"no fixture", "## bgpkit-parser\n\n### K\n\n```pair\nmrt: a\n```\n", "needs a fixture"},
		{"no line", "## bgpkit-parser\n\n### K\n\n```pair\nfixture: f\nmrt: (none)\nbgpkit: (none)\n```\n", "at least one line"},
		{"wrong label", "## bgpkit-parser\n\n### K\n\n```pair\nfixture: f\nmrtdump: a\n```\n", "unknown line"},
		{"unterminated", "## bgpkit-parser\n\n### K\n\n```pair\nfixture: f\nmrt: a\n", "unterminated"},
		{"entry without a pair", "## bgpkit-parser\n\n### K1. empty\n\ntext\n", `"bgpkit-parser/K1. empty" has no pair block`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDivergences(writeDoc(t, tc.doc), "bgpdump", "bgpkit-parser")
			if err == nil || !strings.Contains(err.Error(), tc.why) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.why)
			}
		})
	}
}

func TestDocumentedWithout(t *testing.T) {
	ps := []Pair{{Fixture: "f", Ours: "a"}, {Fixture: "f", Ours: "a", Theirs: "b"}, {Fixture: "g", Ours: "c"}}
	ours, theirs := Documented(ps, "f")
	if !reflect.DeepEqual(ours, map[string]int{"a": 2}) || !reflect.DeepEqual(theirs, map[string]int{"b": 1}) {
		t.Errorf("Documented = %v %v", ours, theirs)
	}
	ls := []string{"a", "b", "a", "c", "a"}
	if got := Without(ls, map[string]int{"a": 2, "c": 1}); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("Without = %q, want [b a]", got)
	}
	// "a" differs twice (3 against 1): documenting it twice holds, three times does not.
	o, g := []string{"a", "a", "a", "b"}, []string{"a", "b"}
	if bad := StillDiffers(o, g, map[string]int{"a": 2}); len(bad) != 0 {
		t.Errorf("a documented twice: %q", bad)
	}
	if bad := StillDiffers(o, g, map[string]int{"a": 3, "b": 1}); !slices.Equal(bad, []string{"a", "b"}) {
		t.Errorf("a three times, b once: %q, want [a b]", bad)
	}
	onlyA, onlyB := Diff([]string{"x", "y", "y"}, []string{"y", "z"})
	if !slices.Equal(onlyA, []string{"x", "y"}) || !slices.Equal(onlyB, []string{"z"}) {
		t.Errorf("Diff = %q %q", onlyA, onlyB)
	}
}
