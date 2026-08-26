package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeHelpWithPluginDir is a --help output that advertises the flag, so the capability
// probe passes without exec'ing anything. Keyed by the canonical binary name, plus any
// other path the probe may be pointed at: probeTarget prefers the program's own first token
// when its basename is claude, so a program given as an absolute path is probed under that
// path and not under `claude`.
func claudeHelpWithPluginDir(alsoAt ...string) map[string]string {
	help := "  " + pluginDirFlag + " <path>   Load a plugin"
	out := map[string]string{string(agent.KeyClaude): help}
	for _, bin := range alsoAt {
		out[bin] = help
	}
	return out
}

// enableAgentSkills forces the switch on for a test and restores it after. The package var
// defaults to "on" via its inverted zero value, but a sibling test may have flipped it.
func enableAgentSkills(t *testing.T, enabled bool) {
	t.Helper()
	prev := agentSkillsDisabled.Load()
	agentSkillsDisabled.Store(!enabled)
	t.Cleanup(func() { agentSkillsDisabled.Store(prev) })
}

func TestSpawnSkillInvocationNamesThePluginAndSkill(t *testing.T) {
	// The invocation is what a user types and what doctor prints. Pinned as a literal
	// because it is a projection of two consts: if either is renamed this fails, which is
	// the moment to decide whether the rename is worth breaking the spelling users know.
	assert.Equal(t, "/atrium:spawn", SpawnSkillInvocation())
}

func TestSpawnSkillFrontmatterMatchesItsDirectory(t *testing.T) {
	require.True(t, strings.HasPrefix(spawnSkillDoc, "---\n"),
		"the skill must open with YAML frontmatter or claude will not register it")
	_, rest, ok := strings.Cut(strings.TrimPrefix(spawnSkillDoc, "---\n"), "\n---\n")
	require.True(t, ok, "the frontmatter block is unterminated")
	front := strings.TrimSuffix(spawnSkillDoc, rest)

	name := regexp.MustCompile(`(?m)^name:\s*(\S+)`).FindStringSubmatch(front)
	require.Len(t, name, 2, "the frontmatter states no name")
	// claude resolves a skill by its directory; the frontmatter name is what it is
	// listed and invoked under. They are two homes for one fact, and only this holds them
	// together — a mismatch ships a skill nobody can find by the name they were told.
	assert.Equal(t, spawnSkillDir, name[1])

	desc := regexp.MustCompile(`(?m)^description:\s*(.+)`).FindStringSubmatch(front)
	require.Len(t, desc, 2, "the frontmatter states no description")
	assert.Greater(t, len(desc[1]), 40,
		"the description is the whole of what makes a skill fire on intent; a terse one "+
			"means the skill is only ever reached by being typed")
}

