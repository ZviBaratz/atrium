package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubDetect makes detectAgentCommand report exactly the given binaries as
// installed, so detection tests never depend on what this machine has.
func stubDetect(t *testing.T, installed map[string]string) {
	t.Helper()
	orig := detectAgentCommand
	detectAgentCommand = func(bin string) (string, error) {
		if p, ok := installed[bin]; ok {
			return p, nil
		}
		return "", fmt.Errorf("%s: not installed", bin)
	}
	t.Cleanup(func() { detectAgentCommand = orig })
}

func TestDetectAgentProfiles(t *testing.T) {
	t.Run("returns installed agents in picker order", func(t *testing.T) {
		stubDetect(t, map[string]string{
			"gemini": "gemini",
			"claude": "/home/u/.claude/local/claude",
		})
		profiles := DetectAgentProfiles()
		assert.Equal(t, []Profile{
			{Name: "claude", Program: "/home/u/.claude/local/claude"},
			{Name: "gemini", Program: "gemini"},
		}, profiles)
	})

	t.Run("no agents installed yields no profiles", func(t *testing.T) {
		stubDetect(t, nil)
		assert.Empty(t, DetectAgentProfiles())
	})
}

func TestMergeDetectedProfiles(t *testing.T) {
	t.Run("appends only new names and reports them", func(t *testing.T) {
		cfg := &Config{
			DefaultProgram: "claude",
			Profiles: []Profile{
				// Hand-edited entry sharing a detected name: must win untouched.
				{Name: "claude", Program: "claude --dangerously-skip-permissions"},
			},
		}
		added := cfg.MergeDetectedProfiles([]Profile{
			{Name: "claude", Program: "/usr/local/bin/claude"},
			{Name: "codex", Program: "codex"},
		})
		assert.Equal(t, []string{"codex"}, added)
		assert.Equal(t, []Profile{
			{Name: "claude", Program: "claude --dangerously-skip-permissions"},
			{Name: "codex", Program: "codex"},
		}, cfg.Profiles)
		assert.Equal(t, "claude", cfg.DefaultProgram, "the default program is never modified")
	})

	t.Run("nothing new is a no-op", func(t *testing.T) {
		cfg := &Config{Profiles: []Profile{{Name: "claude", Program: "claude"}}}
		assert.Empty(t, cfg.MergeDetectedProfiles([]Profile{{Name: "claude", Program: "claude"}}))
		assert.Len(t, cfg.Profiles, 1)
	})
}

func TestSeededDefaultConfig(t *testing.T) {
	t.Run("detected agents become profiles with the first as default", func(t *testing.T) {
		stubDetect(t, map[string]string{"claude": "/usr/local/bin/claude", "aider": "aider"})
		cfg := seededDefaultConfig()
		assert.Equal(t, "claude", cfg.DefaultProgram)
		assert.Equal(t, []Profile{
			{Name: "claude", Program: "/usr/local/bin/claude"},
			{Name: "aider", Program: "aider"},
		}, cfg.Profiles)
		assert.Equal(t, "/usr/local/bin/claude", cfg.GetProgram(),
			"the default program resolves through its profile")
	})

	t.Run("no detected agents falls back to the claude literal", func(t *testing.T) {
		stubDetect(t, nil)
		cfg := seededDefaultConfig()
		assert.Equal(t, "claude", cfg.DefaultProgram)
		assert.Empty(t, cfg.Profiles)
	})
}

// DefaultConfig must stay pure — the hermeticity contract for the many tests
// (here and in app/) that construct defaults directly: no profiles regardless
// of what the machine has installed, and the bare claude literal as program.
func TestDefaultConfigDoesNotProbe(t *testing.T) {
	stubDetect(t, map[string]string{"claude": "/usr/local/bin/claude", "gemini": "gemini"})
	cfg := DefaultConfig()
	assert.Equal(t, "claude", cfg.DefaultProgram)
	assert.Empty(t, cfg.Profiles, "DefaultConfig must not run agent detection")
}
