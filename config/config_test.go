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

		// The unparseable file is preserved for recovery, not silently discarded.
		corrupt, rerr := os.ReadFile(configPath + ".corrupt")
		require.NoError(t, rerr)
		assert.Equal(t, invalidContent, string(corrupt))
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

func TestGetShowReleaseNotesAfterUpdate(t *testing.T) {
	t.Run("default config is on", func(t *testing.T) {
		assert.True(t, DefaultConfig().GetShowReleaseNotesAfterUpdate())
	})
	t.Run("nil config defaults on", func(t *testing.T) {
		var c *Config
		assert.True(t, c.GetShowReleaseNotesAfterUpdate())
	})
	t.Run("nil field (older config) defaults on", func(t *testing.T) {
		assert.True(t, (&Config{}).GetShowReleaseNotesAfterUpdate())
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{ShowReleaseNotesAfterUpdate: &v}).GetShowReleaseNotesAfterUpdate())
	})
	t.Run("explicit false", func(t *testing.T) {
		v := false
		assert.False(t, (&Config{ShowReleaseNotesAfterUpdate: &v}).GetShowReleaseNotesAfterUpdate())
	})
}

func TestGetTrustWorktreesRoot(t *testing.T) {
	t.Run("default config is off", func(t *testing.T) {
		assert.False(t, DefaultConfig().GetTrustWorktreesRoot())
	})
	t.Run("nil field (older config) defaults off", func(t *testing.T) {
		assert.False(t, (&Config{}).GetTrustWorktreesRoot())
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{TrustWorktreesRoot: &v}).GetTrustWorktreesRoot())
	})
	t.Run("explicit false", func(t *testing.T) {
		v := false
		assert.False(t, (&Config{TrustWorktreesRoot: &v}).GetTrustWorktreesRoot())
	})
}

func TestWorktreesDir(t *testing.T) {
	t.Run("derives from the config dir", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)

		dir, err := WorktreesDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(tempHome, ".atrium", "worktrees"), dir)
	})
	t.Run("follows the legacy config dir when only it exists", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		require.NoError(t, os.MkdirAll(filepath.Join(tempHome, ".claude-squad"), 0755))

		dir, err := WorktreesDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(tempHome, ".claude-squad", "worktrees"), dir)
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

func TestGetSmartDispatchAuto(t *testing.T) {
	t.Run("default config is off (confirm-by-default)", func(t *testing.T) {
		assert.False(t, DefaultConfig().GetSmartDispatchAuto())
	})
	t.Run("nil field defaults off", func(t *testing.T) {
		assert.False(t, (&Config{}).GetSmartDispatchAuto())
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{SmartDispatchAuto: &v}).GetSmartDispatchAuto())
	})
}

func TestGetPRCreateDraft(t *testing.T) {
	t.Run("default config opens drafts", func(t *testing.T) {
		assert.True(t, DefaultConfig().GetPRCreateDraft())
	})
	t.Run("nil field (older config) defaults to draft", func(t *testing.T) {
		assert.True(t, (&Config{}).GetPRCreateDraft())
	})
	t.Run("nil receiver defaults to draft", func(t *testing.T) {
		var c *Config
		assert.True(t, c.GetPRCreateDraft())
	})
	t.Run("explicit true", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{PRCreateDraft: &v}).GetPRCreateDraft())
	})
	t.Run("explicit false (ready for review)", func(t *testing.T) {
		v := false
		assert.False(t, (&Config{PRCreateDraft: &v}).GetPRCreateDraft())
	})
}

func TestGetUpdateBaseOnCreate(t *testing.T) {
	t.Run("default config freshens", func(t *testing.T) {
		assert.True(t, DefaultConfig().GetUpdateBaseOnCreate())
	})
	t.Run("nil field (older config) defaults to on", func(t *testing.T) {
		assert.True(t, (&Config{}).GetUpdateBaseOnCreate())
	})
	t.Run("nil receiver defaults to on", func(t *testing.T) {
		var c *Config
		assert.True(t, c.GetUpdateBaseOnCreate())
	})
	t.Run("explicit false", func(t *testing.T) {
		v := false
		assert.False(t, (&Config{UpdateBaseOnCreate: &v}).GetUpdateBaseOnCreate())
	})
}

func TestGetFastForwardLocalBase(t *testing.T) {
	t.Run("default config does not mutate local", func(t *testing.T) {
		assert.False(t, DefaultConfig().GetFastForwardLocalBase())
	})
	t.Run("nil field (older config) defaults to off", func(t *testing.T) {
		assert.False(t, (&Config{}).GetFastForwardLocalBase())
	})
	t.Run("nil receiver defaults to off", func(t *testing.T) {
		var c *Config
		assert.False(t, c.GetFastForwardLocalBase())
	})
	t.Run("explicit true (opt-in)", func(t *testing.T) {
		v := true
		assert.True(t, (&Config{FastForwardLocalBase: &v}).GetFastForwardLocalBase())
	})
}

