package config

import (
	"os"
	"path/filepath"
)

const (
	// runtimeName is the current brand's base identifier. It names the config
	// directory (~/.atrium), the dedicated tmux socket, the session-name prefix,
	// and the managed tmux config file.
	runtimeName = "atrium"
	// legacyRuntimeName is the pre-rebrand identifier. Installs that predate the
	// rename keep using it so their live tmux sessions and worktrees — whose
	// absolute paths are baked into git and state.json — stay reachable.
	legacyRuntimeName = "claudesquad"

	configDirName       = ".atrium"
	legacyConfigDirName = ".claude-squad"
)

// RuntimeName reports the brand identifier matching the active data home: the new
// "atrium" for fresh installs, or legacy "claudesquad" when only ~/.claude-squad
// exists. The tmux socket, session prefix, and managed config filename all derive
// from this, so an install is consistently new or legacy and existing sessions
// remain reachable after the rebrand.
func RuntimeName() string {
	dir, err := GetConfigDir()
	if err == nil && filepath.Base(dir) == legacyConfigDirName {
		return legacyRuntimeName
	}
	return runtimeName
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
