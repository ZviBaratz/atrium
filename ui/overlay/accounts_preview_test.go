package overlay

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
)

func TestPreviewMemberBudget(t *testing.T) {
	cases := []struct {
		name     string
		height   int
		decision bool
		want     int
	}{
		{"height 24, no decision line", 24, false, 5},
		{"height 24, with a decision line", 24, true, 4},
		{"height 40, no decision line", 40, false, 21},
		{"height 40, with a decision line", 40, true, 20},
		{"height 10 (tiny), no decision line: floored at the minimum", 10, false, 2},
		{"height 10 (tiny), with a decision line: still floored at the minimum", 10, true, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, previewMemberBudget(tc.height, tc.decision))
		})
	}
}

func TestPreviewMemberWindow(t *testing.T) {
	t.Run("n fits within budget: everything shown, no overflow line", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(3, 1, 4)
		assert.Equal(t, 0, start)
		assert.Equal(t, 3, end)
		assert.Equal(t, 0, hidden)
	})

	t.Run("n exactly equal to budget: still no overflow line", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(4, 2, 4)
		assert.Equal(t, 0, start)
		assert.Equal(t, 4, end)
		assert.Equal(t, 0, hidden)
	})

	t.Run("chosen at index 0: first three shown, the rest hidden", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(12, 0, 4)
		assert.Equal(t, 0, start)
		assert.Equal(t, 3, end)
		assert.Equal(t, 9, hidden)
	})

	// The case that matters: a cap that could scroll the chosen member out of
	// view would defeat the feature it protects. chosen=11 is the last index
	// of 12, so the window must slide all the way to the tail to keep it lit.
	t.Run("chosen at the last index: the decisive member stays visible", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(12, 11, 4)
		assert.Equal(t, 9, start)
		assert.Equal(t, 12, end)
		assert.Equal(t, 9, hidden)
		assert.True(t, 11 >= start && 11 < end, "chosen index must fall inside [start, end)")
	})

	t.Run("chosen in the middle: window scrolls to keep it in view", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(12, 6, 4)
		assert.Equal(t, 4, start)
		assert.Equal(t, 7, end)
		assert.Equal(t, 9, hidden)
		assert.True(t, 6 >= start && 6 < end, "chosen index must fall inside [start, end)")
	})

	t.Run("clamped: end never exceeds n, start never below 0", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(5, 4, 3)
		assert.GreaterOrEqual(t, start, 0)
		assert.LessOrEqual(t, end, 5)
		assert.Equal(t, 3, start)
		assert.Equal(t, 5, end)
		assert.Equal(t, 3, hidden)
		assert.True(t, 4 >= start && 4 < end, "chosen index must fall inside [start, end)")
	})

	// previewMemberBudget floors at 2 for a very small overlay, giving a
	// one-row window (shown = budget - 1 = 1). The decisive member must still
	// be the one row shown, not scrolled out by the tightest possible budget.
	t.Run("at the floored minimum budget: still exactly the chosen row", func(t *testing.T) {
		start, end, hidden := previewMemberWindow(5, 3, 2)
		assert.Equal(t, 3, start)
		assert.Equal(t, 4, end)
		assert.Equal(t, 4, hidden)
		assert.True(t, 3 >= start && 3 < end, "chosen index must fall inside [start, end)")
	})
}

// previewFullWidth is the width previewDecisionLine actually gets in
// practice at the floor 80x24 terminal: o.inner() (74) minus
// previewIndentWidth (9). Tests that aren't specifically exercising the
// width cascade pass this so they read as "today's real terminal" rather
// than an arbitrary number.
const previewFullWidth = 74 - 9

