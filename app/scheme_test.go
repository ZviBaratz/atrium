package app

import (
	"context"
	"image/color"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// lightBackground and darkBackground are colours whose IsDark() answers are
// unambiguous. tea.BackgroundColorMsg embeds image/color.Color and derives IsDark
// from it, so these tests drive the real predicate rather than a bool.
func lightBackground() color.Color { return color.RGBA{R: 0xe1, G: 0xe2, B: 0xe7, A: 0xff} }
func darkBackground() color.Color  { return color.RGBA{R: 0x1a, G: 0x1b, B: 0x26, A: 0xff} }

// A terminal that answers "light" while `theme: auto` is configured must re-theme
// and push the tmux bar. The bar push is the half nothing else would notice: it
// lives in another process, and Stage A's own tests only cover the settings-panel
// route into it.
func TestBackgroundColorMsgLightRethemesAndPushesTheBar(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	m.appConfig.SessionContextBar = boolPtr(true)
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeUnknown))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{Color: lightBackground()})
	require.NotNil(t, cmd, "a scheme change must command a repaint and a bar push")
	runCmdTree(cmd)

	require.Equal(t, theme.SchemeLight, theme.CurrentScheme())
	require.True(t, theme.IsLight(theme.Current().Palette),
		"auto plus a light terminal must render the light palette")
	require.Equal(t, int32(1), atomic.LoadInt32(&pushed), "the in-pane bar must follow the flip")
}

// An unchanged answer must be a no-op. Without this, every refocus would clear the
// screen and re-push the bar for the whole fleet — a subprocess per focus event,
// which is the #380 defect class exactly.
func TestBackgroundColorMsgUnchangedIsANoOp(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	m.appConfig.SessionContextBar = boolPtr(true)
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeDark))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{Color: darkBackground()})
	require.Nil(t, cmd, "re-reporting the same scheme must command nothing")
	require.Equal(t, int32(0), atomic.LoadInt32(&pushed))
}

// A reply whose colour did not PARSE is not an answer, and it does not look like
// absence either — which is what makes it dangerous. ultraviolet builds the event
// from ansi.XParseColor, which returns nil for anything it cannot read, and its
// isDarkColor(nil) answers true. So a garbled OSC 11 reply arrives as a confident
// "dark" and would flip a correctly detected light terminal. Latching only holds if
// an unreadable answer counts as no answer.
func TestBackgroundColorMsgWithAnUnreadableColourLatches(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	m.appConfig.SessionContextBar = boolPtr(true)
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeLight))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{}) // Color is nil: XParseColor failed
	require.Nil(t, cmd, "an unparseable reply must command nothing")
	require.Equal(t, theme.SchemeLight, theme.CurrentScheme(),
		"a garbled reply must leave a correctly detected scheme alone, not report dark")
	require.Equal(t, int32(0), atomic.LoadInt32(&pushed))
}

// The latch at the function's own contract, not through the one message that can
// reach it today. Mutation testing found this: deleting applyDetectedScheme's
// SchemeUnknown guard broke nothing, because ResolveScheme never returns Unknown for
// a non-nil answer and every caller then passed one. A guard no test can reach is a
// guard that will be deleted as dead code by whoever adds the second caller.
func TestApplyDetectedSchemeLatchesOnUnknown(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	m.appConfig.SessionContextBar = boolPtr(true)
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeLight))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	require.Nil(t, m.applyDetectedScheme(theme.SchemeUnknown),
		"no answer must command nothing")
	require.Equal(t, theme.SchemeLight, theme.CurrentScheme(),
		"no answer must leave the scheme alone, never reset it to the default")
	require.Equal(t, int32(0), atomic.LoadInt32(&pushed))
}

// AC#4 through the live path: a named theme ignores a detected flip entirely.
// theme.TestNamedThemesNeverFollowTheScheme proves compose() ignores the axis; this
// proves the app does not reach past it and call Set itself.
func TestBackgroundColorMsgIgnoredForANamedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = "catppuccin-mocha"
	m.appConfig.SessionContextBar = boolPtr(true)
	t.Cleanup(theme.Set("catppuccin-mocha"))
	t.Cleanup(theme.SetScheme(theme.SchemeUnknown))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{Color: lightBackground()})
	require.Nil(t, cmd)
	require.Equal(t, "catppuccin-mocha", theme.Current().Name)
	require.Equal(t, int32(0), atomic.LoadInt32(&pushed))
}

