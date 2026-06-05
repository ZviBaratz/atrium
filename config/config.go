// Package config persists Atrium's two data-dir artifacts — config.json
// (Config: program, profiles, auto-attach) and state.json (State: serialized
// instances plus UI state) — and resolves the runtime identity (data dir, tmux
// socket, session prefix) shared with legacy claude-squad installs. See
// RuntimeName and GetConfigDir for the prefer-new/fall-back-to-legacy rules.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// ConfigFileName is the name of the config file inside the data dir.
	ConfigFileName = "config.json"
	defaultProgram = "claude"
	// shellProbeTimeout bounds the shell invocation GetClaudeCommand uses to
	// resolve the claude binary, so a hung profile script can't wedge startup.
	shellProbeTimeout = 10 * time.Second
)

// GetConfigDir returns the path to the application's data/config directory.
//
// It prefers the new ~/.atrium layout, falls back to an existing legacy
// ~/.claude-squad directory without moving it, and otherwise defaults to
// ~/.atrium for fresh installs. The directory holds config.json, state.json, and
// the worktrees/ tree; the worktree and tmux paths recorded inside are absolute,
// so a legacy install must keep using its existing directory rather than be
// migrated. See RuntimeName for the matching tmux/socket identifiers.
func GetConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config home directory: %w", err)
	}
	newDir := filepath.Join(homeDir, configDirName)
	if DirExists(newDir) {
		return newDir, nil
	}
	if legacy := filepath.Join(homeDir, legacyConfigDirName); DirExists(legacy) {
		return legacy, nil
	}
	return newDir, nil
}

// Profile represents a named program configuration
type Profile struct {
	Name    string `json:"name"`
	Program string `json:"program"`
}

// Config represents the application configuration
type Config struct {
	// DefaultProgram is the default program to run in new instances
	DefaultProgram string `json:"default_program"`
	// AutoYes is a flag to automatically accept all prompts.
	AutoYes bool `json:"auto_yes"`
	// DaemonPollInterval is the interval (ms) at which the daemon polls sessions for autoyes mode.
	DaemonPollInterval int `json:"daemon_poll_interval"`
	// BranchPrefix is the prefix used for git branches created by the application.
	BranchPrefix string `json:"branch_prefix"`
	// Profiles is a list of named program profiles.
	Profiles []Profile `json:"profiles,omitempty"`
	// TmuxConfigOverride, when set to an existing file path, is used as the tmux
	// config for cs sessions instead of the bundled managed config. When empty,
	// cs materializes and uses its own config.
	TmuxConfigOverride string `json:"tmux_config_override,omitempty"`
	// AutoAttach, when true, automatically attaches to a new session as soon as it
	// starts (and has no initial prompt). nil means use the default (on), so the
	// feature stays enabled for config files written before it existed.
	AutoAttach *bool `json:"auto_attach,omitempty"`
	// KillDoubleTapConfirm, when true, lets a second press of the kill key (Ctrl+X)
	// confirm the kill dialog, so Ctrl+X Ctrl+X tears a session down in one motion.
	// nil means use the default (on), so configs written before it existed keep it.
	KillDoubleTapConfirm *bool `json:"kill_double_tap_confirm,omitempty"`
	// Theme selects the UI color/glyph theme by name (see ui/theme registry:
	// "tokyo-night", "catppuccin-mocha", "unicode"). Empty falls back to the
	// default. The "unicode" theme avoids Nerd-Font glyphs for terminals
	// without a patched font.
	Theme string `json:"theme,omitempty"`
	// SessionContextBar, when true, renders a thin tmux status line inside each
	// attached session (name · repo · branch · status + a strip of sibling
	// sessions in the same repo group). nil means use the default (on), so the
	// feature stays enabled for config files written before it existed. Setting
	// it false restores the chrome-free fullscreen pane (tmux status off).
	SessionContextBar *bool `json:"session_context_bar,omitempty"`
	// HintBar, when true, keeps a one-line key-hint bar at the bottom of the
	// screen during plain navigation. nil means use the default (on). Setting it
	// false restores the chrome-free interface, where the bar appears only for
	// inline interactions that need it (naming, filtering, progress).
	HintBar *bool `json:"hint_bar,omitempty"`
	// MaxSessions caps how many sessions can exist at once; creating one beyond
	// it is rejected with an error in the UI. nil (or a non-positive value)
	// means use DefaultMaxSessions, so older config files keep the old cap.
	MaxSessions *int `json:"max_sessions,omitempty"`
}

