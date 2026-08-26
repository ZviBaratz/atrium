package tmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
)

// Status hooks: an authoritative state signal for Claude Code sessions.
//
// Scraping the pane for the interrupt-hint marker cannot distinguish a "between turns"
// pane from a genuinely-finished one — both lack the marker, and only what happens next
// tells them apart. Claude Code itself knows the difference and emits it via hooks, so we
// inject a tiny settings file (via --settings, merged with the user's own config) whose
// UserPromptSubmit/PreToolUse/PostToolUse hooks latch "working" (and stamp a heartbeat,
// #311) and whose Stop hook — which fires once at true end-of-turn, never between
// auto-accepted tool steps — latches "ready".
//
// Each hook re-invokes the atrium binary's hidden `hook` subcommand, which does a locked
// read-modify-write of a structured state record (see hookstate.go): the working/ready
// latch plus the SET of in-flight sub-agent ids, maintained by the SubagentStart/Stop
// hooks. That set is what lets Poll tell a finished turn from one only waiting on a
// background sub-agent (#290). Poll reads the record as the primary signal, falling back to
// the scrape classifier when the file is absent (non-claude agents, or before the first event).
//
// Artifacts live under <configDir>/hooks/<sanitizedName>/ — outside the git worktree, so
// they survive pause (worktree removal) and never pollute the agent's git status / diff.
// The name is the one the session had AT LAUNCH, not its current one: ensureHookSettings
// bakes the resulting absolute path into every hook command, so the agent's write path is
// fixed for the life of its process and only a relaunch can re-key it. Session.hookName is
// the single source of that name for every reader (see #492).

// Hook state words written by the injected hook commands and read back by Poll.
const (
	hookStateWorking = "working"
	hookStateReady   = "ready"
)

// isClaude reports whether program ultimately runs the Claude Code agent, via the
// adapter registry's wrapper-aware resolution (basename contains-match on the first
// token, so "claude", "/usr/local/bin/claude", "claude --continue", and a
// "launch-claude.sh" wrapper all match while /home/user/.claude-squad/bin/otheragent
// does not).
func isClaude(program string) bool {
	return agent.Resolve(program).Key == agent.KeyClaude
}

// IsClaude is the exported form of isClaude for callers outside this package that need
// the same wrapper-aware detection.
func IsClaude(program string) bool {
	return isClaude(program)
}

// hooksRoot is <configDir>/hooks, the Atrium-owned tree of per-session hook artifacts.
func hooksRoot() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks"), nil
}

// hookSessionDir is the per-session directory holding settings.json and the state file.
//
// An empty name is rejected rather than joined: it would resolve to the hooks ROOT, which
// cleanupHookSession would then RemoveAll — destroying every live session's status channel,
// not just this one's. Callers reach this with a name derived from Session.hookName, which
// never returns empty; the guard is here because that one call's blast radius is the feature.
func hookSessionDir(sanitizedName string) (string, error) {
	if sanitizedName == "" {
		return "", errors.New("hook session dir: empty session name")
	}
	root, err := hooksRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sanitizedName), nil
}

var (
	settingsFlagOnce      sync.Once
	settingsFlagSupported bool
	// settingsFlagOverride forces the probe result in tests (nil = probe normally).
	settingsFlagOverride *bool
)

// claudeSupportsSettingsFlag reports whether the claude binary accepts --settings. It is
// probed once per process via `claude --help` and cached; a negative or failed probe
// disables hook injection entirely so a launch can never fail because of this feature.
//
// The probe always runs the literal `claude` binary (which the agent ultimately exec's,
// directly or via a wrapper), never the configured program. Probing a launcher wrapper
// would run its side effects (trust writes, config copies into the cwd) on every process.
func claudeSupportsSettingsFlag() bool {
	if settingsFlagOverride != nil {
		return *settingsFlagOverride
	}
	settingsFlagOnce.Do(func() {
		claudeBin := string(agent.KeyClaude)
		// One-shot, process-cached probe with no ctx-bearing caller; Background
		// capped at probeTimeout is deliberate.
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, claudeBin, "--help").CombinedOutput()
		if err != nil {
			log.InfoLog.Printf("status hooks disabled: probing %q --help failed: %v", claudeBin, err)
			return
		}
		settingsFlagSupported = strings.Contains(string(out), "--settings")
		if !settingsFlagSupported {
			log.InfoLog.Printf("status hooks disabled: %q has no --settings flag", claudeBin)
		}
	})
	return settingsFlagSupported
}

