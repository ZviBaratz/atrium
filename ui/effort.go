package ui

import (
	"github.com/ZviBaratz/atrium/session/agent"

	"github.com/mattn/go-runewidth"
)

// effortChipMaxWidth caps the effort chip. Like modelChipMaxWidth this is defensive, not a
// budget for any real level: the longest one claude documents is "xhigh" (5). The right
// cluster is fixed-width and never truncates — every chip eats directly from the name
// column's budget — so a value that is neither a known level nor sane (a future level, a
// malformed --effort reaching the unvalidated PinnedEffort) must not be able to blow the
// row open.
const effortChipMaxWidth = 6

// effortLabel returns the display string for an --effort level, capped. Delegates to
// agent.ClaudeEffortLabel, the single source of truth for level→label, so the list chip and
// the create form's chip row stay consistent ("medium" → "med" in both). An unmapped level
// renders verbatim rather than being dropped — the hook reports claude's own resolved
// level, so a level a newer CLI adds should still show up as itself.
func effortLabel(level string) string {
	return runewidth.Truncate(agent.ClaudeEffortLabel(level), effortChipMaxWidth, "…")
}
