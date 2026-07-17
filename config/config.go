// Package config persists Atrium's two data-dir artifacts — config.json
// (Config: program, profiles, auto-attach) and state.json (State: serialized
// instances plus UI state) — and resolves the runtime identity (data dir, tmux
// socket, session prefix) shared with legacy claude-squad installs. See
// RuntimeName and GetConfigDir for the prefer-new/fall-back-to-legacy rules.
//
// The package is split by concern: this file holds the data-dir paths and the
// built-in default construction; types.go the Config type and its routing/account
// types; accessors.go the normalizing Config getters; accounts.go the per-session
// account routing; detect.go the claude-binary shell probe; persist.go config.json
// load/save.
package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/log"
)

const (
	// ConfigFileName is the name of the config file inside the data dir.
	ConfigFileName = "config.json"
	defaultProgram = "claude"
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

// WorktreesDir returns the directory that holds every session worktree:
// <config dir>/worktrees. It is the single source of truth for that path —
// session/git materializes worktrees under it, and the Claude workspace-trust
// shim trusts it — so it must always derive from GetConfigDir (never a
// hardcoded ~/.atrium or ~/.claude-squad).
func WorktreesDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "worktrees"), nil
}

// DefaultDaemonPollIntervalMs is the built-in autoyes daemon poll interval in
// milliseconds. It is the value DefaultConfig seeds and the floor the daemon
// falls back to when a loaded config carries a non-positive interval (the field
// absent from a legacy or hand-edited config.json, or set <= 0) that would
// otherwise panic time.NewTicker.
const DefaultDaemonPollIntervalMs = 1000

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
	mouse := true
	osChrome := true
	showReleaseNotes := true
	updateBaseOnCreate := true
	recordPromptHistory := true
	return &Config{
		DefaultProgram:      defaultProgram,
		AutoYes:             false,
		DaemonPollInterval:  DefaultDaemonPollIntervalMs,
		Theme:               "tokyo-night",
		SessionContextBar:   &sessionContextBar,
		HintBar:             &hintBar,
		Mouse:               &mouse,
		RecordPromptHistory: &recordPromptHistory,
		OSChrome:            &osChrome,
		BranchPrefix: func() string {
			user, err := user.Current()
			if err != nil || user == nil || user.Username == "" {
				log.ErrorLog.Printf("failed to get current user: %v", err)
				return "session/"
			}
			return fmt.Sprintf("%s/", strings.ToLower(user.Username))
		}(),
		AutoAttach:                  &autoAttach,
		KillDoubleTapConfirm:        &killDoubleTap,
		ShowReleaseNotesAfterUpdate: &showReleaseNotes,
		UpdateBaseOnCreate:          &updateBaseOnCreate,
		CarryFiles:                  append([]string(nil), defaultCarryFiles...),
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
