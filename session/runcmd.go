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
// read by mistake and, through the autoyes daemon, TYPE INTO by mistake. The terminal tab
// already solved this the same way with `_term` (ui/terminal.go): same suffix convention,
// same collision guards, same prefix sweep in CleanupSessions.
//
// The name is MINTED from that convention and then OWNED — persisted on the instance, not
// re-derived on each use. Deriving it is wrong twice over, and both cost a running
// server. A deep rename moves the agent's session and leaves the sibling where it is, so
// the derived name stops resolving and the dev server is orphaned, still holding its
// port. And an empty owned name is the only thing that distinguishes "we host one" from
// "something else happens to sit on the name we would mint" — a pre-existing session
// titled "foo.run" sanitizes to exactly session "foo"'s run-session name, and a teardown
// that trusted the derivation would kill that user's live agent.
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

// RunSessionName is the tmux name of the run command this session OWNS, or "" when it has
// never started one. See Instance.runName for why it is stored rather than derived.
func (i *Instance) RunSessionName() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.runName
}

// mintRunSessionName is the name a first start would claim: the session's own tmux name
// plus the reserved suffix. "" when the instance has no tmux name yet — built but never
// started, so there is nothing to derive from and nothing to host.
//
// Only ever consulted by a start, and only when this session owns no run session already.
// Every other caller reads RunSessionName, which is why a rename cannot strand a running
// server: the name it was started under is the name it is torn down under.
func (i *Instance) mintRunSessionName() string {
	name := i.TmuxSessionName()
	if name == "" {
		return ""
	}
	return name + runSessionSuffix
}

// ownedRunTmux returns the cached Session for the run command this session owns, or nil.
//
// Cached rather than rebuilt because Close is what releases the attach pty Start opened:
// a Session dropped without it leaks that pty and its `tmux attach-session` client, and
// enough of those exhaust the process's descriptors.
func (i *Instance) ownedRunTmux() *tmux.Session {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.runSession
}

// probeRunTmux builds a THROWAWAY Session for a has-session or a kill against name.
//
// Deliberately never cached. Neither operation opens a pty, so there is nothing to leak;
// and a cached probe object carries runProbeProgram, which a later Start would reuse —
// launching a bare `sh` under the dev server's name and reporting it as running. Keeping
// the probe out of the cache is what makes "the cached Session is one we started" true
// rather than merely usually true.
func (i *Instance) probeRunTmux(name string) *tmux.Session {
	if name == "" {
		return nil
	}
	return newRunSession(i.baseContext(), name, "run: "+i.Title, runProbeProgram)
}

// runSessionExists answers the has-session for the run command this session owns.
func (i *Instance) runSessionExists() bool {
	name := i.RunSessionName()
	if name == "" {
		return false
	}
	if ts := i.ownedRunTmux(); ts != nil {
		return ts.DoesSessionExist()
	}
	return i.probeRunTmux(name).DoesSessionExist()
}

