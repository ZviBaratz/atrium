package doctor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentSkillsInjectingRequiresEveryRung(t *testing.T) {
	full := CheckAgentSkills(true, true, "/atrium:spawn", "/data/plugin")
	assert.True(t, full.Injecting())

	// One rung short each way. Injecting() is what the report's whole shape hangs off,
	// so each gate must be able to veto on its own.
	assert.False(t, CheckAgentSkills(false, true, "/atrium:spawn", "/data/plugin").Injecting())
	assert.False(t, CheckAgentSkills(true, false, "/atrium:spawn", "/data/plugin").Injecting())
	assert.False(t, CheckAgentSkills(true, true, "/atrium:spawn", "").Injecting())
}

func TestRenderAgentSkillsNamesTheInvocationWhenInjecting(t *testing.T) {
	out := RenderAgentSkills(CheckAgentSkills(true, true, "/atrium:spawn", "/data/plugin"))
	assert.Contains(t, out, "/atrium:spawn",
		"the report's one actionable fact is what the user types")
	assert.Contains(t, out, "/data/plugin")
	// The remedy for a managed-policy refusal must NOT be printed when the feature is
	// working: it would read as a problem where there is none.
	assert.NotContains(t, out, "disableSideloadFlags")
	assert.True(t, strings.HasSuffix(out, "\n"), "every doctor section is newline-terminated")
}

// TestRenderAgentSkillsDeclineReasons pins each rung to the reason printed for it, in the
// order ensureAgentPlugin checks them. A report that names the wrong gate sends the user
// to fix something that was never wrong.
func TestRenderAgentSkillsDeclineReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result AgentSkillsResult
		want   string
		remedy bool
	}{
		{
			name:   "setting off wins over every later gate",
			result: CheckAgentSkills(false, false, "/atrium:spawn", ""),
			want:   "agent_skills is off",
			// Deliberately absent: telling someone who turned it off how to turn it off
			// is noise, and it implies a failure where there was a choice.
			remedy: false,
		},
		{
			name:   "an older claude is named as such",
			result: CheckAgentSkills(true, false, "/atrium:spawn", "/data/plugin"),
			want:   "--plugin-dir",
			remedy: true,
		},
		{
			name:   "an unresolvable directory is named last",
			result: CheckAgentSkills(true, true, "/atrium:spawn", ""),
			want:   "plugin directory",
			remedy: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderAgentSkills(tc.result)
			require.Contains(t, out, "not injected")
			assert.Contains(t, out, tc.want)
			// The managed-settings remedy is the one thing a user cannot derive: claude's
			// refusal happens at launch, on a pane that then dies, and the fix is a config
			// key. It has to appear wherever the feature is wanted but absent.
			if tc.remedy {
				assert.Contains(t, out, "disableSideloadFlags")
				assert.Contains(t, out, "agent_skills")
			} else {
				assert.NotContains(t, out, "disableSideloadFlags")
			}
		})
	}
}
