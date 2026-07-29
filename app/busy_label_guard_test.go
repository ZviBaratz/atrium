package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// busyCallLabelArg maps each progress-row entry point to the index of its label
// argument. These are the four ways an operation can claim the hint row.
var busyCallLabelArg = map[string]int{
	"confirmAction":         1,
	"confirmWorktreeAction": 1,
	"beginAsyncAction":      0,
	"beginBackgroundAction": 0,
}

// TestEveryBusyCallDeclaresItsLabel enforces the named-loading policy at the only
// level that cannot be forgotten: a caller in a file this test has never heard of
// still has to type either a real label or `instantAction`.
//
// The type system does most of the work — busyLabel is a required parameter — so
// what remains is the empty-or-conditionally-empty label a signature cannot catch.
// `instantAction` is the one legal empty, and it is a declaration: an author saying
// "this does no I/O", auditable with one grep, rather than a promise nobody made.
//
// What it CANNOT see, stated so it is not mistaken for coverage:
//   - only package app, and only these four helpers. A key handler that returns a
//     raw tea.Cmd doing tmux work is invisible here — that is how open-PR went
//     unlabelled for so long.
//   - nothing proves a label is TRUE. TestBusyLabels_Voice pins the strings;
//     neither test can tell whether the operation it names is the one running.
func TestEveryBusyCallDeclaresItsLabel(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parsing %s", name)
		files = append(files, f)
	}
	require.NotEmpty(t, files, "the package must have source files to walk")

	// declared reports whether expr is an acceptable label: a non-empty string
	// literal, an interpolation, the instantAction declaration, or an identifier
	// assigned one of those in the same function (the `const label = "…"` idiom).
	var declared func(fn *ast.FuncDecl, expr ast.Expr) bool
	declared = func(fn *ast.FuncDecl, expr ast.Expr) bool {
		switch e := expr.(type) {
		case *ast.BasicLit:
			return e.Kind == token.STRING && e.Value != `""`
		case *ast.Ident:
			if e.Name == "instantAction" {
				return true
			}
			// A forwarder passing its own parameter through (confirmWorktreeAction,
			// handleConfirmState's dispatch) is not where a label is authored — its
			// callers are, and they are checked at their own call sites.
			if isParam(fn, e.Name) {
				return true
			}
			return assignsLabel(fn, e.Name, declared)
		case *ast.SelectorExpr:
			// The stashed label a confirmation carries from its author to the
			// dispatcher. It is the one field that IS a label; any other selector
			// is not accepted.
			return e.Sel.Name == "pendingConfirmBusyLabel"
		case *ast.CallExpr:
			// fmt.Sprintf(...) or a busyLabel(...) conversion around one of the above.
			if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" {
				return true
			}
			if len(e.Args) == 1 {
				return declared(fn, e.Args[0])
			}
		}
		return false
	}

	var offenders []string
	checked := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(x ast.Node) bool {
				call, ok := x.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				idx, watched := busyCallLabelArg[sel.Sel.Name]
				if !watched || idx >= len(call.Args) {
					return true
				}
				checked++
				if !declared(fn, call.Args[idx]) {
					offenders = append(offenders, fmt.Sprintf("%s: %s has no declared label",
						fset.Position(call.Pos()), sel.Sel.Name))
				}
				return true
			})
		}
	}
	// Without this the walk could stop matching anything and the test would still
	// pass — the failure mode every AST guard has.
	require.Positive(t, checked, "the walk must actually visit the progress-row entry points")
	assert.Empty(t, offenders,
		"every operation that claims the progress row must name itself, or declare instantAction")
}

// isParam reports whether name is one of fn's parameters.
func isParam(fn *ast.FuncDecl, name string) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		for _, id := range field.Names {
			if id.Name == name {
				return true
			}
		}
	}
	return false
}

// assignsLabel reports whether fn assigns an acceptable label to name.
func assignsLabel(fn *ast.FuncDecl, name string, ok func(*ast.FuncDecl, ast.Expr) bool) bool {
	found := false
	ast.Inspect(fn.Body, func(x ast.Node) bool {
		switch s := x.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				if id, isIdent := lhs.(*ast.Ident); isIdent && id.Name == name && i < len(s.Rhs) {
					found = found || ok(fn, s.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, id := range s.Names {
				if id.Name == name && i < len(s.Values) {
					found = found || ok(fn, s.Values[i])
				}
			}
		}
		return true
	})
	return found
}
