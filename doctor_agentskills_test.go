package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// TestAgentSkillsSectionHonoursTheSetting is the guard for the one line the extraction
// exists for.
//
// agent_skills is a process-wide var, installed from config by whatever process launches
// sessions. `atrium doctor` launches none, so it has to install it too — and until it did,
// a user who had switched the feature off (the documented remedy for an organization's
// managed settings refusing sideloaded plugins) was told it was injecting, by the very
// report they would have opened to check. The section reads the launch path, so the setting
// has to be installed before it is asked, not passed beside the answer.
//
// Read through the rendered text rather than the var, because the var is what a wrong
// implementation would also set: what matters is that the report changes.
func TestAgentSkillsSectionHonoursTheSetting(t *testing.T) {
	// Process-wide, so a sibling test must not inherit whatever this leaves behind.
	prev := config.LoadConfig().GetAgentSkills()
	t.Cleanup(func() { tmux.SetAgentSkills(prev) })

	off, on := false, true
	for _, tc := range []struct {
		name    string
		setting *bool
		wantOff bool
	}{
		{name: "off is reported as off", setting: &off, wantOff: true},
		{name: "on is not reported as off", setting: &on},
		// An older config has no key at all, and the default is on — the inverted zero
		// value the whole feature leans on.
		{name: "unset defaults to on", setting: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := agentSkillsSection(&config.Config{
				DefaultProgram: "codex",
				AgentSkills:    tc.setting,
			})
			require.True(t, strings.HasPrefix(out, "Agent skills:"),
				"the section always identifies itself")
			isOff := strings.Contains(out, "agent_skills is off")
			assert.Equal(t, tc.wantOff, isOff,
				"the report must follow config, not this process's default: %s", out)
		})
	}
}