func TestGetCarryFiles(t *testing.T) {
	t.Run("default config seeds claude settings.local.json", func(t *testing.T) {
		assert.Equal(t, []string{".claude/settings.local.json"}, DefaultConfig().GetCarryFiles())
	})
	t.Run("nil field (older config) defaults to seed", func(t *testing.T) {
		assert.Equal(t, []string{".claude/settings.local.json"}, (&Config{}).GetCarryFiles())
	})
	t.Run("explicitly empty list opts out", func(t *testing.T) {
		assert.Empty(t, (&Config{CarryFiles: []string{}}).GetCarryFiles())
	})
	t.Run("custom list returned as-is", func(t *testing.T) {
		custom := []string{".env.local", ".claude/settings.local.json"}
		assert.Equal(t, custom, (&Config{CarryFiles: custom}).GetCarryFiles())
	})
	t.Run("returned default is a copy, not the shared seed", func(t *testing.T) {
		got := (&Config{}).GetCarryFiles()
		got[0] = "mutated"
		assert.Equal(t, []string{".claude/settings.local.json"}, (&Config{}).GetCarryFiles())
	})

	// The empty-list opt-out must survive a save/load cycle: with `omitempty`
	// an explicit [] would be dropped on save and silently revert to the default
	// on the next load (e.g. after a settings-panel save of unrelated fields).
	t.Run("empty-list opt-out survives save and load", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)

		cfg := DefaultConfig()
		cfg.CarryFiles = []string{}
		require.NoError(t, SaveConfig(cfg))

		loaded := LoadConfig()
		assert.NotNil(t, loaded.CarryFiles, "explicit [] must round-trip as non-nil")
		assert.Empty(t, loaded.GetCarryFiles())
	})
}

