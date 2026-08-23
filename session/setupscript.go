package session

// setupscript.go — the per-repo setup script (#389): what runs once a session's
// worktree exists, before the agent program is launched into it.
//
// A worktree materializes only tracked files. carry_files copies gitignored config
// in and link_paths symlinks large gitignored trees, but both move FILES — neither
// can install dependencies, apply a migration, or generate local state. This is the
// step that can, and it is deliberately the LAST thing to happen to a worktree
// before an agent sees it.
//
// The two mechanisms interact, and the direction matters. Installing under a LINKED
// path writes into the user's own checkout, shared by every session at once, so for
// an ordinary session linking and installing are alternatives. They compose for a
// dependency-isolated session (#481), which is given none of the links: there this
// script is what fills the empty tree, and it is why skipping the link rather than
// copying it is enough on its own.
//
// Two contracts govern everything here, and both are about not destroying a session.
//
// A failure is recorded, never returned. Instance.Start's deferred unwind calls
// Kill() on any error, and the app handler removes the session from the list — so
// routing a non-zero exit through Start's error would delete the worktree and branch
// of a session whose only problem is that `npm ci` could not reach the network. The
// same reasoning already documented for seedLocalPaths, one rung further: seeding may
// not fail loudly at all, while a script MUST say something, just not there.
//
// It runs once per worktree MATERIALIZATION, not once per session. Pause removes the
// worktree; resume recreates it, empty of every gitignored path the previous run
// installed. So Resume runs this again, and only the resume path that finds its
// worktree still on disk (and therefore skips Worktree.Setup) skips it too.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
)

// setupOutputCap is how much of a script's output is kept for the failure report. A
// setup script can stream for minutes; the tail is the part that says why it failed.
const setupOutputCap = 8 << 10

// setupPhaseLabel is what the row shows while the script runs. Deliberately short and
// deliberately not the script itself: the script is user-authored and unbounded, and
// this shares a row with a branch name and diff stats.
const setupPhaseLabel = "running setup script…"

// setupWaitDelay bounds the two waits os/exec would otherwise make unbounded, both of
// which begin only once the script's own shell is done with: a child that outlived a
// cancel, and output pipes a surviving DESCENDANT still holds open.
//
// The second is the one that matters, and it has nothing to do with cancellation. A
// script that backgrounds anything (`ollama serve &`, `npm run dev &`, anything that
// daemonizes) exits immediately while its child keeps the inherited pipe, and Wait
// blocks on that pipe — not on the process — for as long as the child lives. Without
// this the shell exits, the script "succeeds", and the session stays pinned on
// `running setup script…` forever with no agent ever launched and no timeout to
// recover it.
//
// Short, because it is spent AFTER the work is done: the timer starts when the shell
// exits, so the only thing it buys is time for output already written to be copied
// into the tail.
const setupWaitDelay = 2 * time.Second

// setupRun is everything a run needs, resolved to strings.
//
// The type is the thread-safety argument, not a comment about it: the same one
// customCommandSpec makes. Resolving the config, rendering the templates and building
// the environment all happen before the process starts; only this crosses into the
// exec seam, so nothing there reads an Instance field that a rename could be writing.
type setupRun struct {
	script string
	// run is the rendered run_command — the repo's long-running process (#389).
	// Resolved here rather than by a path of its own because it needs exactly what the
	// script needs: the routed entry, the rendered context, and the session
	// environment. "" when the repo declares none.
	run string
	dir string
	// env is the script's whole environment: Atrium's $ATRIUM_* set plus the repo's
	// own session_env.
	env []string
	// sessionEnv is what tmux injects into the agent's pane: the repo's own values,
	// plus ATRIUM_PORT when the session holds one. The rest of the $ATRIUM_* set is
	// deliberately not repeated here — tmux sets its own ATRIUM_SESSION, to the
	// sanitized session handle rather than to the display name, and exporting a second
	// spelling of the same name is how the two would come to disagree. The port has no
	// such second spelling: one number, one meaning, in both places.
	sessionEnv []string
	session    string
}

// execSetup is the exec leg, a package var so tests can substitute a recorder and
// assert that a refused entry spawns NO PROCESS. A gate that only suppressed the
// recorded failure would satisfy every assertion about what the row says while still
// running the script.
var execSetup = runSetupProcess

