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
	"sync/atomic"
	"text/template"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
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
//
// configOverridePath and managedConfigInvalid are atomic for the same reason
// agentOOMMargin is (see oom.go): Init no longer runs only at startup. A live theme
// change re-materializes the managed conf from a tea.Cmd goroutine
// (RewriteManagedConfig, barstyle.go) while tmuxConfigPath reads both from whatever
// goroutine is launching or polling a session — every tmux subprocess crosses
// tmuxCommand, and the 500ms metadata sweep fans out one goroutine per instance.
var configOverridePath atomic.Pointer[string]

// managedConfigInvalid is set by Init when the config it just wrote fails to parse.
// tmuxConfigPath then omits -f so sessions fall back to tmux defaults (degraded, but
// usable) instead of being blocked: a config tmux refuses to load otherwise surfaces
// only when a client attaches, locking the user out of the pane.
var managedConfigInvalid atomic.Bool

// overrideConfigPath returns the configured override, "" when none is set.
func overrideConfigPath() string {
	if p := configOverridePath.Load(); p != nil {
		return *p
	}
	return ""
}

// WheelScrollLines is how many lines one mouse-wheel notch scrolls, in BOTH surfaces a
// notch can land on: the tmux pane of an attached session (rendered into the managed
// config's copy-mode-vi wheel bindings, overriding tmux's default of 5) and the preview
// pane in the session list (app.wheelScrollLines aliases this). A notch moves several
// lines for a fluid feel, where the keyboard scroll keys move one for precise positioning.
//
// It lives here, not in app, because the template that consumes it is embedded in this
// package and app already imports it — the reverse direction would be an import cycle.
// Single-sourcing it is the point: the value used to be spelled independently in the
// template and in app, with a comment in each claiming they matched, so changing one
// silently made a notch travel a different distance in a pane than in the preview.
const WheelScrollLines = 3

