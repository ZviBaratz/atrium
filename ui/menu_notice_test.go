package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

// A notice temporarily replaces the hint line in the menu's reserved row, so
// transient feedback never changes the frame's height. Clearing it restores
// the hints.
func TestMenu_NoticeReplacesHints(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 1)
	m.SetState(StateEmpty)

	require.Contains(t, m.String(), "new", "sanity: empty-state hints visible before the notice")

	m.SetNotice("branch 'zvi/foo' copied", NoticeInfo)
	out := m.String()
	require.Contains(t, out, "branch 'zvi/foo' copied")
	require.NotContains(t, out, "new", "hints must yield the row to the notice")
	require.True(t, m.HasNotice())

	m.ClearNotice()
	require.Contains(t, m.String(), "new", "hints return once the notice clears")
	require.False(t, m.HasNotice())
}

// Error-level notices ride the same row; the menu reports content for both
// levels so the hide timer can clear either.
func TestMenu_ErrorNoticeShown(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 1)
	m.SetState(StateDefault)

	m.SetNotice("session is paused — r to resume", NoticeError)
	require.Contains(t, m.String(), "session is paused")
	require.True(t, m.HasNotice())
}

// Notices truncate to the menu width instead of wrapping: a wrapped notice
// would grow the row and cause exactly the layout shift the mechanism exists
// to prevent.
func TestMenu_NoticeTruncatesToWidth(t *testing.T) {
	m := NewMenu()
	m.SetSize(20, 1)
	m.SetState(StateEmpty)

	m.SetNotice("this notice is far wider than the twenty columns available", NoticeInfo)
	out := m.String()
	require.Equal(t, 1, lipgloss.Height(out), "notice must stay a single row")
	require.LessOrEqual(t, lipgloss.Width(out), 20, "notice must not exceed the menu width")
}

// quiet blanks the always-on hint sets so chrome-free mode (hint_bar off) keeps
// the reserved row but shows nothing on it. A notice still overrides the blank,
// and contextual states (Busy/…) still render — they carry unique info even with
// the bar off.
func TestMenu_QuietBlanksHintsNotNotices(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 1)
	m.SetQuiet(true)

	m.SetState(StateDefault)
	require.Empty(t, strings.TrimSpace(m.String()), "quiet blanks the default hint line")
	require.Equal(t, 1, lipgloss.Height(m.String()), "the reserved row is still exactly one line")

	m.SetState(StateEmpty)
	require.Empty(t, strings.TrimSpace(m.String()), "quiet blanks the empty hint line too")

	// A notice overrides the blank.
	m.SetNotice("branch copied", NoticeInfo)
	require.Contains(t, m.String(), "branch copied", "a notice still shows despite quiet")
	m.ClearNotice()

	// Contextual states still render — they are forced visible even with the bar off.
	m.SetBusy(BusyAction, "pushing…")
	require.Contains(t, m.String(), "pushing…", "busy progress renders despite quiet")
}