// GetAutoUpdateMode must normalize every input to a valid mode. The default is
// notify; a typo must never silently disable update hints ("off") nor enable
// unattended binary swaps ("auto").
func TestGetAutoUpdateMode(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"empty defaults to notify", "", AutoUpdateNotify},
		{"explicit notify", "notify", AutoUpdateNotify},
		{"auto", "auto", AutoUpdateAuto},
		{"off", "off", AutoUpdateOff},
		{"unknown falls back to notify", "yolo", AutoUpdateNotify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.AutoUpdate = tc.value
			if got := cfg.GetAutoUpdateMode(); got != tc.want {
				t.Errorf("GetAutoUpdateMode(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}

	var nilCfg *Config
	if got := nilCfg.GetAutoUpdateMode(); got != AutoUpdateNotify {
		t.Errorf("nil config: got %q, want %q", got, AutoUpdateNotify)
	}
}

// GetSessionSort must normalize every input to a valid mode. The default is
// creation (the existing manual order); a typo must never silently rearrange the
// list into status order.
func TestGetSessionSort(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"empty defaults to creation", "", SessionSortCreation},
		{"explicit creation", "creation", SessionSortCreation},
		{"status", "status", SessionSortStatus},
		{"unknown falls back to creation", "yolo", SessionSortCreation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.SessionSort = tc.value
			if got := cfg.GetSessionSort(); got != tc.want {
				t.Errorf("GetSessionSort(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}

	var nilCfg *Config
	if got := nilCfg.GetSessionSort(); got != SessionSortCreation {
		t.Errorf("nil config: got %q, want %q", got, SessionSortCreation)
	}
}

func TestResolveClaudeAccount(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	personal := ClaudeAccount{Name: "personal", ConfigDir: "~/.claude"} // no rules → inferred default
	work := ClaudeAccount{Name: "quantivly", ConfigDir: "~/.claude-quantivly",
		RemoteMatches: []string{"quantivly/", "github-quantivly:"},
		PathMatches:   []string{"/quantivly/"}}

	cfg := &Config{ClaudeAccounts: []ClaudeAccount{personal, work}}

	cases := []struct {
		name          string
		accounts      []ClaudeAccount
		remote        string
		path          string
		wantName      string
		wantDir       string
		wantIsDefault bool
	}{
		{"unconfigured", nil, "git@github.com:quantivly/x.git", "", "", "", false},
		{"https remote match", cfg.ClaudeAccounts, "https://github.com/quantivly/x.git", "", "quantivly", "/home/tester/.claude-quantivly", false},
		{"ssh alias remote match", cfg.ClaudeAccounts, "github-quantivly:quantivly/x.git", "", "quantivly", "/home/tester/.claude-quantivly", false},
		{"case-insensitive remote", cfg.ClaudeAccounts, "https://github.com/Quantivly/X.git", "", "quantivly", "/home/tester/.claude-quantivly", false},
		{"no match -> inferred default (no-rule account)", cfg.ClaudeAccounts, "git@github.com:someoneelse/y.git", "", "personal", "/home/tester/.claude", true},
		{"empty remote and path -> inferred default", cfg.ClaudeAccounts, "", "", "personal", "/home/tester/.claude", true},
		// Direct/non-git target: no remote, but the directory path routes by PathMatches.
		{"direct path match", cfg.ClaudeAccounts, "", "/home/zvi/quantivly/qspace", "quantivly", "/home/tester/.claude-quantivly", false},
		{"path case-insensitive", cfg.ClaudeAccounts, "", "/home/zvi/Quantivly/Qspace", "quantivly", "/home/tester/.claude-quantivly", false},
		{"path present but no match -> default", cfg.ClaudeAccounts, "", "/home/zvi/personal/proj", "personal", "/home/tester/.claude", true},
		{"remote matches while path doesn't", cfg.ClaudeAccounts, "https://github.com/quantivly/x.git", "/home/zvi/elsewhere", "quantivly", "/home/tester/.claude-quantivly", false},
		{"no match, every account has rules -> inherit env", []ClaudeAccount{work}, "git@github.com:other/z.git", "/tmp/other", "default", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{ClaudeAccounts: tc.accounts}
			name, dir, isDefault := c.ResolveClaudeAccount(tc.remote, tc.path)
			if name != tc.wantName || dir != tc.wantDir || isDefault != tc.wantIsDefault {
				t.Fatalf("ResolveClaudeAccount(%q, %q) = (%q,%q,%v), want (%q,%q,%v)",
					tc.remote, tc.path, name, dir, isDefault, tc.wantName, tc.wantDir, tc.wantIsDefault)
			}
		})
	}

	// An account with only PathMatches is a route rule, not the catch-all default:
	// a non-matching target inherits env rather than landing on the path-only account.
	pathOnly := ClaudeAccount{Name: "work", ConfigDir: "/w", PathMatches: []string{"/work/"}}
	if name, _, isDefault := (&Config{ClaudeAccounts: []ClaudeAccount{pathOnly}}).
		ResolveClaudeAccount("", "/home/zvi/personal/x"); name != "default" || !isDefault {
		t.Fatalf("path-only account treated as default: got (%q, isDefault=%v), want (\"default\", true)", name, isDefault)
	}

	// First matching account wins when two could match.
	a := ClaudeAccount{Name: "a", ConfigDir: "/a", RemoteMatches: []string{"acme"}}
	b := ClaudeAccount{Name: "b", ConfigDir: "/b", RemoteMatches: []string{"acme"}}
	c := &Config{ClaudeAccounts: []ClaudeAccount{a, b}}
	if name, _, _ := c.ResolveClaudeAccount("https://x/acme/r.git", ""); name != "a" {
		t.Fatalf("first-match-wins: got %q, want %q", name, "a")
	}

	// List order dominates the within-account remote-then-path order: when a target
	// hits an EARLIER account's path_matches AND a LATER account's remote_matches,
	// the earlier (path) account wins. This pins the per-account-in-order semantics
	// against a refactor into a global remote-first pass, which would flip the result.
	byPath := ClaudeAccount{Name: "byPath", ConfigDir: "/p", PathMatches: []string{"/acme/"}}
	byRemote := ClaudeAccount{Name: "byRemote", ConfigDir: "/r", RemoteMatches: []string{"acme"}}
	ordered := &Config{ClaudeAccounts: []ClaudeAccount{byPath, byRemote}}
	if name, _, _ := ordered.ResolveClaudeAccount("https://x/acme/r.git", "/home/acme/proj"); name != "byPath" {
		t.Fatalf("earlier path_matches must beat later remote_matches: got %q, want %q", name, "byPath")
	}
}

func TestGetProjectSearchRoots(t *testing.T) {
	cases := []struct {
		name  string
		roots []string
		want  []string
	}{
		{"nil (older config) defaults to home", nil, []string{"~"}},
		{"explicit empty defaults to home", []string{}, []string{"~"}},
		{"explicit roots returned as-is", []string{"~/work", "/srv/repos"}, []string{"~/work", "/srv/repos"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{ProjectSearchRoots: tc.roots}
			got := c.GetProjectSearchRoots()
			if len(got) != len(tc.want) {
				t.Fatalf("GetProjectSearchRoots() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("GetProjectSearchRoots() = %v, want %v", got, tc.want)
				}
			}
		})
	}

	// The default must be a copy: mutating the returned slice cannot poison
	// later callers.
	c := &Config{}
	c.GetProjectSearchRoots()[0] = "/mutated"
	if got := c.GetProjectSearchRoots(); got[0] != "~" {
		t.Fatalf("default roots shared/mutated: got %v", got)
	}

	// Explicit roots must be copied too: mutating the returned slice cannot
	// reach back into the Config's stored slice.
	c = &Config{ProjectSearchRoots: []string{"~/work"}}
	c.GetProjectSearchRoots()[0] = "/mutated"
	if got := c.GetProjectSearchRoots(); got[0] != "~/work" {
		t.Fatalf("explicit roots shared/mutated: got %v", got)
	}
}

func TestGetProjectSearchDepth(t *testing.T) {
	intp := func(i int) *int { return &i }
	cases := []struct {
		name  string
		depth *int
		want  int
	}{
		{"nil (older config) defaults", nil, 3},
		{"explicit zero disables", intp(0), 0},
		{"negative disables", intp(-2), 0},
		{"explicit value returned", intp(5), 5},
		{"absurd value clamped", intp(30), 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{ProjectSearchDepth: tc.depth}
			if got := c.GetProjectSearchDepth(); got != tc.want {
				t.Fatalf("GetProjectSearchDepth() = %d, want %d", got, tc.want)
			}
		})
	}
}