// TestSpawnSkillNamesOnlyRealClaudeValues is the drift guard the skill needs and nothing
// else provides: the shipped prose is a claim about claude's flags, and the agent package
// is the authority on them. A value that is not in these vocabularies is either a typo or
// a flag claude rejects at argv parse time — which kills the session it was recommended
// for, at launch, for a reason no Go test would otherwise see.
// TestSpawnSkillNamesOnlyRealClaudeValues holds each of the skill's three tables to the
// vocabulary of the flag that table is about.
//
// Per table, not against the union of all three, and that is the whole point. A flat set
// keyed by value accepted a cell drawn from a sibling flag's vocabulary: `auto` is a
// permission mode, so adding "| `auto` | when the task should pick its own model |" to the
// MODEL table passed — and every claude session would then be handed a worked command
// recommending `--model auto`, which claude rejects at argv parse, killing the session at
// launch. That is the exact failure this test exists to prevent, so the union had to go.
//
// The model table is here rather than in prose so that it is covered at all. ValidModelName
// — the check the worked commands go through — is a charset rule for embedding a value in a
// shell command and accepts "opusss"; ClaudeModelAliases is the vocabulary. model.go is
// explicit that the alias list is "never a validation allowlist", i.e. a full model name
// outside it still reaches the CLI and works. So this asserts what the skill RECOMMENDS,
// not what claude would accept: the skill recommends aliases, and a typo in one is what
// this catches.
func TestSpawnSkillNamesOnlyRealClaudeValues(t *testing.T) {
	// Keyed by the axis heading each table sits under, which is what binds a cell to its
	// own flag.
	vocab := map[string][]string{
		"Permission mode": agent.ClaudePermissionModes,
		"Effort":          agent.ClaudeEffortLevels,
		"Model":           agent.ClaudeModelAliases,
	}
	axis := regexp.MustCompile(`^\*\*(Permission mode|Effort|Model) `)
	// The first cell of every table row, which is where the three ladders state their
	// values. A row may name more than one (the mechanical effort rung names two).
	rowCell := regexp.MustCompile(`^\| ([^|]+) \|`)
	backticked := regexp.MustCompile("`([^`]+)`")

	current := ""
	seen := map[string]bool{}
	for _, line := range strings.Split(spawnSkillDoc, "\n") {
		if m := axis.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		row := rowCell.FindStringSubmatch(line)
		if row == nil {
			continue
		}
		for _, tok := range backticked.FindAllStringSubmatch(row[1], -1) {
			value := tok[1]
			// A table outside every axis heading is not a table this guard can check, and
			// silently skipping it is how the guard stops guarding. If a new table's first
			// column is not a flag value, give it a heading this ignores rather than
			// loosening the match.
			require.NotEmptyf(t, current, "the skill's table names %q before any of the "+
				"three axis headings, so nothing here checks it", value)
			assert.Containsf(t, vocab[current], value,
				"the skill's %s table names %q, which is not one of claude's %s values "+
					"(%v). A value from another flag's vocabulary is the failure this "+
					"guard is keyed per-table to catch: the session dies at argv parse.",
				current, value, current, vocab[current])
			seen[current+":"+value] = true
		}
	}

	// Without this the loop above passes by matching nothing at all — the headings are
	// prose and prose gets reworded.
	for axisName := range vocab {
		found := false
		for key := range seen {
			if strings.HasPrefix(key, axisName+":") {
				found = true
				break
			}
		}
		require.Truef(t, found, "no table row was attributed to the %q axis: its heading "+
			"was reworded and that table is now unchecked", axisName)
	}

	// Every effort level must appear. The table is a ladder over the whole range, so a
	// level added to the CLI leaves it silently incomplete — recommending "max" for the
	// hardest work when something above it now exists.
	for _, e := range agent.ClaudeEffortLevels {
		assert.Truef(t, seen["Effort:"+e],
			"effort level %q is missing from the skill's ladder", e)
	}
	// Modes and models are deliberately NOT covered exhaustively: the offered chips
	// include a mode the skill does not recommend, and two aliases it has no advice
	// about. Only validity is asserted above for those, plus the presence of the ones it
	// does recommend — each of which must survive a rename of its vocabulary.
	assert.True(t, seen["Permission mode:plan"] && seen["Permission mode:acceptEdits"],
		"the skill's mode table must name both modes it recommends")
	assert.True(t, seen["Model:opus"] && seen["Model:sonnet"],
		"the skill's model table must name both models it recommends")
}

// TestSpawnSkillExamplesUseValidFlagValues covers the other half: the worked commands.
// A table can be right while the example beneath it pins a model alias that does not
// exist, and the example is the part a reader copies.
//
// Each arm holds its value to the closed vocabulary for that flag, never to a
// well-formedness check. ValidModelName is the trap: it is what the launch path calls, so
// reaching for it here looks right, and it is a charset rule that accepts "opusss" —
// leaving the arm that guards the one flag whose values are not an enum unable to fail.
func TestSpawnSkillExamplesUseValidFlagValues(t *testing.T) {
	pins := regexp.MustCompile(`--(model|effort|permission-mode) ([A-Za-z0-9._:/-]+)`)
	matches := pins.FindAllStringSubmatch(spawnSkillDoc, -1)
	require.NotEmpty(t, matches, "the skill shows no worked command; the examples are the "+
		"part a reader copies, so their absence is a regression too")
	for _, m := range matches {
		switch m[1] {
		case "model":
			assert.Containsf(t, agent.ClaudeModelAliases, m[2],
				"--model %q is not an alias claude documents; a full model name would "+
					"pass ValidModelName, but the skill recommends aliases and a typo in "+
					"one is what this arm exists to catch", m[2])
		case "effort":
			assert.Containsf(t, agent.ClaudeEffortLevels, m[2],
				"--effort %q is not a level claude accepts", m[2])
		case "permission-mode":
			assert.Truef(t, agent.ValidPermissionMode(m[2]),
				"--permission-mode %q is outside claude's closed enum, so a session "+
					"launched with it dies at argv parse time", m[2])
		}
	}
}

