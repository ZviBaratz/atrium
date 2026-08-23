package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file used to open with a -race guard,
// TestEnsureSessionDoesNotReadTitleWhileAdoptRenameWritesIt, driving EnsureSession against
// a spinning AdoptRename. It was deleted by #795, which unexported the identity fields and
// put them behind identityMu (session/identity.go): AdoptRename now writes nothing a reader
// on this path could race, so that test could no longer fail for the reason it existed, and
// a test that cannot fail is worse than none. The race half of the argument lives in
// session's TestIdentityReadsDoNotRaceItsWrites now. What survives here is the OTHER half,
// below — the capture path must take its identity as a parameter — and #795 did not make
// that unnecessary, it only changed why. See TestCaptureGoroutineTakesItsIdentityByParameter.

// TestTermLegacyNameIsFrozen holds termLegacyName to its literal.
//
// Extracting it gave the string one home, which is what stops the production site and the
// test drifting apart — but a single home is also a single place to "clean up", and a
// helper both sides call agrees with itself no matter what it returns. This is the guard
// that makes it a fact rather than a convention: the names it builds belong to shells
// created by Atrium versions already installed on people's machines, so a change here does
// not rename anything, it strands them. See #708 for what the name became after.
func TestTermLegacyNameIsFrozen(t *testing.T) {
	require.Equal(t, "term_my-session", termLegacyName("my-session"),
		"the pre-#708 shell name is frozen: it names shells older Atrium versions created")
}

// captureGoroutineFuncs lists the ui functions that take an *Instance on the app's capture
// goroutine: as of #718 the whole production path from app's captureTerminalFrame down, which
// is two hops.
//
// Both hops, not just the one that does the work: EnsureTerminalSession is a one-line
// wrapper, and "return w.terminal.EnsureSession(instance, instance.Title)" is the natural
// edit for anyone who wants "the current title". That reintroduces #718 one level up, where
// a guard reading only terminal.go cannot see it.
//
// This is a hand-maintained list, and nothing holds it to the tree — the same weakness that
// moved #719's reader census out of a comment and into a grep recipe. It is a table rather
// than prose so at least the values are readable, but a THIRD entry point onto the capture
// path (a helper extracted from EnsureSession, a new pane API called from
// captureTerminalFrame) is covered only if someone adds it here. app/shell_gate_test.go's
// call-site count does not cover that either: it counts sites named EnsureSession or
// EnsureTerminalSession, so a differently-named one trips neither guard.
var captureGoroutineFuncs = []struct{ file, fn string }{
	{"terminal.go", "EnsureSession"},
	{"tabbed_window.go", "EnsureTerminalSession"},
}

// frameScopedInstanceReads are the Instance members the rename handler rewrites on the
// update thread: AdoptRename sets Title and Branch, then SetDisplayName("") and SetNote()
// in the two statements after it (app/app_update.go). Reading any of them down here would
// take a value from a different instant than the frame this capture belongs to.
//
// All four, not just the Title #718 found. DisplayName matters most of the three additions
// because it FALLS BACK to Title, so it is a second route onto the field the rename
// rewrites.
//
// Since #795 these are guarded accessors rather than plain fields, so such a read would be
// safe rather than a data race — and that is exactly why this list still has to exist. A
// lock makes a read safe, not current; nothing in the compiler or the detector will object
// to a shell named from a rename that landed mid-capture.
//
// Method names sit in the same set as field names because an ast.SelectorExpr is what both
// look like: `x.Title()` and `x.DisplayName()` differ only in the CallExpr wrapped around
// them, which this guard has no reason to distinguish.
var frameScopedInstanceReads = map[string]bool{
	"Title": true, "Branch": true, "DisplayName": true, "Note": true,
}

// TestCaptureGoroutineTakesItsIdentityByParameter is what is left of #718's guard, and it
// is the half the normal gate can fail on.
//
// It asserts that nothing on the capture path reads the instance's identity for itself: the
// title it needs is snapshotted on the update thread and passed in, so the shell is named
// from the frame being captured. Since #795 that is a freshness property rather than a
// safety one — the accessors take identityMu, so such a read would not be a data race —
// which is precisely why an assertion is needed. Nothing else in the toolchain objects to
// reading the right field at the wrong instant.
//
// It names the members rather than banning "any X.field" because the guarded accessors
// (Started, Paused, WorkingDir, ClaimTerminalSessionName) are reads on the same receiver
// and must stay allowed. And it resolves the receiver from the SIGNATURE — every parameter
// declared *session.Instance — rather than matching an identifier spelled "instance". Both
// halves of that are load-bearing. Hardcoding the name lets a rename to `inst`, which is
// what the rest of this package calls it, turn the guard into a silent no-op; dropping the
// receiver check altogether would fire on any unrelated `.Title` — a RenamedIdentity
// literal, a config struct — with an error message alleging a race that does not exist.
//
// Two things it cannot see, both inherent to reading one function's shape:
//
//   - The read moved one call deeper. A helper invoked from these bodies runs on the same
//     goroutine and its body is not inspected.
//   - The instance aliased into a local first (`x := instance; x.Title`), which is not a
//     selector on a parameter.
//
// That is why the -race test above is the primary guard and this one is the companion the
// normal gate can fail on.
func TestCaptureGoroutineTakesItsIdentityByParameter(t *testing.T) {
	for _, target := range captureGoroutineFuncs {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, target.file, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		fn := captureGoroutineFunc(t, file, target.file, target.fn)
		receivers := instanceParams(t, target.fn, fn)

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !frameScopedInstanceReads[sel.Sel.Name] {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || !receivers[ident.Name] {
				return true
			}
			t.Errorf("%s reads %s.%s at %s — the handler that adopts a rename writes Title, "+
				"Branch, displayName and note on the update thread with no lock, so reading "+
				"any of them on the capture goroutine is a data race (#718). Snapshot the "+
				"value on the update thread (frameTarget.termTitle) and pass it in instead.",
				target.fn, ident.Name, sel.Sel.Name, fset.Position(sel.Pos()))
			return true
		})
	}
}

// instanceParams returns the names of fn's *session.Instance parameters, failing the test
// when there are none: a signature that stopped taking an instance means this guard is
// watching the wrong function, which must be loud rather than vacuously green.
func instanceParams(t *testing.T, name string, fn *ast.FuncDecl) map[string]bool {
	t.Helper()
	named := map[string]bool{}
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Instance" {
			continue
		}
		for _, ident := range field.Names {
			named[ident.Name] = true
		}
	}
	require.NotEmpty(t, named, "%s must take a *session.Instance parameter for this guard to mean anything", name)
	return named
}

// captureGoroutineFunc returns the named method's declaration, failing the test when it
// cannot be found — a rename must not turn the guard above into a no-op that passes.
func captureGoroutineFunc(t *testing.T, file *ast.File, filename, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv == nil {
			continue
		}
		require.NotNil(t, fn.Body, name+" must have a body")
		return fn
	}
	t.Fatalf("no method named %s in ui/%s — if it was renamed, re-point this guard", name, filename)
	return nil
}
