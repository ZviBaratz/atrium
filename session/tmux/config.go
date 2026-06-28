package tmux

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui/theme"
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

//go:embed atrium.conf.tmpl
var embeddedTmuxConfigTemplate string

// configOverridePath, when non-empty and pointing at an existing file, is used as
// the tmux config instead of the managed one. Set via Init from config.json.
var configOverridePath string

// managedConfigInvalid is set by Init when the config it just wrote fails to parse.
// tmuxConfigPath then omits -f so sessions fall back to tmux defaults (degraded, but
// usable) instead of being blocked: a config tmux refuses to load otherwise surfaces
// only when a client attaches, locking the user out of the pane.
var managedConfigInvalid bool

// renderManagedConfig renders the embedded template. ContextBar toggles the header
// strip; BarBg/BarFg fill that strip's full-width background from the active theme's
// dedicated header-bar token (a slate a clear step above BgElevated) so the header
// reads as a distinct band over the agent's near-black pane.
func renderManagedConfig(contextBar bool) ([]byte, error) {
	tmpl, err := template.New("atrium.conf").Parse(embeddedTmuxConfigTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse managed tmux config template: %w", err)
	}
	th := theme.Current()
	data := struct {
		ContextBar   bool
		BarBg, BarFg string
	}{
		ContextBar: contextBar,
		BarBg:      string(th.Palette.BarBg),
		BarFg:      string(th.Palette.Fg),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to render managed tmux config: %w", err)
	}
	return buf.Bytes(), nil
}

// Init records an optional user-supplied tmux config override and, when none is
// set, materializes the bundled config into the config dir. contextBar toggles the
// in-session status line (config session_context_bar). The managed file is
// overwritten on every launch so it stays in sync with the binary. Call once at
// startup; it is idempotent and safe to call from both the TUI and daemon
// processes.
func Init(overridePath string, contextBar bool) error {
	configOverridePath = overridePath
	managedConfigInvalid = false
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
	rendered, err := renderManagedConfig(contextBar)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, managedConfigFileName())
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return err
	}
	if err := validateConfig(path); err != nil {
		log.ErrorLog.Printf("managed tmux config did not parse; starting sessions without it "+
			"(custom titles/mouse/clipboard/status bar disabled until fixed): %v", err)
		managedConfigInvalid = true
	}
	return nil
}

// validateConfig asks a throwaway tmux server to parse path via source-file, the only
// way to surface a tmux config parse error synchronously: a detached `new-session -d`
// returns success and defers the error until a client attaches. It is best-effort —
// if tmux is absent or a probe server can't be started (reasons unrelated to parsing),
// it returns nil so a usable config is never disabled over an unrelated hiccup. The
// check runs on a dedicated "-precheck" socket so it never touches the live server or
// its sessions.
func validateConfig(path string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		// No tmux on PATH: the real session start fails identically with or without
		// -f, so there is nothing to fall back to.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxOpTimeout)
	defer cancel()
	sock := socketName() + "-precheck"
	// Start a clean server (no -f) and keep it alive with a session so source-file has
	// a server to run against.
	start := exec.CommandContext(ctx, "tmux", "-L", sock, "new-session", "-d", "sleep 60")
	if err := start.Run(); err != nil {
		return nil
	}
	defer func() {
		// Always tear the probe server down, even if ctx already expired — so use a
		// fresh short-lived context rather than the (possibly cancelled) parent.
		killCtx, killCancel := context.WithTimeout(context.Background(), tmuxOpTimeout)
		defer killCancel()
		_ = exec.CommandContext(killCtx, "tmux", "-L", sock, "kill-server").Run()
	}()
	out, err := exec.CommandContext(ctx, "tmux", "-L", sock, "source-file", path).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
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
	if managedConfigInvalid {
		// Init found the managed config unparseable; omit -f and let sessions run on
		// the isolated socket with tmux defaults rather than failing on attach.
		return ""
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
