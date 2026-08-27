package tmux

import (
	_ "embed"
	"encoding/json"
	"errors"
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

// claudeSupportsPluginDir reports whether the claude the session will run accepts
// --plugin-dir, probed once per process through the shared help-probe cache.
//
// probeTarget picks what is probed, for the reason it documents: a claude at an absolute
// path outside PATH answers `--help` under its own name and not under `claude`, so probing
// the bare name would report "no such flag" for the very binary the session runs — and a
// wrapper, whose side effects must not run on a probe, falls back to the canonical name.
// binHelpContains cannot tell an exec failure from a --help that lacks the needle (it
// caches the empty output either way), which is why the decision of WHAT to probe is the
// only defence against that misreading.
func claudeSupportsPluginDir(program string) bool {
	return binHelpContains(probeTarget(program, agent.KeyClaude), pluginDirFlag)
}

// AgentPluginDecision is what a launch of one program right now would do about the skill:
// each gate's answer, and the directory or the IO failure the last of them produced.
//
// It exists so that `atrium doctor` can report the decision instead of predicting it. The
// gates are fail-open and silent by design, so the state worth reporting is exactly the
// state nothing else surfaces — and a reporter that re-evaluated the same conditions on its
// own would be a second copy of them, free to disagree with the launch it describes.
type AgentPluginDecision struct {
	// Program is the configured program this decision is about, as configured.
	Program string
	// Enabled is config.AgentSkills as this process holds it (see SetAgentSkills), and
	// the first gate. Process-wide, so it is the same in every decision this process
	// makes — which is what lets a report state it once.
	Enabled bool
	// Claude is the second gate: whether Program resolves to the claude adapter. Every
	// field below a gate that refused is at its zero value, because the ladder
	// short-circuits — nothing beyond it was probed and nothing was written.
	Claude bool
	// FlagSupported is whether the claude this program runs advertises --plugin-dir.
	FlagSupported bool
	// Dir is what --plugin-dir would name: the materialized plugin root, or "" when a
	// gate refused or the write failed.
	Dir string
	// Err is the IO failure that cost the materialize, nil when there was none. The
	// launch logs it and starts the session without the skill.
	Err error
}

// Injecting reports whether a launch of this program right now would hand over the skill.
func (d AgentPluginDecision) Injecting() bool { return d.Dir != "" && d.Err == nil }

// decideAgentPlugin is the gate ladder, and the only copy of it. Both callers below are
// projections: the launch wants the directory, the report wants to know which gate refused.
//
// The two side-effecting gates come last, and that is what the order is for: the setting
// and isClaude are pure reads, so an Atrium with the feature off and a codex install both
// exec no --help and create no directory. The setting is read before isClaude only so that
// Enabled is meaningful in every decision — a report that has to say "the feature is off"
// cannot learn it from a ladder that short-circuited one rung earlier.
func decideAgentPlugin(program string) AgentPluginDecision {
	d := AgentPluginDecision{Program: program, Enabled: !agentSkillsDisabled.Load()}
	if !d.Enabled {
		return d
	}
	if d.Claude = isClaude(program); !d.Claude {
		return d
	}
	if d.FlagSupported = claudeSupportsPluginDir(program); !d.FlagSupported {
		return d
	}
	if d.Dir, d.Err = materializeAgentPlugin(); d.Err != nil {
		// Dir means "what --plugin-dir would name", and on a failed write that is
		// nothing. The path the reader needs is in the error, which names the file that
		// actually refused rather than the root it sits under.
		d.Dir = ""
	}
	return d
}

// ensureAgentPlugin materializes the plugin and returns the directory to pass to
// --plugin-dir, or "" when injection should be skipped: a non-claude program, the setting
// off, or a claude with no --plugin-dir flag. It returns ("", err) only on a real IO
// failure, which the caller logs and treats as "skip injection" so the launch still
// proceeds — ensureHookSettings' stance, for ensureHookSettings' reason. Nothing about
// this feature is worth a session that will not start.
func ensureAgentPlugin(program string) (string, error) {
	d := decideAgentPlugin(program)
	return d.Dir, d.Err
}

// AgentPluginStatus reports what a launch of program would do about the skill, for `atrium
// doctor`. It IS the launch path — the same ladder, the same write — so the report and the
// launch cannot disagree.
//
// That makes it a writer, which a reporter would rather not be, and the alternative is
// worse. A temp-file probe standing in for the write is wrong in both directions at once.
// Too strict: writeFileIfChanged returns before writing when the bytes already match, which
// is the steady state, so a current-but-unwritable tree launches fine and a probe demanding
// create-and-unlink calls it broken. Too weak: an empty probe file exercises neither the
// payload write, the chmod, nor the rename, so a full disk or an unreplaceable target passes
// the probe and loses the skill at every launch.
//
// The side effect is bounded by the gates above it — nothing is written for a non-claude
// program, for the setting off, or for a claude without the flag — and where it does write,
// it writes the bytes the next launch would write anyway, over identical bytes in the steady
// state. A doctor run beside a live session therefore rewrites nothing it is reading.
func AgentPluginStatus(program string) AgentPluginDecision { return decideAgentPlugin(program) }

// materializeAgentPlugin writes the plugin and returns its root.
func materializeAgentPlugin() (string, error) {
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
	//
	// Nothing here deletes, and that is the load-bearing half. A skill this Atrium no
	// longer ships stays on disk and stays loaded — the accepted cost, because the only
	// thing that makes rewriting in place safe is that it never removes a file a live
	// session is pointed at. freezeHookName may sweep because its directory is
	// per-session and its agent is provably dead; this tree is fleet-shared and its
	// readers are alive, and two binaries over one data dir would delete each other's
	// skill on alternating launches. Retiring a skill is a migration, not a launch-time
	// sweep.
	if err := writeFileIfChanged(filepath.Join(root, ".claude-plugin", "plugin.json"), manifest); err != nil {
		return "", err
	}
	skills := filepath.Join(root, "skills")
	for name, doc := range shippedSkills() {
		if err := writeFileIfChanged(filepath.Join(skills, name, "SKILL.md"), doc); err != nil {
			return "", err
		}
	}
	return root, nil
}

// shippedSkills is every skill this Atrium writes, keyed by the directory it lands in. One
// entry today; a map rather than the single write it currently expands to so that adding a
// second skill is an entry here and nothing else.
func shippedSkills() map[string][]byte {
	return map[string][]byte{spawnSkillDir: []byte(spawnSkillDoc)}
}

// writeFileIfChanged writes data to path unless it is already exactly there, creating
// parents as needed.
//
// The comparison is not an optimization: without it every launch rewrites files a live
// session may be reading, for no change at all. It is also what makes this cheap enough
// for `atrium doctor` to run the real launch path — in the steady state there is no write.
//
// config.WriteFileAtomic does the write, rather than a second hand-rolled copy of the same
// temp-file-then-rename dance: rename within a directory is atomic and a write in place is
// not, and that primitive already adds the fsync that makes the committed bytes durable.
func writeFileIfChanged(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return writeErr(path, err)
	}
	return writeErr(path, config.WriteFileAtomic(path, data, 0o644))
}