// DefaultMaxSessions is the session cap used when MaxSessions is unset.
const DefaultMaxSessions = 10

// GetMaxSessions returns the configured session cap, falling back to
// DefaultMaxSessions for a nil or non-positive value.
func (c *Config) GetMaxSessions() int {
	if c.MaxSessions == nil || *c.MaxSessions < 1 {
		return DefaultMaxSessions
	}
	return *c.MaxSessions
}

// GetSessionContextBar reports whether attached sessions should render the
// in-session context status line. A nil SessionContextBar (e.g. an older config
// file with no such key) defaults to on, mirroring GetAutoAttach.
func (c *Config) GetSessionContextBar() bool {
	return c.SessionContextBar == nil || *c.SessionContextBar
}

// GetHintBar reports whether the always-on bottom hint bar is enabled. A nil
// HintBar (e.g. an older config file with no such key) defaults to on.
func (c *Config) GetHintBar() bool {
	return c.HintBar == nil || *c.HintBar
}

// GetAutoAttach reports whether new sessions should auto-attach on creation.
// A nil AutoAttach (e.g. an older config file with no such key) defaults to on.
func (c *Config) GetAutoAttach() bool {
	return c.AutoAttach == nil || *c.AutoAttach
}

// GetKillDoubleTapConfirm reports whether a second press of the kill key confirms
// the kill dialog. A nil KillDoubleTapConfirm (e.g. an older config file with no
// such key) defaults to on.
func (c *Config) GetKillDoubleTapConfirm() bool {
	return c.KillDoubleTapConfirm == nil || *c.KillDoubleTapConfirm
}

// GetProgram returns the program to run. If Profiles is non-empty and
// DefaultProgram matches a profile name, that profile's Program is returned.
// Otherwise DefaultProgram is returned as-is.
func (c *Config) GetProgram() string {
	for _, p := range c.Profiles {
		if p.Name == c.DefaultProgram {
			return p.Program
		}
	}
	return c.DefaultProgram
}

// GetProfiles returns a unified list of profiles. If Profiles is defined,
// those are returned with the default profile first. Otherwise, a single
// profile is synthesized from DefaultProgram.
func (c *Config) GetProfiles() []Profile {
	if len(c.Profiles) == 0 {
		return []Profile{{Name: c.DefaultProgram, Program: c.DefaultProgram}}
	}
	// Reorder so the default profile comes first.
	profiles := make([]Profile, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		if p.Name == c.DefaultProgram {
			profiles = append(profiles, p)
			break
		}
	}
	for _, p := range c.Profiles {
		if p.Name != c.DefaultProgram {
			profiles = append(profiles, p)
		}
	}
	return profiles
}

// DefaultConfig returns the built-in defaults without probing the machine: no
// profiles, and the bare "claude" literal as the program. It is what tests
// construct directly (hermetic — no PATH lookups or shell spawns), while every
// LoadConfig fallback goes through seededDefaultConfig so a real user always
// gets detection.
func DefaultConfig() *Config {
	autoAttach := true
	killDoubleTap := true
	sessionContextBar := true
	hintBar := true
	maxSessions := DefaultMaxSessions
	return &Config{
		DefaultProgram:     defaultProgram,
		AutoYes:            false,
		DaemonPollInterval: 1000,
		Theme:              "tokyo-night",
		SessionContextBar:  &sessionContextBar,
		HintBar:            &hintBar,
		BranchPrefix: func() string {
			user, err := user.Current()
			if err != nil || user == nil || user.Username == "" {
				log.ErrorLog.Printf("failed to get current user: %v", err)
				return "session/"
			}
			return fmt.Sprintf("%s/", strings.ToLower(user.Username))
		}(),
		AutoAttach:           &autoAttach,
		KillDoubleTapConfirm: &killDoubleTap,
		MaxSessions:          &maxSessions,
	}
}

// seededDefaultConfig is DefaultConfig with profiles seeded from the installed
// agent CLIs so the create-form picker works out of the box; claude leads when
// present (it is first in knownAgentBins) and becomes the default program,
// falling back to the bare "claude" literal when nothing is detected (the
// historical behavior for a machine with no agents yet). It is the config a
// user actually receives from every LoadConfig fallback — kept separate from
// DefaultConfig so tests constructing defaults never probe the machine.
func seededDefaultConfig() *Config {
	cfg := DefaultConfig()
	cfg.Profiles = DetectAgentProfiles()
	if len(cfg.Profiles) > 0 {
		cfg.DefaultProgram = cfg.Profiles[0].Name
	} else {
		log.WarningLog.Printf("no agent CLIs detected; defaulting program to %q", defaultProgram)
	}
	return cfg
}

