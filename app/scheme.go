package app

import (
	"os"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
)

// scheme.go is the terminal-polarity detection wiring: the queries Atrium sends,
// what it does with an answer, and where it re-asks.
//
// Atrium ASKS rather than being told, and both ways of being told were measured
// against the pinned stack before that was settled.
//
// Mode 2031 — the terminal pushing a scheme change — is decodable here today:
// x/ansi carries SetModeLightDark, ultraviolet decodes CSI ? 997 ; N n into
// Dark/LightColorSchemeEvent (decoder.go:432), and translateInputEvent ends in
// `return e`, so an unrecognised ultraviolet event reaches Update untouched and a
// case for one would compile. It is rejected because it is a PERSISTENT MODE that
// nothing here unwinds. Bubble Tea's teardown is declarative: cursedRenderer.close()
// resets exactly the modes tea.View models, read back off the last View — and
// tea.View has no light/dark field, so 2031 is not tracked. (restoreTerminalState is
// not the mechanism; it restores termios and flushes.) An unmatched ESC[?2031h
// therefore outlives Atrium on every exit path — quit, ctx.Done()/SIGTERM, and panic
// alike — and past every tea.Exec attach, where the terminal keeps emitting
// CSI?997;Nn into an input stream tmux now owns, injecting stray bytes into the
// agent's pane. Owning that lifecycle is real work, and #396 has to build it anyway
// for the kitty keyboard protocol.
//
// ansi.RequestLightDarkReport (ESC[?996n) is the one-shot version, with no mode to
// unwind, so "persistent" is not by itself the whole argument. It loses on two other
// counts: it reports the OS colour-scheme PREFERENCE rather than the terminal's
// background, which is what Atrium actually renders against, and Bubble Tea does not
// translate its reply — consuming it would mean importing ultraviolet directly,
// promoting an indirect dependency pinned at an untagged pseudo-version.
//
// OSC 11 asks for the background colour itself, is answered by nearly everything,
// has nothing to unwind, and stays inside Bubble Tea's stable public API. So Atrium
// asks: at startup, on refocus, and after an attach.

// requestSchemeCmd asks the terminal for its background colour, or nil when the
// answer could not be acted on.
//
// Gating on the configured theme rather than querying unconditionally is what keeps
// a named theme from spending a query per focus event that it would then discard.
// The gate reads CONFIG, not theme.Current(): `auto` resolved to a dark palette is
// still `auto`, and Current() cannot tell that from a user who named tokyo-night —
// so a gate written against the rendered theme would go quiet the moment detection
// said dark and never notice the terminal going light again.
//
// RequestBackgroundColor is a func() Msg, so it IS a Cmd — passed unparenthesised,
// like tea.RequestWindowSize.
func (m *home) requestSchemeCmd() tea.Cmd {
	if m.appConfig.GetTheme() != theme.AutoThemeName {
		return nil
	}
	return tea.RequestBackgroundColor
}

// applyDetectedScheme records a detected polarity and, if it changed anything,
// re-themes: a hard repaint plus the tmux bar push, exactly as the settings panel's
// theme arm does.
//
// It returns nil for an unchanged scheme, and that is load-bearing rather than an
// optimization. Atrium re-queries on every refocus, so without the comparison each
// focus event would clear the screen and spawn a subprocess for the whole fleet — a
// subprocess count that grows with a behaviour the user cannot see, which is the
// #380 defect class.
//
// SchemeUnknown is dropped rather than applied. The ladder reports "nothing
// answered" as unknown, and treating that as a flip to dark would mean a terminal
// that went quiet for one query undid a correct detection. Detection latches: only a
// real answer moves it.
func (m *home) applyDetectedScheme(s theme.Scheme) tea.Cmd {
	if s == theme.SchemeUnknown {
		return nil
	}
	if m.appConfig.GetTheme() != theme.AutoThemeName {
		return nil
	}
	if s == theme.CurrentScheme() {
		return nil
	}
	theme.SetScheme(s)
	return tea.Sequence(
		tea.ClearScreen,
		tea.Batch(tea.RequestWindowSize, m.barStylePushCmd()),
	)
}

// initialScheme is the startup ladder's lower rung, read once: COLORFGBG, for
// terminals that will never answer the OSC 11 query Init also sends.
//
// It is deliberately applied BEFORE the query rather than instead of it. An answer
// that arrives later overrides this, because ResolveScheme ranks OSC 11 above
// COLORFGBG — the variable is inherited by child processes and is not updated when
// the terminal's theme changes, so it is routinely stale and must never correct a
// live answer.
func initialScheme() theme.Scheme {
	return theme.ResolveScheme(nil, os.Getenv("COLORFGBG"))
}

// boolPtrOf lifts a definite answer into the *bool ResolveScheme takes, whose nil
// means "the terminal did not reply".
func boolPtrOf(b bool) *bool { return &b }
