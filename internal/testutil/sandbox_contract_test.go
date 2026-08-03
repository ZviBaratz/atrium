package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// os.Exit inside a test body terminates the process without unwinding, so every
// pending defer dies with it — including SandboxHomeMain's, which is what removes
// the throwaway HOME and reaps the private tmux socket root.
//
// This is not hypothetical tidiness. ui's splash re-exec harness did exactly this,
// and because it spawns two children per run it leaked two HOME directories on every
// clean `go test ./ui/` — 633 of them had piled up in /tmp over five days, and the
// same skip in a package that starts a real tmux session strands the server too.
// The startup sweep in sandboxroot.go cleans up after the exits nobody can prevent
// (signals, -timeout aborts); this covers the one kind that is purely a choice.
//
// The fix at the call site is always `return`: a re-exec child that returns still
// exits 0, still prints what it printed, and lets TestMain finish. A child that
// needs a non-zero status can fail the test normally.
//
// TestMain itself is exempt — os.Exit(m.Run()) is its documented contract, and the
// cleanup it is responsible for has already run by then.
func TestNoTestCallsOsExit(t *testing.T) {
	// Relative to internal/testutil, where this test runs.
	root := filepath.Join("..", "..")

	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// web/ is a Next.js app with its own toolchain and no Go in it.
			if name := d.Name(); name == "web" || name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		// Parsed rather than grepped: the string "os.Exit" appears in a dozen comments
		// in this module, including several in this very file.
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "parse %s", path)
		checked++

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name == "TestMain" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Exit" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
					return true
				}
				t.Errorf("%s calls os.Exit in %s, which skips every pending defer in the "+
					"process — including SandboxHomeMain's, so this run leaks its temp HOME "+
					"and strands any tmux server it started. Use `return`; only TestMain may "+
					"call os.Exit. (%s)", path, fn.Name.Name, fset.Position(call.Pos()))
				return true
			})
		}
		return nil
	})
	require.NoError(t, err)

	// A walk that reached nothing would pass silently and mean nothing.
	require.Greaterf(t, checked, 100,
		"only %d test files parsed — the walk is not reaching the module", checked)
}