// Refocus re-queries. This is the whole of AC#2 on terminals without mode 2031,
// which is all of them as far as Atrium is concerned (see app/scheme.go).
func TestFocusMsgRequeriesTheBackgroundColour(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName

	_, cmd := m.Update(tea.FocusMsg{})
	require.NotNil(t, cmd, "refocus must re-query: a flip while blurred is otherwise invisible")
	require.True(t, m.focused, "the notification gate must still see the focus")
}

// A named theme must not spend a query it cannot act on.
func TestFocusMsgDoesNotQueryForANamedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = "tokyo-night"

	_, cmd := m.Update(tea.FocusMsg{})
	require.Nil(t, cmd)
	require.True(t, m.focused)
}

// The gate reads CONFIG, not theme.Current(). `auto` that resolved to a dark palette
// is still `auto`, and Current() cannot tell that from a user who named tokyo-night —
// so a gate written against the rendered theme would stop querying the moment
// detection said dark, and never notice the terminal going light again.
func TestRequestSchemeCmdGatesOnConfigNotTheRenderedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeDark))

	require.Equal(t, theme.DefaultThemeName, theme.Current().Name,
		"precondition: auto under a dark scheme renders the default palette")
	require.NotNil(t, m.requestSchemeCmd(),
		"auto must keep querying even while it renders the same palette a named theme would")
}

// An empty theme is unset, which means the default, which is not auto. The gate goes
// through GetTheme so "unset" has one meaning here and at startup.
func TestRequestSchemeCmdTreatsUnsetAsTheDefault(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = ""

	require.Nil(t, m.requestSchemeCmd())
}

// The gate compares against the literal "auto", so it depends on GetTheme having
// folded the case first. config.TestGetTheme pins the folding; this pins the WIRE,
// because a gate that read c.Theme directly would pass that test and still leave a
// hand-edited "Auto" silently undetected.
func TestRequestSchemeCmdAcceptsAutoInAnyCase(t *testing.T) {
	m := newCreateFormHome(t)

	for _, written := range []string{"auto", "Auto", "AUTO", "  auto  "} {
		m.appConfig.Theme = written
		require.NotNil(t, m.requestSchemeCmd(),
			"theme %q must be the reserved auto value, not an unknown palette", written)
	}
}

