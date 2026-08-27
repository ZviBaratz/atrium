package doctor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// The decisions below are built by hand rather than by tmux.AgentPluginStatus, because the
// states worth reporting are the ones the machine running the suite does not have: a claude
// too old for the flag, a data dir that refuses a write. What holds these shapes to the real
// ones is that the launch path fills the same fields — see
// TestAgentPluginStatusIsTheLaunchDecision in session/tmux.
var errPlugin = errors.New("permission denied")

// injecting is a claude program that gets the skill; the helpers below are one rung short
// each, in the launch's order.
func injecting(program string) tmux.AgentPluginDecision {
	return tmux.AgentPluginDecision{
		Program: program, Enabled: true, Claude: true, FlagSupported: true, Dir: "/data/plugin",
	}
}

func settingOff(program string) tmux.AgentPluginDecision {
	return tmux.AgentPluginDecision{Program: program}
}

func notClaude(program string) tmux.AgentPluginDecision {
	return tmux.AgentPluginDecision{Program: program, Enabled: true}
}

func noFlag(program string) tmux.AgentPluginDecision {
	return tmux.AgentPluginDecision{Program: program, Enabled: true, Claude: true}
}

func unwritable(program, path string) tmux.AgentPluginDecision {
	return tmux.AgentPluginDecision{
		Program: program, Enabled: true, Claude: true, FlagSupported: true,
		Err: fmt.Errorf("%s: %w", path, errPlugin),
	}
}

// stub answers with a decision per program, and records what it was asked. The record is
// the point of several cases below: the launch path is a writer, so a report that asks about
// a program no session runs is a side effect on a machine that wanted a read.
func stub(t *testing.T, answers map[string]tmux.AgentPluginDecision) (
	func(string) tmux.AgentPluginDecision, *[]string) {
	t.Helper()
	var asked []string
	return func(program string) tmux.AgentPluginDecision {
		asked = append(asked, program)
		d, ok := answers[program]
		require.Truef(t, ok, "asked about %q, which the case did not configure", program)
		return d
	}, &asked
}

// TestCheckAgentSkillsAsksAboutEveryProgramOnce is the check's whole behaviour: the set of
// programs a session could run, deduplicated.
//
// Deduplication is not tidiness. The status call materializes the plugin, so asking twice
// about one binary is a second write, and two profiles naming the same claude is the common
// way that happens.
func TestCheckAgentSkillsAsksAboutEveryProgramOnce(t *testing.T) {
	status, asked := stub(t, map[string]tmux.AgentPluginDecision{
		"claude": injecting("claude"),
		"codex":  notClaude("codex"),
	})
	r := CheckAgentSkills("/atrium:spawn", []string{"claude", "codex", "claude", "", "codex"}, status)

	assert.Equal(t, []string{"claude", "codex"}, *asked,
		"each distinct program once, in configured order")
	require.Len(t, r.Programs, 2)
	assert.Equal(t, "claude", r.Programs[0].Program, "the default program is reported first")
}

// TestAgentSkillsInjectingRequiresEveryClaudeProgram: the summary must not be true while
// some claude session gets nothing. A default claude that works plus a profile claude too
// old for the flag is the shape that made the previous single-program report false.
func TestAgentSkillsInjectingRequiresEveryClaudeProgram(t *testing.T) {
	one, _ := stub(t, map[string]tmux.AgentPluginDecision{"claude": injecting("claude")})
	assert.True(t, CheckAgentSkills("/atrium:spawn", []string{"claude"}, one).Injecting())

	// A non-claude program alongside a working claude is not a failure: it was never
	// going to get the skill and the status line does not claim it would.
	mixed, _ := stub(t, map[string]tmux.AgentPluginDecision{
		"claude": injecting("claude"), "codex": notClaude("codex"),
	})
	assert.True(t, CheckAgentSkills("/atrium:spawn", []string{"claude", "codex"}, mixed).Injecting())

	// Two claudes that disagree.
	split, _ := stub(t, map[string]tmux.AgentPluginDecision{
		"claude": injecting("claude"), "/old/claude": noFlag("/old/claude"),
	})
	assert.False(t, CheckAgentSkills("/atrium:spawn", []string{"claude", "/old/claude"}, split).Injecting(),
		"a claude profile that gets nothing makes the summary false")

	// No claude at all. The trap here is letting the word "claude" in the status line carry
	// the gate — "injected into new claude sessions" reads as hedged, and a codex install
	// then gets the strongest statement the section has for sessions that get nothing.
	none, _ := stub(t, map[string]tmux.AgentPluginDecision{"codex": notClaude("codex")})
	assert.False(t, CheckAgentSkills("/atrium:spawn", []string{"codex"}, none).Injecting())
}

