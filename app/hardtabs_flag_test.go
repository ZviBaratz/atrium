//go:build linux || solaris || aix || darwin || freebsd || dragonfly

package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sync"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestSuppressHardTabsMovesAndRestoresTheFlag reads the termios either side of
// the call, because the pty guard in hardtabs_test.go cannot see this half:
// every case there opens its own pty, so a suppressHardTabs that never put the
// flag back would leave those tests green while leaking a changed terminal into
// the user's shell for the rest of the session. Measured — dropping the restore
// to a no-op fails nothing else in this package.
//
// The assertion is on TABDLY specifically, and on the WHOLE Oflag for the
// restore. Bubble Tea reads exactly one predicate, Oflag&TABDLY == TAB0 in
// checkOptimizedMovements, so "not TAB0" is the entire contract going in;
// coming out, anything short of the original word is a leak.
func TestSuppressHardTabsMovesAndRestoresTheFlag(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()
	fd := int(tty.Fd())

	before, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)
	require.Zero(t, before.Oflag&tabdly,
		"a fresh pty is expected to be TAB0 — if it is not, the fix has nothing to switch off "+
			"and this test is measuring the wrong thing")

	restore := suppressHardTabs(tty)

	during, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)
	require.NotZero(t, during.Oflag&tabdly,
		"suppressHardTabs must leave TABDLY at something other than TAB0, or Bubble Tea "+
			"keeps hard tabs enabled")

	restore()

	after, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)
	require.Equal(t, before.Oflag, after.Oflag, "restore must put the whole Oflag back")
}

// TestSuppressHardTabsRestoreHealsAChildsTermios is what makes "the whole Oflag" above
// more than a coincidence: nothing in that test moves any other bit, so a restore
// narrowed to TABDLY alone passes it. Here bits the fix never touches are cleared while
// the suppression is in effect, standing in for a custom command that ran `stty -echo`
// and died before putting it back.
//
// TWO fields, and the second is the point. ECHO lives in Lflag, so an Oflag-only
// assertion leaves the `stty -echo` case this docstring names entirely unguarded —
// measured, by narrowing the restore to write back Oflag alone, which the whole package
// stayed green through. The claim is "the termios app.Run found", so the fixture has to
// differ somewhere outside the word the fix moves.
//
// The heal is load-bearing because bubbletea does not do it. RestoreTerminal re-snapshots
// its own restore state from term.MakeRaw inside initInput, so after an exec the state it
// puts back at shutdown is whatever the last child left. Atrium's defer is the only thing
// that still holds the terminal as the user's shell had it — and it can only heal what it
// saved.
func TestSuppressHardTabsRestoreHealsAChildsTermios(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()
	fd := int(tty.Fd())

	before, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)
	require.NotZero(t, before.Oflag&unix.OPOST,
		"precondition: a fresh pty has OPOST on, so clearing it below is a real change")

	require.NotZero(t, before.Lflag&unix.ECHO,
		"precondition: a fresh pty has ECHO on, so clearing it below is a real change")

	// The restore has to run mid-test, before the reads below, AND be guaranteed against
	// a failing require between here and there: a leaked hardTabsAsFound stays set for
	// the rest of the package and fails TestYieldHardTabsIsANoOpWhenNothingIsSuppressed
	// as a cascade, which reports a second, confusing failure for the first one's sake.
	// Once-only so the two spellings cannot both fire.
	var once sync.Once
	suppressed := suppressHardTabs(tty)
	restore := func() { once.Do(suppressed) }
	defer restore()

	mangled := *before
	mangled.Oflag &^= unix.OPOST
	mangled.Lflag &^= unix.ECHO
	require.NoError(t, unix.IoctlSetTermios(fd, setTermios, &mangled))

	restore()

	after, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)
	require.Equal(t, before.Oflag, after.Oflag,
		"restore must put back the termios app.Run found, not only the field it moved — "+
			"otherwise a child that mangled the tty and died hands the user's shell back broken")
	require.Equal(t, before.Lflag, after.Lflag,
		"including the fields the fix never touches: `stty -echo` in a custom command that "+
			"died is the case the whole-word write exists for, and it is not in Oflag")
}

