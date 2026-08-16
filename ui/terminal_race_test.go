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
// That is safe for what the test measures because the reads under test happen while
// EnsureSession is building argv — "term_"+Title at the legacy probe and "term: "+Title
// at the create — strictly before any subprocess. The assertion in the test body pins
// that: EnsureSession's start-failure error is produced only after both reads, so a
// future change that short-circuits earlier turns this test red rather than quiet.
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
// TestEnsureSessionReadsNoInstanceTitle is what the normal gate can see.
//
// Observed on the unfixed tree at e43de60: WARNING: DATA RACE, write at
// session/instance.go:2228 by AdoptRename against read at ui/terminal.go:412 in
// EnsureSession.
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

// TestEnsureSessionReadsNoInstanceTitle is the half of #718's guard that the normal gate
// can fail on.
//
// The -race test above cannot: a data race is invisible to `go build`, `go vet`,
// golangci-lint and an untagged `go test`, so on every gate except `just test-race` it
// reports success whether or not the fix is present. This one asserts the shape of the
// fix instead of its effect — EnsureSession reads no unguarded Instance field, because
// the title it needs is snapshotted on the update thread and passed in.
//
// Title and Branch by name rather than "any instance.X": the guarded accessors
// (Started, Paused, WorkingDir, ClaimTerminalSessionName) are reads on the same receiver
// and must stay allowed.
func TestEnsureSessionReadsNoInstanceTitle(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "terminal.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	// Branch is in the set although EnsureSession has never read it: AdoptRename writes
	// both fields on the same two lines, so a future read of either is the same defect.
	unguarded := map[string]bool{"Title": true, "Branch": true}

	ast.Inspect(ensureSessionBody(t, file), func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !unguarded[sel.Sel.Name] {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != "instance" {
			return true
		}
		t.Errorf("EnsureSession reads instance.%s at %s — Title and Branch are unguarded "+
			"fields written by AdoptRename on the update thread, so reading them on the "+
			"capture goroutine is a data race (#718). Snapshot the value into "+
			"frameTarget.termTitle and pass it in instead.",
			sel.Sel.Name, fset.Position(sel.Pos()))
		return true
	})
}

// ensureSessionBody returns the body of TerminalPane.EnsureSession, failing the test when
// it cannot be found — a rename must not turn the guard above into a no-op that passes.
func ensureSessionBody(t *testing.T, file *ast.File) *ast.BlockStmt {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "EnsureSession" || fn.Recv == nil {
			continue
		}
		require.NotNil(t, fn.Body, "EnsureSession must have a body")
		return fn.Body
	}
	t.Fatal("no method named EnsureSession in ui/terminal.go — if it was renamed, re-point this guard")
	return nil
}
