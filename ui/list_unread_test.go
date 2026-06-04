package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// markUnread drives an instance through a Running→Ready transition, the edge
// that flags the unread bit (NewInstance starts at Ready without a transition).
func markUnread(t *testing.T, inst *session.Instance) {
	t.Helper()
	inst.SetStatus(session.Running)
	inst.SetStatus(session.Ready)
	require.True(t, inst.Unread())
}

// An unread Ready session keeps today's bright look (Success + filled Ready
// glyph); a seen one dims to SuccessDim with the hollow ReadySeen glyph. Both
// shape and color change so the signal survives colorblindness and low color.
func TestStateParts_UnreadBrightSeenDim(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}

	inst, err := session.NewInstance(session.InstanceOptions{Title: "u", Path: ".", Program: "echo"})
	require.NoError(t, err)
	markUnread(t, inst)

	glyph, word, color := r.stateParts(inst, th)
	require.Equal(t, th.Glyphs.Ready, glyph, "unread keeps the filled Ready glyph")
	require.Equal(t, "ready", word)
	require.Equal(t, th.Palette.Success, color, "unread keeps the bright Success color")

	inst.MarkSeen()
	glyph, word, color = r.stateParts(inst, th)
	require.Equal(t, th.Glyphs.ReadySeen, glyph, "seen switches to the hollow glyph")
	require.Equal(t, "ready", word, "the state word stays 'ready' either way")
	require.Equal(t, th.Palette.SuccessDim, color, "seen dims to SuccessDim")
}

// groupUnreadCount counts only unread Ready sessions: a seen Ready session and
// a non-Ready session must not inflate a collapsed header's unread badge.
func TestGroupUnreadCount_OnlyUnreadReady(t *testing.T) {
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoA")
	markUnread(t, l.items[0])
	markUnread(t, l.items[1])
	l.items[1].MarkSeen()
	l.items[2].SetStatus(session.Running)

	require.Equal(t, 1, l.groupUnreadCount(0, 3))
}

// A collapsed group must still signal its unread sessions: the header carries a
// "●N" badge (mirroring the existing "◆N" needs-input badge), and both badges
// coexist when a group has unread and blocked sessions at once.
func TestCollapsedHeader_UnreadBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	l := newGroupList(t, "/x/repoA", "/x/repoA", "/x/repoB")
	markUnread(t, l.items[0])
	l.items[1].SetStatus(session.NeedsInput)

	l.SetSelectedInstance(0)
	require.True(t, l.Collapse())

	out := l.String()
	require.Contains(t, out, "●1", "collapsed header must badge its unread count")
	require.Contains(t, out, "◆1", "the needs-input badge must coexist with the unread badge")

	require.True(t, l.Expand())
	out = l.String()
	require.NotContains(t, out, "●1", "an expanded group shows per-row state, not a header badge")
}
