package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
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
