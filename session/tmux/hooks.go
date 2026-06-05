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
// UserPromptSubmit/PreToolUse hooks latch "working" and whose Stop hook — which fires once
// at true end-of-turn, never between auto-accepted tool steps — latches "ready". The agent
// writes a one-word state file that Poll reads as the primary signal, falling back to the
// scrape classifier when the file is absent (non-claude agents, or before the first event).
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

// hookWriteCmd builds the shell one-liner a hook runs: atomically write word to stateFile.
// The absolute path is baked in, so the hook needs no jq, no stdin parsing, and no env.
//
// The temp file is suffixed with "$$" (the hook shell's PID), so it is unique per invocation.
// Agent teams fire many hooks concurrently — the parent session's injected --settings applies
// to every subagent tool call — and a single shared temp made them race: the first `mv` won
// and the rest failed with "cannot stat …/state.tmp", losing state writes (including the
// final "ready") and stranding the file on "working". A per-process temp removes the
// collision; the `mv` stays atomic and last-writer-wins, which is correct because racing
// writers all write the same word.
func hookWriteCmd(stateFile, word string) string {
	// Single-quote the static prefix, then append a double-quoted "$$" so the shell expands
	// it to the PID (single quotes would keep it literal). Result, e.g.:
	//   printf 'ready' > '/dir/state.tmp.'"$$" && mv -f '/dir/state.tmp.'"$$" '/dir/state'
	tmp := shellSingleQuote(stateFile+".tmp.") + `"$$"`
	return "printf " + shellSingleQuote(word) +
		" > " + tmp +
		" && mv -f " + tmp + " " + shellSingleQuote(stateFile)
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookMatcherGroup struct {
	// Matcher is omitted for events that don't support it (UserPromptSubmit, Stop); for
	// PreToolUse it is "*" to match all tools.
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookSettings struct {
	Hooks map[string][]hookMatcherGroup `json:"hooks"`
}

// buildHookSettings marshals the settings.json content wiring the three status hooks.
func buildHookSettings(stateFile string) ([]byte, error) {
	work := hookCommand{Type: "command", Command: hookWriteCmd(stateFile, hookStateWorking)}
	ready := hookCommand{Type: "command", Command: hookWriteCmd(stateFile, hookStateReady)}
	s := hookSettings{Hooks: map[string][]hookMatcherGroup{
		"UserPromptSubmit": {{Hooks: []hookCommand{work}}},
		"PreToolUse":       {{Matcher: "*", Hooks: []hookCommand{work}}},
		// Stop fires at a clean end-of-turn; StopFailure fires when the turn ends on an API
		// error. Both mean the agent has stopped, so both latch "ready" — without StopFailure
		// an errored turn would leave the file on "working" until the poller's time cap.
		"Stop":        {{Hooks: []hookCommand{ready}}},
		"StopFailure": {{Hooks: []hookCommand{ready}}},
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
	data, err := buildHookSettings(stateFile)
	if err != nil {
		return "", err
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		return "", err
	}
	return settingsPath, nil
}

// readHookState returns the latched hook state word and true, or ("", false) when there is
// no usable signal: an agent without hook support, or the file is absent/unreadable (hooks
// not yet fired, hooks unsupported/disabled). Callers fall back to the scrape classifier
// on false.
func (t *Session) readHookState() (string, bool) {
	if !t.adapter.HookSupport {
		return "", false
	}
	dir, err := hookSessionDir(t.snapshotName())
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, "state"))
	if err != nil {
		return "", false
	}
	switch strings.TrimSpace(string(b)) {
	case hookStateWorking:
		return hookStateWorking, true
	case hookStateReady:
		return hookStateReady, true
	}
	return "", false
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