func TestPreviewDecisionLine(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	members := []config.ClaudeAccount{{Name: "work-1"}, {Name: "work-2"}, {Name: "work-3"}}

	t.Run("no skip: the cursor's own member was already available", func(t *testing.T) {
		got := previewDecisionLine(members, nil, 0, 0, false, now, previewFullWidth)
		assert.Equal(t, "", got)
	})

	t.Run("one member skipped", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{"work-1": {Limited: true}}
		got := previewDecisionLine(members, avail, 0, 1, false, now, previewFullWidth)
		assert.Equal(t, "work-1 limited → rotating to work-2", got)
	})

	t.Run("two members skipped", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true},
			"work-2": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 2, false, now, previewFullWidth)
		assert.Equal(t, "2 members limited → rotating to work-3", got)
	})

	// The cursor is the common case that actually wraps in real rotation: it
	// advances past the end of the pool and lands back at the front. start=2
	// (work-3, the last member) is limited, so the walk must cross the n%n
	// boundary to reach chosen=0 (work-1) rather than reading the loop as
	// only ever counting forward without wraparound.
	t.Run("one member skipped, cursor wraps past the end", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{"work-3": {Limited: true}}
		got := previewDecisionLine(members, avail, 2, 0, false, now, previewFullWidth)
		assert.Equal(t, "work-3 limited → rotating to work-1", got)
	})

	// allLimited callers must pass the caller's own config.SoonestResetMember
	// pick as chosen (previewDecisionLine no longer recomputes it) — here that
	// pick is index 0 (work-1), same as renderPoolDecision's marked would be.
	t.Run("all limited, all indefinite: falls back to the first member", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true},
			"work-2": {Limited: true},
			"work-3": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 0, true, now, previewFullWidth)
		assert.Equal(t, "creating asks to confirm, then uses work-1 (first member)", got)
	})

	// SoonestResetMember picks index 1 (work-2) here, so chosen must be 1 — the
	// value renderPoolDecision's marked would hold — not the cursor 0.
	t.Run("all limited, one has a parseable Until: resets soonest", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true},
			"work-2": {Limited: true, Until: "2026-07-25T18:00:00Z"},
			"work-3": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 1, true, now, previewFullWidth)
		assert.Equal(t, "creating asks to confirm, then uses work-2 (resets soonest)", got)
	})

	// SoonestResetMember itself treats an unparseable Until as indefinite (it
	// `continue`s past it), so the pinned member here is still work-1 (the
	// fallback index 0) — but previewDecisionLine must independently re-check
	// that Until parses before claiming "(resets soonest)" for it, rather than
	// trusting a non-empty Until string alone.
	t.Run("all limited, malformed Until: still the first-member wording", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true, Until: "not-a-time"},
			"work-2": {Limited: true},
			"work-3": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 0, true, now, previewFullWidth)
		assert.Equal(t, "creating asks to confirm, then uses work-1 (first member)", got)
	})

	t.Run("empty members: nothing to report", func(t *testing.T) {
		got := previewDecisionLine(nil, nil, 0, 0, false, now, previewFullWidth)
		assert.Equal(t, "", got)
	})
}

// TestAllLimitedDecisionWidthCascade is the property the defect report asked
// for: never emit a line wider than width, across a range of widths
// including one narrow enough to force every rung and a long member name
// that overflows the full wording even at the default terminal's width.
// Content is asserted too (not just width) so a rung that fits by saying
// nothing useful — or by silently swapping the reason — would fail.
func TestAllLimitedDecisionWidthCascade(t *testing.T) {
	cases := []struct {
		name   string
		acct   string
		reason string
		width  int
		want   string
	}{
		{"full width, short name, first member", "work-1", "(first member)", previewFullWidth,
			"creating asks to confirm, then uses work-1 (first member)"},
		{"full width, short name, resets soonest", "work-1", "(resets soonest)", previewFullWidth,
			"creating asks to confirm, then uses work-1 (resets soonest)"},
		{"full width, long name: full wording overflows (76 cells), drops to the terser form",
			"quantivly-work-longname", "(resets soonest)", previewFullWidth,
			"confirm → quantivly-work-longname (resets soonest)"},
		// The terse rung's own boundary, stated as a pair so the comment above it
		// cannot drift into a lie: 31 cells is exactly what it costs for this name
		// and reason, and one cell less drops both the reason and the confirm claim.
		{"terse rung fits exactly at 31 cells", "work-1", "(first member)", 31,
			"confirm → work-1 (first member)"},
		{"one cell narrower: the reason no longer fits alongside the confirm claim",
			"work-1", "(first member)", 30,
			"all limited → work-1"},
		{"narrower still: even the reason no longer fits alongside the name",
			"work-1", "(first member)", 25,
			"all limited → work-1"},
		{"narrowest: forces the name-free floor", "quantivly-work-longname", "(resets soonest)", 15,
			"all limited"},
		{"too narrow for even the floor: nothing fits, so nothing is said",
			"quantivly-work-longname", "(resets soonest)", 5,
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allLimitedDecision(tc.acct, tc.reason, tc.width)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, lipgloss.Width(got), tc.width,
				"must never emit a line wider than the width given")
		})
	}
}

