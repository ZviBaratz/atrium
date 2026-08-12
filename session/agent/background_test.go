package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The footers below are verbatim captures from a live claude 2.1.228 pane driven with a
// real Bash(run_in_background) and a real Monitor (tmux capture-pane, 2026-08-12), plus the
// two-of-each rung. They are the whole reason this detector is not a substring match: the
// shell and monitor counts share ONE "·"-delimited segment and are joined by a COMMA, so an
// alternation delimited only by "·" matches neither half.
const (
	footerBgShell         = "⏵⏵ auto mode on · 1 shell · ← for agents · ↓ to manage"
	footerBgShells        = "⏵⏵ auto mode on · 2 shells · ← for agents · ↓ to manage"
	footerBgMonitor       = "⏵⏵ auto mode on · 1 monitor · esc to interrupt · ← for agents · ↓ to manage"
	footerBgBoth          = "⏵⏵ auto mode on · 1 shell, 1 monitor · esc to interrupt · ← for agents · ↓ to manage"
	footerBgBothPlural    = "⏵⏵ auto mode on · 2 shells, 2 monitors · esc to interrupt · ← for agents · ↓ to manage"
	footerBgClean         = "⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	footerBgCleanWithChip = "⏵⏵ auto mode on (shift+tab to cycle) · PR #347 · ctrl+t to hide tasks · ← for agents"
)

func TestClaudeBackgroundWorkVisible_FiresOnEveryCapturedChipShape(t *testing.T) {
	for name, footer := range map[string]string{
		"one shell":           footerBgShell,
		"two shells":          footerBgShells,
		"shell and monitor":   footerBgBoth,
		"shells and monitors": footerBgBothPlural,
		"monitor alone":       footerBgMonitor,
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, claude.BackgroundWorkVisible(idlePane("● Done for now.", footer)),
				"the %s footer reports background work: %q", name, footer)
		})
	}
}

func TestClaudeBackgroundWorkVisible_SilentWithoutChips(t *testing.T) {
	for name, footer := range map[string]string{
		"clean idle footer":            footerBgClean,
		"idle footer with other chips": footerBgCleanWithChip,
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, claude.BackgroundWorkVisible(idlePane("● Done for now.", footer)),
				"a footer with no shell/monitor chip is not background work: %q", footer)
		})
	}
}

// The counts render as "<N> shell", never "<N> shell command" — the transcript's own
// "Ran 3 shell commands" summary (workingPane's second line) and the "● Running 1 shell
// command…" line claude prints while a foreground Bash runs both carry the same two words.
// Requiring a "·" or "," to close the run is what separates the chip from the prose, and it
// is the guard that keeps a finished turn from reading as background work forever.
func TestClaudeBackgroundWorkVisible_TranscriptProseIsNotAChip(t *testing.T) {
	require.False(t, claude.BackgroundWorkVisible(workingPane(
		"✽ Unravelling… (2m 4s · ↓ 46.1k tokens)", footerBgClean)),
		"'Ran 3 shell commands' in the transcript must not read as a chip")

	// The regex on its own, independent of the region: these are the live lines claude
	// prints around a FOREGROUND Bash, and none of them closes its run on "·" or ",".
	for _, prose := range []string{
		"● Running 1 shell command…",
		"  ⎿  Ran 3 shell commands, read 1 file",
		"  ⎿  Read 1 file, ran 1 shell command",
	} {
		require.False(t, claudeBackgroundChipRegex.MatchString(prose),
			"transcript prose is not a chip: %q", prose)
	}
}

// No box border, no signal. The detector fails CLOSED because it is the one latching state
// with no watchdog and no animation gate (see background.go): a pane that cannot prove its
// bottom lines are live chrome must read as it did before this existed, not as busy forever.
func TestClaudeBackgroundWorkVisible_BorderlessPaneCannotProveLiveChrome(t *testing.T) {
	require.False(t, claude.BackgroundWorkVisible(strings.Join([]string{
		"● Some earlier reasoning.",
		"❯ ",
		"  " + footerBgShells,
	}, "\n")), "with no box border the bottom lines are not provably live chrome")
}

// The footer truncates on overflow rather than wrapping, but a wrapped one must still read:
// the region is flattened before matching, so a chip split across two physical lines joins
// back up.
func TestClaudeBackgroundWorkVisible_WrappedFooterStillReads(t *testing.T) {
	require.True(t, claude.BackgroundWorkVisible(strings.Join([]string{
		rule,
		"❯ ",
		rule,
		"  ⏵⏵ auto mode on ·",
		"  2 shells · ← for agents",
	}, "\n")), "a footer wrapped mid-segment still carries the chip")
}

// A chip quoted in the SCROLLBACK, above a live input box, is the #342/#343 forgery shape:
// an agent discussing its own footer once read as a live prompt. footerBelowBox keeps only
// what follows the LAST horizontal rule on screen, so a quote above the box is not in the
// region the predicate ever sees. (Not the segment scan: background.go rejects
// footerVisibleInSegments, whose no-border fallback is the hole this must not inherit.)
func TestClaudeBackgroundWorkVisible_QuotedChipAboveTheBoxIsNotLive(t *testing.T) {
	require.False(t, claude.BackgroundWorkVisible(strings.Join([]string{
		"● The footer read: ⏵⏵ auto mode on · 2 shells, 2 monitors · ← for agents",
		"",
		rule,
		"❯ ",
		rule,
		"  " + footerBgClean,
	}, "\n")), "a footer quoted above the box must not read as live background work")
}

// Every other adapter leaves BackgroundWork nil, which the accessor reports as "unsupported"
// rather than panicking — the same graceful degradation every optional adapter hook has.
func TestBackgroundWorkVisible_UnsupportedAdaptersStaySilent(t *testing.T) {
	for _, a := range registry {
		if a.Key == KeyClaude {
			continue
		}
		require.False(t, a.BackgroundWorkVisible(idlePane("● Done.", footerBgBoth)),
			"%s declares no BackgroundWork, so it never reports any", a.Key)
	}
}
