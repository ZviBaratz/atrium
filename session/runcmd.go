package session

// runcmd.go — the per-repo run command (#389): the repo's long-running process, hosted
// so it outlives the thing that started it.
//
// It is the third leg of the same feature. setup_script installs into a fresh worktree
// and is AWAITED; port_range hands the session a number nothing else will take; this
// runs the server that binds it. A dev server is not a script to wait for — it is a
// process to keep, read, and tear down with the session — so it gets a home rather than
// a goroutine.
//
// That home is a SIBLING tmux session, `<tmuxName>_run`, not a second window in the
// agent's own. session/tmux/pane.go says why: capture-pane and send-keys resolve to a
// session's ACTIVE pane, and paneTarget() falls back to the session name when pane-id
// resolution fails — so a second window in the agent's session is a pane the poll can
// read by mistake and, through the autoyes daemon, TYPE INTO by mistake. The terminal
// tab already solved this the same way with `_term` (ui/terminal.go), and `_run` copies
// it: same derivation, same collision guards, same prefix sweep in CleanupSessions.
//
// Three lifecycle rules, each for its own reason:
//
//   - Kill closes it, from Instance.Kill, so every retire path — single, batch,
//     post-merge, Start's own error unwind — is covered without an app-layer hook.
//   - Pause stops it. Pause removes the WORKTREE, which is the command's working
//     directory; a server left behind would be running in a deleted tree.
//   - Resume restarts it, when the pause was what stopped it. The port is kept across a
//     pause (see port.go), so the restart lands on the same number rather than
//     renumbering underneath a user who has the old one in a browser tab.
//
// What it deliberately does not do is start on its own. A session is not always one you
// want a server for, and a fleet that started one per session would bind a port per
// session too.

