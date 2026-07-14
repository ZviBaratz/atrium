package tmux

import (
	"context"
	"encoding/json"
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
// Scraping the pane for the "esc to interrupt" marker cannot distinguish a "between turns"
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
func hookSessionDir(sanitizedName string) (string, error) {
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
// capability gate before applying a version-sensitive flag (e.g. gemini --resume). Like
// claudeSupportsSettingsFlag, it probes the literal canonical binary — never the
// configured program, whose wrapper side effects must not run on a probe — and caches the
// output per process so resurrecting many sessions costs one subprocess per binary. A
// failed probe caches as empty output: the capability reads as absent and the caller
// degrades (relaunch without resume) rather than failing the launch.
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
// event is a fixed literal (no quoting needed). The sub-agent events additionally read
// their agent_id from the hook's stdin payload, which Claude pipes in — the command line
// is identical, only the subcommand's stdin handling differs.
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
// is the atrium binary each hook re-invokes (see hookEventCommand).
func buildHookSettings(binPath, stateFile string) ([]byte, error) {
	cmd := func(event string) hookCommand {
		return hookCommand{Type: "command", Command: hookEventCommand(binPath, stateFile, event)}
	}
	s := hookSettings{Hooks: map[string][]hookMatcherGroup{
		// UserPromptSubmit latches working exactly like the tool-use edges, but via its own
		// event: its $CLAUDE_EFFORT is a stale pre-resolution value, so it must not record
		// effort (see HookEventPromptSubmit).
		//
		// Splitting it out is safe across an atrium upgrade, but bounded rather than
		// risk-free. ensureHookSettings runs at session Start, so a session already live when
		// the binary is replaced in place keeps a settings.json routing this edge to the old
		// "working" verb — which the new binary does record effort on. That session's chip can
		// show the stale model default mid-turn, until the turn's first PreToolUse (or its
		// Stop) overwrites it with the resolved truth. Self-correcting within the turn, and
		// the chip only settles at turn end anyway; a relaunch clears it for good.
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
	return json.MarshalIndent(s, "", "  ")
}

// ensureHookSettings writes the per-session settings.json for a claude session and returns
// its path, clearing any stale state file from a prior incarnation of the same name. It
// returns ("", nil) when injection should be skipped (non-claude program, or no --settings
// support); it returns ("", err) only on a real IO failure, which the caller logs and treats
// as "skip injection" so the launch still proceeds.
func ensureHookSettings(sanitizedName, program string) (string, error) {
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
	data, err := buildHookSettings(binPath, stateFile)
	if err != nil {
		return "", err
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		return "", err
	}
	return settingsPath, nil
}

// HookStateFile returns the absolute path of this session's structured hook state file
// (the working/ready latch plus the in-flight sub-agent set), or an error if the config
// dir can't be resolved. Shared by the record reader and the watchdog's ClearInflight;
// exported so it also names the canonical status-record location for diagnostics.
func (t *Session) HookStateFile() (string, error) {
	dir, err := hookSessionDir(t.snapshotName())
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
	return UpdateHookState(path, HookEventResetInflight, "", "")
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
