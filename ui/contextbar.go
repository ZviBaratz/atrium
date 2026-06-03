package ui

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// contextbar.go composes the strings Atrium pushes into each session's tmux user
// options for the in-session context bar (see session/tmux/context.go). Styling
// uses tmux #[fg=...] markup rather than ANSI/lipgloss because tmux renders the
// status line itself. The composition mirrors the session-list theme so the bar and
// the list agree.

// RepoKey exposes the repo-grouping key (repo name, else base dir) used to build the
// session list's groups, so app.go can form the same groups when pushing per-session
// context (each session's sibling strip is its repo group).
func RepoKey(i *session.Instance) string { return repoKey(i) }

// tmuxEsc escapes '#' so dynamic text (names, branches) can't be misread as a tmux
// format directive (#[...] / #{...}).
func tmuxEsc(s string) string { return strings.ReplaceAll(s, "#", "##") }

// barState returns a session's status glyph and tmux color. Unlike the list — which
// animates a spinner for Running/Loading — the pushed header is static, so working
// states get a steady filled marker. The glyph's color is the only state signal in
// the header, so no status word is needed.
func barState(s session.Status, th *theme.Theme) (glyph, color string) {
	switch s {
	case session.Running:
		return "●", string(th.Palette.Working)
	case session.Loading:
		return "●", string(th.Palette.Working)
	case session.Ready:
		return th.Glyphs.Ready, string(th.Palette.Success)
	case session.NeedsInput:
		return th.Glyphs.Waiting, string(th.Palette.Attention)
	case session.Paused:
		return th.Glyphs.Paused, string(th.Palette.FgDim)
	default:
		return " ", string(th.Palette.FgDim)
	}
}

// ComposeSessionContext renders the two strings pushed to a session's tmux user
// options for the context bar:
//   - name: the plain display name (drives the terminal title via set-titles-string)
//   - left: the styled header — "<glyph> <repo> · <name>"
//
// The header rides a slate background band (status-style, set in the managed config),
// where dim greys wash out — so hierarchy comes from weight, not color: the glyph
// carries the state color (the only state signal), the repo + separator ride the
// bar's default foreground, and the name is bold so the eye lands on it. Branch and
// status word are intentionally omitted: the branch duplicates the name in practice,
// and the glyph color already conveys state. repo is empty for direct-mode (non-git)
// sessions, which collapse to "<glyph> <name>".
func ComposeSessionContext(current *session.Instance, repo string) (name, left string) {
	th := theme.Current()
	name = current.DisplayName()

	glyph, color := barState(current.GetStatus(), th)

	// #[default] after the glyph resets fg AND attributes back to the bar's
	// status-style, so repo/separator render in the bar's bright default foreground.
	var b strings.Builder
	fmt.Fprintf(&b, "#[fg=%s]%s#[default]", color, glyph)
	if repo != "" {
		fmt.Fprintf(&b, " %s ·", tmuxEsc(repo))
	}
	fmt.Fprintf(&b, " #[bold]%s#[default]", tmuxEsc(name))
	left = b.String()

	return name, left
}