func TestRenderAgentSkillsNamesTheInvocationWhenInjecting(t *testing.T) {
	status, _ := stub(t, map[string]tmux.AgentPluginDecision{"claude": injecting("claude")})
	out := RenderAgentSkills(CheckAgentSkills("/atrium:spawn", []string{"claude"}, status))

	assert.Contains(t, out, "/atrium:spawn",
		"the report's one actionable fact is what the user types")
	assert.Contains(t, out, "/data/plugin")
	assert.Contains(t, out, "claude", "which binary the report is about")
	// The managed-policy remedy belongs on this path and only this path: claude can refuse
	// --plugin-dir only where Atrium is passing it, so the state that produces the dead pane
	// the remedy is about is exactly this one.
	assert.Contains(t, out, "disableSideloadFlags")
	assert.Contains(t, out, "agent_skills")
	assert.True(t, strings.HasSuffix(out, "\n"), "every doctor section is newline-terminated")
}

// TestRenderAgentSkillsDeclineReasons pins each rung to the reason printed for it, in the
// order the launch evaluates them. A report that names the wrong gate sends the user to fix
// something that was never wrong.
//
// None of these prints the managed-settings remedy, and that is the assertion with teeth.
// Every one of them is a state in which --plugin-dir is never passed, so claude cannot be
// refusing it: offering "set agent_skills false" here names a feature that was not involved
// in whatever the user came to diagnose.
func TestRenderAgentSkillsDeclineReasons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		programs []string
		answers  map[string]tmux.AgentPluginDecision
		want     string
		absent   string
	}{{
		name:     "the setting off wins over every later gate",
		programs: []string{"claude"},
		answers:  map[string]tmux.AgentPluginDecision{"claude": settingOff("claude")},
		want:     "agent_skills is off",
	}, {
		// Reachable: an empty default_program with no profiles leaves nothing to ask
		// about, and the dedup drops the blank rather than asking the launch path about
		// "". The stub is never called, which is the assertion inside this one.
		name:     "nothing is configured",
		programs: []string{""},
		answers:  map[string]tmux.AgentPluginDecision{},
		want:     "no program is configured",
	}, {
		name:     "no configured program is claude",
		programs: []string{"codex", "gemini"},
		answers: map[string]tmux.AgentPluginDecision{
			"codex": notClaude("codex"), "gemini": notClaude("gemini"),
		},
		want: "no configured program is claude",
	}, {
		name:     "a claude whose --help lacks the flag is not called old",
		programs: []string{"claude"},
		answers:  map[string]tmux.AgentPluginDecision{"claude": noFlag("claude")},
		want:     "--plugin-dir",
		// The probe cannot tell an old CLI from one it could not run, so the reason
		// must not assert the first: an absolute-path claude off PATH reads the same,
		// and "upgrade claude" is then a wasted afternoon.
		absent: "no --plugin-dir flag",
	}, {
		// Two claudes, both declining: no session is handed the flag anywhere, so this
		// belongs with the single-program declines and not with the split case below. The
		// easy slip is a two-way summary — all or "some, not others" — which states a
		// partial success for a state that has none, and prints the remedy with it.
		name:     "several claude programs that all decline",
		programs: []string{"claude", "/old/claude"},
		answers: map[string]tmux.AgentPluginDecision{
			"claude": noFlag("claude"), "/old/claude": noFlag("/old/claude"),
		},
		want: "no claude program gets the skill",
		// The arm this exercises printed "invoked as" unconditionally, so the report
		// contradicted itself one line after the status. The loop's blanket assertion
		// below is what holds it.
	}, {
		name:     "an unwritable directory names the write",
		programs: []string{"claude"},
		answers: map[string]tmux.AgentPluginDecision{
			"claude": unwritable("claude", "/data/plugin/skills/spawn/SKILL.md"),
		},
		want: "cannot be written",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := stub(t, tc.answers)
			out := RenderAgentSkills(CheckAgentSkills("/atrium:spawn", tc.programs, status))
			require.Contains(t, out, "not injected")
			assert.Contains(t, out, tc.want)
			if tc.absent != "" {
				assert.NotContains(t, out, tc.absent)
			}
			assert.NotContains(t, out, "disableSideloadFlags",
				"the remedy for a refusal that cannot happen in this state")
			// Every case here is a not-injected one, so nothing will answer the
			// invocation. Blanket rather than per-case: the row is printed from two
			// places — renderOne and the multi-claude arm — and the arm shipped without
			// the gate while renderOne had it, which a per-case assertion on the shape
			// that happened to be tested would not have caught.
			assert.NotContains(t, out, "invoked as",
				"what to type, for a skill no session gets")
		})
	}
}

