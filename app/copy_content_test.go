package app

import (
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/actions"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// captureClipboard swaps the clipboard writer for one that records, restoring it
// after the test. Same package-var seam the existing copy-branch tests use.
func captureClipboard(t *testing.T) *string {
	t.Helper()
	var got string
	orig := actions.CopyToClipboard
	actions.CopyToClipboard = func(text string) error {
		got = text
		return nil
	}
	t.Cleanup(func() { actions.CopyToClipboard = orig })
	return &got
}

// TestCopyContent_DiffTabYieldsRawGitOutput: the rendered diff is colorized,
// tab-expanded and truncated to the pane width, so copying what is on screen would
// paste a mangled approximation. The copy takes the text the pane rendered FROM.
func TestCopyContent_DiffTabYieldsRawGitOutput(t *testing.T) {
	got := captureClipboard(t)
	spy := newFrameSpy("")
	h, inst := newCaptureHome(t, spy)
	h.tabbedWindow.SetActiveTab(ui.DiffTab)

	const raw = "diff --git a/main.go b/main.go\n@@ -1,3 +1,4 @@\n+\tif err != nil {\treturn err }\n"
	inst.SetDiffStats(&git.DiffStats{Content: raw})

	pressKey(h, 'Y')

	require.Equal(t, raw, *got, "the diff must be copied exactly as git wrote it")
	require.Contains(t, h.menu.NoticeText(), "diff copied")
}

// TestCopyContent_PaneTabStripsStyling: pane captures carry the agent's own ANSI
// (capture-pane -e), which is noise in a paste.
func TestCopyContent_PaneTabStripsStyling(t *testing.T) {
	got := captureClipboard(t)
	spy := newFrameSpy("")
	h, inst := newCaptureHome(t, spy)

	h.Update(paneFrameMsg{
		target: frameTarget{instance: inst},
		text:   "\x1b[32mbuild passed\x1b[0m\n\x1b[1m3 files changed\x1b[0m",
		at:     time.Now(),
	})

	pressKey(h, 'Y')

	require.Equal(t, "build passed\n3 files changed", *got, "the pane copy must be plain text")
	require.NotContains(t, *got, "\x1b", "no escape sequences may survive")
	require.Contains(t, h.menu.NoticeText(), "pane copied")
}

// TestCopyContent_SaysSoWhenThereIsNothing: copying "" and claiming success is
// worse than declining — the user would paste nothing and not know why.
func TestCopyContent_SaysSoWhenThereIsNothing(t *testing.T) {
	got := captureClipboard(t)
	spy := newFrameSpy("")
	h, _ := newCaptureHome(t, spy)
	h.tabbedWindow.SetActiveTab(ui.DiffTab)

	pressKey(h, 'Y')

	require.Empty(t, *got, "nothing must reach the clipboard")
	require.Contains(t, h.menu.NoticeText(), "nothing to copy")
}

// TestCopyContent_KeyIsWired presses the key through handleKeyPress, which is the
// one drift site with NO automated guard: a key can be declared, registered,
// documented, README'd and completely dead. Every assertion above depends on this
// one being true.
func TestCopyContent_KeyIsWired(t *testing.T) {
	got := captureClipboard(t)
	spy := newFrameSpy("")
	h, inst := newCaptureHome(t, spy)
	h.Update(paneFrameMsg{target: frameTarget{instance: inst}, text: "content", at: time.Now()})

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})

	require.Equal(t, "content", *got, "'Y' must reach copyPaneContent — nothing else asserts the case exists")
}

// TestCopyContent_AllowedWhileBusy: copying reads cached content and writes the
// clipboard, so there is nothing for an in-flight action to race — and a reviewer
// lifting a diff while a push runs is exactly when they want it.
func TestCopyContent_AllowedWhileBusy(t *testing.T) {
	got := captureClipboard(t)
	spy := newFrameSpy("")
	h, inst := newCaptureHome(t, spy)
	h.Update(paneFrameMsg{target: frameTarget{instance: inst}, text: "still copyable", at: time.Now()})
	h.beginAsyncAction("pushing…", nil)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})

	require.Equal(t, "still copyable", *got, "a read-only copy must not be swallowed by the busy gate")
	require.False(t, strings.Contains(h.menu.NoticeText(), "busy"))
}