import (
	"fmt"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// runSessionSuffix is what turns the agent session's name into its run session's. It is
// reserved against every session title by the collision guards (see
// session.DerivedTmuxNameCollides), so no agent session can ever mint it.
const runSessionSuffix = "_run"

// runProbeProgram is the placeholder command a run Session is built with when all that
// is wanted from it is a has-session probe or a kill — after a restart, say, where the
// `_run` session outlived the Atrium that started it and nothing here knows what it was
// launched with. Neither operation reads the program (Close and DoesSessionExist address
// the session by name), so the value only has to be something agent.Resolve can classify
// harmlessly.
const runProbeProgram = "sh"

// runSettleDelay is how long StartRunCommand waits before believing the session it just
// created. It is the window a command that cannot run at all dies in — sh exits 127 on a
// missing binary in single-digit milliseconds, and a server that finds its port taken
// takes a few hundred — and it is spent only on the start path, behind a busy row.
//
// A var so tests can zero it; nothing else writes it at run time.
var runSettleDelay = 500 * time.Millisecond

// newRunSession builds the run command's tmux Session, a package var for the same
// reason execSetup and probePort are: it is the seam a test substitutes to assert what
// this file does to a tmux session — that a pause closes one and a resume opens one —
// without a tmux server to do it to.
var newRunSession = tmux.NewSessionWithName

// runSessionName is the tmux name of this session's run command, or "" when the instance
// has no tmux name yet — an instance built but never started, which has nothing to
// derive from and nothing to host.
func (i *Instance) runSessionName() string {
	name := i.TmuxSessionName()
	if name == "" {
		return ""
	}
	return name + runSessionSuffix
}

// runTmux returns the tmux Session for this instance's run command, building and caching
// it on first use.
//
// Cached, unlike the throwaway Sessions a probe could get away with, because Close is
// what releases the attach pty Start opened: a Session dropped after Start leaks that
// pty for the life of the process. program is used only when the object is built — a
// later call with a different one reuses what is there, which is right, since the only
// caller that has a real program is the one that is about to Start it.
func (i *Instance) runTmux(program string) *tmux.Session {
	name := i.runSessionName()
	if name == "" {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.runSession == nil {
		i.runSession = newRunSession(i.baseContext(), name, "run: "+i.Title, program)
	}
	return i.runSession
}

// dropRunTmux forgets the cached run Session, so the next use rebuilds it. Called where
// the tmux session it points at is gone for good.
func (i *Instance) dropRunTmux() {
	i.mu.Lock()
	i.runSession = nil
	i.mu.Unlock()
}

// StartRunCommand launches the repo's run_command in this session's `_run` tmux session,
// and reports why it could not when it could not.
//
// Off the UI thread: it forks git for the origin remote, reads the config, and waits on
// a tmux new-session.
func (i *Instance) StartRunCommand() error {
	if i.Paused() {
		return fmt.Errorf("%q is paused — resume it before starting its run command", i.DisplayName())
	}
	name := i.runSessionName()
	if name == "" {
		return fmt.Errorf("%q has not started yet", i.DisplayName())
	}
	dir := i.WorkingDir()
	if dir == "" {
		return fmt.Errorf("%q has no working directory to run in", i.DisplayName())
	}

	run, ok := i.resolveSetupRun(dir)
	if !ok || run.run == "" {
		return fmt.Errorf("no run_command is configured for this repository — add one to a repo_scripts entry in config.json")
	}

	ts := i.runTmux(run.run)
	// The command's own environment, on the same `new-session -e` channel the agent's
	// pane uses: $ATRIUM_PORT plus the repo's session_env. It has to be set before the
	// launch, because a tmux session's environment can only be set as it is born — the
	// whole reason session_env exists rather than the server's own env.
	ts.SetSessionEnv(run.sessionEnv)

	if ts.DoesSessionExist() {
		// Already up — from an earlier press, or from the Atrium that ran before this
		// one. Re-attach rather than relaunch: a second server on the same port is
		// exactly what the managed port exists to prevent, and the running one is the
		// one the user's browser is pointed at.
		if err := ts.Restore(); err == nil {
			i.setRunWanted(true)
			i.SetRunLive(true)
			return nil
		}
		// It exists but cannot be attached to — wedged. Kill it and start clean, the
		// same recovery the terminal pane makes for its own shell.
		if err := ts.Close(); err != nil {
			log.WarningLog.Printf("run command for %q: failed to close a wedged session: %v", i.Title, err)
		}
		i.dropRunTmux()
		ts = i.runTmux(run.run)
		ts.SetSessionEnv(run.sessionEnv)
	}

	if err := ts.Start(dir); err != nil {
		// Say what was run — the user's own template, not a stack of tmux wrapping.
		i.dropRunTmux()
		return fmt.Errorf("run command for %q did not start (%s): %w", i.DisplayName(), run.run, err)
	}

	// Start returning nil is NOT enough to claim the command is running, and this was a
	// live-drive finding rather than a theoretical one: a command that cannot run at all
	// — a missing binary, a syntax error, a port already taken — still gets a tmux
	// session created for it, which Start's existence poll catches alive before the
	// shell exits 127 a moment later. Reporting that as "dev command running" is a lie
	// the row then quietly corrects a tick later, which is the worst of both.
	//
	// So look again after a beat. A session still up here is running something; one that
	// is gone took its own output with it, so the report can only say that it exited and
	// point at where the reason is visible.
	if runSettleDelay > 0 {
		time.Sleep(runSettleDelay)
	}
	if !ts.DoesSessionExist() {
		i.dropRunTmux()
		return fmt.Errorf("run command for %q exited immediately (%s) — run it in the terminal tab to see why",
			i.DisplayName(), run.run)
	}

	i.setRunWanted(true)
	i.SetRunLive(true)
	return nil
}

// StopRunCommand kills the run session, and is a no-op when there is none. It clears the
// wanted flag: this is the user saying stop, so a later resume must not bring it back.
func (i *Instance) StopRunCommand() error {
	i.setRunWanted(false)
	return i.closeRunSession()
}

// closeRunSession tears the run session down WITHOUT touching the wanted flag. It is the
// half pause needs: the server has to stop because its worktree is about to be deleted,
// but the fact that the user wanted one has to survive so resume can restart it.
func (i *Instance) closeRunSession() error {
	i.SetRunLive(false)
	if i.runSessionName() == "" {
		return nil
	}
	ts := i.runTmux(runProbeProgram)
	if ts == nil || !ts.DoesSessionExist() {
		i.dropRunTmux()
		return nil
	}
	err := ts.Close()
	i.dropRunTmux()
	if err != nil {
		return fmt.Errorf("failed to stop the run command for %q: %w", i.DisplayName(), err)
	}
	return nil
}

// pauseRunCommand stops the run command over a pause, keeping the wanted flag so Resume
// restarts it. Best-effort and never fatal to the pause: a session that cannot park
// because its dev server would not die is a worse outcome than a stray tmux session,
// which `atrium reset` and the next kill both sweep.
func (i *Instance) pauseRunCommand() {
	if !i.RunWanted() {
		return
	}
	if err := i.closeRunSession(); err != nil {
		log.WarningLog.Printf("pause %q: %v", i.Title, err)
	}
}

// resumeRunCommand restarts a run command the pause stopped. Best-effort for the mirror
// of pause's reason: a resumed session with no dev server is a keypress away from having
// one, while a resume that failed outright would strand a materialized worktree.
func (i *Instance) resumeRunCommand() {
	if !i.RunWanted() {
		return
	}
	if err := i.StartRunCommand(); err != nil {
		log.WarningLog.Printf("resume %q: %v", i.Title, err)
	}
}

// RunState is what one metadata tick observed about a session's dev command: whether
// its repository declares one at all, and whether one is running.
//
// The two *Known flags are what let a tick say nothing rather than say "no". Neither
// question is asked every tick — the first is memoized, the second is skipped for a
// session that has never started a server — so a zero value must not be applied as an
// observation, or every skipped tick would erase the last real one.
type RunState struct {
	Configured      bool
	ConfiguredKnown bool
	Live            bool
	LiveKnown       bool
}

// ComputeRunState is the poll-goroutine half of the run-command refresh, paired with
// ApplyRunState on the main thread — the same split ComputeModel/SetModelMeta make, and
// for the same reason: the subprocesses belong off the update thread and the state the
// renderer reads belongs on it.
//
// What it must NOT do is what the setup path does with the same config: it routes
// through routeRepoScript, which has no side effects, never through resolveSetupRun,
// which reserves and releases ports.
//
// Both halves are priced to cost nothing for the vast majority of sessions, whose config
// has no repo_scripts at all:
//
//   - "configured" is answered once per process — a second tick sees ConfiguredKnown and
//     skips — and its first answer early-outs on an empty repo_scripts before forking git
//     (GetRemoteURL is an uncached fork). A config edit that adds a run_command therefore
//     reaches the hint bar at the next resume or restart; the key itself re-resolves the
//     config on every press, so the action is never stale, only its advertisement.
//   - "live" is one has-session, and only for a session that has actually started a run
//     command. A session that never has never probes.
func (i *Instance) ComputeRunState() (r RunState) {
	if i.Paused() || !i.Started() {
		return
	}
	i.mu.RLock()
	known := i.runConfiguredKnown
	i.mu.RUnlock()
	if !known {
		r.ConfiguredKnown = true
		if dir := i.WorkingDir(); dir != "" {
			if compiled, _, ok := i.routeRepoScript(dir); ok {
				r.Configured = compiled.HasRunCommand()
			}
		}
	}
	if i.RunWanted() {
		r.LiveKnown = true
		ts := i.runTmux(runProbeProgram)
		r.Live = ts != nil && ts.DoesSessionExist()
	}
	return r
}

// ApplyRunState lands one tick's observation. Main thread only.
//
// A probe that finds the session gone clears the WANTED flag too, not just the live one.
// That is the self-correction the whole arrangement rests on: a server that crashed, or
// was killed from outside, otherwise leaves a session probing for it forever and a
// resume restarting a server the user never asked to have back.
func (i *Instance) ApplyRunState(r RunState) {
	i.mu.Lock()
	if r.ConfiguredKnown {
		i.runConfigured, i.runConfiguredKnown = r.Configured, true
	}
	if r.LiveKnown {
		i.runLive = r.Live
		if !r.Live {
			i.runWanted = false
		}
	}
	drop := r.LiveKnown && !r.Live
	i.mu.Unlock()
	if drop {
		i.dropRunTmux()
	}
}

// SetRunLive records the run command's liveness directly, for the paths that know it
// without probing: the start and stop actions, which have just made it true or false.
func (i *Instance) SetRunLive(live bool) {
	i.mu.Lock()
	i.runLive = live
	i.mu.Unlock()
}

// RunConfigured reports whether this session's repository is known to declare a run
// command. False before the first poll has looked, which is the right default for an
// advertisement: the hint bar under-promises for a tick rather than offering an action
// that then refuses.
func (i *Instance) RunConfigured() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.runConfigured
}

// RunCommandUnavailable is the opposite question, and deliberately not !RunConfigured():
// it is true only once the poll has actually LOOKED and found nothing.
//
// The distinction is what keeps a refusal and its explanation in step. The palette dims
// a row it can name a reason for, and a guard that read "not known to be configured"
// would dim `d` on a session nobody had polled yet — and then the key, which resolves
// the config for itself on every press, would run anyway. One predicate for both sides
// means the palette never claims something the key contradicts.
func (i *Instance) RunCommandUnavailable() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.runConfiguredKnown && !i.runConfigured
}

// RunLive reports whether this session's run command is running, as of the last observed
// probe.
func (i *Instance) RunLive() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.runLive
}

// RunWanted reports whether the user has a run command started for this session. It is
// the persisted half of the pair: it survives a restart (so the row is right on the
// first frame, and the probe knows whether to run at all) where runLive does not.
func (i *Instance) RunWanted() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.runWanted
}

func (i *Instance) setRunWanted(wanted bool) {
	i.mu.Lock()
	i.runWanted = wanted
	i.mu.Unlock()
}