func TestEnsureAgentPluginMaterializesALoadablePlugin(t *testing.T) {
	forceHelpProbe(t, claudeHelpWithPluginDir())
	enableAgentSkills(t, true)

	dir, err := ensureAgentPlugin("claude")
	require.NoError(t, err)
	require.NotEmpty(t, dir)

	// The two paths are claude's contract, not ours: the manifest is found at
	// .claude-plugin/plugin.json and a skill at skills/<name>/SKILL.md. Asserted as
	// literal paths because a plugin laid out any other way loads as nothing at all,
	// with no error anywhere.
	raw, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	require.NoError(t, err, "the manifest is missing, so claude would load no plugin")
	var manifest pluginManifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	assert.Equal(t, agentPluginName, manifest.Name)
	assert.NotEmpty(t, manifest.Version, "the manifest requires a version")

	skill, err := os.ReadFile(filepath.Join(dir, "skills", spawnSkillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, spawnSkillDoc, string(skill))
}

func TestEnsureAgentPluginIsIdempotent(t *testing.T) {
	forceHelpProbe(t, claudeHelpWithPluginDir())
	enableAgentSkills(t, true)

	dir, err := ensureAgentPlugin("claude")
	require.NoError(t, err)
	path := filepath.Join(dir, "skills", spawnSkillDir, "SKILL.md")
	before, err := os.Stat(path)
	require.NoError(t, err)

	again, err := ensureAgentPlugin("claude")
	require.NoError(t, err)
	assert.Equal(t, dir, again)
	after, err := os.Stat(path)
	require.NoError(t, err)
	// Unchanged content must not be rewritten: every launch calls this, and a live
	// session's agent is free to re-read the plugin it was handed.
	assert.Equal(t, before.ModTime(), after.ModTime(),
		"an unchanged skill was rewritten under a session that may be reading it")

	// Nothing is left behind by the write path either.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".tmp-"),
			"a temp file survived: %s", e.Name())
	}
}

func TestEnsureAgentPluginRewritesChangedContent(t *testing.T) {
	forceHelpProbe(t, claudeHelpWithPluginDir())
	enableAgentSkills(t, true)

	dir, err := ensureAgentPlugin("claude")
	require.NoError(t, err)
	path := filepath.Join(dir, "skills", spawnSkillDir, "SKILL.md")
	require.NoError(t, os.WriteFile(path, []byte("a stale skill from an older atrium"), 0o644))

	_, err = ensureAgentPlugin("claude")
	require.NoError(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, spawnSkillDoc, string(got),
		"an upgraded atrium must replace the skill its predecessor wrote")
}

// TestEnsureAgentPluginGates walks the ladder. Each rung returns "" — no directory, no
// flag, launch unaffected — rather than an error, because none of them is a reason to
// fail a session's start.
func TestEnsureAgentPluginGates(t *testing.T) {
	t.Run("a non-claude agent is never handed a plugin", func(t *testing.T) {
		forceHelpProbe(t, claudeHelpWithPluginDir())
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin("codex")
		require.NoError(t, err)
		assert.Empty(t, dir)
	})

	t.Run("the setting off skips injection", func(t *testing.T) {
		forceHelpProbe(t, claudeHelpWithPluginDir())
		enableAgentSkills(t, false)
		dir, err := ensureAgentPlugin("claude")
		require.NoError(t, err)
		assert.Empty(t, dir)
	})

	t.Run("a claude without the flag skips injection", func(t *testing.T) {
		// The rung that matters for an older CLI: passing a flag it does not know kills
		// the launch, so the probe has to hold.
		forceHelpProbe(t, map[string]string{string(agent.KeyClaude): "  --settings <file>"})
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin("claude")
		require.NoError(t, err)
		assert.Empty(t, dir)
	})

	t.Run("a wrapper that resolves to claude is handed one", func(t *testing.T) {
		forceHelpProbe(t, claudeHelpWithPluginDir("/usr/local/bin/claude"))
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin("/usr/local/bin/claude --continue")
		require.NoError(t, err)
		assert.NotEmpty(t, dir, "resolution is wrapper-aware for --settings and must be here too")
	})
}

