package tmux

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
)

// Atrium's own skills, handed to the agent it launches.
//
// The status hooks in hooks.go and the SessionStart brief in brief.go are the two
// existing channels into a claude session, and neither can carry this. Hooks point
// inward — they read the agent's state. The brief points outward but is re-delivered on
// every session start, every /clear and every compaction, so it is budgeted in runes and
// spends exactly one clause pointing at `atrium guide`. A page an agent pulls once is the
// right shape for reference material, and `atrium guide` already is one. What neither can
// be is INVOCABLE: nothing lets the person at the keyboard type `/spawn` and nothing fires
// when an agent forms the intent to hand off without first thinking to read a CLI page.
//
// A skill is that shape, and `claude --plugin-dir <path>` is how one gets in: it loads a
// plugin for the life of one process, with no install, no marketplace entry and no
// enabledPlugins edit. So this materializes a two-file plugin under the data dir and
// start() appends the flag beside --settings. Nothing is written into the user's repo (a
// worktree's .claude/skills would show up as untracked files an agent could commit) and
// nothing into their claude config directory (a global side effect outliving Atrium).
//
// Verified by probe against claude 2.1.246, in a directory that was not an Atrium
// worktree, before any of this was written: the plugin loads with no consent dialog of its
// own, the skill is listed, and invoking it runs the body. The dialog question is the one
// that had to be answered live rather than reasoned about — a startup dialog blocks a
// session's boot, which is why bypassPermissions is kept out of ClaudePermissionModes —
// and the answer is that only the ordinary WORKSPACE trust dialog appears, for the
// directory, exactly as it does without the flag.
//
// The failure this cannot gate on is policy. `disableSideloadFlags` in an organization's
// managed settings makes claude reject --plugin-dir at startup, and it is resolved from a
// tier Atrium cannot read a single file to learn: server-managed settings, an MDM plist, a
// Windows registry key, or managed-settings.json, whichever the org uses. Re-implementing
// that resolution here would be a second, staler copy of a fact claude owns, and wrong
// first on the two platforms hardest to test. So it is not predicted: the refusal is
// claude's own, it names disableSideloadFlags on the pane, and config.AgentSkills turns the
// injection off. `atrium doctor` carries the symptom→cause→fix so the setting is findable
// from the failure.

// spawnSkillDoc is the shipped skill. Embedded rather than generated so the prose is
// reviewed as prose — and so TestNoProseCitesAPosition scans it like any other markdown
// in the tree, which is why it cites symbols and keeps its commands in fenced blocks.
//
//go:embed spawn_skill.md
var spawnSkillDoc string

const (
	// agentPluginName is the plugin's name, and therefore half of what the skill is
	// invoked as: claude namespaces a plugin's skills, so the shipped `spawn` skill is
	// reached as `/atrium:spawn` (typing `/spawn` still matches it).
	agentPluginName = "atrium"
	// spawnSkillDir is the skill's directory name under the plugin's skills/ tree. It
	// must equal the `name` in the skill's own frontmatter; TestSpawnSkillFrontmatter
	// holds the two together.
	spawnSkillDir = "spawn"
	// pluginDirFlag is the claude flag that loads a plugin for one session. Also the
	// needle the capability probe looks for in --help, so the two cannot drift.
	pluginDirFlag = "--plugin-dir"
	// agentPluginVersion is static on purpose. It is a required manifest field that
	// nothing here reads back: the plugin is loaded by path, never resolved by version,
	// so there is no upgrade to advertise and no comparison to lose. Atrium's own version
	// would be the tempting value and is the wrong one — it would rewrite the manifest on
	// every release whether or not the skill changed.
	agentPluginVersion = "1.0.0"
)

// agentSkillsDisabled is the process-wide off switch, config.AgentSkills as seen by every
// launch. Inverted — "disabled" rather than "enabled" — so that the zero value is the
// feature's DEFAULT rather than its opposite: a process that never calls SetAgentSkills
// (a test, a code path added later that forgets the wiring) injects the skill, which is
// what an unconfigured Atrium is supposed to do. agentOOMMargin can take the other
// convention because its default really is off.
//
// Atomic and read afresh by each start() for that var's reason too: the Settings panel may
// flip it on the UI thread while a session launches on a background goroutine, and a
// session already running keeps whatever it launched with until it is relaunched.
var agentSkillsDisabled atomic.Bool

