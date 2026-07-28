package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// stalePane returns a pane rendering text as a live frame stamped age ago.
func stalePane(width, height int, text string, age time.Duration) *PreviewPane {
	p := NewPreviewPane()
	p.SetSize(width, height)
	p.previewState = previewState{fallback: false, text: text}
	p.frameAt = time.Now().Add(-age)
	return p
}

// TestStaleMarker_AppearsOnlyOnceOverdue pins both halves of the threshold. The
// "fresh" half is the one that matters day to day: a capture costs single-digit
// milliseconds, so a marker that could fire at ordinary latency would be on
// screen permanently and mean nothing.
func TestStaleMarker_AppearsOnlyOnceOverdue(t *testing.T) {
	fresh := stalePane(80, 10, "agent output", previewStaleAfter/2)
	require.NotContains(t, fresh.String(), "stale", "a frame inside the threshold is not stale")

	overdue := stalePane(80, 10, "agent output", previewStaleAfter+2*time.Second)
	require.Contains(t, overdue.String(), "stale", "a frame past the threshold must announce itself")
	require.Contains(t, overdue.String(), "agent output", "the last good frame stays on screen")
}

// TestStaleMarker_SilentWhenNothingWasStamped is the goldens-are-safe property:
// every pane built without an applied frame — which is every pane in every test
// that predates this feature, and every fallback state — renders exactly as it
// did before the marker existed.
func TestStaleMarker_SilentWhenNothingWasStamped(t *testing.T) {
	unstamped := NewPreviewPane()
	unstamped.SetSize(80, 10)
	unstamped.previewState = previewState{fallback: false, text: "agent output"}

	stamped := stalePane(80, 10, "agent output", previewStaleAfter+time.Hour)
	stamped.frameAt = time.Time{} // the only difference from the overdue pane above

	require.Equal(t, unstamped.String(), stamped.String())
	require.NotContains(t, unstamped.String(), "stale")
}

// TestStaleMarker_SuppressedWhereTheFreezeIsAlreadyLabeled guards against saying
// the same thing twice: scroll and hint mode carry their own "this is frozen"
// affordance, and a fallback has no frame to be stale.
func TestStaleMarker_SuppressedWhereTheFreezeIsAlreadyLabeled(t *testing.T) {
	long := previewStaleAfter + time.Minute

	t.Run("scroll mode", func(t *testing.T) {
		p := stalePane(80, 10, "agent output", long)
		p.isScrolling = true
		require.Empty(t, p.staleMarker(time.Now()))
	})

	t.Run("hint mode", func(t *testing.T) {
		p := stalePane(80, 10, "agent output", long)
		p.hintContent = "decorated frame"
		require.Empty(t, p.staleMarker(time.Now()))
	})

	t.Run("fallback", func(t *testing.T) {
		p := stalePane(80, 10, "agent output", long)
		p.setFallbackState("Setting up workspace...")
		require.Empty(t, p.staleMarker(time.Now()))
	})
}

// TestStaleMarker_NeverChangesTheRenderedBox is the layout guarantee: the marker
// overwrites a row the pane already had. If it ever appended one, View's
// JoinHorizontal would push the whole frame past the terminal height and the UI
// would scroll and snap — the #251 failure this pane is forbidden to cause.
func TestStaleMarker_NeverChangesTheRenderedBox(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {40, 10}, {120, 40}, {20, 5}, {12, 3}} {
		for _, text := range []string{
			"short line",
			strings.Repeat("wide content ", 30),           // forces per-row truncation
			strings.Repeat("many\n", 60),                  // forces the ellipsis row
			"\x1b[31mred\x1b[0m and \x1b[32mgreen\x1b[0m", // ANSI must survive the stamp
		} {
			fresh := stalePane(size.w, size.h, text, 0).String()
			overdue := stalePane(size.w, size.h, text, previewStaleAfter+time.Second).String()

			require.Equal(t, lipgloss.Height(fresh), lipgloss.Height(overdue),
				"the marker must not change the row count at %dx%d", size.w, size.h)
			// The stamped row is padded flush to the pane width, which is what
			// TabbedWindow's lipgloss.Place does to the whole block anyway. What must
			// never happen is exceeding that width: an over-wide row survives Place
			// and pushes the right column past the terminal.
			require.LessOrEqual(t, lipgloss.Width(overdue), size.w,
				"the marker must never push the pane past its own width at %dx%d", size.w, size.h)
		}
	}
}

// TestStampRight_KeepsTheRowExactlyOneRowWide covers the helper directly,
// including the ANSI case a byte-wise truncate would corrupt.
func TestStampRight_KeepsTheRowExactlyOneRowWide(t *testing.T) {
	marker := "— stale 3s"
	cases := []struct{ name, base string }{
		{"empty base", ""},
		{"short base", "hi"},
		{"base wider than the row", strings.Repeat("x", 200)},
		{"ansi base", "\x1b[31m" + strings.Repeat("r", 100) + "\x1b[0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stampRight(40, c.base, marker)
			require.Equal(t, 40, xansi.StringWidth(got), "the stamped row must fill exactly the pane width")
			require.True(t, strings.HasSuffix(got, marker), "the marker must sit flush right")
			require.NotContains(t, got, "\n", "the stamp must not introduce a row")
		})
	}
}
