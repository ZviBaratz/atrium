//go:build linux || solaris || aix || darwin || freebsd || dragonfly

package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
	_, tty, err := pty.Open()
	require.NoError(t, err)
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
		ast.Inspect(d, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "suppressHardTabs" {
				deferred = true
			}
			return !deferred
		})
		return !deferred
	})
	require.True(t, deferred,
		"Run must defer suppressHardTabs, or every frame Atrium draws carries the tab bytes "+
			"TestFrameEmitsNoRawTab forbids (#796)")
}
