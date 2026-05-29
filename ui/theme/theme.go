// Package theme is the single source of truth for the UI's look: semantic
// color tokens, the icon (glyph) set, and box-drawing style. Components read
// the active theme via Current(); it is set once at startup from config.
package theme

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Palette holds the semantic color tokens for a theme. Colors are truecolor
// hex strings; lipgloss down-samples automatically on lesser terminals.
type Palette struct {
	Bg          lipgloss.Color // window background
	BgElevated  lipgloss.Color // selected-row / panel fill
	Fg          lipgloss.Color // primary text
	FgDim       lipgloss.Color // secondary text (line-2 git info)
	FgFaint     lipgloss.Color // tertiary text / inactive borders & rules
	Accent      lipgloss.Color // active border, selection
	AccentMuted lipgloss.Color // dimmed accent
	Purple      lipgloss.Color // app title / banner
	Success     lipgloss.Color // ready, additions
	Working     lipgloss.Color // working spinner tint
	Attention   lipgloss.Color // waiting / behind (the one attention color)
	Danger      lipgloss.Color // deletions, errors, destructive actions
	Cyan        lipgloss.Color // hunks, info
	BadgeBg     lipgloss.Color // AUTO badge background
	BadgeFg     lipgloss.Color // AUTO badge foreground
}

// Glyphs holds every icon as a token so themes can swap Nerd-Font glyphs for
// plain Unicode. Every cell glyph must render at terminal width 1 (enforced by
// TestGlyphWidths) so column alignment and the view-bounds invariant hold.
type Glyphs struct {
	SpinnerFrames []string
	SpinnerFPS    time.Duration
	Ready         string // idle, ready for input
	Waiting       string // blocked on user input (attention)
	Paused        string // session halted
	Branch        string // precedes a branch name
	Ahead         string // commits ahead of base
	Behind        string // commits behind base
	Dirty         string // uncommitted changes
	AutoBadge     string // leading icon for the AUTO badge (may be empty)
	FoldOpen      string // expanded repo group
	FoldClosed    string // collapsed repo group
	SelectionMark string // left accent bar on the selected row
	DiffAdd       string // "+" in diff stats
	DiffDel       string // "-" in diff stats
}

// Borders carries the box-drawing style so a fallback theme can use square
// corners where rounded ones might render poorly.
type Borders struct {
	Style lipgloss.Border
}

// Theme is the single source of truth for the UI's look.
type Theme struct {
	Name    string
	Palette Palette
	Glyphs  Glyphs
	Borders Borders
}

// Semantic style helpers. Each returns a fresh style so callers can chain
// .Background()/.Width()/.Bold() without mutating shared state.
func (t *Theme) FgStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(t.Palette.Fg) }
func (t *Theme) DimStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(t.Palette.FgDim) }
func (t *Theme) FaintStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Palette.FgFaint) }
func (t *Theme) AccentStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Palette.Accent)
}
func (t *Theme) PurpleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Palette.Purple)
}
func (t *Theme) SuccessStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Palette.Success)
}
func (t *Theme) WorkingStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Palette.Working)
}
func (t *Theme) AttentionStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Palette.Attention)
}
func (t *Theme) DangerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Palette.Danger)
}
func (t *Theme) CyanStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Palette.Cyan) }
func (t *Theme) SelectedRowStyle() lipgloss.Style {
	return lipgloss.NewStyle().Background(t.Palette.BgElevated)
}
func (t *Theme) BadgeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Background(t.Palette.BadgeBg).Foreground(t.Palette.BadgeFg).Bold(true)
}
