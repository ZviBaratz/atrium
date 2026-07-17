package actions

import (
	"fmt"
	"io"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// CopyToClipboard writes text to the system clipboard. It is a package var so
// tests (and alternate front ends) can substitute a fake without touching the
// host clipboard. The default, copyToClipboard, drives two independent legs: an
// OSC 52 escape emitted to the terminal — which crosses SSH, where the remote
// has no clipboard binary — and the exec-based OS copier (xclip/xsel/pbcopy),
// which lands when the terminal ignores OSC 52. Either leg succeeding is a copy.
var CopyToClipboard = copyToClipboard

// execCopy is the OS-clipboard leg: atotto/clipboard shells out to
// xclip/xsel/wl-copy/pbcopy. A package var so tests can exercise the fallback
// (and its failure) without a real clipboard utility on PATH.
var execCopy = clipboard.WriteAll

// clipboardOutput is the terminal writer OSC 52 sequences are emitted to — the
// TUI's own output, wired once by SetClipboardOutput before the render loop
// starts. It stays nil in tests and any non-TUI caller, where the OSC 52 leg is
// skipped and only execCopy runs.
var clipboardOutput io.Writer

// SetClipboardOutput wires w as the terminal the OSC 52 clipboard escape is
// written to (the program's stdout). It is called once at startup, before the
// event loop, so it needs no synchronization against the copies that later fire
// on the UI goroutine. A nil w disables the OSC 52 leg.
func SetClipboardOutput(w io.Writer) { clipboardOutput = w }

// ClipboardOSC52 is the OSC 52 "set system clipboard" escape for text
// (ESC ] 52 ; c ; <base64> BEL). Pure, so the emitted bytes are unit-testable
// and the value is the same one copyToClipboard writes to the terminal.
func ClipboardOSC52(text string) string { return ansi.SetSystemClipboard(text) }

// copyToClipboard copies text over both legs, so it lands whether the user is
// local (execCopy) or on the far side of an SSH session (OSC 52). It reports
// failure only when neither leg could deliver, and then the error names the
// next step so a stuck copy is actionable rather than silent.
func copyToClipboard(text string) error {
	emittedOSC := emitClipboardOSC52(text)
	execErr := execCopy(text)
	if emittedOSC || execErr == nil {
		return nil
	}
	return fmt.Errorf(
		"could not copy: no clipboard utility (%w) and no terminal wired for OSC 52 — install xclip/xsel/wl-clipboard, or run inside a terminal with clipboard (OSC 52) support",
		execErr)
}

// emitClipboardOSC52 writes the OSC 52 escape for text to the wired terminal,
// reporting whether it was emitted (a writer is present and the write did not
// error). A nil writer (no TUI attached, e.g. tests) or a write error means the
// OSC 52 leg did not deliver and the caller must fall back to the exec leg.
func emitClipboardOSC52(text string) bool {
	w := clipboardOutput
	if w == nil {
		return false
	}
	_, err := io.WriteString(w, ClipboardOSC52(text))
	return err == nil
}