// applySessionEnv hands the repo's rendered session_env to the tmux session that is
// about to be created. It must run before the launch: tmux can only set a session's
// environment at birth, which is the whole reason this mechanism exists rather than
// the tmux server's own env (frozen when the server started).
//
// Separate from runSetupScript, and called from more places, because the two answer
// different questions. The script is about a worktree that was just materialized; the
// environment is about a tmux session that is about to be born, and a resume can do
// the second without the first.
func (i *Instance) applySessionEnv(dir string) {
	run, _ := i.resolveSetupRun(dir)
	i.applyResolvedSessionEnv(run)
}

// applyResolvedSessionEnv is applySessionEnv over an already-resolved run, for the
// caller that is about to do both halves and should not resolve twice — resolution
// reads config.json and, when repo_scripts is non-empty, shells out to git for the
// origin remote, both on the session-creation critical path.
func (i *Instance) applyResolvedSessionEnv(run setupRun) {
	ts := i.tmux()
	if ts == nil {
		return
	}
	// Set unconditionally, including to nil when nothing resolved. A Session object
	// outlives its tmux session — a pause and resume relaunch through this same one —
	// so leaving the previous value in place would relaunch with an environment the
	// config no longer asks for.
	ts.SetSessionEnv(run.sessionEnv)
}

// StartRepoEnvironment runs the setup script and applies the session environment for a
// worktree that has just been materialized, resolving the config once for both.
//
// The two halves are separately reachable (a resume can apply the environment without
// re-running the script), but a first-time start always does both, and doing them
// through their own entry points made the session-creation path load the config twice
// and fork git twice for one answer.
func (i *Instance) StartRepoEnvironment(dir string) {
	run, ok := i.resolveSetupRun(dir)
	if !ok {
		i.applyResolvedSessionEnv(setupRun{})
		return
	}
	i.runResolvedSetupScript(run)
	i.applyResolvedSessionEnv(run)
}

// RunSetupScript runs the configured setup script in dir, recording the outcome on the
// instance. dir is passed rather than derived because the caller is the only one that
// knows the worktree is materialized RIGHT NOW — WorkingDir() falls back to the user's
// own checkout when the worktree pointer is nil, and running `rm -rf build` there is
// the one outcome this must never produce.
//
// Direct (non-git) sessions never reach here: their working directory IS the user's
// checkout, which is already warm and is not Atrium's to install into.
func (i *Instance) RunSetupScript(dir string) {
	run, ok := i.resolveSetupRun(dir)
	if !ok {
		return
	}
	i.runResolvedSetupScript(run)
}

// runResolvedSetupScript is RunSetupScript over an already-resolved run.
func (i *Instance) runResolvedSetupScript(run setupRun) {
	if run.script == "" {
		return
	}

	// A cancel of its own, published so shutdown can end a script that would
	// otherwise outlast the app. The lifecycle context covers a signal shutdown, but
	// NOT the force-quit path — there ctx is still live, and a script with no timeout
	// would hold Start's goroutine past the reconciliation drain, orphaning the
	// worktree and branch it had already created and leaving an `npm ci` running after
	// Atrium exited. See AbortSetupScript.
	ctx, cancel := context.WithCancel(i.baseContext())
	defer cancel()
	i.setSetupPhase(setupPhaseLabel, cancel)
	defer i.setSetupPhase("", nil)

	output, err := execSetup(ctx, run)
	i.setSetupResult(output, err)
	if err != nil {
		log.ErrorLog.Printf("setup script for %q failed: %v", i.Title(), err)
	}
}

// resolveSetupRun loads the config, routes this repo to an entry, and renders it.
// ok is false whenever there is nothing to run — an unconfigured repo, an entry that
// declares no script, or one the validator refused.
//
// The config is read here rather than captured on the Instance for the reason carry.go
// gives for the same choice: instances are rebuilt once at startup and Resume reuses
// them, so a captured entry would freeze whatever the config said when the app
// launched and an edit would never reach a resumed session.
func (i *Instance) resolveSetupRun(dir string) (setupRun, bool) {
	compiled, repoPath, ok := i.routeRepoScript(dir)
	if !ok {
		// Nothing routes here any more, so a port this session was holding under an
		// older config goes back to whoever asks next.
		i.releasePort()
		return setupRun{}, false
	}

	// Before the context is built, because the port is one of its leaves: a template
	// spelling {{.Session.Port}} and a script reading $ATRIUM_PORT both resolve from
	// what this call decides (#389).
	i.reservePort(compiled)

	return i.renderSetupRun(compiled, repoPath, dir)
}

