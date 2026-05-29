package tmux

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/ZviBaratz/atrium/config"
)

// socketName is the dedicated tmux socket Atrium runs all of its sessions on.
// Using a private socket (tmux -L) gives Atrium a fresh server, which is what
// makes the -f config below take effect (tmux only reads -f when a server starts)
// and keeps Atrium sessions out of the user's default `tmux ls`. It derives from
// config.RuntimeName so legacy installs stay on the "claudesquad" socket and keep
// their live sessions reachable after the rebrand.
func socketName() string {
	return config.RuntimeName()
}

// managedConfigFileName is the managed config materialized under the config dir,
// named to match the active brand (atrium.conf / claudesquad.conf).
func managedConfigFileName() string {
	return config.RuntimeName() + ".conf"
}

//go:embed atrium.conf
var embeddedTmuxConfig []byte

// configOverridePath, when non-empty and pointing at an existing file, is used as
// the tmux config instead of the managed one. Set via Init from config.json.
var configOverridePath string

// Init records an optional user-supplied tmux config override and, when none is
// set, materializes the bundled config into the config dir. The managed file is
// overwritten on every launch so it stays in sync with the binary. Call once at
// startup; it is idempotent and safe to call from both the TUI and daemon
// processes.
func Init(overridePath string) error {
	configOverridePath = overridePath
	if overridePath != "" {
		return nil
	}
	dir, err := config.GetConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, managedConfigFileName()), embeddedTmuxConfig, 0o644)
}

// tmuxConfigPath returns the path to pass via tmux -f: the override when it is set
// and exists, otherwise the managed file when it exists. Returns "" when neither
// is available — the command helper then omits -f and relies on socket isolation
// alone. The existence check matters: `tmux -f <missing-file>` fails outright, so
// we must never pass a path that isn't on disk (e.g. if Init failed to write it).
func tmuxConfigPath() string {
	if configOverridePath != "" {
		if _, err := os.Stat(configOverridePath); err == nil {
			return configOverridePath
		}
	}
	dir, err := config.GetConfigDir()
	if err != nil {
		return ""
	}
	managed := filepath.Join(dir, managedConfigFileName())
	if _, err := os.Stat(managed); err != nil {
		return ""
	}
	return managed
}