// TestAllLimitedDecisionNeverOverflows sweeps a wide range of widths (rather
// than the hand-picked breakpoints above) so a future rung boundary bug can't
// hide between the cases table above happens to hit.
func TestAllLimitedDecisionNeverOverflows(t *testing.T) {
	for _, acct := range []string{"w", "work-1", "quantivly-work-super-long-account-name"} {
		for _, reason := range []string{"(first member)", "(resets soonest)"} {
			for width := 0; width <= 90; width++ {
				got := allLimitedDecision(acct, reason, width)
				assert.LessOrEqualf(t, lipgloss.Width(got), width,
					"acct=%q reason=%q width=%d got=%q", acct, reason, width, got)
			}
		}
	}
}

func TestSkipOneDecisionWidthCascade(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
		width    int
		want     string
	}{
		{"full width, short names", "work-1", "work-2", previewFullWidth, "work-1 limited → rotating to work-2"},
		{"narrow width: drops 'rotating to'", "work-1", "work-2", 30, "work-1 limited → work-2"},
		{"long names: full wording overflows even the default width (73 of 65), drops to the terser form",
			"quantivly-work-longname-1", "quantivly-work-longname-2", previewFullWidth,
			"quantivly-work-longname-1 limited → quantivly-work-longname-2"},
		{"very narrow: only the destination survives", "work-1", "work-2", 10, "→ work-2"},
		{"long names, too narrow even for the arrow form: the name-free floor",
			"quantivly-work-longname-1", "quantivly-work-longname-2", 20,
			"limited → rotating"},
		{"too narrow for even the floor: nothing fits, so nothing is said",
			"quantivly-work-longname-1", "quantivly-work-longname-2", 5,
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skipOneDecision(tc.from, tc.to, tc.width)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, lipgloss.Width(got), tc.width,
				"must never emit a line wider than the width given")
		})
	}
}

func TestSkipManyDecisionWidthCascade(t *testing.T) {
	cases := []struct {
		name    string
		skipped int
		to      string
		width   int
		want    string
	}{
		{"full width, short name", 2, "work-3", previewFullWidth, "2 members limited → rotating to work-3"},
		{"narrow width: drops 'rotating to'", 2, "work-3", 20, "2 limited → work-3"},
		{"full width, long name: still fits (57 of 65) — this branch's names are usually short enough",
			5, "quantivly-work-longname-3", previewFullWidth, "5 members limited → rotating to quantivly-work-longname-3"},
		{"long name, narrower width: full wording no longer fits, drops to the terser form",
			5, "quantivly-work-longname-3", 40, "5 limited → quantivly-work-longname-3"},
		{"very narrow: only the destination survives", 2, "work-3", 10, "→ work-3"},
		{"long name, too narrow even for the arrow form: the name-free floor",
			5, "quantivly-work-longname-3", 20, "limited → rotating"},
		{"too narrow for even the floor: nothing fits, so nothing is said",
			5, "quantivly-work-longname-3", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skipManyDecision(tc.skipped, tc.to, tc.width)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, lipgloss.Width(got), tc.width,
				"must never emit a line wider than the width given")
		})
	}
}
