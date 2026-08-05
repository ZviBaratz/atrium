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

// setupRun is everything a run needs, resolved to strings.
//
// The type is the thread-safety argument, not a comment about it: the same one
// customCommandSpec makes. Resolving the config, rendering the templates and building
// the environment all happen before the process starts; only this crosses into the
// exec seam, so nothing there reads an Instance field that a rename could be writing.
type setupRun struct {
	script string
	dir    string
	// env is the script's whole environment: Atrium's $ATRIUM_* set plus the repo's
	// own session_env.
	env []string
	// sessionEnv is only the repo's half. It is what tmux injects into the agent's
	// pane, where the $ATRIUM_* set is not repeated: tmux sets its own ATRIUM_SESSION
	// there, to the sanitized session handle rather than to the display name, and
	// exporting a second spelling of the same name is how the two would come to
	// disagree.
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
	ts := i.tmux()
	if ts == nil {
		return
	}
	// Set unconditionally, including to nil when nothing resolves. A Session object
	// outlives its tmux session — a pause and resume relaunch through this same one —
	// so leaving the previous value in place would relaunch with an environment the
	// config no longer asks for.
	run, _ := i.resolveSetupRun(dir)
	ts.SetSessionEnv(run.sessionEnv)
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
	if !ok || run.script == "" {
		return
	}

	i.setSetupPhase(setupPhaseLabel)
	defer i.setSetupPhase("")

	output, err := execSetup(i.baseContext(), run)
	i.setSetupResult(output, err)
	if err != nil {
		log.ErrorLog.Printf("setup script for %q failed: %v", i.Title, err)
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
	if dir == "" {
		return setupRun{}, false
	}
	cfg := config.LoadConfig()
	if len(cfg.RepoScripts) == 0 {
		return setupRun{}, false
	}

	repoPath := i.GetRepoPath()
	if repoPath == "" {
		repoPath = i.Path
	}
	entry, ok := cfg.ResolveRepoScript(git.GetRemoteURL(i.baseContext(), repoPath), repoPath)
	if !ok {
		return setupRun{}, false
	}

	// Validated one entry at a time, so a broken sibling entry cannot stop this one.
	// The problems reported here are the same ones the startup report and `atrium
	// doctor` show; logging is enough at this point, because by now the user has
	// already been told.
	scripts, problems := repocfg.Validate([]config.RepoScript{entry})
	for _, p := range problems {
		log.WarningLog.Printf("repo_scripts entry for %q: %s", i.Title, p.Error())
	}
	if len(scripts) == 0 {
		return setupRun{}, false
	}

	ctx := i.repoScriptCtx(dir, repoPath)
	script, err := scripts[0].RenderSetup(ctx)
	if err != nil {
		// Validation rendered this template against a fully-populated probe, so an
		// error here is about this session, not the template.
		log.ErrorLog.Printf("setup script for %q (repo_scripts entry %q) failed to render: %v", i.Title, scripts[0].Name, err)
		return setupRun{}, false
	}
	env, err := scripts[0].RenderEnv(ctx)
	if err != nil {
		log.ErrorLog.Printf("session_env for %q (repo_scripts entry %q) failed to render: %v", i.Title, scripts[0].Name, err)
		return setupRun{}, false
	}

	return setupRun{
		script: script,
		dir:    dir,
		// The $ATRIUM_* set plus the repo's own. They cannot collide: every name
		// customcmd.Env exports starts with ATRIUM_, and repocfg refuses that prefix.
		env:        append(customcmd.Env(ctx), env...),
		sessionEnv: env,
		session:    i.Title,
	}, true
}

// repoScriptCtx is the template and environment context for this session. dir is the
// worktree the caller proved is materialized; repoPath is the origin checkout.
func (i *Instance) repoScriptCtx(dir, repoPath string) repocfg.Ctx {
	return repocfg.Ctx{
		Session: repocfg.SessionCtx{
			Title:    i.Title,
			Name:     i.DisplayName(),
			Branch:   i.Branch,
			Worktree: dir,
		},
		Repo: repocfg.RepoCtx{Path: repoPath, Name: i.GroupKey()},
	}
}

// runSetupProcess runs the resolved script and records it, returning the tail of its
// combined output.
//
// Bound to ctx, which is the instance's lifecycle context, so quitting cancels a
// runaway. There is no wall-clock timeout on purpose: `npm ci` on a cold cache
// legitimately runs for minutes, and this feature exists to run exactly that.
func runSetupProcess(ctx context.Context, run setupRun) (string, error) {
	c := exec.CommandContext(ctx, "sh", "-c", run.script)
	c.Dir = run.dir
	c.Env = append(os.Environ(), run.env...)
	// One writer for both streams: os/exec guarantees at most one goroutine writes at
	// a time when Stdout and Stderr are the same comparable value, which a pointer is.
	out := &setupTail{limit: setupOutputCap}
	c.Stdout, c.Stderr = out, out

	start := time.Now()
	err := c.Run()

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

func (i *Instance) setSetupPhase(phase string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setupPhase = phase
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
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.setupErr == nil {
		return ""
	}
	report := fmt.Sprintf("The setup script for %q failed: %v\n\nThe session is running — its worktree and branch are intact, but whatever the script installs is not there.", i.Title, i.setupErr)
	if i.setupOutput != "" {
		report += "\n\n" + i.setupOutput
	}
	return report
}