// TestRenderAgentSkillsDetailsTheFileThatRefused: the write failure is the one decline whose
// reason is not self-explanatory. Which file, and what it said, is what the user acts on —
// and it goes on its own line, because the status line is a label/value row and a path in
// its value column buries the reason it is there to state.
func TestRenderAgentSkillsDetailsTheFileThatRefused(t *testing.T) {
	const refused = "/data/plugin/skills/spawn/SKILL.md"
	status, _ := stub(t, map[string]tmux.AgentPluginDecision{
		"claude": unwritable("claude", refused),
	})
	out := RenderAgentSkills(CheckAgentSkills("/atrium:spawn", []string{"claude"}, status))

	var statusLine, detail string
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		switch {
		case strings.Contains(line, "status"):
			statusLine = line
		case strings.Contains(line, "→"):
			detail = line
		}
	}
	require.NotEmpty(t, statusLine, "the section always states a status")
	require.NotEmpty(t, detail, "a write failure the report does not locate is not actionable")

	assert.Contains(t, detail, refused+": permission denied")
	assert.NotContains(t, statusLine, refused,
		"the path belongs on the detail line, not inside the label/value row")
}

// TestRenderAgentSkillsSplitsDisagreeingClaudes is the case a single status line cannot
// state: two claude binaries, one that gets the skill and one that does not. Whichever way a
// one-line summary was written it would be false for one of them, so both are named.
func TestRenderAgentSkillsSplitsDisagreeingClaudes(t *testing.T) {
	status, _ := stub(t, map[string]tmux.AgentPluginDecision{
		"claude":      injecting("claude"),
		"/old/claude": noFlag("/old/claude"),
	})
	out := RenderAgentSkills(CheckAgentSkills("/atrium:spawn",
		[]string{"claude", "/old/claude"}, status))

	assert.Contains(t, out, "not others", "the summary admits the split")
	assert.Contains(t, out, "/old/claude", "the program that gets nothing is named")
	assert.Contains(t, out, "--plugin-dir", "and why")
	// Some session here IS handed the flag, so the managed-policy refusal is reachable and
	// its remedy belongs on the page — the opposite of every all-declined state above.
	assert.Contains(t, out, "disableSideloadFlags")
	// And the invocation is owed for the same reason, which is why the row is gated on
	// "any program injecting" rather than on all of them: typing it reaches the sessions
	// of the claude that got the skill.
	assert.Contains(t, out, "invoked as", "the invocation works for the program that got it")
}
