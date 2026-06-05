package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/log"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain initializes the logger and sandboxes HOME so config tests resolve the
// data dir under a throwaway directory — never the developer's real ~/.atrium or
// legacy ~/.claude-squad. Tests that need a specific layout override HOME locally.
// Agent detection is stubbed to "nothing installed" for the same reason: PATH is
// not sandboxed, so LoadConfig's seeded fallbacks would otherwise pick up
// whatever agent CLIs this machine happens to have. Detection tests install
// their own stubs (see stubDetect).
func TestMain(m *testing.M) {
	log.Initialize(false)
	detectAgentCommand = func(bin string) (string, error) {
		return "", fmt.Errorf("hermetic tests: %s not detectable", bin)
	}
	code := testutil.SandboxHomeMain(m)
	log.Close()
	os.Exit(code)
}

func TestGetClaudeCommand(t *testing.T) {
	originalShell := os.Getenv("SHELL")
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("SHELL", originalShell)
		_ = os.Setenv("PATH", originalPath)
	}()

	t.Run("finds claude in PATH", func(t *testing.T) {
		// Create a temporary directory with a mock claude executable
		tempDir := t.TempDir()
		claudePath := filepath.Join(tempDir, "claude")

		// Create a mock executable
		err := os.WriteFile(claudePath, []byte("#!/bin/bash\necho 'mock claude'"), 0755)
		require.NoError(t, err)

		// Set PATH to include our temp directory
		_ = os.Setenv("PATH", tempDir+":"+originalPath)
		_ = os.Setenv("SHELL", "/bin/bash")

		result, err := GetClaudeCommand()

		assert.NoError(t, err)
		assert.True(t, strings.Contains(result, "claude"))
	})

	t.Run("handles missing claude command", func(t *testing.T) {
		// Set PATH to a directory that doesn't contain claude
		tempDir := t.TempDir()
		_ = os.Setenv("PATH", tempDir)
		_ = os.Setenv("SHELL", "/bin/bash")

		result, err := GetClaudeCommand()

		assert.Error(t, err)
		assert.Equal(t, "", result)
		assert.Contains(t, err.Error(), "claude command not found")
	})

	t.Run("handles empty SHELL environment", func(t *testing.T) {
		// Create a temporary directory with a mock claude executable
		tempDir := t.TempDir()
		claudePath := filepath.Join(tempDir, "claude")

		// Create a mock executable
		err := os.WriteFile(claudePath, []byte("#!/bin/bash\necho 'mock claude'"), 0755)
		require.NoError(t, err)

		// Set PATH and unset SHELL
		_ = os.Setenv("PATH", tempDir+":"+originalPath)
		_ = os.Unsetenv("SHELL")

		result, err := GetClaudeCommand()

		assert.NoError(t, err)
		assert.True(t, strings.Contains(result, "claude"))
	})

	t.Run("handles alias parsing", func(t *testing.T) {
		// Test core alias formats
		aliasRegex := regexp.MustCompile(`(?:aliased to|->|=)\s*([^\s]+)`)

		// Standard alias format
		output := "claude: aliased to /usr/local/bin/claude"
		matches := aliasRegex.FindStringSubmatch(output)
		assert.Len(t, matches, 2)
		assert.Equal(t, "/usr/local/bin/claude", matches[1])

		// Direct path (no alias)
		output = "/usr/local/bin/claude"
		matches = aliasRegex.FindStringSubmatch(output)
		assert.Len(t, matches, 0)
	})
}