// SetAgentSkills sets whether Atrium injects its own skills into claude sessions. Call at
// startup from config.GetAgentSkills and again whenever the setting changes.
func SetAgentSkills(enabled bool) { agentSkillsDisabled.Store(!enabled) }

// pluginManifest is claude's plugin.json. Built from a struct and marshalled rather than
// assembled as a string for buildHookSettings' reason: the field names are the whole
// contract, and a plugin claude cannot parse is not an error anywhere — the skill simply
// never appears.
type pluginManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// agentPluginRoot is <configDir>/plugin, the Atrium-owned tree holding the plugin. A
// sibling of hooks/, and outside the git worktree for the same reason: it survives a pause
// (which removes the worktree) and can never turn up in an agent's git status or diff.
func agentPluginRoot() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugin"), nil
}

// claudeSupportsPluginDir reports whether the claude binary accepts --plugin-dir, probed
// once per process through the shared help-probe cache. Like every capability gate here it
// probes the literal `claude` binary rather than the configured program, whose wrapper
// side effects must not run on a probe.
func claudeSupportsPluginDir() bool {
	return binHelpContains(string(agent.KeyClaude), pluginDirFlag)
}

// ensureAgentPlugin materializes the plugin and returns the directory to pass to
// --plugin-dir, or "" when injection should be skipped: a non-claude program, the setting
// off, or a claude with no --plugin-dir flag. It returns ("", err) only on a real IO
// failure, which the caller logs and treats as "skip injection" so the launch still
// proceeds — ensureHookSettings' stance, for ensureHookSettings' reason. Nothing about
// this feature is worth a session that will not start.
func ensureAgentPlugin(program string) (string, error) {
	if !isClaude(program) || agentSkillsDisabled.Load() || !claudeSupportsPluginDir() {
		return "", nil
	}
	manifest, err := json.MarshalIndent(pluginManifest{
		Name:        agentPluginName,
		Description: "Atrium session orchestration skills",
		Version:     agentPluginVersion,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	root, err := agentPluginRoot()
	if err != nil {
		return "", err
	}
	// One stable directory, rewritten in place, rather than a content-addressed one per
	// version. A versioned directory would have to be swept, and the thing to sweep is
	// the copy a session launched BEFORE the upgrade may still read — claude is free to
	// re-read a plugin it was handed. Rewriting in place has no such window: each file is
	// replaced by an atomic rename, so a concurrent reader sees the old bytes or the new
	// ones, never a half-written manifest, and two sessions racing to materialize write
	// identical content.
	for _, f := range []struct {
		path string
		data []byte
	}{
		{filepath.Join(root, ".claude-plugin", "plugin.json"), manifest},
		{filepath.Join(root, "skills", spawnSkillDir, "SKILL.md"), []byte(spawnSkillDoc)},
	} {
		if err := writeFileIfChanged(f.path, f.data); err != nil {
			return "", err
		}
	}
	return root, nil
}

// writeFileIfChanged writes data to path unless it is already exactly there, creating
// parents as needed.
//
// The comparison is not an optimization: without it every launch rewrites files a live
// session may be reading, for no change at all. The write itself goes to a temp file in
// the SAME directory and is renamed over the target, which is what makes the update atomic
// — rename within a directory is, a write in place is not.
func writeFileIfChanged(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	// Best-effort cleanup: after a successful rename there is nothing at this name, and
	// the error is the one case where the temp file must not be left behind.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SpawnSkillInvocation is how the shipped skill is typed, for the doctor check and any
// other reader that has to name it. A projection of the plugin and skill names above
// rather than a literal, so a rename cannot leave a stale spelling behind.
func SpawnSkillInvocation() string {
	return fmt.Sprintf("/%s:%s", agentPluginName, spawnSkillDir)
}

// ClaudeSupportsPluginDir is the exported form of the capability probe, for `atrium
// doctor`. Exported rather than duplicated so the report and the launch gate can only ever
// agree.
func ClaudeSupportsPluginDir() bool { return claudeSupportsPluginDir() }

// AgentPluginDir is the directory sessions are pointed at, resolved without creating
// anything — the read-only half of ensureAgentPlugin, for a reporter that must not have
// side effects.
func AgentPluginDir() (string, error) { return agentPluginRoot() }