// releaseRunTmux closes the cached Session — releasing its attach pty and, with it, the
// client process — and forgets both it and the owned name.
//
// Close rather than a bare drop, on every path where the run session is gone for good.
// tmux's kill-session against an already-dead session is the teardown goal already met
// (sessionAlreadyGone), so this is safe to call on a command that exited on its own; what
// it must not do is skip the pty, which is exactly how a user retrying a broken
// run_command accumulated descriptors until unrelated launches failed with "error opening
// PTY".
func (i *Instance) releaseRunTmux() error {
	i.mu.Lock()
	ts, name := i.runSession, i.runName
	i.runSession, i.runName = nil, ""
	i.runGen++
	i.mu.Unlock()

	if ts == nil {
		if name == "" {
			return nil
		}
		ts = i.probeRunTmux(name)
		if !ts.DoesSessionExist() {
			return nil
		}
	}
	return ts.Close()
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
	// A direct session's working directory IS the user's own checkout, which is the same
	// reason the setup script refuses one (see setupscript.go): starting a dev server
	// there means a second one writing over the build the user is already running, in a
	// tree Atrium does not own and cannot roll back.
	if i.IsDirect() {
		return fmt.Errorf("%q is a direct session — its directory is your own checkout, not an isolated worktree, so Atrium will not start a server in it", i.DisplayName())
	}
	if i.TmuxSessionName() == "" {
		return fmt.Errorf("%q has not started yet", i.DisplayName())
	}
	dir := i.WorkingDir()
	if dir == "" {
		return fmt.Errorf("%q has no working directory to run in", i.DisplayName())
	}

	// resolveRunOnly, never resolveSetupRun: this reads the config, and must not
	// reconcile the managed port on its way to doing so.
	run, ok := i.resolveRunOnly(dir)
	if !ok || run.run == "" {
		return fmt.Errorf("no run_command is configured for this repository — add one to a repo_scripts entry in config.json")
	}

	// Adopt a run session this instance already owns, before considering a fresh one.
	if ts := i.adoptOwnedRunSession(); ts != nil {
		i.noteRunStarted()
		return nil
	}

	name := i.RunSessionName()
	if name == "" {
		// Nothing owned, so this is a first start and the name has to be claimed. A
		// session already sitting on it is NOT ours — this instance has never started
		// one — so it belongs to somebody else, and the only safe move is to say so.
		// Killing it or attaching to it would, for a pre-existing session titled
		// "foo.run", mean tearing down or reporting on another agent's live pane.
		name = i.mintRunSessionName()
		if name == "" {
			return fmt.Errorf("%q has not started yet", i.DisplayName())
		}
		if i.probeRunTmux(name).DoesSessionExist() {
			return fmt.Errorf("cannot host the run command for %q: a tmux session named %q already exists and is not this session's — rename one of them",
				i.DisplayName(), name)
		}
	}

	ts := newRunSession(i.baseContext(), name, "run: "+i.Title, run.run)
	// The command's own environment, on the same `new-session -e` channel the agent's
	// pane uses: $ATRIUM_PORT plus the repo's session_env. It has to be set before the
	// launch, because a tmux session's environment can only be set as it is born — the
	// whole reason session_env exists rather than the server's own env.
	ts.SetSessionEnv(run.sessionEnv)

	if err := ts.Start(dir); err != nil {
		// Say what was run — the user's own template, not a stack of tmux wrapping.
		// Close first: Start may have opened a pty before failing.
		_ = ts.Close()
		return fmt.Errorf("run command for %q did not start (%s): %w", i.DisplayName(), run.run, err)
	}

	// Own it from here on, so every teardown path can find it even if the start below is
	// judged a failure — a session that exists is one somebody has to be able to kill.
	i.mu.Lock()
	i.runSession, i.runName = ts, name
	i.mu.Unlock()

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
		// releaseRunTmux, not a bare drop: Start's Restore left an attach pty open.
		_ = i.releaseRunTmux()
		return fmt.Errorf("run command for %q exited immediately (%s) — run it in the terminal tab to see why",
			i.DisplayName(), run.run)
	}

	i.noteRunStarted()
	return nil
}

// adoptOwnedRunSession re-attaches to a run session this instance already owns and
// returns it, or nil when there is nothing of ours up.
//
// Adopting rather than relaunching is what keeps a restart from starting a second server
// on a port the first is still bound to — the collision the managed port exists to
// prevent, reached by way of "helpfully" restarting. It is gated on OWNERSHIP: only a
// name this instance minted and persisted can be adopted, so a session that merely
// occupies the derived name is never attached to.
func (i *Instance) adoptOwnedRunSession() *tmux.Session {
	name := i.RunSessionName()
	if name == "" {
		return nil
	}
	ts := i.ownedRunTmux()
	if ts == nil {
		// Owned across a restart: the tmux session outlived the Atrium that made it, so
		// rebuild the object. The program is the placeholder — the process is already
		// running and nothing here will Start it.
		ts = i.probeRunTmux(name)
	}
	if !ts.DoesSessionExist() {
		// Owned but gone — it died while we were not looking. Release rather than return
		// bare: a cached object here holds the attach pty this process opened, and the
		// caller is about to overwrite the field with a fresh Session, which would drop
		// that pty on the floor.
		_ = i.releaseRunTmux()
		return nil
	}
	if err := ts.Restore(); err != nil {
		// Up but unattachable — wedged. Kill it and let the caller start clean, the same
		// recovery the terminal pane makes for its own shell.
		log.WarningLog.Printf("run command for %q: session %q is wedged, recreating: %v", i.Title, name, err)
		_ = i.releaseRunTmux()
		return nil
	}
	i.mu.Lock()
	i.runSession, i.runName = ts, name
	i.mu.Unlock()
	return ts
}

// noteRunStarted records a successful start or adoption as one local write.
func (i *Instance) noteRunStarted() {
	i.mu.Lock()
	i.runWanted, i.runLive = true, true
	i.runGen++
	i.mu.Unlock()
}

// StopRunCommand kills the run session, and is a no-op when there is none. It clears the
// wanted flag: this is the user saying stop, so a later resume must not bring it back.
func (i *Instance) StopRunCommand() error {
	i.mu.Lock()
	i.runWanted, i.runLive = false, false
	i.runGen++
	i.mu.Unlock()
	return i.closeRunSession()
}

