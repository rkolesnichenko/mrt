package bgp

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestDiagnosticCodesAreConstants fails on any string literal that spells a
// diagnostic code ("bgp/...") outside diag.go, and on any Diagnostic composite
// literal (keyed or positional, also elided inside []Diagnostic{{...}}) whose
// Code is not an identifier naming a Diag* constant of diag.go. A parameter
// passed through (a helper's code argument) is followed to every call site of
// the helper, where it must satisfy the same rule.
func TestDiagnosticCodesAreConstants(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	consts := map[string]bool{}
	for _, d := range files["diag.go"].Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
			for _, sp := range g.Specs {
				for _, id := range sp.(*ast.ValueSpec).Names {
					if strings.HasPrefix(id.Name, "Diag") {
						consts[id.Name] = true
					}
				}
			}
		}
	}
	if len(consts) < 10 {
		t.Fatalf("found only %d Diag* constants in diag.go", len(consts))
	}
	var nLiterals, nPassed int
	checkLiterals(t, fset, files, consts, &nLiterals, &nPassed)
	if nLiterals < 15 || nPassed < 5 {
		t.Errorf("checked only %d Diagnostic literals and %d pass-through call sites; the scan is not finding them", nLiterals, nPassed)
	}

	for name, f := range files {
		if name == "diag.go" {
			continue
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

// walk calls fn for every node under root with the stack of its ancestors,
// the node last.
func walk(root ast.Node, fn func(n ast.Node, stack []ast.Node)) {
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		fn(n, stack)
		return true
	})
}

// isDiagnosticLit reports whether lit (the last node of stack) builds a Diagnostic.
func isDiagnosticLit(lit *ast.CompositeLit, stack []ast.Node) bool {
	if id, ok := lit.Type.(*ast.Ident); ok {
		return id.Name == "Diagnostic"
	}
	if lit.Type != nil || len(stack) < 2 {
		return false
	}
	outer, ok := stack[len(stack)-2].(*ast.CompositeLit) // []Diagnostic{{...}}
	if !ok {
		return false
	}
	at, ok := outer.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	id, ok := at.Elt.(*ast.Ident)
	return ok && id.Name == "Diagnostic"
}

func checkLiterals(t *testing.T, fset *token.FileSet, files map[string]*ast.File, consts map[string]bool, nLiterals, nPassed *int) {
	t.Helper()
	// check requires expr, evaluated at the end of stack, to be a Diag* constant
	// or a parameter every call site of whose function passes one.
	var check func(expr ast.Expr, stack []ast.Node, seen map[string]bool)
	check = func(expr ast.Expr, stack []ast.Node, seen map[string]bool) {
		id, ok := expr.(*ast.Ident)
		if !ok {
			t.Errorf("%s: Diagnostic code is not an identifier naming a Diag* constant", fset.Position(expr.Pos()))
			return
		}
		if consts[id.Name] {
			return
		}
		// A parameter of the innermost function that declares it?
		for i := len(stack) - 1; i >= 0; i-- {
			var ft *ast.FuncType
			switch fn := stack[i].(type) {
			case *ast.FuncDecl:
				ft = fn.Type
			case *ast.FuncLit:
				ft = fn.Type
			default:
				continue
			}
			idx, found := -1, false
			for _, fld := range ft.Params.List {
				for _, nm := range fld.Names {
					idx++
					if nm.Name == id.Name {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				continue
			}
			// The helper's name and the code that may call it.
			var helper string
			var scope ast.Node
			if fd, ok := stack[i].(*ast.FuncDecl); ok {
				helper = fd.Name.Name
				scope = nil // every file
			} else {
				if as, ok := stack[i-1].(*ast.AssignStmt); ok && len(as.Lhs) == 1 {
					if hid, ok := as.Lhs[0].(*ast.Ident); ok {
						helper = hid.Name
					}
				}
				for j := i - 1; j >= 0; j-- {
					if _, ok := stack[j].(*ast.FuncDecl); ok {
						scope = stack[j]
						break
					}
				}
			}
			key := fmt.Sprintf("%s/%s/%d", helper, id.Name, idx)
			if helper == "" || seen[key] {
				if helper == "" {
					t.Errorf("%s: code %s is a parameter of an anonymous function", fset.Position(id.Pos()), id.Name)
				}
				return
			}
			seen[key] = true
			calls := 0
			visit := func(n ast.Node, st []ast.Node) {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return
				}
				switch f := c.Fun.(type) {
				case *ast.Ident:
					if f.Name != helper {
						return
					}
				case *ast.SelectorExpr:
					if scope != nil || f.Sel.Name != helper {
						return
					}
				default:
					return
				}
				if idx >= len(c.Args) {
					t.Errorf("%s: call of %s has no argument %d", fset.Position(c.Pos()), helper, idx)
					return
				}
				calls++
				*nPassed++
				check(c.Args[idx], slices.Clone(st), seen)
			}
			if scope != nil {
				walk(scope, visit)
			} else {
				for _, f := range files {
					walk(f, visit)
				}
			}
			if calls == 0 {
				t.Errorf("%s: no call site of %s passes code %s", fset.Position(id.Pos()), helper, id.Name)
			}
			return
		}
		t.Errorf("%s: Diagnostic code %s is neither a Diag* constant nor a parameter", fset.Position(id.Pos()), id.Name)
	}

	for name, f := range files {
		if name == "diag.go" {
			continue
		}
		walk(f, func(n ast.Node, stack []ast.Node) {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isDiagnosticLit(lit, stack) {
				return
			}
			*nLiterals++
			var code ast.Expr
			for i, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Code" {
						code = kv.Value
					}
				} else if i == 1 {
					code = e
				}
			}
			if code == nil {
				t.Errorf("%s: Diagnostic literal without a Code", fset.Position(lit.Pos()))
				return
			}
			check(code, slices.Clone(stack), map[string]bool{})
		})
	}
}