// TestEnsureAgentPluginProbesTheBinaryTheSessionRuns pins WHICH claude the capability gate
// asks. binHelpContains caches empty output when a probe cannot be exec'd at all, so "this
// binary has no such flag" and "there is no such binary" are one answer here — and a claude
// installed at an absolute path outside PATH is a real configuration, not a corner case. Ask
// the wrong binary and the gate refuses the very session it was meant to qualify, silently,
// for a reason no error anywhere states.
func TestEnsureAgentPluginProbesTheBinaryTheSessionRuns(t *testing.T) {
	const offPath = "/opt/claude/bin/claude"

	t.Run("a claude off PATH is probed under its own path", func(t *testing.T) {
		// Nothing named `claude` is reachable, which is what makes this the failing case
		// for a gate that probes the bare name.
		forceHelpProbe(t, map[string]string{
			offPath: "  " + pluginDirFlag + " <path>   Load a plugin",
		})
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin(offPath)
		require.NoError(t, err)
		assert.NotEmpty(t, dir, "the configured binary advertises the flag; asking a "+
			"different one costs this session the skill")
	})

	t.Run("its own answer is the one that counts", func(t *testing.T) {
		// The other direction, and the half that makes the first assertion mean
		// something: a canonical claude on PATH that supports the flag must not qualify
		// an absolute-path claude that does not.
		forceHelpProbe(t, claudeHelpWithPluginDir())
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin(offPath)
		require.NoError(t, err)
		assert.Empty(t, dir, "the flag was never seen on the binary this session runs")
	})

	t.Run("a wrapper is probed under the canonical name", func(t *testing.T) {
		// probeTarget's carve-out, and the reason the two gates disagree on purpose.
		// isClaude is a basename CONTAINS match, so a launcher script counts as claude
		// and gets the skill; probeTarget requires the basename to be claude exactly,
		// because a wrapper's side effects must not run on a probe. So the wrapper is
		// qualified by asking the canonical binary — accepting, for that case only, that
		// a claude reachable solely as the wrapper's own path is not found.
		forceHelpProbe(t, claudeHelpWithPluginDir())
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin("/usr/local/bin/launch-claude.sh")
		require.NoError(t, err)
		assert.NotEmpty(t, dir, "a wrapper resolves as claude, so it is qualified by the "+
			"canonical binary rather than by running the wrapper")
	})
}

// TestEnsureAgentPluginNeverDeletesUnderTheSharedTree pins the absence of a sweep, which is
// the whole reason rewriting in place is safe.
//
// The tempting feature is the opposite one: a directory whose name is no longer shipped is
// never rewritten, so a retired skill stays on disk and claude loads it beside its
// replacement. Sweeping it costs more than it buys. This tree is ONE directory shared by
// every live session — deliberately not versioned — so a delete here removes a file a running
// agent is pointed at and may re-read, the exact window the atomic-rename write is arranged
// to avoid; two binaries over one data dir would delete each other's skill on alternating
// launches; and anything a user put under skills/ themselves would go with it. freezeHookName
// may sweep because its directory is per-session and its agent is provably dead.
//
// So: a stale skill is a migration's problem, and this asserts the launch path leaves the
// tree alone.
func TestEnsureAgentPluginNeverDeletesUnderTheSharedTree(t *testing.T) {
	forceHelpProbe(t, claudeHelpWithPluginDir())
	enableAgentSkills(t, true)

	dir, err := ensureAgentPlugin("claude")
	require.NoError(t, err)

	// Two things a sweep would take: a predecessor's skill, laid out exactly as this Atrium
	// lays out its own, and something a user added.
	stale := filepath.Join(dir, "skills", "handoff")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "SKILL.md"),
		[]byte("---\nname: handoff\n---\n\nadvice for flags that no longer parse"), 0o644))
	mine := filepath.Join(dir, "skills", "my-own-skill")
	require.NoError(t, os.MkdirAll(mine, 0o755))

	_, err = ensureAgentPlugin("claude")
	require.NoError(t, err)

	assert.DirExists(t, stale, "the launch path deleted inside a tree every live session "+
		"is pointed at; a retired skill is a migration, not a launch-time sweep")
	assert.DirExists(t, mine, "a user's own file under the plugin tree is not Atrium's to remove")
	// And what it does write is still current, which is what makes leaving the rest alone
	// an acceptable trade rather than simply doing nothing.
	assert.FileExists(t, filepath.Join(dir, "skills", spawnSkillDir, "SKILL.md"))
}

