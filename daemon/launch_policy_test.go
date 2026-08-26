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

// TestDaemonInstallsEveryProcessWidePolicy is the guard the settings it checks were all
// missing or misplaced, and it exists because the failure is invisible from every other
// direction.
//
// The daemon is a second process, with its own config load. Anything the TUI installs on a
// package-level var in newHome — a cap, a launch flag — reaches the daemon only if the
// daemon installs it too, and the daemon relaunches agents: the startup load recovers every
// session whose tmux session is gone, which reaches tmux.start() and everything start()
// decides from process state. A missing install is a build-clean behaviour split, one
// feature running under two different settings depending on which process happened to
// launch the session.
//
// Presence is not the whole claim. A launch policy installed BELOW the load governs only
// relaunches the daemon never performs, so it reads as wired while being inert — and a
// present-but-late call is what a presence-only check is least able to see. So a policy
// tmux.start() reads carries beforeLoad, and this reads the order out of RunDaemon.
//
// Read out of the source rather than exercised, for the reason the daemon's own comments
// give: nothing in the suite drives RunDaemon, so there is no run in which the calls could
// be observed. That is a real limit — this proves what is written, not that it is reached —
// and it is still the difference between the wire being absent and being present, which is
// the mistake actually made here three times.
//
// Add to the list when a new process-wide setter starts affecting a session's launch or a
// daemon-side reconciliation, and only then: a knob the daemon has no business installing
// (anything about rendering, anything the TUI owns exclusively) does not belong here. Give
// it beforeLoad only if tmux.start() reads it — SetPendingWatchdog deliberately does not,
// and requiring it early would pin a position that means nothing.
func TestDaemonInstallsEveryProcessWidePolicy(t *testing.T) {
	// loadCall is the daemon's one relaunching load: LoadInstances recovers every session
	// whose tmux session is gone, reaching tmux.start() through reattach.
	const loadCall = "LoadInstances"

	required := []struct {
		setter     string
		beforeLoad bool
		why        string
	}{{
		setter:     "SetAgentOOMMargin",
		beforeLoad: true,
		why: "the daemon relaunches agents, and this margin's zero value DISABLES the " +
			"wrapper while config's default enables it: unwired, the recovered fleet runs " +
			"with oom_score_adj unraised and memory pressure sheds the shared tmux server " +
			"— every session — instead of one recoverable agent",
	}, {
		setter:     "SetAgentSkills",
		beforeLoad: true,
		why: "the daemon relaunches agents, and this switch is INVERTED so an unwired " +
			"process injects: without the install, agent_skills false is honoured by the " +
			"TUI and ignored here, and a managed-settings refusal kills every session the " +
			"daemon recovers",
	}, {
		setter: "SetPendingWatchdog",
		why: "the daemon runs ApplyPaneState, so it reconciles stuck Pending rows on " +
			"whatever cap this process holds (#799)",
	}}

	// An explicit walk rather than parser.ParseDir, which staticcheck flags as deprecated
	// (SA1019) since Go 1.25 — the same walk app's dispatchCaseLabels uses.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()

	// Presence is a package-wide question; order is only meaningful inside RunDaemon,
	// where both calls sit in one file and token positions are comparable.
	called := map[string]bool{}
	inRunDaemon := map[string]token.Pos{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parsing %s", name)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			run := fn.Name.Name == "RunDaemon"
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// A qualified call, pkg.Setter(...) — which is how every one of these is
				// written, since they all live in another package.
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				called[sel.Sel.Name] = true
				// First occurrence: the question is whether the policy is installed
				// before the load, so a later repeat cannot answer it.
				if run {
					if _, seen := inRunDaemon[sel.Sel.Name]; !seen {
						inRunDaemon[sel.Sel.Name] = call.Pos()
					}
				}
				return true
			})
		}
	}

	// Without this the order checks below pass vacuously the moment the load is renamed,
	// which is the failure mode this whole file is a reaction to.
	loadPos, found := inRunDaemon[loadCall]
	require.Truef(t, found, "no call to %s in RunDaemon: either the daemon stopped relaunching "+
		"agents — in which case beforeLoad means nothing and these entries should lose it — "+
		"or the load was renamed and every order check below now asserts nothing", loadCall)

	for _, req := range required {
		require.Truef(t, called[req.setter],
			"no call to %s anywhere in the daemon package: %s", req.setter, req.why)
		if !req.beforeLoad {
			continue
		}
		pos, ok := inRunDaemon[req.setter]
		require.Truef(t, ok, "%s is called in the daemon package but not in RunDaemon, so it "+
			"is not installed before the load that relaunches the fleet: %s", req.setter, req.why)
		require.Lessf(t, pos, loadPos,
			"%s is installed at or after %s, so it governs only relaunches the daemon never "+
				"performs — the fleet it does recover launches on the process default: %s",
			req.setter, loadCall, req.why)
	}
}
