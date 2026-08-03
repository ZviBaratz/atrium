package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// The sandbox HOME must carry its owner marker. Without one, rootIsStale falls
// through to age alone, so a root outliving rootGrace reads as an orphan and the
// next package binary's sweep deletes it — mid-run, taking this run's state.json and
// any worktrees underneath it.
func TestSandboxHomeCarriesItsOwnerMarker(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Truef(t, strings.HasPrefix(filepath.Base(home), homeRootPrefix),
		"HOME is %q, not a sandbox root: this package's TestMain must call SandboxHomeMain", home)

	raw, err := os.ReadFile(filepath.Join(home, ownerMarkerFile))
	require.NoErrorf(t, err, "sandbox HOME %q carries no %s: an unmarked root is swept by age "+
		"alone, so a sibling `go test ./...` package would reap this one mid-run", home, ownerMarkerFile)

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoErrorf(t, err, "%s is not a pid: %q", ownerMarkerFile, raw)
	require.Equalf(t, os.Getpid(), pid,
		"%s names another process, which reads as an owner that has exited", ownerMarkerFile)
}

// sweepStaleRoots is what deletes directories, so its refusals are the safety
// surface. A prefix that matched nothing of ours, or a self it failed to skip, is
// how a sweep reaps the run that started it.
func TestSweepStaleRootsSpares(t *testing.T) {
	parent := t.TempDir()

	// Named like ours and provably stale — but it is self. The marker names an exited
	// process on purpose: an owner that is alive would be spared by rootIsStale, and
	// the self-skip this exists to assert would never be reached. That was this test's
	// first shape, and deleting the self-skip did not fail it.
	self := filepath.Join(parent, homeRootPrefix+"self")
	require.NoError(t, os.Mkdir(self, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(self, ownerMarkerFile),
		[]byte(strconv.Itoa(deadPID(t))), 0o600))
	old := time.Now().Add(-2 * rootGrace)
	require.NoError(t, os.Chtimes(self, old, old))
	require.True(t, rootIsStale(self),
		"the self fixture is not stale, so sparing it would prove nothing about the self-skip")

	// Stale by every measure, but not named like ours.
	foreign := filepath.Join(parent, "someone-elses-scratch")
	require.NoError(t, os.Mkdir(foreign, 0o700))
	require.NoError(t, os.Chtimes(foreign, old, old))

	// Ours, stale, and reapable — the control, without which "nothing was deleted"
	// would hold because the sweep never ran.
	doomed := filepath.Join(parent, homeRootPrefix+"doomed")
	require.NoError(t, os.Mkdir(doomed, 0o700))
	require.NoError(t, os.Chtimes(doomed, old, old))

	sweepStaleRoots(parent, homeRootPrefix, self, nil)

	require.DirExists(t, self, "the sweep deleted the root of the run that started it")
	require.DirExists(t, foreign, "the sweep deleted a directory it did not create — the prefix "+
		"is the only thing bounding what this removes")
	require.NoDirExists(t, doomed, "the sweep removed nothing at all, so the two assertions above prove nothing")
}

// A release veto must stop the removal, not just the reaping. This is how the tmux
// sweep keeps a socket whose server it could not kill.
func TestSweepStaleRootsHonoursAReleaseVeto(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, homeRootPrefix+"vetoed")
	require.NoError(t, os.Mkdir(root, 0o700))
	old := time.Now().Add(-2 * rootGrace)
	require.NoError(t, os.Chtimes(root, old, old))

	sweepStaleRoots(parent, homeRootPrefix, "", func(string) bool { return false })
	require.DirExists(t, root, "a vetoed root was removed anyway: the tmux sweep relies on this "+
		"to leave a live server's socket addressable (#547)")

	sweepStaleRoots(parent, homeRootPrefix, "", func(string) bool { return true })
	require.NoDirExists(t, root, "the veto path is unreachable, so the assertion above is vacuous")
}