// TestAgentPluginStatusIsTheLaunchDecision holds the report to the launch.
//
// `atrium doctor` reports this feature by running the launch path rather than predicting it,
// because a second evaluation of the same gates is free to disagree with it. A write probe
// is the tempting shortcut and disagrees in both directions at once: it calls a tree whose
// bytes are already current "unwritable" when a launch would not write to it at all, and
// calls a full disk fine because an empty temp file never reaches the payload write.
//
// So the property is that one ladder answers both callers. Each case below stops the ladder
// on a different rung and asserts the decision's fields, that everything past the refusing
// gate is left at its zero value — doctor's report reads those fields to name the gate — and
// that ensureAgentPlugin returns exactly what the decision says.
func TestAgentPluginStatusIsTheLaunchDecision(t *testing.T) {
	onlySettings := map[string]string{string(agent.KeyClaude): "  --settings <file>"}

	for _, tc := range []struct {
		name                                          string
		program                                       string
		help                                          map[string]string
		enabled                                       bool
		wantEnabled, wantClaude, wantFlag, wantInject bool
	}{{
		name: "every gate open", program: "claude", help: claudeHelpWithPluginDir(), enabled: true,
		wantEnabled: true, wantClaude: true, wantFlag: true, wantInject: true,
	}, {
		name: "the setting off stops the ladder first", program: "claude",
		help: claudeHelpWithPluginDir(), enabled: false,
	}, {
		name: "a non-claude program stops it second", program: "codex",
		help: claudeHelpWithPluginDir(), enabled: true, wantEnabled: true,
	}, {
		name: "a claude without the flag stops it third", program: "claude",
		help: onlySettings, enabled: true, wantEnabled: true, wantClaude: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			forceHelpProbe(t, tc.help)
			enableAgentSkills(t, tc.enabled)

			d := AgentPluginStatus(tc.program)
			assert.Equal(t, tc.program, d.Program, "the decision names what it is about")
			assert.Equal(t, tc.wantEnabled, d.Enabled)
			assert.Equal(t, tc.wantClaude, d.Claude)
			assert.Equal(t, tc.wantFlag, d.FlagSupported)
			assert.Equal(t, tc.wantInject, d.Injecting())
			require.NoError(t, d.Err, "no gate here is an IO failure")
			if !tc.wantInject {
				assert.Empty(t, d.Dir, "a refused gate names no directory")
			}

			dir, err := ensureAgentPlugin(tc.program)
			require.NoError(t, err)
			assert.Equal(t, d.Dir, dir,
				"the report and the launch must return the same directory — they are one call")
		})
	}
}

// TestWriteFileIfChangedLeavesNothingBehindOnFailure is the assertion the happy path cannot
// make. A successful rename consumes the temp file, so a scan of the finished directory stays
// green with the cleanup deleted; the failure branches are the only place it does anything,
// and an accumulating temp file is inside the tree claude enumerates as the plugin's skills.
//
// The check is on the directory's whole contents rather than on a temp-file naming pattern,
// because the pattern belongs to config.WriteFileAtomic and is not this test's to know: a
// guard keyed to one spelling passes vacuously the moment that spelling changes.
func TestWriteFileIfChangedLeavesNothingBehindOnFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file belongs: the write, sync, close and chmod all succeed and
	// the rename cannot, which is the branch order that leaves a temp file at its most
	// finished. Forcing it this way needs no permission games and behaves the same on
	// every platform the suite runs on.
	target := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.MkdirAll(target, 0o755))

	before, err := os.ReadDir(dir)
	require.NoError(t, err)

	err = writeFileIfChanged(target, []byte("a skill"))
	require.Error(t, err, "renaming over a directory must not be reported as a successful write")
	// `atrium doctor` prints this verbatim as the one actionable line a write failure gives
	// someone, so it has to name the file Atrium meant to write — not the atomic writer's
	// temp file, which the reader has never seen and cannot act on.
	assert.Contains(t, err.Error(), target, "the error must name the file it failed to write")
	assert.NotContains(t, err.Error(), ".tmp-", "and not the temp file it failed on")

	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := func(entries []os.DirEntry) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	assert.Equal(t, names(before), names(after),
		"a failed write left something behind in the tree claude reads as the plugin")
}