func TestResolveClaudeCandidate(t *testing.T) {
	// Provide a real, executable `claude` on PATH so the candidates that are
	// expected to resolve can succeed.
	tempDir := t.TempDir()
	claudePath := filepath.Join(tempDir, "claude")
	require.NoError(t, os.WriteFile(claudePath, []byte("#!/bin/sh\n"), 0o755))

	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The multi-line body `which claude` prints when `claude` is a zsh function.
	// The alias regex captures "$?" from `local ret=$?`; that token is not a
	// runnable program, so resolution must report no match.
	functionBody := "claude () {\n" +
		"\tif [[ -n \"$TMUX\" ]]\n" +
		"\tthen\n" +
		"\t\ttmux setw monitor-activity off\n" +
		"\t\tcommand claude \"$@\"\n" +
		"\t\tlocal ret=$?\n" +
		"\t\ttmux setw monitor-activity on\n" +
		"\t\treturn $ret\n" +
		"\telse\n" +
		"\t\tcommand claude \"$@\"\n" +
		"\tfi\n" +
		"}"

	// A function body whose first `=` assignment has a right-hand side that is a
	// real binary on PATH (here, `claude` itself). The alias regex captures that
	// token; without the multi-line guard it would resolve via exec.LookPath and
	// be wrongly accepted as the program to launch.
	functionBodyResolvable := "claude () {\n" +
		"\tlocal helper=claude\n" +
		"\tcommand claude \"$@\"\n" +
		"}"

	tests := []struct {
		name     string
		output   string
		wantOK   bool
		wantPath string
	}{
		{"plain absolute path", claudePath, true, claudePath},
		{"alias definition", "claude: aliased to " + claudePath, true, claudePath},
		{"bare name resolved via PATH", "claude", true, claudePath},
		{"shell function body is rejected", functionBody, false, ""},
		{"function body whose first assignment resolves on PATH is rejected", functionBodyResolvable, false, ""},
		{"empty output", "   \n\t", false, ""},
		{"non-executable alias target", "claude=/nonexistent/definitely/not/here", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveClaudeCandidate(tt.output)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantPath, got)
			} else {
				assert.Empty(t, got)
				assert.NotEqual(t, "$?", got, "must never return the mis-parsed function-body token")
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Run("creates config with default values", func(t *testing.T) {
		config := DefaultConfig()

		assert.NotNil(t, config)
		assert.NotEmpty(t, config.DefaultProgram)
		assert.False(t, config.AutoYes)
		assert.Equal(t, 1000, config.DaemonPollInterval)
		assert.NotEmpty(t, config.BranchPrefix)
		assert.True(t, strings.HasSuffix(config.BranchPrefix, "/"))
	})

}

func TestGetConfigDir(t *testing.T) {
	t.Run("returns valid config directory", func(t *testing.T) {
		configDir, err := GetConfigDir()

		assert.NoError(t, err)
		assert.NotEmpty(t, configDir)
		assert.True(t, strings.HasSuffix(configDir, ".atrium"))

		// Verify it's an absolute path
		assert.True(t, filepath.IsAbs(configDir))
	})
}

func TestLoadConfig(t *testing.T) {
	t.Run("returns default config when file doesn't exist", func(t *testing.T) {
		// Use a temporary home directory to avoid interfering with real config
		originalHome := os.Getenv("HOME")
		tempHome := t.TempDir()
		_ = os.Setenv("HOME", tempHome)
		defer func() { _ = os.Setenv("HOME", originalHome) }()

		config := LoadConfig()

		assert.NotNil(t, config)
		assert.NotEmpty(t, config.DefaultProgram)
		assert.False(t, config.AutoYes)
		assert.Equal(t, 1000, config.DaemonPollInterval)
		assert.NotEmpty(t, config.BranchPrefix)
	})

	t.Run("loads valid config file", func(t *testing.T) {
		// Create a temporary config directory
		tempHome := t.TempDir()
		configDir := filepath.Join(tempHome, ".claude-squad")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		// Create a test config file
		configPath := filepath.Join(configDir, ConfigFileName)
		configContent := `{
			"default_program": "test-claude",
			"auto_yes": true,
			"daemon_poll_interval": 2000,
			"branch_prefix": "test/"
		}`
		err = os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err)

		// Override HOME environment
		originalHome := os.Getenv("HOME")
		_ = os.Setenv("HOME", tempHome)
		defer func() { _ = os.Setenv("HOME", originalHome) }()

		config := LoadConfig()

		assert.NotNil(t, config)
		assert.Equal(t, "test-claude", config.DefaultProgram)
		assert.True(t, config.AutoYes)
		assert.Equal(t, 2000, config.DaemonPollInterval)
		assert.Equal(t, "test/", config.BranchPrefix)
	})

	t.Run("returns default config on invalid JSON", func(t *testing.T) {
		// Create a temporary config directory
		tempHome := t.TempDir()
		configDir := filepath.Join(tempHome, ".claude-squad")
		err := os.MkdirAll(configDir, 0755)
		require.NoError(t, err)

		// Create an invalid config file
		configPath := filepath.Join(configDir, ConfigFileName)
		invalidContent := `{"invalid": json content}`
		err = os.WriteFile(configPath, []byte(invalidContent), 0644)
		require.NoError(t, err)

		// Override HOME environment
		originalHome := os.Getenv("HOME")
		_ = os.Setenv("HOME", tempHome)
		defer func() { _ = os.Setenv("HOME", originalHome) }()

		config := LoadConfig()

		// Should return default config when JSON is invalid
		assert.NotNil(t, config)
		assert.NotEmpty(t, config.DefaultProgram)
		assert.False(t, config.AutoYes)                  // Default value
		assert.Equal(t, 1000, config.DaemonPollInterval) // Default value
	})
}