// TestSuppressHardTabsToleratesANonTerminal pins the degradation. A frame with
// tab bytes in it is a blemish; refusing to start is not a trade Atrium should
// make for it, and Run calls this unconditionally — including when stdin is a
// pipe, which is every `atrium < /dev/null` and every CI invocation.
func TestSuppressHardTabsToleratesANonTerminal(t *testing.T) {
	require.NotPanics(t, func() { suppressHardTabs(nil)() })

	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NotPanics(t, func() { suppressHardTabs(f)() })
}

// TestRunSuppressesHardTabs is the wiring guard, and it reads the source
// because the behaviour cannot be reached: app.Run builds a real home and hands
// the terminal to a live Bubble Tea program, so no test in this package calls
// it. The pty guard exercises suppressHardTabs directly and would stay green if
// the call site were deleted tomorrow — measured, by deleting it — which is
// exactly the gap this closes.
//
// What it proves is narrow and worth stating plainly: that Run's body carries a
// defer naming suppressHardTabs. It cannot prove the ordering that comment argues
// for (before NewProgram, restoring after Bubble Tea's own restore); nothing
// automated here can, and the live drive in the PR is what covered it.
func TestRunSuppressesHardTabs(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "app.go", nil, 0)
	require.NoError(t, err)

	var run *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "Run" {
			run = fn
		}
	}
	require.NotNil(t, run, "app.go no longer declares func Run")

	var deferred bool
	ast.Inspect(run.Body, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		// The shape, not just the name: `defer suppressHardTabs(os.Stdin)()` defers the
		// RESTORE the call returns, so the suppression itself runs now. Drop the trailing
		// pair and `defer suppressHardTabs(os.Stdin)` still compiles — the returned func
		// is simply discarded — but the fix is then inert (TABDLY is still TAB0 when
		// checkOptimizedMovements reads it) and the suppression lands at Run's EXIT with
		// its restore thrown away, leaving the user's shell expanding tabs. Measured: with
		// only the name asserted, every other test in this file stays green through that.
		inner, ok := d.Call.Fun.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := inner.Fun.(*ast.Ident); ok && id.Name == "suppressHardTabs" {
			deferred = true
		}
		return !deferred
	})
	require.True(t, deferred,
		"Run must defer the restore that suppressHardTabs returns — `defer suppressHardTabs(os.Stdin)()` "+
			"— or every frame Atrium draws carries the tab bytes TestFrameEmitsNoRawTab forbids (#796)")
}

// TestYieldHardTabsHandsTheFieldBackAndTakesItAgain drives the real round trip on a
// pty, because the attach guard in hardtabs_attach_test.go stubs the seam and so
// proves only that Run calls something at the right moment — a yieldHardTabs that
// moved nothing would leave it green.
//
// The order is the contract: as-found while the child holds the terminal, expanded
// again by the time it returns, since bubbletea re-reads the field on the way in.
func TestYieldHardTabsHandsTheFieldBackAndTakesItAgain(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()
	fd := int(tty.Fd())

	asFound, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)

	restore := suppressHardTabs(tty)
	defer restore()
	require.NotZero(t, delayOf(t, fd), "precondition: the fix is in effect")

	resuppress := yieldHardTabs(tty)
	require.Equal(t, uint64(asFound.Oflag&tabdly), delayOf(t, fd),
		"a cooked child must inherit the field as the user's shell had it, or the driver "+
			"expands its tabs and miscounts its ANSI escapes as columns (#796)")

	resuppress()
	require.NotZero(t, delayOf(t, fd),
		"and the field must be back before bubbletea's RestoreTerminal re-reads it")
}

// A yield with nothing suppressed must not invent a state to restore: yieldHardTabs
// runs on every cooked attach, including on the platforms and terminals where
// suppressHardTabs bailed out.
func TestYieldHardTabsIsANoOpWhenNothingIsSuppressed(t *testing.T) {
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()
	fd := int(tty.Fd())

	require.Nil(t, hardTabsAsFound, "precondition: no suppression is in flight")
	before := delayOf(t, fd)
	require.NotPanics(t, func() { yieldHardTabs(tty)() })
	require.NotPanics(t, func() { yieldHardTabs(nil)() })
	require.Equal(t, before, delayOf(t, fd), "an unsuppressed tty must come back untouched")
}

func delayOf(t *testing.T, fd int) uint64 {
	t.Helper()
	term, err := unix.IoctlGetTermios(fd, getTermios)
	require.NoError(t, err)
	return uint64(term.Oflag & tabdly)
}