// resolveRunOnly is resolveSetupRun for a caller that only wants to READ the config: it
// routes and renders, and touches no port.
//
// The split is not cosmetic. resolveSetupRun reconciles the managed port as a side
// effect — reserving one the config now asks for, releasing one it no longer does — which
// is correct where a worktree has just been materialized and wrong everywhere else. The
// `d` key ran through it and so could hand a live session's port back to the allocator on
// its way to reporting that no run_command is configured, while the pane's frozen
// $ATRIUM_PORT and the server bound to it carried on.
func (i *Instance) resolveRunOnly(dir string) (setupRun, bool) {
	compiled, repoPath, ok := i.routeRepoScript(dir)
	if !ok {
		return setupRun{}, false
	}
	return i.renderSetupRun(compiled, repoPath, dir)
}

// renderSetupRun renders an already-routed entry against this session. Pure: no port, no
// tmux, no process.
func (i *Instance) renderSetupRun(compiled repocfg.Script, repoPath, dir string) (setupRun, bool) {
	ctx := i.repoScriptCtx(dir, repoPath)
	script, err := compiled.RenderSetup(ctx)
	if err != nil {
		// Validation rendered this template against a fully-populated probe, so an
		// error here is about this session, not the template.
		log.ErrorLog.Printf("setup script for %q (repo_scripts entry %q) failed to render: %v", i.Title(), compiled.Name, err)
		return setupRun{}, false
	}
	// A run_command that will not render is reported, and then STEPPED OVER rather than
	// failing the whole resolution. It is the one field here nothing else depends on: the
	// setup script still has to run and the session environment still has to be injected,
	// and validation cannot catch every case — repocfg renders each template once against
	// a probe, so a placeholder inside a conditional the probe does not enter escapes to
	// here. Failing outward would let a typo in an on-demand dev-server command silently
	// leave every new session in that repo with an uninstalled worktree and no
	// $ATRIUM_PORT, for a feature the user may never press.
	runCmd, err := compiled.RenderRun(ctx)
	if err != nil {
		log.ErrorLog.Printf("run_command for %q (repo_scripts entry %q) failed to render: %v", i.Title(), compiled.Name, err)
		runCmd = ""
	}
	env, err := compiled.RenderEnv(ctx)
	if err != nil {
		log.ErrorLog.Printf("session_env for %q (repo_scripts entry %q) failed to render: %v", i.Title(), compiled.Name, err)
		return setupRun{}, false
	}

	return setupRun{
		script: script,
		run:    runCmd,
		dir:    dir,
		// The $ATRIUM_* set plus the repo's own. They cannot collide: every name
		// customcmd.Env exports starts with ATRIUM_, and repocfg refuses that prefix.
		// ATRIUM_PORT rides the first half, since the port is a context leaf.
		env: append(customcmd.Env(ctx), env...),
		// The tmux half carries the port explicitly, because customcmd.Env is not in
		// it: only the repo's own values and the ones Atrium must set per session go
		// through `new-session -e`. A session with no port injects nothing at all, so
		// the pane inherits whatever launched Atrium — deliberately, since tmux's
		// session environment is frozen at birth and a placeholder set there could
		// never be corrected. Note this is NOT how the setup script sees it: its
		// environment is customcmd.Env's, which exports every $ATRIUM_* name whether or
		// not it has a value (ATRIUM_BRANCH does the same for a direct session), so
		// `${ATRIUM_PORT+x}` is set-and-empty in the script and absent in the pane. Test
		// the value, not its existence.
		sessionEnv: withPortEnv(i.PortText(), env),
		session:    i.Title(),
	}, true
}

// withPortEnv prepends ATRIUM_PORT to the repo's own rendered pairs, or returns them
// unchanged when the session holds no port. It copies rather than appending in place:
// env is the slice the setup script's environment is also built from.
func withPortEnv(port string, env []string) []string {
	if port == "" {
		return env
	}
	return append([]string{"ATRIUM_PORT=" + port}, env...)
}

