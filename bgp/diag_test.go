package bgp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestDiagnosticCodesAreConstants fails on any string literal that spells a
// diagnostic code ("bgp/...") outside diag.go: code in this package must use
// the Diag* constants.
func TestDiagnosticCodesAreConstants(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "diag.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(s, "bgp/") {
				t.Errorf("%s: string literal %s; use the Diag* constant", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}