// collectCmdTree is runCmdTree returning the leaf messages instead of discarding
// them, so a test can assert that a particular Cmd is IN a batch. Same structural
// recursion, and for the same reason: tea.Sequence's message type is unexported, so
// both are matched as "a slice of tea.Cmd" rather than by type.
//
// It exists so the OSC 11 query can be found by running the real
// tea.RequestBackgroundColor and comparing what it produces. The alternative — a
// package-level seam swapped by tests, like barStyleApplier — would be production
// surface that exists only for the test, and it would let the wire pass while
// carrying a stand-in rather than the query.
func collectCmdTree(c tea.Cmd) []tea.Msg {
	if c == nil {
		return nil
	}
	msg := c()
	v := reflect.ValueOf(msg)
	if v.IsValid() && v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := range v.Len() {
			out = append(out, collectCmdTree(v.Index(i).Interface().(tea.Cmd))...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// countSchemeQueries reports how many OSC 11 queries a Cmd tree carries, by running
// it and counting the messages tea.RequestBackgroundColor itself produces.
func countSchemeQueries(c tea.Cmd) int {
	want := tea.RequestBackgroundColor()
	n := 0
	for _, msg := range collectCmdTree(c) {
		if msg == want {
			n++
		}
	}
	return n
}

// Detach is the third query point, and the one no message announces: tea.Exec
// suspended the loop and tmux owned the terminal for the whole attach, so neither an
// OSC 11 reply nor a focus event could reach us. repaintAfterAttach is the one moment
// we know that.
func TestRepaintAfterAttachRequeriesUnderAuto(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName

	require.Equal(t, 1, countSchemeQueries(m.repaintAfterAttach()),
		"a detach must re-ask: detection was blind for the whole attach")
}

// The same wire, negatively — a named theme must not spend a query on every detach.
func TestRepaintAfterAttachDoesNotQueryForANamedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = "tokyo-night"

	require.Zero(t, countSchemeQueries(m.repaintAfterAttach()))
}

// Init's query is the startup rung's upper half, and it is a wire of its own: the
// COLORFGBG read in newHome does not replace it, it only fills the gap until an
// answer arrives.
func TestInitQueriesTheBackgroundColourUnderAuto(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName

	require.Equal(t, 1, countSchemeQueries(m.Init()))
}

// And negatively, so the gate is guarded at this site too rather than inherited
// from requestSchemeCmd's own test.
func TestInitDoesNotQueryForANamedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = "tokyo-night"

	require.Zero(t, countSchemeQueries(m.Init()))
}

// initialScheme is the startup rung, and it must be exactly ResolveScheme's
// COLORFGBG half — no OSC 11 answer exists yet at that point.
func TestInitialSchemeReadsCOLORFGBG(t *testing.T) {
	t.Setenv("COLORFGBG", "0;15")
	require.Equal(t, theme.SchemeLight, initialScheme())

	t.Setenv("COLORFGBG", "15;0")
	require.Equal(t, theme.SchemeDark, initialScheme())

	t.Setenv("COLORFGBG", "")
	require.Equal(t, theme.SchemeUnknown, initialScheme(),
		"no COLORFGBG must be no answer, so startup latches on the default")
}

// Selecting `auto` in the settings panel is the fourth query point, and the only one
// where the gate that suppressed every earlier query is the very thing being changed.
// A user who launched on a named theme was never queried — requestSchemeCmd returned
// nil at Init — so curScheme is whatever COLORFGBG said, usually nothing. Without a
// query here, picking `auto` on a light terminal renders the dark default until the
// user happens to blur and refocus the window.
//
// The row is timingLive, whose footerNote() is "": the panel promises this applies
// immediately, and "immediately" cannot mean "at the next unrelated focus event".
func TestApplySettingChangeThemeArmQueriesWhenAutoIsSelected(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // applySettingChange persists the config first
	t.Cleanup(theme.Set(theme.Current().Name))
	t.Cleanup(theme.SetScheme(theme.CurrentScheme()))
	defer swapBarStyleApplier(func(context.Context, bool) {})()

	m := newCreateFormHome(t)
	m.appConfig.Theme = "catppuccin-mocha"
	require.Zero(t, countSchemeQueries(m.Init()),
		"precondition: launching on a named theme spends no query, so nothing has been detected")

	m.appConfig.Theme = theme.AutoThemeName
	require.Equal(t, 1, countSchemeQueries(m.applySettingChange("theme")),
		"selecting auto must ask the terminal now, not leave it to the next refocus")
}

// The same wire, negatively at both of its gates. Switching BETWEEN named palettes
// must not query — the gate is on the new config value, not on "the theme row moved"
// — and glyph_set, which shares this arm and cannot change the palette selection,
// must not query even while auto is configured.
//
// The second half is what makes the key check real rather than incidental:
// requestSchemeCmd's own gate passes under auto, so without it the glyph_set arm
// would spend a query per rung change.
func TestApplySettingChangeThemeArmDoesNotQueryOtherwise(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(theme.Set(theme.Current().Name))
	t.Cleanup(theme.SetScheme(theme.CurrentScheme()))
	defer swapBarStyleApplier(func(context.Context, bool) {})()

	m := newCreateFormHome(t)

	m.appConfig.Theme = "catppuccin-mocha"
	require.Zero(t, countSchemeQueries(m.applySettingChange("theme")),
		"a named palette never follows the terminal, so it must not ask about it")

	m.appConfig.Theme = theme.AutoThemeName
	require.Zero(t, countSchemeQueries(m.applySettingChange("glyph_set")),
		"glyph_set shares the arm but moves no palette: nothing to re-detect")
}