// routeRepoScript loads the config and resolves the entry that governs this session's
// repository, validated and ready to render. ok is false when the section is empty,
// when nothing routes here, or when the routed entry is one the validator refuses.
//
// The config is read here rather than captured on the Instance for the reason carry.go
// gives for the same choice: instances are rebuilt once at startup and Resume reuses
// them, so a captured entry would freeze whatever the config said when the app launched
// and an edit would never reach a resumed session.
func (i *Instance) routeRepoScript(dir string) (repocfg.Script, string, bool) {
	if dir == "" {
		return repocfg.Script{}, "", false
	}
	cfg := config.LoadConfig()
	if len(cfg.RepoScripts) == 0 {
		return repocfg.Script{}, "", false
	}

	repoPath := i.GetRepoPath()
	if repoPath == "" {
		repoPath = i.Path
	}
	entry, index, ok := cfg.ResolveRepoScript(i.originRemote(repoPath), repoPath)
	if !ok {
		return repocfg.Script{}, "", false
	}

	// Validated one entry at a time, so a broken sibling entry cannot stop this one —
	// carrying the entry's real position, because that is what a message about it is
	// found by. The same problem is what the startup report and `atrium doctor` show, so
	// logging is enough at this point: by now the user has already been told.
	compiled, problem := repocfg.ValidateOne(index, entry)
	if problem != nil {
		log.WarningLog.Printf("setup script for %q not run: %s", i.Title(), problem.Error())
		return repocfg.Script{}, "", false
	}
	return compiled, repoPath, true
}

// originRemote is the repository's origin remote URL, forked once per instance and then
// remembered.
//
// Memoized because it is the only expensive half of routing — `git config --get
// remote.origin.url` is an uncached subprocess (git.GetRemoteURL) — and because it cannot
// go stale in a way that matters: a session's repo path is fixed at creation, and a user
// who re-points origin mid-session is re-pointing the repository this session was made
// from. The CONFIG half is deliberately not memoized with it, so an edit to repo_scripts
// reaches a running session on the next poll rather than at the next restart.
//
// The empty answer is memoized too (a repo with no origin routes on its path alone), so a
// non-git or remote-less session forks once rather than every sweep.
func (i *Instance) originRemote(repoPath string) string {
	i.mu.RLock()
	url, known := i.originURL, i.originURLKnown
	i.mu.RUnlock()
	if known {
		return url
	}
	url = git.GetRemoteURL(i.baseContext(), repoPath)
	i.mu.Lock()
	i.originURL, i.originURLKnown = url, true
	i.mu.Unlock()
	return url
}

// repoScriptCtx is the template and environment context for this session. dir is the
// worktree the caller proved is materialized; repoPath is the origin checkout.
//
// No empty-leaf guard here, unlike customcmd's MissingFields, because on this path
// there is nothing to guard: a setup script runs only for a git session that has just
// materialized a worktree, and every leaf is non-empty there — Title is required,
// DisplayName falls back to it, Branch is what the worktree was created on, dir is
// checked by resolveSetupRun, and repoPath falls back to i.Path. The one leaf that CAN
// render empty is Session.Branch for a direct session, which gets session_env and never
// a script (see Instance.Start). If a leaf that can be absent is ever added, the
// `rm -rf {{.Session.X}}/build` case comes back and that guard has to come with it.
func (i *Instance) repoScriptCtx(dir, repoPath string) repocfg.Ctx {
	return repocfg.Ctx{
		Session: repocfg.SessionCtx{
			Port:     i.PortText(),
			Title:    i.Title(),
			Name:     i.DisplayName(),
			Branch:   i.Branch(),
			Worktree: dir,
		},
		Repo: repocfg.RepoCtx{Path: repoPath, Name: i.GroupKey()},
	}
}