// writeErr states a failed write as a fact about the file Atrium meant to write, nil for
// nil. The atomic writer's error names its own temp file — a path the reader has never seen
// and cannot act on — and `atrium doctor` prints this verbatim as the one line a write
// failure gives someone to act on.
//
// Both syscall error shapes have to be unwrapped, which is not obvious and is why the
// guard for this asserts on the message: create, open and chmod fail with *os.PathError,
// and the rename fails with *os.LinkError, whose two paths are BOTH temp-file noise once
// the target is stated. Handling only the first leaves the rename — the failure a reader is
// most likely to hit, since it is the one an unreplaceable target produces — reported as
// the full three-path original.
func writeErr(path string, err error) error {
	if err == nil {
		return nil
	}
	var perr *os.PathError
	if errors.As(err, &perr) {
		return fmt.Errorf("%s: %w", path, perr.Err)
	}
	var lerr *os.LinkError
	if errors.As(err, &lerr) {
		return fmt.Errorf("%s: %w", path, lerr.Err)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// PluginDirFlag is the flag Atrium appends, for a report that has to name it. Exported from
// the same constant the probe's needle is, so a report cannot name a flag the gate does not
// look for.
const PluginDirFlag = pluginDirFlag

// SpawnSkillInvocation is how the shipped skill is typed, for the doctor check and any
// other reader that has to name it. A projection of the plugin and skill names above
// rather than a literal, so a rename cannot leave a stale spelling behind.
func SpawnSkillInvocation() string {
	return fmt.Sprintf("/%s:%s", agentPluginName, spawnSkillDir)
}

// SpawnSkillDoc is the shipped skill's text, for a guard that has to hold its prose to
// something this package cannot import. The skill's worked commands are `atrium new` lines
// an agent copies verbatim, so the flags and subcommands it names have to be held to the
// ones the CLI registers — and the CLI lives in package main, which imports this one.
func SpawnSkillDoc() string { return spawnSkillDoc }