func TestAgentSkillsDefaultsOnAndTheSetterInverts(t *testing.T) {
	// The inverted zero value is load-bearing: a process that never wires the setter must
	// still inject, so the DEFAULT is asserted on the real var rather than described in a
	// comment. Restored after, since the package var is process-wide.
	prev := agentSkillsDisabled.Load()
	t.Cleanup(func() { agentSkillsDisabled.Store(prev) })

	agentSkillsDisabled.Store(false) // the zero value, i.e. never set
	forceHelpProbe(t, claudeHelpWithPluginDir())
	dir, err := ensureAgentPlugin("claude")
	require.NoError(t, err)
	assert.NotEmpty(t, dir, "an unconfigured process must still inject the skill")

	SetAgentSkills(false)
	dir, err = ensureAgentPlugin("claude")
	require.NoError(t, err)
	assert.Empty(t, dir, "SetAgentSkills(false) must stop injection")

	SetAgentSkills(true)
	dir, err = ensureAgentPlugin("claude")
	require.NoError(t, err)
	assert.NotEmpty(t, dir)
}

// TestStartPassesThePluginDirToClaude covers the one link the unit tests above cannot
// reach: ensureAgentPlugin can be perfect and the feature still ship dead if start() never
// appends what it returns. The append is three lines inside start(), so the only way to
// see it is to launch something and read the command tmux was given.
//
// A real tmux server, so RequireTmux rather than a hand-rolled LookPath skip: it carries
// both gates. Isolation, because the socket name is config.RuntimeName(), which is "atrium"
// for a sandbox HOME *and* for every non-legacy install, so only TMUX_TMPDIR separates this
// from the developer's live fleet (#581). And CI's ATRIUM_CI_REQUIRE_TMUX, which turns a
// missing tmux into a hard failure — this guard is the only thing standing between a
// dropped append in start() and a skill that ships dead, so it must never go dark quietly
// (#274). Skipping on its own would opt out of that: CI -skips one test by name, and this
// is not it.
func TestStartPassesThePluginDirToClaude(t *testing.T) {
	testutil.RequireTmux(t)
	enableAgentSkills(t, true)

	// Named `claude` because resolution is a basename match on the first token — the
	// same reason a launcher wrapper resolves as claude. It must not exit immediately:
	// a dead pane has no start command to read back.
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755))
	// Probed under its own path, which is where an absolute-path claude answers.
	forceHelpProbe(t, claudeHelpWithPluginDir(bin))

	name := fmt.Sprintf("plugindir-%d", rand.Int31())
	worktree := t.TempDir()
	session := NewSession(context.Background(), name, bin)
	require.NoError(t, session.Start(worktree))
	t.Cleanup(func() { _ = session.Close() })

	out, err := tmuxCommand(context.Background(), "list-panes", "-t", session.sanitizedName,
		"-F", "#{pane_start_command}").CombinedOutput()
	require.NoError(t, err, "output: %s", out)
	require.Contains(t, string(out), pluginDirFlag,
		"start() never handed the agent the plugin, so the skill ships dead")

	// What the flag POINTS AT, checked against something other than the function that
	// produced it. Comparing it to agentPluginRoot would move both sides of the assertion
	// together, which is the same shape as asserting the literal "plugin" — a substring of
	// "--plugin-dir" — and just as unable to fail.
	arg := regexp.MustCompile(pluginDirFlag + ` '([^']*)'`).FindStringSubmatch(string(out))
	require.Len(t, arg, 2, "the flag must carry a single-quoted path: tmux hands the "+
		"command to sh -c, so an unquoted one breaks on the first space. Got: %s", out)
	path := arg[1]

	require.True(t, filepath.IsAbs(path), "a relative path resolves against whatever "+
		"directory the pane happens to start in: %q", path)
	dataDir, err := config.GetConfigDir()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(path, dataDir+string(filepath.Separator)),
		"the plugin must live under the data dir (%s), not at %q", dataDir, path)
	assert.NotContains(t, path, worktree, "a plugin inside the agent's own worktree is "+
		"untracked files it could commit, and it goes away when the session is paused")

	// And it must be a loadable plugin, not merely a plausible path: claude reports
	// nothing at all for a --plugin-dir with no manifest under it.
	assert.FileExists(t, filepath.Join(path, ".claude-plugin", "plugin.json"))
	assert.FileExists(t, filepath.Join(path, "skills", spawnSkillDir, "SKILL.md"))
}