// runSetupProcess runs the resolved script and records it, returning the tail of its
// combined output.
//
// Bound to ctx, which is the instance's lifecycle context, so quitting cancels a
// runaway. There is no wall-clock timeout on the SCRIPT on purpose: `npm ci` on a cold
// cache legitimately runs for minutes, and this feature exists to run exactly that.
// Everything after the script is bounded, though — see setupWaitDelay and
// isolateProcessGroup, which together are what keep "no timeout" from meaning "no way
// out".
func runSetupProcess(ctx context.Context, run setupRun) (string, error) {
	c := exec.CommandContext(ctx, "sh", "-c", run.script)
	c.Dir = run.dir
	c.Env = append(os.Environ(), run.env...)
	// One writer for both streams: os/exec guarantees at most one goroutine writes at
	// a time when Stdout and Stderr are the same comparable value, which a pointer is.
	out := &setupTail{limit: setupOutputCap}
	c.Stdout, c.Stderr = out, out
	// Neither stream is an *os.File, so os/exec plumbs both through PIPES — which is
	// what makes a descendant able to hold Wait open, and why both bounds below are
	// needed rather than one.
	c.WaitDelay = setupWaitDelay
	isolateProcessGroup(c)

	start := time.Now()
	err := c.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The shell exited 0 and something it left behind kept the output pipe open, so
		// os/exec closed the pipes after setupWaitDelay and reported this in place of
		// nil. The script itself succeeded — leaving a dev server running is a
		// legitimate thing for one to do — so this must not become a failure modal that
		// says otherwise. The only real consequence is a tail that may be short.
		log.WarningLog.Printf("setup script for %q left a process holding its output open; recorded output may be truncated", run.session)
		err = nil
	}

	// Record a synthetic argv, NEVER the rendered script. cmdlog.Redact models one
	// NAME=VALUE per argv token, and a whole shell script in a single token defeats it
	// twice: a token inside a flag has no leading NAME= and is stored verbatim, while a
	// leading FOO=bar match returns everything before the first '=' and throws the rest
	// of the command away.
	c.Args = []string{"atrium", "setup-script", run.session}
	cmdlog.RecordCmd(c, run.session, start, out.bytes(), err)
	return string(out.bytes()), err
}

// setupTail keeps only the last limit bytes written to it. The head is what an
// unbounded buffer would be holding for the whole run, and the tail is the part that
// says why the script failed.
type setupTail struct {
	limit int
	buf   []byte
}

func (t *setupTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *setupTail) bytes() []byte { return t.buf }

// SetupPhase is the short label for what the setup script is doing right now, or ""
// when nothing is. Read on the update thread while the Start goroutine writes it.
func (i *Instance) SetupPhase() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.setupPhase
}

// setSetupPhase publishes the phase and, with it, the cancel that ends the process
// producing it. The two move together because they describe the same run: a phase with
// no cancel is a script nothing can stop, and a cancel outliving its phase would end
// the NEXT run.
func (i *Instance) setSetupPhase(phase string, cancel context.CancelFunc) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setupPhase, i.setupCancel = phase, cancel
}

// AbortSetupScript ends a setup script that is currently running, and does nothing
// when none is.
//
// Shutdown reconciliation calls it for every session before draining the Start
// goroutines — every session, not only the Loading ones, because Resume runs a script
// too and does it while the instance is still Paused. A script deliberately has no
// timeout, so on the force-quit path — where the lifecycle context stays live — this is
// the only thing that ends one, and without it the drain times out, the session is
// "left as-is" with a worktree and branch never written to state.json, and the script
// keeps running with no app left to report it.
//
// It ends the script's whole process GROUP, not just the shell: see
// isolateProcessGroup, without which cancelling a `npm ci && npm run build` kills the
// `sh` and leaves the build holding the output pipe for its full remaining duration —
// the drain timing out exactly as if nothing had been cancelled at all.
func (i *Instance) AbortSetupScript() {
	i.mu.RLock()
	cancel := i.setupCancel
	i.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// SetupError is the last setup script's failure, or nil. It is not persisted: a
// failure describes one run of one worktree, and a restart re-materializes nothing.
func (i *Instance) SetupError() error {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.setupErr
}

// SetupOutput is the tail of the last setup script's combined output.
func (i *Instance) SetupOutput() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.setupOutput
}

// setSetupResult records one run's outcome, replacing the previous one whole — a
// failure from before a pause must not still be showing after a resume that worked.
func (i *Instance) setSetupResult(output string, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setupOutput, i.setupErr = output, err
}

// ClearSetupError drops a recorded failure once it has been shown, so the same one is
// not reported again on the next poll.
func (i *Instance) ClearSetupError() {
	i.setSetupResult("", nil)
}

// SetupFailureReport is the message the user sees for a failed setup script: what
// failed, followed by the tail of what it said. Empty when the last run succeeded.
func (i *Instance) SetupFailureReport() string {
	// Taken before i.mu, not inside it: identityMu and i.mu are never held together
	// (see Instance.identityMu), and this is the one site that would have nested them.
	title := i.Title()
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.setupErr == nil {
		return ""
	}
	report := fmt.Sprintf("The setup script for %q failed: %v\n\nThe session is running — its worktree and branch are intact, but whatever the script installs is not there.", title, i.setupErr)
	if i.setupOutput != "" {
		report += "\n\n" + i.setupOutput
	}
	return report
}