var (
	helpProbeMu    sync.Mutex
	helpProbeCache = map[string]string{}
	// helpProbeOverride forces probe output per binary in tests (nil = probe normally).
	helpProbeOverride map[string]string
)

// binHelpContains reports whether bin's --help output contains needle. Used as a
// capability gate before applying a version-sensitive flag (e.g. gemini --resume). It
// caches the output per process, so resurrecting many sessions costs one subprocess per
// binary. A failed probe caches as empty output: the capability reads as absent and the
// caller degrades (relaunch without resume) rather than failing the launch.
//
// WHICH binary is the caller's decision, not this function's, and the two callers decide
// it differently on purpose. claudeSupportsSettingsFlag hard-codes the canonical name,
// whose wrapper side effects can then never run on a probe. The gates that route through
// probeTarget accept the configured program's own first token where its basename is
// exactly the canonical name, because a binary installed outside PATH answers only under
// its own path — and fall back to the canonical name for anything else, which is the
// wrapper case. Read probeTarget before adding a caller: the empty-output cache means a
// probe of the wrong binary is indistinguishable from a flag that does not exist.
//
// The lock covers only the map accesses, never the subprocess — a slow --help (the
// probe allows up to probeTimeout) must not block concurrent resurrections of other
// agents. Two goroutines racing on the same uncached binary may both probe; both write
// the same output, so last-writer-wins is correct.
func binHelpContains(bin, needle string) bool {
	helpProbeMu.Lock()
	if helpProbeOverride != nil {
		out := helpProbeOverride[bin]
		helpProbeMu.Unlock()
		return strings.Contains(out, needle)
	}
	out, ok := helpProbeCache[bin]
	helpProbeMu.Unlock()
	if ok {
		return strings.Contains(out, needle)
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	b, err := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
	if err != nil {
		log.InfoLog.Printf("capability probe %q --help failed: %v", bin, err)
		b = nil
	}
	out = string(b)

	helpProbeMu.Lock()
	helpProbeCache[bin] = out
	helpProbeMu.Unlock()
	return strings.Contains(out, needle)
}

// shellSingleQuote wraps s for safe use inside the hook's shell command.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HookSubcommand is the hidden CLI verb the injected hooks invoke on the atrium binary
// itself (main.go registers it). Exported so the command builder here and the subcommand
// registration in main can't drift apart.
const HookSubcommand = "hook"

// resolvedBinPath is the running atrium binary's path, resolved ONCE at package load and
// reused for the process's whole life — mirroring daemon.selfPath (#104). ensureHookSettings
// bakes this path into every session's injected settings.json hooks and runs on each session
// create and pause→resume, i.e. repeatedly over a long-lived TUI. os.Executable is a live
// readlink of /proc/self/exe on Linux, so after `atrium update` swaps the binary in place a
// fresh call reports the now-deleted old inode (".../atrium (deleted)"); a session started or
// resumed after such a swap would bake that dead path in and every one of its hooks would
// fail to exec. Resolving here, before any in-process update can run, keeps the path valid.
var resolvedBinPath, resolvedBinPathErr = os.Executable()

// hookEventCommand builds the shell command a Claude hook runs: it calls the atrium
// binary's hidden `hook` subcommand, which does the locked read-modify-write of the
// state record. The binary path and state-file path are baked in and single-quoted; the
// event is a fixed literal (no quoting needed). Some events additionally read fields from
// the hook's stdin payload, which Claude pipes in (see HookEventReadsStdin) — the command
// line is identical, only the subcommand's stdin handling differs.
//
// This replaces the Phase 1 printf one-liner: the in-flight SET needs a locked JSON
// read-modify-write that shell can't do portably (macOS has no flock(1)), so every event
// routes through the binary. Calling back into os.Executable() mirrors how the absolute
// state path was already baked into the Phase 1 command.
func hookEventCommand(binPath, stateFile, event string) string {
	return shellSingleQuote(binPath) + " " + HookSubcommand +
		" --event " + event +
		" --state-file " + shellSingleQuote(stateFile)
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookMatcherGroup struct {
	// Matcher is omitted for events that don't support it (UserPromptSubmit, Stop); the
	// per-tool events (PreToolUse, PostToolUse) set it to "*" to match all tools.
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookSettings struct {
	Hooks map[string][]hookMatcherGroup `json:"hooks"`
}

// buildHookSettings marshals the settings.json content wiring the status hooks. binPath
// is the atrium binary each hook re-invokes (see hookEventCommand). brief adds the outward-
// facing SessionStart entry (#485) when its facts are complete; a zero brief omits that event
// entirely, which is how a direct (non-git) session — no worktree, no branch — stays silent.
func buildHookSettings(binPath, stateFile string, brief SessionBrief) ([]byte, error) {
	cmd := func(event string) hookCommand {
		return hookCommand{Type: "command", Command: hookEventCommand(binPath, stateFile, event)}
	}
	s := hookSettings{Hooks: map[string][]hookMatcherGroup{
		// UserPromptSubmit latches working exactly like the per-tool edges, but via its own
		// event, for two independent reasons that happen to demand the same split: its
		// $CLAUDE_EFFORT is a stale pre-resolution value so it must NOT record effort (#325),
		// and it is the once-per-turn edge so it is the cheap place to read the payload's
		// permission_mode (#324) without putting stdin on the per-tool hot path below. See
		// HookEventPromptSubmit.
		//
		// Splitting it out is safe across an atrium upgrade, but bounded rather than
		// risk-free. ensureHookSettings runs at session Start, so a session already live when
		// the binary is replaced in place keeps a settings.json routing this edge to the old
		// "working" verb — which the new binary does record effort on, and reads no mode from.
		// That session's chip can show the stale model default mid-turn, until the turn's first
		// PreToolUse (or its Stop) overwrites it with the resolved truth; its mode is
		// unaffected, still landing on the Stop edge. Self-correcting within the turn, and the
		// chip only settles at turn end anyway; a relaunch clears it for good.
		"UserPromptSubmit": {{Hooks: []hookCommand{cmd(HookEventPromptSubmit)}}},
		"PreToolUse":       {{Matcher: "*", Hooks: []hookCommand{cmd(HookEventWorking)}}},
		// PostToolUse re-latches working at each tool boundary. It carries no new latch value
		// (a completed tool means the turn is still processing), but it bumps the heartbeat
		// (#311) so an active turn stays fresh in the gap AFTER a long tool completes and
		// before the next PreToolUse — holding working without scraping the pane.
		"PostToolUse": {{Matcher: "*", Hooks: []hookCommand{cmd(HookEventWorking)}}},
		// Stop fires at a clean end-of-turn; StopFailure fires when the turn ends on an API
		// error. Both mean the agent has stopped, so both latch "ready" — without StopFailure
		// an errored turn would leave the file on "working" until the poller's time cap.
		"Stop":        {{Hooks: []hookCommand{cmd(HookEventReady)}}},
		"StopFailure": {{Hooks: []hookCommand{cmd(HookEventReady)}}},
		// Sub-agent lifecycle. These fire in the MAIN session under the injected --settings
		// (verified by docs + a live probe on Claude 2.1.206), each carrying a matching
		// agent_id, so they are the authoritative in-flight edges: add on start, discard on
		// stop. Tracked as a SET (see hookstate.go) because unmatched Stops from nested agents
		// would drive a ++/-- counter negative. A non-empty set at end-of-turn is what makes a
		// background sub-agent read as pending rather than done (#290).
		"SubagentStart": {{Hooks: []hookCommand{cmd(HookEventSubagentStart)}}},
		"SubagentStop":  {{Hooks: []hookCommand{cmd(HookEventSubagentStop)}}},
	}}
	// The only event that writes TO the agent rather than reading it: a short situational brief
	// delivered as SessionStart additionalContext (see brief.go). Hooks from all settings
	// sources are merged and deduped by command string, so this cannot clobber a user's own
	// SessionStart hooks. It carries no state path — it mutates nothing.
	if brief.ok() {
		s.Hooks["SessionStart"] = []hookMatcherGroup{{
			Matcher: sessionStartMatcher,
			Hooks:   []hookCommand{{Type: "command", Command: hookSessionStartCommand(binPath, brief)}},
		}}
	}
	return json.MarshalIndent(s, "", "  ")
}

// ensureHookSettings writes the per-session settings.json for a claude session and returns
// its path, clearing any stale state file from a prior incarnation of the same name. It
// returns ("", nil) when injection should be skipped (non-claude program, or no --settings
// support); it returns ("", err) only on a real IO failure, which the caller logs and treats
// as "skip injection" so the launch still proceeds.
//
// brief carries the per-session facts the SessionStart context brief is rendered from (#485).
// The caller reads it from the Session's provider immediately before calling this, so the facts
// baked into the file are the ones live at THIS launch — session create, pause→resume and
// recover-in-place all route through start(), and a rename between two of them must not leave
// the second describing the first.
func ensureHookSettings(sanitizedName, program string, brief SessionBrief) (string, error) {
	if !agent.Resolve(program).HookSupport || !claudeSupportsSettingsFlag() {
		return "", nil
	}
	// The hooks re-invoke this very binary. If its path can't be resolved, skip injection
	// (degrade to marker-only) rather than fail the launch — the same fail-open stance the
	// --settings capability probe takes.
	binPath, err := resolvedBinPath, resolvedBinPathErr
	if err != nil {
		log.InfoLog.Printf("status hooks disabled: cannot resolve atrium executable: %v", err)
		return "", nil
	}
	dir, err := hookSessionDir(sanitizedName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stateFile := filepath.Join(dir, "state")
	// A reused name must not read a prior incarnation's value before the first hook fires.
	_ = os.Remove(stateFile)
	data, err := buildHookSettings(binPath, stateFile, brief)
	if err != nil {
		return "", err
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		return "", err
	}
	return settingsPath, nil
}

// freezeHookName records the name this launch's hook artifacts are keyed by, and returns it.
// Called from start() immediately before ensureHookSettings, so the name the reader will use
// and the name the writer's baked --state-file is derived from are the same read of
// sanitizedName — a concurrent deep Rename can land either side of it, never between.
//
// It also sweeps the previous launch's directory when the session was renamed in between.
// start() is the only caller and it runs solely when no live session exists (its has-session
// entry guard), so that agent is provably dead and its directory is referenced by nothing.
// Sweeping HERE rather than in Close is what bounds the leak to zero at all times: Close knows
// at most two names, while rename→relaunch repeated N times strands N directories, so a
// "Close sweeps both" rule would still leak on the third rename and every one after it.
func (t *Session) freezeHookName() string {
	t.mu.Lock()
	stale := t.hookSessionName
	name := t.sanitizedName
	t.hookSessionName = name
	t.mu.Unlock()

	if stale != "" && stale != name {
		cleanupHookSession(stale)
	}
	return name
}

// hookName is the session name this session's hook artifacts are keyed by — the name frozen
// at the last launch (see the hookSessionName field), falling back to the current session
// name for a Session that has neither launched nor been rehydrated: one freshly constructed
// in this process and not yet through start(). Rehydration pins the name even when the
// persisted value is empty (see SetHookSessionName), so the fallback is not what carries a
// restored session. Never empty, which is what keeps hookSessionDir's empty-name guard
// unreachable from here.
func (t *Session) hookName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.hookSessionName != "" {
		return t.hookSessionName
	}
	return t.sanitizedName
}

// HookSessionName returns the frozen hook name verbatim — empty only for a Session that has
// neither launched in this process nor been rehydrated from persisted state. It is the value
// Instance persists (InstanceData.HookName); the empty case is meaningful there (an instance
// with no hook directory to name yet), so this deliberately does NOT apply hookName's fallback.
func (t *Session) HookSessionName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hookSessionName
}

// SetHookSessionName restores the frozen hook name when rehydrating a Session from persisted
// state. It must run before the instance is published to the poll loop. Restoring it is what
// makes the #492 fix survive a TUI restart: the rebuilt Session carries the POST-rename tmux
// name, while the agent that outlived the restart is still writing to the PRE-rename
// directory, and reattach (Restore) never re-runs the bake that would re-freeze it.
//
// An empty value — a state.json predating the field — PINS the name the session carries right
// now rather than leaving hookName's lazy fallback in place. The fallback resolves through
// sanitizedName, which Rename mutates, so a session that was already running when the user
// upgraded would lose its channel to the first deep rename: #492 again, in the one window a
// persisted name cannot cover, because that agent was launched before anything recorded one.
// Pinning is inert for a session with no live agent — every relaunch routes through
// freezeHookName, which re-keys and sweeps — and Close wants it either way, since a paused
// session's artifacts on disk are under the pre-rename name.
func (t *Session) SetHookSessionName(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if name == "" {
		name = t.sanitizedName
	}
	t.hookSessionName = name
}

// HookStateFile returns the absolute path of this session's structured hook state file
// (the working/ready latch plus the in-flight sub-agent set), or an error if the config
// dir can't be resolved. Shared by the record reader and the watchdog's ClearInflight;
// exported so it also names the canonical status-record location for diagnostics.
//
// It keys off hookName, not the current session name: the running agent writes to the
// absolute path baked into its launch settings, so the reader has to follow the writer (#492).
func (t *Session) HookStateFile() (string, error) {
	dir, err := hookSessionDir(t.hookName())
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state"), nil
}

// readHookRecord returns this session's parsed hook state record and true, or (zero,
// false) when there is no usable signal: an agent without hook support, or the file is
// absent/unreadable/unparseable (hooks not yet fired, hooks unsupported/disabled). The
// read is lock-free — UpdateHookState's atomic rename guarantees a whole record — so it
// stays cheap on the 500ms poll path. Callers fall back to the scrape classifier on false.
func (t *Session) readHookRecord() (hookRecord, bool) {
	if !t.adapter.HookSupport {
		return hookRecord{}, false
	}
	path, err := t.HookStateFile()
	if err != nil {
		return hookRecord{}, false
	}
	return readHookRecordFile(path)
}

// ClearInflight empties this session's in-flight sub-agent set via the same locked update
// path the hooks use. The watchdog calls it when a session has sat "pending" past its cap
// (a SubagentStop that never fired left the set stuck non-empty): clearing the set is the
// deterministic latch-clear that lets the poller commit the session to idle without
// re-entering pending on the next tick (#46 anti-oscillation). A no-op for non-hook agents.
func (t *Session) ClearInflight() error {
	if !t.adapter.HookSupport {
		return nil
	}
	path, err := t.HookStateFile()
	if err != nil {
		return err
	}
	return UpdateHookState(path, HookEventResetInflight, HookPayload{}, "")
}

// cleanupHookSession removes a session's hook artifacts. Called from Close on kill; missing
// dirs and errors are non-fatal.
func cleanupHookSession(sanitizedName string) {
	dir, err := hookSessionDir(sanitizedName)
	if err != nil {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		log.ErrorLog.Printf("failed to remove hook dir %s: %v", dir, err)
	}
}

// cleanupAllHookSessions removes the entire hooks tree. Called by CleanupSessions (the
// `reset` command), which wipes all Atrium sessions and storage.
func cleanupAllHookSessions() {
	root, err := hooksRoot()
	if err != nil {
		return
	}
	if err := os.RemoveAll(root); err != nil {
		log.ErrorLog.Printf("failed to remove hooks root %s: %v", root, err)
	}
}
