package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDaemonInstallsEveryProcessWidePolicy is the guard the two knobs it checks were both
// missing, and it exists because the failure is invisible from every other direction.
//
// The daemon is a second process, with its own config load. Anything the TUI installs on a
// package-level var in newHome — a cap, a launch flag — reaches the daemon only if the
// daemon installs it too, and the daemon relaunches agents: the startup load recovers every
// session whose tmux session is gone, which reaches tmux.start() and everything start()
// decides from process state. A missing install is a build-clean behaviour split, one
// feature running under two different settings depending on which process happened to
// launch the session.
//
// Read out of the source rather than exercised, for the reason the daemon's own comments
// give: nothing in the suite drives RunDaemon, so there is no run in which the call could be
// observed. That is a real limit — this proves the call is written, not that it is reached —
// and it is still the difference between the wire being absent and being present, which is
// the mistake actually made here twice.
//
// Add to the list when a new process-wide setter starts affecting a session's launch or a
// daemon-side reconciliation, and only then: a knob the daemon has no business installing
// (anything about rendering, anything the TUI owns exclusively) does not belong here.
func TestDaemonInstallsEveryProcessWidePolicy(t *testing.T) {
	required := map[string]string{
		"SetPendingWatchdog": "the daemon runs ApplyPaneState, so it reconciles stuck " +
			"Pending rows on whatever cap this process holds (#799)",
		"SetAgentSkills": "the daemon relaunches agents, and this switch is inverted so an " +
			"unwired process INJECTS: without the install, agent_skills false is honoured " +
			"by the TUI and ignored here, and a managed-settings refusal kills every " +
			"session the daemon recovers",
	}

	// An explicit walk rather than parser.ParseDir, which staticcheck flags as deprecated
	// (SA1019) since Go 1.25 — the same walk app's dispatchCaseLabels uses.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()

	called := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parsing %s", name)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// A qualified call, pkg.Setter(...) — which is how every one of these is
			// written, since they all live in another package.
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				called[sel.Sel.Name] = name
			}
			return true
		})
	}

	for setter, why := range required {
		_, ok := called[setter]
		require.Truef(t, ok, "no call to %s anywhere in the daemon package: %s", setter, why)
	}
}
