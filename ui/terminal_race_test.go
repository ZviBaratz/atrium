package ui

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

// titleRaceTurns is how many EnsureSession calls the race test drives.
//
// A loop rather than a single call because the race detector reports only the
// interleavings it actually observes: it holds a bounded shadow history per memory word,
// so one unlucky pair can complete with the two accesses too far apart to be compared.
//
// The two sides are deliberately NOT symmetric loops. AdoptRename is nanoseconds and
// EnsureSession is milliseconds, so a writer with a matching turn count finishes before
// the reader's first turn does and the detector never sees a concurrent pair — the shape
// this test started as, which passed on the unfixed tree. The writer spins until the
// reader is done instead (the session/git/worktree_git_test.go shape: the slow side sets
// the duration, the fast side is a spinner).
const titleRaceTurns = 50

// raceEnsurePane returns a TerminalPane whose subprocesses cannot run: its base context
// is already cancelled, so every exec.Cmd EnsureSession builds fails at Start with
// ctx.Err() before forking (os/exec checks the context first) and pty.Start closes both
// ends of the pty it opened. Nothing touches the tmux socket, so this test needs neither
// testutil.RequireTmux nor a teardown that names a socket.
//
// That is safe for what the test measures because every use of the title happens while
// EnsureSession is building argv — the legacy reap probe, then the create and the recreate,
// three sites, all of them strictly before any subprocess. The assertion in the test body
// pins that: EnsureSession's start-failure error is produced only after them, so a future
// change that short-circuits earlier turns this test red rather than quiet.
//
// It also keeps every turn on the create path. The failed start releases the name the
// turn minted (releaseIfMinted), and no entry is ever installed, so the cached-and-alive
// early return — which would skip the reads entirely from turn two onward — is never
// reached.
func raceEnsurePane(t *testing.T) *TerminalPane {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tp := NewTerminalPane(ctx)
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)
	return tp
}

// TestEnsureSessionDoesNotReadTitleWhileAdoptRenameWritesIt is the #718 guard: the write
// at AdoptRename (session/instance.go, update thread) against the reads in EnsureSession
// (the app's capture goroutine — app/app_frames.go's captureTerminalFrame →
// ui/tabbed_window.go's EnsureTerminalSession → here).
//
// Instance.Title is a plain exported field with no mutex, so the pair is a data race in
// the Go memory model's sense and not merely a stale read: a concurrent read of a string
// header can observe a mismatched pointer/length pair. Reachable in production by
// pressing R on a session whose terminal tab is open.
//
// THE DETECTOR IS THE ASSERTION, AND ONLY UNDER -race. This test passes vacuously under
// `just test`, `just ci`, and both non-race CI test jobs; the only gate that can fail it
// is `just test-race` and CI's "Race detector" job. Its sibling
// TestCaptureGoroutineReadsNoUnguardedInstanceField is what the normal gate can see.
//
// To watch it fail, put the reads back: replace `title` with `instance.Title` at all three
// EnsureSession sites — the legacy reap probe, the create and the recreate — and keep the
// two-argument signature, since this file cannot compile against the pre-fix one-argument
// version, so checking out e43de60 wholesale is not the reproduction. That yields, on every
// run, "WARNING: DATA RACE" pairing a write in session.(*Instance).AdoptRename against a
// read in ui.(*TerminalPane).EnsureSession, the legacy probe being the read that lands
// first. Deliberately no line numbers here: this comment has already outlived one set of
// them, cited from a commit whose own edits moved the lines it named.
func TestEnsureSessionDoesNotReadTitleWhileAdoptRenameWritesIt(t *testing.T) {
	inst := makeStartedInstance(t, "race")
	t.Cleanup(func() { _ = inst.Kill() })
	tp := raceEnsurePane(t)

	// The tmux name must stay non-empty across every AdoptRename: it is what
	// ClaimTerminalSessionName mints the shell key from, and a blank one makes
	// EnsureSession return at the empty-key guard without reaching either read.
	tmuxName := inst.TmuxSessionName()
	require.NotEmpty(t, tmuxName, "precondition: the fixture must have a persisted tmux name")
	require.NotEmpty(t, inst.WorkingDir(), "precondition: EnsureSession refuses an empty cwd")

	// The title the reader will pass, snapshotted HERE — before the writer goroutine
	// exists, so this read is uncontended, and standing in for resolveFrameTarget taking
	// it on the update thread. The reader loop below must NOT read inst.Title itself: that
	// would put the capture side back on the unguarded field and race whatever the
	// argument is called, which is exactly the finding and not a way to test it.
	snapshot := inst.Title

	// One turn on its own, so a broken precondition fails as an assertion rather than as
	// a silently uncovered loop.
	_, err := tp.EnsureSession(inst, snapshot)
	require.ErrorContains(t, err, "failed to start session",
		"precondition: the turn must reach the create path, which is what performs the reads")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Fabricated rather than driven through Rename: the I/O half deliberately
			// writes neither Title nor Branch, so it is AdoptRename that is under test.
			// Two alternating titles so each turn is a real write, not a store of the
			// value already there.
			title := "renamed"
			if i%2 == 0 {
				title = "renamed-again"
			}
			inst.AdoptRename(session.RenamedIdentity{Title: title, TmuxName: tmuxName})
		}
	}()

	for range titleRaceTurns {
		_, _ = tp.EnsureSession(inst, snapshot)
	}
	close(stop)
	wg.Wait()
}

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

// unguardedInstanceReads are the Instance members that AdoptRename's handler writes on the
// update thread with no lock, so reading any of them off that thread is a data race.
//
// Not just Title, which is the one #718 found. The handler writes all four within three
// statements — AdoptRename sets Title and Branch, then SetDisplayName("") and SetNote()
// (app/app_update.go) — and none of the four is behind i.mu on either side: SetDisplayName
// and SetNote take no lock, and DisplayName() and Note() read none. DisplayName() matters
// most of the three additions, because it FALLS BACK to Title, so it is a second route to
// the exact field this issue is about, and off-thread callers of it already exist
// (session/runcmd.go, behind the actionInFlight gate).
//
// Method names sit in the same set as field names because an ast.SelectorExpr is what both
// look like: `x.Title` and `x.DisplayName()` differ only in the CallExpr wrapped around the
// latter, which this guard has no reason to distinguish.
var unguardedInstanceReads = map[string]bool{
	"Title": true, "Branch": true, "DisplayName": true, "Note": true,
}

// TestCaptureGoroutineReadsNoUnguardedInstanceField is the half of #718's guard that the
// normal gate can fail on.
//
// The -race test above cannot: a data race is invisible to `go build`, `go vet`,
// golangci-lint and an untagged `go test`, so on every gate except `just test-race` it
// reports success whether or not the fix is present. This one asserts the shape of the
// fix instead of its effect — nothing on the capture path reads an unguarded Instance
// field, because the title it needs is snapshotted on the update thread and passed in.
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
func TestCaptureGoroutineReadsNoUnguardedInstanceField(t *testing.T) {
	for _, target := range captureGoroutineFuncs {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, target.file, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		fn := captureGoroutineFunc(t, file, target.file, target.fn)
		receivers := instanceParams(t, target.fn, fn)

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !unguardedInstanceReads[sel.Sel.Name] {
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