// closeRunSession tears the run session down WITHOUT clearing the wanted flag. It is the
// half pause needs: the server has to stop because its worktree is about to be deleted,
// but the fact that the user wanted one has to survive so resume can restart it.
//
// It does clear the OWNED NAME (releaseRunTmux does), which is right in both cases: the
// tmux session is gone, so nothing is left to own, and a resume mints a fresh name from
// whatever the session is called by then.
func (i *Instance) closeRunSession() error {
	i.mu.Lock()
	i.runLive = false
	i.runGen++
	i.mu.Unlock()
	if err := i.releaseRunTmux(); err != nil {
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
// question is asked every tick — a light tick polls only the selected session, and the
// probe is skipped for a session that has never started a server — so a zero value must
// not be applied as an observation, or every skipped tick would erase the last real one.
type RunState struct {
	Configured      bool
	ConfiguredKnown bool
	Live            bool
	LiveKnown       bool
	// gen is the instance's run-state generation when this observation was taken. Every
	// local write (start, stop, pause) bumps it, so ApplyRunState can tell an
	// observation that describes the current state from one taken before a keypress it
	// knows nothing about. Unexported: only the Compute/Apply pair may set or read it.
	gen uint64
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
//   - "configured" is re-resolved every time this runs, so a config edit that adds a
//     run_command reaches the hint bar within a sweep. It is affordable because the
//     expensive half is memoized elsewhere: an empty repo_scripts early-outs before
//     anything forks, and the origin remote — the one subprocess — is remembered per
//     instance (originRemote). What is left is one small JSON read per polled session.
//     It is deliberately NOT memoized here: the answer gates the `d` key, and a memo that
//     never expired left the key permanently dead for any session whose first tick
//     happened to answer "no".
//   - "live" is one has-session, and only for a session that has actually started a run
//     command. A session that never has never probes.
func (i *Instance) ComputeRunState() (r RunState) {
	if i.Paused() || !i.Started() {
		return
	}
	// Read before the probes, so a local write that lands DURING them is detected as
	// making this observation stale rather than being overwritten by it.
	i.mu.RLock()
	r.gen = i.runGen
	i.mu.RUnlock()

	r.ConfiguredKnown = true
	if dir := i.WorkingDir(); dir != "" && !i.IsDirect() {
		if compiled, _, ok := i.routeRepoScript(dir); ok {
			r.Configured = compiled.HasRunCommand()
		}
	}
	if i.RunWanted() {
		r.LiveKnown = true
		r.Live = i.runSessionExists()
	}
	return r
}

// ApplyRunState lands one tick's observation. Main thread only.
//
// A probe reports LIVENESS and nothing else — see the body for why it may not revoke the
// user's intent. A dead one does release the tmux object, so the next start builds a
// fresh Session rather than reusing a handle to something that is gone.
//
// A STALE observation is discarded whole. The metadata sweep fans out and then waits on
// its slowest member, so an observation can be minutes older than the keypress it lands
// after; applying one unconditionally let a batch taken before a stop re-assert a dead
// server on the row, indefinitely. The generation makes "older than the current state"
// answerable rather than guessed at.
func (i *Instance) ApplyRunState(r RunState) {
	if !r.ConfiguredKnown && !r.LiveKnown {
		return
	}
	i.mu.Lock()
	if r.gen != i.runGen {
		i.mu.Unlock()
		return
	}
	if r.ConfiguredKnown {
		i.runConfigured, i.runConfiguredKnown = r.Configured, true
	}
	drop := false
	if r.LiveKnown {
		i.runLive = r.Live
		// Only liveness. The WANTED flag is the user's intent — they pressed `d` — and a
		// probe is in no position to revoke it. It used to: a probe that found the
		// session gone cleared both, which meant a poll landing in the seconds a pause
		// spends committing and removing the worktree (pause stops the server first, and
		// only reaches SetStatus(Paused) at the end) silently converted "restart this on
		// resume" into "the user never wanted one", and the resume brought nothing back.
		//
		// Keeping it costs one has-session per full sweep for a session whose server
		// died, and buys a state that means what it says: wanted is intent, live is fact,
		// and the row already renders the second.
		if !r.Live {
			drop = true
		}
	}
	i.mu.Unlock()
	if drop {
		// releaseRunTmux, not a bare drop: the cached Session may hold the attach pty
		// this process opened, and Close is the only thing that gives it back.
		if err := i.releaseRunTmux(); err != nil {
			log.WarningLog.Printf("run command for %q: %v", i.Title, err)
		}
	}
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
