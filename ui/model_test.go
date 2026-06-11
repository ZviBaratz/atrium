package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// TestShortModelName pins the mechanical id→chip transform. Every shape below
// is observed in real transcripts; there is deliberately no name table, so new
// model releases render reasonably without a code change.
func TestShortModelName(t *testing.T) {
	for _, tc := range []struct {
		id, want string
	}{
		{"claude-opus-4-7", "opus 4.7"},
		{"claude-sonnet-4-6", "sonnet 4.6"},
		{"claude-fable-5", "fable 5"},
		{"claude-haiku-4-5-20251001", "haiku 4.5"},
		{"fable", "fable"}, // bare alias from `--model fable`
		{"opus", "opus"},
		{"claude-3-5-sonnet-20241022", "3-5-sonnet"}, // legacy: version leads, accepted
		{"", ""},
		{"weird-very-long-model-identifier", "weird-very-lo…"}, // capped at modelChipMaxWidth
	} {
		if got := shortModelName(tc.id); got != tc.want {
			t.Errorf("shortModelName(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// TestRender_ModelChip pins the visibility rule per mode, following the
// account-badge precedent: pinned (default) shows only flag-pinned sessions,
// always also shows transcript-known models, off shows nothing.
func TestRender_ModelChip(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	pinned, err := session.NewInstance(session.InstanceOptions{Title: "p", Path: ".", Program: "claude --model fable"})
	require.NoError(t, err)
	known, err := session.NewInstance(session.InstanceOptions{Title: "k", Path: ".", Program: "claude"})
	require.NoError(t, err)
	known.SetModelMeta("claude-opus-4-7", transcript.Stamp{Path: "/t", Size: 1})

	// Default (pinned mode): the flag value renders; transcript-only does not.
	require.Contains(t, ansi.Strip(r.Render(pinned, 0, false)), "fable",
		"a --model-pinned session must show its chip in the default mode")
	require.NotContains(t, ansi.Strip(r.Render(known, 0, false)), "opus",
		"a transcript-known but unpinned model stays hidden in pinned mode")

	// Always mode: both render; the known model goes through the transform.
	r.modelIndicator = "always"
	require.Contains(t, ansi.Strip(r.Render(known, 0, false)), "opus 4.7",
		"always mode surfaces transcript-known models")
	require.Contains(t, ansi.Strip(r.Render(pinned, 0, false)), "fable")

	// Transcript truth overrides the flag value on a pinned session.
	pinned.SetModelMeta("claude-fable-5", transcript.Stamp{Path: "/t", Size: 1})
	require.Contains(t, ansi.Strip(r.Render(pinned, 0, false)), "fable 5",
		"the chip shows transcript truth once known, not the raw flag")

	// Off mode: nothing renders.
	r.modelIndicator = "off"
	require.NotContains(t, ansi.Strip(r.Render(pinned, 0, false)), "fable")
	require.NotContains(t, ansi.Strip(r.Render(known, 0, false)), "opus")
}

// TestRender_ModelChip_BrandUnit pins the chip's placement and tinting: the
// chip rides the agent icon as one brand-colored unit — after the AUTO badge,
// one space before the icon, agent brand color when pinned, dim when the model
// is only transcript-observed.
func TestRender_ModelChip_BrandUnit(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	pinned, err := session.NewInstance(session.InstanceOptions{Title: "p", Path: ".", Program: "claude --model fable"})
	require.NoError(t, err)
	pinned.AutoYes = true
	known, err := session.NewInstance(session.InstanceOptions{Title: "k", Path: ".", Program: "claude"})
	require.NoError(t, err)
	known.SetModelMeta("claude-opus-4-7", transcript.Stamp{Path: "/t", Size: 1})

	// Placement: AUTO badge, then chip, then icon — the badge must not split
	// the chip from the icon — and exactly one space binds chip to icon.
	plain := ansi.Strip(r.Render(pinned, 0, false))
	idxAuto, idxModel, idxIcon := strings.Index(plain, "AUTO"), strings.Index(plain, "fable"), strings.Index(plain, "✻")
	require.True(t, idxAuto >= 0 && idxModel >= 0 && idxIcon >= 0, "row must carry badge, chip and icon: %q", plain)
	require.True(t, idxAuto < idxModel && idxModel < idxIcon,
		"chip must sit between AUTO and the agent icon: %q", plain)
	require.Contains(t, plain, "fable ✻", "one space between chip and icon")

	// Tint: claude's brand coral #d97757 for a pinned chip, the muted variant
	// #9a5a44 for an observed-only chip. The icon is always full coral, so
	// count — a pinned row colors the chip too (2 coral spans), an observed-only
	// row has 1 full span (the icon) plus the muted chip.
	// Sequences as termenv actually emits them: hex parses through float
	// channels and truncates on emission, so #9a5a44's 0x5a (90) lands as 89.
	const coral = "38;2;217;119;87"
	const mutedCoral = "38;2;154;89;68"
	out := r.Render(pinned, 0, false)
	require.Equal(t, 2, strings.Count(out, coral),
		"pinned chip + icon must both carry the brand color")
	require.NotContains(t, out, mutedCoral, "a pinned chip is never muted")
	r.modelIndicator = "always"
	out = r.Render(known, 0, false)
	require.Equal(t, 1, strings.Count(out, coral),
		"an unpinned chip must not use the full brand color; only the icon does")
	require.Equal(t, 1, strings.Count(out, mutedCoral),
		"an unpinned chip carries the muted brand color")
}
