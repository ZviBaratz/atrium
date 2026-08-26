package doctor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginAt is a plugin resolver that succeeds, for the cases whose subject is a gate before
// it. errPlugin is its failure: an unwritable data dir, which is the gate no read-only check
// can see.
func pluginAt(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

var errPlugin = errors.New("permission denied")

func TestAgentSkillsInjectingRequiresEveryRung(t *testing.T) {
	full := CheckAgentSkills(true, true, "/atrium:spawn", pluginAt("/data/plugin"))
	assert.True(t, full.Injecting())

	// One rung short each way. Injecting() is what the report's whole shape hangs off,
	// so each gate must be able to veto on its own.
	assert.False(t, CheckAgentSkills(false, true, "/atrium:spawn", pluginAt("/data/plugin")).Injecting())
	assert.False(t, CheckAgentSkills(true, false, "/atrium:spawn", pluginAt("/data/plugin")).Injecting())
	assert.False(t, CheckAgentSkills(true, true, "/atrium:spawn", pluginAt("")).Injecting())
	assert.False(t, CheckAgentSkills(true, true, "/atrium:spawn", func() (string, error) {
		return "/data/plugin", errPlugin
	}).Injecting(), "a directory that cannot be written costs the skill at every launch")
}

// TestCheckAgentSkillsReachesThePluginInLaunchOrder pins the one thing this check does
// beyond copying its arguments. Resolving the plugin directory has a side effect — it
// creates the directories and probes them with a temp file — and ensureAgentPlugin never
// reaches that step when an earlier gate has refused. A report that probed anyway would
// create a plugin tree for a feature that is switched off, and would name the wrong gate.
func TestCheckAgentSkillsReachesThePluginInLaunchOrder(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		enabled, flagSupport bool
		wantCalled           bool
	}{
		{name: "every earlier gate open", enabled: true, flagSupport: true, wantCalled: true},
		{name: "setting off", enabled: false, flagSupport: true},
		{name: "flag unsupported", enabled: true, flagSupport: false},
		{name: "both", enabled: false, flagSupport: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			CheckAgentSkills(tc.enabled, tc.flagSupport, "/atrium:spawn", func() (string, error) {
				called = true
				return "/data/plugin", nil
			})
			assert.Equal(t, tc.wantCalled, called)
		})
	}
}

func TestRenderAgentSkillsNamesTheInvocationWhenInjecting(t *testing.T) {
	out := RenderAgentSkills(CheckAgentSkills(true, true, "/atrium:spawn", pluginAt("/data/plugin")))
	assert.Contains(t, out, "/atrium:spawn",
		"the report's one actionable fact is what the user types")
	assert.Contains(t, out, "/data/plugin")
	// The managed-policy remedy belongs on this path and only this path: claude can refuse
	// --plugin-dir only where Atrium is passing it, so the state that produces the dead pane
	// the remedy is about is exactly this one.
	assert.Contains(t, out, "disableSideloadFlags")
	assert.Contains(t, out, "agent_skills")
	assert.True(t, strings.HasSuffix(out, "\n"), "every doctor section is newline-terminated")
}

// TestRenderAgentSkillsDeclineReasons pins each rung to the reason printed for it, in the
// order CheckAgentSkills evaluates them. A report that names the wrong gate sends the user
// to fix something that was never wrong.
//
// None of these prints the managed-settings remedy, and that is the assertion with teeth.
// Every one of them is a state in which --plugin-dir is never passed, so claude cannot be
// refusing it: offering "set agent_skills false" here names a feature that was not
// involved in whatever the user came to diagnose.
func TestRenderAgentSkillsDeclineReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result AgentSkillsResult
		want   string
		absent string
	}{
		{
			name:   "setting off wins over every later gate",
			result: CheckAgentSkills(false, false, "/atrium:spawn", pluginAt("")),
			want:   "agent_skills is off",
		},
		{
			name:   "a claude whose --help lacks the flag is not called old",
			result: CheckAgentSkills(true, false, "/atrium:spawn", pluginAt("/data/plugin")),
			want:   "--plugin-dir",
			// The probe cannot tell an old CLI from one it could not run, so the reason
			// must not assert the first: an absolute-path claude off PATH reads the same,
			// and "upgrade claude" is then a wasted afternoon.
			absent: "no --plugin-dir flag",
		},
		{
			name: "an unwritable directory names the write, and which one",
			result: CheckAgentSkills(true, true, "/atrium:spawn", func() (string, error) {
				return "/data/plugin", fmt.Errorf("/data/plugin/skills/spawn: %w", errPlugin)
			}),
			want: "cannot be written",
		},
		{
			name:   "an unresolvable directory is named last",
			result: CheckAgentSkills(true, true, "/atrium:spawn", pluginAt("")),
			want:   "could not be resolved",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderAgentSkills(tc.result)
			require.Contains(t, out, "not injected")
			assert.Contains(t, out, tc.want)
			if tc.absent != "" {
				assert.NotContains(t, out, tc.absent)
			}
			assert.NotContains(t, out, "disableSideloadFlags",
				"the remedy for a refusal that cannot happen in this state")
		})
	}
}

// TestRenderAgentSkillsDetailsTheDirectoryThatRefused: the write failure is the one decline
// whose reason is not self-explanatory. Which directory, and what it said, is what the user
// acts on — and it goes on its own line, because the status line is a label/value row and a
// path in its value column buries the reason it is there to state.
func TestRenderAgentSkillsDetailsTheDirectoryThatRefused(t *testing.T) {
	const refused = "/data/plugin/skills/spawn"
	out := RenderAgentSkills(CheckAgentSkills(true, true, "/atrium:spawn", func() (string, error) {
		return "/data/plugin", fmt.Errorf("%s: %w", refused, errPlugin)
	}))

	var status, detail string
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		switch {
		case strings.Contains(line, "status"):
			status = line
		case strings.Contains(line, "→"):
			detail = line
		}
	}
	require.NotEmpty(t, status, "the section always states a status")
	require.NotEmpty(t, detail, "a write failure the report does not locate is not actionable")

	assert.Contains(t, detail, refused+": permission denied")
	assert.NotContains(t, status, refused,
		"the path belongs on the detail line, not inside the label/value row")
}
