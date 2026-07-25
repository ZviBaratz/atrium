package overlay

// settingCategory identifies which section (PR A) and rail entry (PR B) a settings
// row belongs to. It is a closed vocabulary rendered from allCategories(), so adding
// a category is one deliberate edit — unlike the free-string `section` it replaces,
// where a typo silently created an eleventh section of one row.
type settingCategory int

const (
	catSessions settingCategory = iota
	catWorktrees
	catAppearance
	catSessionList
	catNotifications
	catAutomation
	catInput
	catProjects
	catUpdates
	catAdvanced
)

// allCategories returns every category in rail/section display order. It is the
// single ordered source: the renderer walks it rather than deriving order from the
// row declarations, so a row's position in newSettingRows cannot reorder sections.
func allCategories() []settingCategory {
	return []settingCategory{
		catSessions,
		catWorktrees,
		catAppearance,
		catSessionList,
		catNotifications,
		catAutomation,
		catInput,
		catProjects,
		catUpdates,
		catAdvanced,
	}
}

// label returns the category's section/rail label.
func (c settingCategory) label() string {
	switch c {
	case catSessions:
		return "Sessions"
	case catWorktrees:
		return "Worktrees & git"
	case catAppearance:
		return "Appearance"
	case catSessionList:
		return "Session list"
	case catNotifications:
		return "Notifications"
	case catAutomation:
		return "Automation"
	case catInput:
		return "Input"
	case catProjects:
		return "Projects"
	case catUpdates:
		return "Updates"
	case catAdvanced:
		return "Advanced"
	}
	return ""
}

// applyTiming says when a change to a setting takes effect. It is a closed enum with
// two projections, so the two renderers cannot drift: footerNote() is the prose the
// single-column footer appends after "·" (empty for live, which is most rows — saying
// "live" 25 times would be noise), and badge() is the right-aligned per-row chip the
// two-pane renderer adds in PR B.
//
// It deliberately has no "modifies your local branch" member. That was one of the old
// free-text applyNote's four values, and it is a caution rather than a timing; it now
// lives in fast_forward_local_base's detail (spec §5).
type applyTiming int

const (
	timingLive applyTiming = iota
	timingNewSessions
	timingRestart
)

// footerNote returns the prose the single-column footer appends, or "" for a change
// that applies immediately.
func (t applyTiming) footerNote() string {
	switch t {
	case timingNewSessions:
		return "affects new sessions"
	case timingRestart:
		return "applies on restart"
	}
	return ""
}

// badge returns the short right-aligned chip text for the two-pane renderer.
func (t applyTiming) badge() string {
	switch t {
	case timingNewSessions:
		return "new sessions"
	case timingRestart:
		return "restart"
	}
	return "live"
}

// settingScope is the seam for a later per-repo override layer (#477) and per-session
// settings (#454). Every row is scopeGlobal today, and the renderer and navigation
// must stay scope-agnostic so that layer adds a column and a switcher without
// reshaping this schema. Do not special-case scopeGlobal anywhere (spec §5).
type settingScope int

const (
	scopeGlobal settingScope = iota
)