func TestGetProgram(t *testing.T) {
	t.Run("no profiles returns default_program as-is", func(t *testing.T) {
		cfg := &Config{DefaultProgram: "/usr/local/bin/claude"}
		assert.Equal(t, "/usr/local/bin/claude", cfg.GetProgram())
	})

	t.Run("profiles defined and default_program matches a profile name", func(t *testing.T) {
		cfg := &Config{
			DefaultProgram: "claude",
			Profiles: []Profile{
				{Name: "claude", Program: "/usr/local/bin/claude"},
				{Name: "aider", Program: "aider --model ollama_chat/gemma3:1b"},
			},
		}
		assert.Equal(t, "/usr/local/bin/claude", cfg.GetProgram())
	})

	t.Run("profiles defined but default_program does not match any profile", func(t *testing.T) {
		cfg := &Config{
			DefaultProgram: "some-other-program",
			Profiles: []Profile{
				{Name: "claude", Program: "/usr/local/bin/claude"},
			},
		}
		assert.Equal(t, "some-other-program", cfg.GetProgram())
	})
}

func TestGetProfiles(t *testing.T) {
	t.Run("no profiles returns single synthetic profile", func(t *testing.T) {
		cfg := &Config{DefaultProgram: "/usr/local/bin/claude"}
		profiles := cfg.GetProfiles()
		assert.Len(t, profiles, 1)
		assert.Equal(t, "/usr/local/bin/claude", profiles[0].Name)
		assert.Equal(t, "/usr/local/bin/claude", profiles[0].Program)
	})

	t.Run("profiles defined returns them with default first", func(t *testing.T) {
		cfg := &Config{
			DefaultProgram: "aider",
			Profiles: []Profile{
				{Name: "claude", Program: "/usr/local/bin/claude"},
				{Name: "aider", Program: "aider --model gemma"},
			},
		}
		profiles := cfg.GetProfiles()
		assert.Len(t, profiles, 2)
		assert.Equal(t, "aider", profiles[0].Name)
		assert.Equal(t, "claude", profiles[1].Name)
	})

	t.Run("profiles defined but default not matching preserves order", func(t *testing.T) {
		cfg := &Config{
			DefaultProgram: "other",
			Profiles: []Profile{
				{Name: "claude", Program: "/usr/local/bin/claude"},
				{Name: "aider", Program: "aider --model gemma"},
			},
		}
		profiles := cfg.GetProfiles()
		assert.Len(t, profiles, 2)
		assert.Equal(t, "claude", profiles[0].Name)
		assert.Equal(t, "aider", profiles[1].Name)
	})
}

func TestSaveConfig(t *testing.T) {
	t.Run("saves config to file", func(t *testing.T) {
		// Create a temporary config directory
		tempHome := t.TempDir()

		// Override HOME environment
		originalHome := os.Getenv("HOME")
		_ = os.Setenv("HOME", tempHome)
		defer func() { _ = os.Setenv("HOME", originalHome) }()

		// Create a test config
		testConfig := &Config{
			DefaultProgram:     "test-program",
			AutoYes:            true,
			DaemonPollInterval: 3000,
			BranchPrefix:       "test-branch/",
		}

		err := SaveConfig(testConfig)
		assert.NoError(t, err)

		// Verify the file was created (fresh HOME → new ~/.atrium layout)
		configDir := filepath.Join(tempHome, ".atrium")
		configPath := filepath.Join(configDir, ConfigFileName)

		assert.FileExists(t, configPath)

		// Load and verify the content
		loadedConfig := LoadConfig()
		assert.Equal(t, testConfig.DefaultProgram, loadedConfig.DefaultProgram)
		assert.Equal(t, testConfig.AutoYes, loadedConfig.AutoYes)
		assert.Equal(t, testConfig.DaemonPollInterval, loadedConfig.DaemonPollInterval)
		assert.Equal(t, testConfig.BranchPrefix, loadedConfig.BranchPrefix)
	})
}

func TestGetAutoAttach(t *testing.T) {
	t.Run("default config is on", func(t *testing.T) {
		assert.True(t, DefaultConfig().GetAutoAttach())
	})
	t.Run("nil field (older config) defaults on", func(t *testing.T) {
		assert.True(t, (&Config{}).GetAutoAttach())
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{AutoAttach: &v}).GetAutoAttach())
	})
	t.Run("explicit false", func(t *testing.T) {
		v := false
		assert.False(t, (&Config{AutoAttach: &v}).GetAutoAttach())
	})
}

func TestGetKillDoubleTapConfirm(t *testing.T) {
	t.Run("default config is on", func(t *testing.T) {
		assert.True(t, DefaultConfig().GetKillDoubleTapConfirm())
	})
	t.Run("nil field (older config) defaults on", func(t *testing.T) {
		assert.True(t, (&Config{}).GetKillDoubleTapConfirm())
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{KillDoubleTapConfirm: &v}).GetKillDoubleTapConfirm())
	})
	t.Run("explicit false", func(t *testing.T) {
		v := false
		assert.False(t, (&Config{KillDoubleTapConfirm: &v}).GetKillDoubleTapConfirm())
	})
}
