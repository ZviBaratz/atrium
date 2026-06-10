// Package actions holds the OS-integration actions Atrium fires on user
// gestures — copying text to the system clipboard and opening a URL in the
// browser. It has no UI or tmux dependencies, so any front end can share it.
package actions

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode"
)

// OpenInBrowser launches the user's opener on a URL, detached from the
// caller's terminal. Package var so tests can substitute a fake (same
// pattern as CopyToClipboard).
var OpenInBrowser = openDetached

// linuxOpeners are tried in order on non-darwin systems; wslview (from wslu)
// covers WSL, where xdg-open is typically absent.
var linuxOpeners = []string{"xdg-open", "x-www-browser", "wslview"}

// chooseOpener picks the opener command for goos using lookPath. Split out
// and parameterized so the selection logic is testable without the host's
// actual binaries.
func chooseOpener(goos string, lookPath func(string) (string, error)) (string, error) {
	if goos == "darwin" {
		return "open", nil
	}
	for _, c := range linuxOpeners {
		if _, err := lookPath(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no URL opener found (tried %v)", linuxOpeners)
}

// OpenableURL reports whether target is worth handing to a browser opener:
// web pages and local files open something useful; ssh/git URLs and
// scp-style remotes do not, so their hints degrade to copy upstream.
func OpenableURL(target string) bool {
	return strings.HasPrefix(target, "http://") ||
		strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "file://")
}

// openDetached starts the opener and reaps it in the background. A failure to
// start surfaces to the caller; the opener's own exit status does not — by
// then the caller has moved on and the browser owns the outcome.
func openDetached(target string) error {
	// Pane content is untrusted: a crafted markdown link like [x](-flag) would
	// otherwise smuggle a flag into the opener's argv.
	if strings.HasPrefix(target, "-") {
		return fmt.Errorf("refusing to open %q: looks like a flag, not a URL", target)
	}
	// A control byte means an escape-stripping gap upstream; refuse rather
	// than launch something mangled.
	if strings.ContainsFunc(target, unicode.IsControl) {
		return fmt.Errorf("refusing to open %q: contains control bytes", target)
	}
	opener, err := chooseOpener(runtime.GOOS, exec.LookPath)
	if err != nil {
		return err
	}
	// context.Background on purpose: the open is detached from the caller's
	// lifecycle, so there is no parent context that should cancel it.
	cmd := exec.CommandContext(context.Background(), opener, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
