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
// agent's pane. Owning that lifecycle would be real work.
//
// The kitty keyboard protocol is the contrast that makes the rule sharp rather than
// a counterexample to it. It is just as persistent a mode, and it costs Atrium
// nothing (#396) — because tea.View DOES model it, so close() writes the disable
// unconditionally and the renderer re-negotiates it across every alt-screen switch.
// The difference is not "keyboard modes are cheaper"; it is that one mode is inside
// the declarative set Bubble Tea unwinds and the other is not.
//
// ansi.RequestLightDarkReport (ESC[?996n) is the one-shot version, with no mode to
// unwind, so "persistent" is not by itself the whole argument. It loses on the count
// that survives: it reports the OS colour-scheme PREFERENCE rather than the
// terminal's background, which is what Atrium actually renders against.
//
// It used to lose on a second count — that Bubble Tea does not translate its reply,
// so consuming it would mean importing ultraviolet directly and promoting an indirect
// dependency pinned at an untagged pseudo-version. That price has since been paid, by
// the image overlay's pixel rung (#398, app/image_kitty.go), which needs the terminal's
// graphics reply for the image ID every placeholder cell is addressed by — and gets
// positive capability confirmation in the same round trip. So the import is no longer
// a cost this decision can charge for; ultraviolet is in the direct require block and
// a 996 reply would cost only its own case. The PREFERENCE-versus-background argument
// is what still decides it, and it is the one to re-examine if this is revisited.
//
// OSC 11 asks for the background colour itself, is answered by nearly everything,
// has nothing to unwind, and stays inside Bubble Tea's stable public API. So Atrium
// asks at four points: at startup, on refocus, after a detach, and when the settings
// panel selects `auto`. The first three re-ask on behalf of a selection that was
// already `auto`; the fourth is the one where it just became so.

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

// applySchemeQueryCmd is requestSchemeCmd for the settings panel's theme arm, which
// is the fourth query point and the one the other three cannot stand in for.
//
// Startup, refocus and detach all ask on behalf of a selection that was ALREADY
// `auto`. Here the selection may have just become it — and a session that launched
// on a named palette spent no query at Init, because requestSchemeCmd's gate read
// the theme this change is replacing. So curScheme is still whatever COLORFGBG said
// at startup, usually nothing, and composing `auto` against it renders the shipped
// dark default. On a light terminal that is the wrong palette, and nothing would
// correct it until the user happened to blur and refocus the window.
//
// The row is timingLive, whose footerNote() is "" — the panel promises the change
// applies immediately, and a promise the next unrelated focus event has to keep is
// not that.
//
// Keyed like applyBarStyleCmd, and for the same reason: the arm is shared with
// glyph_set, which cannot change the palette selection, so it has nothing to
// re-detect. Gating on the key rather than on requestSchemeCmd's config check alone
// is what keeps a rung change from spending a query — that check passes under
// `auto` no matter which row moved.
func (m *home) applySchemeQueryCmd(key string) tea.Cmd {
	if key != "theme" {
		return nil
	}
	return m.requestSchemeCmd()
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
