package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeHelpWithPluginDir is a --help output that advertises the flag, so the capability
// probe passes without exec'ing anything.
func claudeHelpWithPluginDir() map[string]string {
	return map[string]string{string(agent.KeyClaude): "  " + pluginDirFlag + " <path>   Load a plugin"}
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
func TestSpawnSkillNamesOnlyRealClaudeValues(t *testing.T) {
	valid := map[string]string{}
	for _, e := range agent.ClaudeEffortLevels {
		valid[e] = "effort"
	}
	for _, m := range agent.ClaudePermissionModes {
		valid[m] = "mode"
	}

	// The first cell of every table row, which is where the two ladders state their
	// values. A row may name more than one (the mechanical rung names two).
	rowCell := regexp.MustCompile(`(?m)^\| ([^|]+) \|`)
	backticked := regexp.MustCompile("`([^`]+)`")
	seen := map[string]bool{}
	for _, row := range rowCell.FindAllStringSubmatch(spawnSkillDoc, -1) {
		for _, tok := range backticked.FindAllStringSubmatch(row[1], -1) {
			value := tok[1]
			kind, ok := valid[value]
			require.Truef(t, ok,
				"the skill's table names %q, which is neither a claude effort level nor a "+
					"permission mode. If a new table was added whose first column is not a "+
					"flag value, scope this guard to the two ladders rather than deleting it.",
				value)
			seen[kind+":"+value] = true
		}
	}

	// Every effort level must appear. The table is a ladder over the whole range, so a
	// level added to the CLI leaves it silently incomplete — recommending "max" for the
	// hardest work when something above it now exists.
	for _, e := range agent.ClaudeEffortLevels {
		assert.Truef(t, seen["effort:"+e],
			"effort level %q is missing from the skill's ladder", e)
	}
	// Modes are deliberately NOT covered exhaustively: the offered chips include one the
	// skill does not recommend, so only validity is asserted above. plan and acceptEdits
	// are the two it does recommend, and both must survive a rename of the enum.
	assert.True(t, seen["mode:plan"] && seen["mode:acceptEdits"],
		"the skill's mode table must name both modes it recommends")
}

// TestSpawnSkillExamplesUseValidFlagValues covers the other half: the worked commands.
// A table can be right while the example beneath it pins a model alias that does not
// exist, and the example is the part a reader copies.
func TestSpawnSkillExamplesUseValidFlagValues(t *testing.T) {
	pins := regexp.MustCompile(`--(model|effort|permission-mode) ([A-Za-z0-9._:/-]+)`)
	matches := pins.FindAllStringSubmatch(spawnSkillDoc, -1)
	require.NotEmpty(t, matches, "the skill shows no worked command; the examples are the "+
		"part a reader copies, so their absence is a regression too")
	for _, m := range matches {
		switch m[1] {
		case "model":
			assert.Truef(t, agent.ValidModelName(m[2]),
				"--model %q would not survive composition into the launch command", m[2])
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
		forceHelpProbe(t, claudeHelpWithPluginDir())
		enableAgentSkills(t, true)
		dir, err := ensureAgentPlugin("/usr/local/bin/claude --continue")
		require.NoError(t, err)
		assert.NotEmpty(t, dir, "resolution is wrapper-aware for --settings and must be here too")
	})
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
// A real tmux server, and therefore both isolation gates: the socket name is
// config.RuntimeName(), which is "atrium" for a sandbox HOME *and* for every non-legacy
// install, so only TMUX_TMPDIR separates this from the developer's live fleet (#581).
func TestStartPassesThePluginDirToClaude(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	testutil.RequireSandboxedTmux(t)
	forceHelpProbe(t, claudeHelpWithPluginDir())
	enableAgentSkills(t, true)

	// Named `claude` because resolution is a basename match on the first token — the
	// same reason a launcher wrapper resolves as claude. It must not exit immediately:
	// a dead pane has no start command to read back.
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755))

	name := fmt.Sprintf("plugindir-%d", rand.Int31())
	session := NewSession(context.Background(), name, bin)
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	out, err := tmuxCommand(context.Background(), "list-panes", "-t", session.sanitizedName,
		"-F", "#{pane_start_command}").CombinedOutput()
	require.NoError(t, err, "output: %s", out)
	assert.Contains(t, string(out), pluginDirFlag,
		"start() never handed the agent the plugin, so the skill ships dead")
	assert.Contains(t, string(out), filepath.Join("plugin", ""),
		"the flag must name Atrium's own plugin directory")
}
