package overlay

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
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
}

func TestPreviewDecisionLine(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	members := []config.ClaudeAccount{{Name: "work-1"}, {Name: "work-2"}, {Name: "work-3"}}

	t.Run("no skip: the cursor's own member was already available", func(t *testing.T) {
		got := previewDecisionLine(members, nil, 0, 0, false, now)
		assert.Equal(t, "", got)
	})

	t.Run("one member skipped", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{"work-1": {Limited: true}}
		got := previewDecisionLine(members, avail, 0, 1, false, now)
		assert.Equal(t, "work-1 limited → rotating to work-2", got)
	})

	t.Run("two members skipped", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true},
			"work-2": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 2, false, now)
		assert.Equal(t, "2 members limited → rotating to work-3", got)
	})

	t.Run("all limited, all indefinite: falls back to the first member", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true},
			"work-2": {Limited: true},
			"work-3": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 0, true, now)
		assert.Equal(t, "creating asks to confirm, then uses work-1 (first member)", got)
	})

	t.Run("all limited, one has a parseable Until: resets soonest", func(t *testing.T) {
		avail := map[string]config.AccountAvailability{
			"work-1": {Limited: true},
			"work-2": {Limited: true, Until: "2026-07-25T18:00:00Z"},
			"work-3": {Limited: true},
		}
		got := previewDecisionLine(members, avail, 0, 0, true, now)
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
		got := previewDecisionLine(members, avail, 0, 0, true, now)
		assert.Equal(t, "creating asks to confirm, then uses work-1 (first member)", got)
	})

	t.Run("empty members: nothing to report", func(t *testing.T) {
		got := previewDecisionLine(nil, nil, 0, 0, false, now)
		assert.Equal(t, "", got)
	})
}
