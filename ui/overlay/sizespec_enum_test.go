package overlay

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpecVarTablesAreTotal holds the "every spec" tests to the package the
// way the surface registry's completeness walk holds surfaceSpecs to the
// state enum: it enumerates the exported SizeSpec declarations from the
// source — the only place package-level vars can be enumerated — and
// requires each one to have a specFitTable row and a mention in the width
// contract. Without it both tables are hand-lists, and the next overlay's
// spec lands with no rows: a mistyped fraction or cap then surfaces only as
// a moved box at a terminal size no test renders, under two docstrings that
// still say "every".
func TestSpecVarTablesAreTotal(t *testing.T) {
	declared := exportedSizeSpecVars(t)
	require.NotEmpty(t, declared, "no exported SizeSpec vars found — the parser lost the declarations this guard exists to count")

	tabled := map[string]bool{}
	for _, row := range specFitTable {
		tabled[row.name] = true
	}
	contract, err := os.ReadFile("widthcontract_test.go")
	require.NoError(t, err)

	for _, name := range declared {
		assert.Truef(t, tabled[name],
			"%s has no specFitTable row — pin its Fit values at the five sizes", name)
		assert.Truef(t, strings.Contains(string(contract), name),
			"%s is never mentioned in widthcontract_test.go — give its overlay a width-contract case", name)
	}
	for name := range tabled {
		assert.Containsf(t, declared, name,
			"specFitTable row %q names no exported SizeSpec var — delete the stale row", name)
	}
}

// exportedSizeSpecVars parses the package's non-test files and returns the
// names of exported package-level vars declared as SizeSpec composite
// literals — the declaration shape every spec var uses.
func exportedSizeSpecVars(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		for _, decl := range parsed.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, id := range vs.Names {
					if !id.IsExported() || i >= len(vs.Values) {
						continue
					}
					cl, ok := vs.Values[i].(*ast.CompositeLit)
					if !ok {
						continue
					}
					if tid, ok := cl.Type.(*ast.Ident); ok && tid.Name == "SizeSpec" {
						names = append(names, id.Name)
					}
				}
			}
		}
	}
	return names
}