// renderManagedConfig renders the embedded template. ContextBar toggles the header
// strip; BarBg/BarFg fill that strip's full-width background so the header reads as
// a distinct band over the agent's near-black pane. Those two come from
// barStyleColours rather than being read here, because the live push in barstyle.go
// sets the same tmux option and the two must not drift — normally the theme's
// dedicated header-bar token (a slate a clear step above BgElevated), and "default"
// under NO_COLOR. WheelScrollLines is interpolated rather than written into the
// template so it cannot drift from the preview pane's notch distance.
func renderManagedConfig(contextBar bool) ([]byte, error) {
	tmpl, err := template.New("atrium.conf").Parse(embeddedTmuxConfigTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse managed tmux config template: %w", err)
	}
	barBg, barFg := barStyleColours()
	data := struct {
		ContextBar       bool
		BarBg, BarFg     string
		WheelScrollLines int
	}{
		ContextBar:       contextBar,
		BarBg:            barBg,
		BarFg:            barFg,
		WheelScrollLines: WheelScrollLines,
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
// overwritten on every launch so it stays in sync with the binary.
//
// Called at startup and again whenever a live setting invalidates the rendered conf
// (session_context_bar, tmux_config_override, and a theme change via
// RewriteManagedConfig). It is idempotent, safe to call from both the TUI and daemon
// processes, and safe off the update thread: the state it publishes is atomic and the
// file is swapped by rename, so a session launching concurrently reads either the old
// conf or the new one, never a half-written one.
func Init(override string, contextBar bool) error {
	// Record whether tmux is too old for `new-session -e` (see MinVersion). Done here
	// because Init already runs once at startup, off the update thread, and already
	// shells out; Available then stays a pure atomic read. The verdict is *stored, never
	// returned*: main.go only logs Init's error, but app_layout.go routes it to
	// handleError, so returning too-old here would fire a modal on an unrelated
	// session_context_bar toggle.
	//
	// It must not short-circuit the rest of Init either. The managed conf is still
	// written and validated on a too-old tmux — the two failures are independent, and
	// skipping the conf would disable titles/mouse/clipboard on exactly the host whose
	// log is already confusing enough.
	probeVersionOnce()
	configOverridePath.Store(&override)
	managedConfigInvalid.Store(false)
	if override != "" {
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
	if err := writeFileAtomic(path, rendered); err != nil {
		return err
	}
	if err := validateConfig(path); err != nil {
		log.ErrorLog.Printf("managed tmux config did not parse; starting sessions without it "+
			"(custom titles/mouse/clipboard/status bar disabled until fixed): %v", err)
		managedConfigInvalid.Store(true)
	}
	return nil
}

// writeFileAtomic materializes content at path via a temp file in the same directory
// plus a rename, which is atomic on POSIX. A plain os.WriteFile truncates in place, so
// a session started mid-rewrite could hand `tmux -f` a partial config — a parse error
// that only surfaces when the user attaches. Init runs off the update thread now, so
// that window is reachable rather than theoretical.
func writeFileAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// probeSocketCounter distinguishes two probe sockets minted by the same process.
// Init is documented safe to call off the update thread and fires on every live
// session_context_bar / tmux_config_override / theme change, so two validateConfig
// calls can be in flight at once (barstyle_test.go drives eight); the PID alone would
// give every one of them the same name.
var probeSocketCounter atomic.Uint64

// probeSocketName returns a tmux socket name no other validateConfig call — in this
// process or any other — can mint. That uniqueness is what makes the teardown in
// validateConfig safe to arm before the server starts and safe to unlink after it
// dies: both act on a socket only this invocation can name.
//
// A shared name was not merely untidy. Two concurrent Inits landed on one probe
// server; whichever finished first killed it, the other's source-file then failed with
// "no server running", and Init records that as a parse error — silently launching
// every later session with no custom titles, mouse, clipboard or status bar.
//
// Length is bounded and small: the longest form is a legacy install's
// "claudesquad-precheck-<pid>-<n>", which still fits sockaddr_un's sun_path (104 on
// darwin, 108 on linux) under the socket roots internal/testutil mints. That budget is
// asserted in testutil's TestSandboxInstallsAPrivateTmuxRoot.
func probeSocketName() string {
	return fmt.Sprintf("%s-precheck-%d-%d", socketName(), os.Getpid(), probeSocketCounter.Add(1))
}

// validateConfig asks a throwaway tmux server to parse path via source-file, the only
// way to surface a tmux config parse error synchronously: a detached `new-session -d`
// returns success and defers the error until a client attaches. It is best-effort —
// if tmux is absent or a probe server can't be started (reasons unrelated to parsing),
// it returns nil so a usable config is never disabled over an unrelated hiccup. The
// check runs on a "-precheck-<pid>-<n>" socket of its own (probeSocketName) so it
// never touches the live server or its sessions, and never a concurrent probe either.
//
// The probe deliberately omits -e: it is checking the *config*, not the tmux version, so
// on a tmux below MinVersion it succeeds and -f is still passed — and only the real
// `new-session -e` (tmux.go) fails. This asymmetry is intentional. The managed conf is
// cosmetic and recoverable, so it fails open (managedConfigInvalid, above); -e is
// load-bearing and unrecoverable, so the version floor fails closed in Available.
// A passing precheck is therefore not evidence that a real session start will work.
func validateConfig(path string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		// No tmux on PATH: the real session start fails identically with or without
		// -f, so there is nothing to fall back to.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxOpTimeout)
	defer cancel()
	sock := probeSocketName()
	// The socket file tmux binds, filled in once the server is up. tmux never unlinks
	// it when the server dies, so without the removal below every probe leaves a file
	// behind — in the very directory that holds the live socket (#331, and #547's
	// class (a): /tmp/tmux-<uid>/atrium-precheck was sitting next to the live socket in
	// production).
	var probePath string
	// Armed before the server starts, not after: a new-session that half-succeeds —
	// server up, command failed — used to leave nothing to tear it down. Arming it this
	// early is safe only because sock is unique; under the old shared name this
	// kill-server would have reached a concurrent Init's live probe.
	defer func() {
		// Always tear the probe server down, even if ctx already expired — so use a
		// fresh short-lived context rather than the (possibly cancelled) parent.
		killCtx, killCancel := context.WithTimeout(context.Background(), tmuxOpTimeout)
		defer killCancel()
		_ = exec.CommandContext(killCtx, "tmux", "-L", sock, "kill-server").Run()
		// Unlinked unconditionally after the kill, unlike internal/testutil's teardown,
		// which confirms the server is gone first. The difference is what the socket
		// fronts: there a $SHELL that never exits, so unlinking a survivor strands it
		// forever (#547); here `sleep 60` under tmux's default exit-empty, so the worst
		// case is a socket-less server that retires itself within a minute holding
		// nothing.
		if probePath != "" && filepath.Base(probePath) == sock {
			_ = os.Remove(probePath)
		}
	}()
	// Start a clean server (no -f) and keep it alive with a session so source-file has
	// a server to run against.
	start := exec.CommandContext(ctx, "tmux", "-L", sock, "new-session", "-d", "sleep 60")
	if err := start.Run(); err != nil {
		return nil
	}
	// Ask tmux where it put the socket rather than reconstructing
	// $TMUX_TMPDIR/tmux-<uid>/<sock> — that layout is tmux's to change, not ours (#331).
	// #{socket_path} is tmux 2.2+ and MinVersion is 3.2 (enforced before any session
	// starts), so it is present wherever this code can run. A failure here costs one
	// stale socket file, never a wrong unlink: probePath stays empty and the guard
	// above skips.
	if out, err := exec.CommandContext(ctx, "tmux", "-L", sock,
		"display-message", "-p", "#{socket_path}").Output(); err == nil {
		probePath = strings.TrimSpace(string(out))
	}
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
	if override := overrideConfigPath(); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override
		}
	}
	if managedConfigInvalid.Load() {
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