// GetClaudeCommand attempts to find the "claude" command in the user's shell
// It checks in the following order:
// 1. Shell alias resolution: using "which" command
// 2. PATH lookup
//
// If both fail, it returns an error.
func GetClaudeCommand() (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash" // Default to bash if SHELL is not set
	}

	// Force the shell to load the user's profile and then run the command
	// For zsh, source .zshrc; for bash, source .bashrc
	var shellCmd string
	if strings.Contains(shell, "zsh") {
		shellCmd = "source ~/.zshrc &>/dev/null || true; which claude"
	} else if strings.Contains(shell, "bash") {
		shellCmd = "source ~/.bashrc &>/dev/null || true; which claude"
	} else {
		shellCmd = "which claude"
	}

	// One-shot startup probe with no ctx-bearing caller (config load runs before
	// any lifecycle context exists); Background capped at the probe timeout is
	// deliberate.
	ctx, cancel := context.WithTimeout(context.Background(), shellProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-c", shellCmd)
	output, err := cmd.Output()
	if err == nil {
		if program, ok := resolveClaudeCandidate(string(output)); ok {
			return program, nil
		}
	}

	// Otherwise, try to find in PATH directly
	claudePath, err := exec.LookPath("claude")
	if err == nil {
		return claudePath, nil
	}

	return "", fmt.Errorf("claude command not found in aliases or PATH")
}

// resolveClaudeCandidate interprets the output of `which claude` and returns a
// usable program path. The output may be a plain path, an alias definition
// (e.g. "claude: aliased to /usr/local/bin/claude"), or — when `claude` is a
// shell function — the full multi-line function body. We extract the alias
// target when present, then require the result to resolve to a real executable
// via exec.LookPath. If it does not (as happens with a function body, where the
// alias regex can capture a non-path token such as "$?"), we report no match so
// the caller falls back to a direct PATH lookup instead of persisting an
// unrunnable program as default_program — which otherwise causes new sessions to
// fail with "timed out waiting for tmux session ... (cleanup error: ...)".
func resolveClaudeCandidate(whichOutput string) (string, bool) {
	path := strings.TrimSpace(whichOutput)
	if path == "" {
		return "", false
	}

	// A shell function prints its entire multi-line body through `which`; that is
	// never a usable program path, and running the alias regex over it can capture
	// a stray token that happens to resolve (e.g. a binary name from an inline
	// "VAR=cmd" prefix, or "$?" from "local ret=$?"). Anything spanning multiple
	// lines is not a path, so reject it here and let the caller fall back to the
	// direct PATH lookup.
	if strings.ContainsAny(path, "\n\r") {
		return "", false
	}

	// Extract the target if the output is an alias definition.
	// Handle formats like "claude: aliased to /path/to/claude" or other shell-specific formats.
	aliasRegex := regexp.MustCompile(`(?:aliased to|->|=)\s*([^\s]+)`)
	if matches := aliasRegex.FindStringSubmatch(path); len(matches) > 1 {
		path = matches[1]
	}

	// Only trust the candidate if it actually resolves to an executable.
	if resolved, lookErr := exec.LookPath(path); lookErr == nil {
		return resolved, true
	}
	return "", false
}

// LoadConfig reads config.json from the data dir. It never fails: a missing
// file is created with defaults, and any read/parse error logs a warning and
// falls back to DefaultConfig.
func LoadConfig() *Config {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return seededDefaultConfig()
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create and save default config if file doesn't exist
			defaultCfg := seededDefaultConfig()
			if saveErr := saveConfig(defaultCfg); saveErr != nil {
				log.WarningLog.Printf("failed to save default config: %v", saveErr)
			}
			return defaultCfg
		}

		log.WarningLog.Printf("failed to get config file: %v", err)
		return seededDefaultConfig()
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		log.ErrorLog.Printf("failed to parse config file: %v", err)
		return seededDefaultConfig()
	}

	return &config
}

// saveConfig saves the configuration to disk
func saveConfig(config *Config) error {
	configDir, err := GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0644)
}

// SaveConfig exports the saveConfig function for use by other packages
func SaveConfig(config *Config) error {
	return saveConfig(config)
}
