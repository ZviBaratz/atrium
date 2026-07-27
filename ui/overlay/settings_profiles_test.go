package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profilesRailIndex is the rail index of the Profiles editor, derived rather than counted from
// the end of railEntries() so a future entry cannot silently move every test that lands there.
func profilesRailIndex() int {
	for i, e := range railEntries() {
		if e.kind == railProfiles {
			return i
		}
	}
	return -1
}

// profilesCfg builds a config whose profile list and default_program are exactly as given.
// config.DefaultConfig() leaves Profiles nil (TestDefaultConfigDoesNotProbe pins that it never
// probes), so a test that does not call this is exercising the EMPTY pane.
func profilesCfg(defaultProgram string, profiles ...config.Profile) *config.Config {
	cfg := config.DefaultConfig()
	cfg.DefaultProgram = defaultProgram
	cfg.Profiles = profiles
	return cfg
}

// threeProfiles is the standard fixture: three records, two of them holding a hand-written
// command with flags, and default_program naming the first.
//
// The default record's NAME and PROGRAM deliberately differ. cfg.DefaultProgram and
// cfg.GetProgram() are interchangeable whenever they coincide, and the delete guard must
// compare against the pointer rather than the resolved command — a fixture where both are
// "claude" cannot tell those apart, so the guard's mutation would come back negative.
func threeProfiles() *config.Config {
	return profilesCfg("claude",
		config.Profile{Name: "claude", Program: "claude --model opus"},
		config.Profile{Name: "aider", Program: "aider --model ollama_chat/gemma3:1b"},
		config.Profile{Name: "codex", Program: "codex"},
	)
}

// profilesAt opens the panel on the Profiles editor with its pane focused, which is the state
// every editor test starts from.
//
// The explicit focusRail is load-bearing, not tidiness. settingsAt goes through OpenAt, which
// sets focusRows — so a test that parks the cursor on a row first and THEN calls this would
// send its Enter to the editor's own key handler rather than to handleRailKey, opening the edit
// form on profile 0. The require below would still pass, because focus is already focusRows.
// That silent divergence broke three tests before it was found by running them.
func profilesAt(t *testing.T, o *SettingsOverlay) {
	t.Helper()
	require.GreaterOrEqual(t, profilesRailIndex(), 0, "the rail must have a Profiles editor")
	o.focus = focusRail
	o.SetRailIndex(profilesRailIndex())
	require.Equal(t, railProfiles, o.selectedEntry().kind)
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, focusRows, o.focus, "the forward key must focus the editor's pane")
	require.Nil(t, o.profileForm, "landing on the editor must not open a form")
}

// paneText renders the rows pane and returns its plain lines, which is what width and content
// assertions must measure: Render() pads every line to the box width, so asserting on it is a
// tautology that cannot fail.
func paneText(o *SettingsOverlay) []string {
	out := []string{}
	for _, l := range o.rowsPaneContent(o.rowsPaneWidth()) {
		out = append(out, stripANSI(l.text))
	}
	return out
}

// TestProfilesEntryFocusesItsEditor replaces TestProfilesEntryStaysANoOp, which pinned the
// deliberate PR C asymmetry that Enter on Profiles did nothing. All three forward keys now
// focus the editor's pane, and none of them closes the panel or asks home for a handoff — the
// editor lives inside the panel, unlike Accounts.
func TestProfilesEntryFocusesItsEditor(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter}, {Type: tea.KeyRight}, {Type: tea.KeyTab},
	} {
		o := NewSettingsOverlay(threeProfiles())
		o.SetRailIndex(profilesRailIndex())
		require.Equal(t, focusRail, o.focus)

		closed, changed := o.HandleKeyPress(key)
		assert.Falsef(t, closed, "%v must not close the panel", key)
		assert.Emptyf(t, changed, "%v changes no setting", key)
		assert.Equal(t, HandoffNone, o.Handoff(), "the editor is not a handoff")
		assert.Equalf(t, focusRows, o.focus, "%v must focus the editor", key)
	}
}

// TestProfilesPaneListsEveryProfile is the pane's contract: one line per record, name in the
// label column and program in the value column, with the default marked. At 100x32 the panel is
// two-pane, so the rail names the category and the pane adds no header of its own.
func TestProfilesPaneListsEveryProfile(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	lines := paneText(o)
	require.Len(t, lines, 3, "one line per profile, no headers and no spacers")
	for i, want := range []string{"claude", "aider", "codex"} {
		assert.Containsf(t, lines[i], want, "line %d must name its profile", i)
	}
	assert.Contains(t, lines[1], "aider --model ollama_chat/gemma3:1b",
		"the program is the value column")
	assert.Contains(t, lines[0], profileDefaultBadge,
		"the profile default_program names carries the default badge")
	assert.NotContains(t, lines[1], profileDefaultBadge)
	assert.NotContains(t, lines[2], profileDefaultBadge)
}

// TestEmptyProfilesPaneNamesTheWayOut: config.DefaultConfig() has no profiles at all, which is
// the state a fresh install with no detected agent lands in. An empty pane reads as a broken
// panel — the same obligation a handoff note carries — so it must name both keys that fill it
// and the help pane must explain what runs meanwhile.
func TestEmptyProfilesPaneNamesTheWayOut(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	require.Empty(t, o.cfg.Profiles, "precondition: DefaultConfig declares no profiles")
	profilesAt(t, o)

	pane := strings.Join(paneText(o), " ")
	assert.Contains(t, pane, "press n to add one",
		"the empty pane must name the key that adds a profile")
	assert.Contains(t, pane, "D to detect", "and the key that detects installed agents")
	assert.NotContains(t, pane, "Profiles",
		"the empty line already names the subject; a heading above it repeats itself, and at "+
			"40x10 it costs the line that keeps the text inside the pane")

	prose, danger := o.profilesHelp()
	assert.False(t, danger)
	assert.Contains(t, prose, "Default program",
		"with no profiles, default_program IS the launch command — say so")
}

// TestProfilesPaneNeverDescribesAnUnrelatedRow is the trap this pane inherits: s.cursor still
// points at whatever settingRow it last sat on, and selectedRow() is unguarded. Describing that
// row in the help pane would put an unrelated setting's summary under a list of profiles — the
// same lie railHandoff's blank prose avoids.
func TestProfilesPaneNeverDescribesAnUnrelatedRow(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	settingsAt(t, o, "theme") // park s.cursor on a real row first
	summary := o.selectedRow().summary
	require.NotEmpty(t, summary)
	profilesAt(t, o)

	help := stripANSI(strings.Join(o.helpLines(), " "))
	assert.NotContains(t, help, summary, "the help pane must not describe s.cursor's row here")
	assert.Contains(t, help, "claude", "it describes the highlighted profile instead")
}

// TestProfilesHelpShowsTheProgramInFull: the row line truncates a long program with a tail
// ellipsis, and spec §10 requires the full value to reappear in the help pane rather than being
// lost. Asserted at the tightest two-pane geometry, where the truncation actually bites.
func TestProfilesHelpShowsTheProgramInFull(t *testing.T) {
	long := "aider --model ollama_chat/gemma3:1b --no-auto-commits --dark-mode"
	o := NewSettingsOverlay(profilesCfg("claude", config.Profile{Name: "aider", Program: long}))
	o.SetSize(80, 24)
	profilesAt(t, o)

	require.Contains(t, paneText(o)[0], "…", "precondition: this geometry truncates the program")
	prose, _ := o.profilesHelp()
	assert.Equal(t, long, prose, "the truncated value must be recoverable from the help pane")
}

// TestProfilesContextLineCountsProfiles: the position readout must count the profile list, not
// whatever category the rail last marked — "2/3" has to mean the second of three profiles.
func TestProfilesContextLineCountsProfiles(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j"))

	ctx := stripANSI(o.contextLine(o.innerWidth()))
	assert.Contains(t, ctx, "2/3")
	assert.NotContains(t, ctx, "Default program launches",
		"aider is not the default, so the badge's fallback sentence stays off")

	_, _ = o.HandleKeyPress(keyRunes("k"))
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), "Default program launches this profile.",
		"the sentence behind the badge, for the width where the badge was dropped")
}

// TestProfileCursorIsBoundedByTheList pins that j/k cannot walk off either end — the cursor
// indexes cfg.Profiles, and an out-of-range value panics the very next render.
func TestProfileCursorIsBoundedByTheList(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	for i := 0; i < 5; i++ {
		_, _ = o.HandleKeyPress(keyRunes("k"))
	}
	assert.Equal(t, 0, o.profileCursor, "up stops at the first profile")
	for i := 0; i < 9; i++ {
		_, _ = o.HandleKeyPress(keyRunes("j"))
	}
	assert.Equal(t, 2, o.profileCursor, "down stops at the last profile")
}

// TestSelectedProfileIsAlwaysVisible is the windowing guard's twin for the second index space
// this pane introduces. paneLine.rowIdx used to mean "index into s.rows" and rowsPaneLines
// matched it against s.cursor; a profile line carries a profile index, and s.cursor is a small
// int too, so matching against the wrong cursor silently windows around an unrelated line.
func TestSelectedProfileIsAlwaysVisible(t *testing.T) {
	var profiles []config.Profile
	for i := 0; i < 20; i++ {
		profiles = append(profiles, config.Profile{
			Name: "p" + strings.Repeat("x", i%4) + string(rune('a'+i)), Program: "cmd",
		})
	}
	o := NewSettingsOverlay(profilesCfg("none", profiles...))
	o.SetSize(100, 20)
	profilesAt(t, o)
	require.Greater(t, len(profiles), o.paneHeight(), "precondition: the list must overflow")

	for i := range profiles {
		o.profileCursor = i
		found := false
		for _, l := range o.rowsPaneLines() {
			if strings.Contains(stripANSI(l), profiles[i].Name) {
				found = true
			}
		}
		assert.Truef(t, found, "profile %d is off-screen with the cursor on it", i)
	}
}

// TestProfilesPaneFitsEveryGeometry is the width sweep. A profile name and a program are user
// data of unbounded length, unlike the fixed schema labels spec §10 says never to truncate — so
// the name column is capped and tail-ellipsized, and no line may exceed the pane at any size
// the panel supports. Swept rather than pinned at one size, because the rows pane is widest in
// single-pane mode and narrowest just above the two-pane threshold.
func TestProfilesPaneFitsEveryGeometry(t *testing.T) {
	cfg := profilesCfg("claude",
		config.Profile{Name: "claude", Program: "claude"},
		config.Profile{
			Name:    "a-deliberately-very-long-profile-name-nobody-would-type",
			Program: "aider --model ollama_chat/gemma3:1b --no-auto-commits --dark-mode",
		},
	)
	checked := 0
	for _, h := range []int{settingsVChrome + settingsMinBody, 16, 24, 40} {
		for w := 40; w <= 200; w++ {
			o := NewSettingsOverlay(cfg)
			o.SetSize(w, h)
			o.SetRailIndex(profilesRailIndex())
			o.focus = focusRows
			paneW := o.rowsPaneWidth()
			lines := o.rowsPaneContent(paneW)

			// Below the two-pane threshold the rail is off screen and the pane names itself, so
			// the expected count is width-dependent. Hardcoding 2 here would forbid that header.
			want := 2
			if !o.twoPane() {
				want = 3
			}
			require.Lenf(t, lines, want, "%dx%d: one line per profile, plus the single-pane header", w, h)

			for _, l := range lines {
				plain := stripANSI(l.text)
				if l.rowIdx < 0 {
					assert.LessOrEqualf(t, ansi.StringWidth(plain), paneW,
						"%dx%d: the header overflows the pane: %q", w, h, plain)
					continue
				}
				// EXACTLY paneW, not <=. composeRowLine bounds its own output on every path, so
				// a <= assertion is a tautology no bug can trip — the plan's own Global
				// Constraints forbid it, and it was measured letting the name-truncation
				// mutation pass. == catches the gap arithmetic instead.
				assert.Equalf(t, paneW, ansi.StringWidth(plain),
					"%dx%d: a profile line is not exactly the pane width: %q", w, h, plain)
			}

			// The presence half: a width assertion alone is satisfied by a blank line.
			joined := stripANSI(lines[len(lines)-1].text)
			assert.Containsf(t, joined, "aider", "%dx%d: the program column vanished: %q", w, h, joined)
			checked++
		}
	}
	require.Greater(t, checked, 600, "the sweep must actually visit the geometries")
}

// TestEmptyProfilesPaneFitsEveryGeometry is the width half of the empty-state line, and it
// exists because that line replaced one the tree already swept.
//
// TestEveryHandoffNoteFitsItsPane sweeps every handoff note over w=40..200 x four heights,
// because a static string that over-wraps makes windowPane draw a "n more" marker on a pane
// with nothing to scroll to. This PR takes Profiles out of that sweep and puts a LONGER static
// string (68 cells, against the note's 65) in the same position. In this repo a copy change is a
// width change, so the sweep has to come with it.
func TestEmptyProfilesPaneFitsEveryGeometry(t *testing.T) {
	checked := 0
	for _, h := range []int{settingsVChrome + settingsMinBody, 16, 24, 40} {
		for w := 40; w <= 200; w++ {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(w, h)
			o.SetRailIndex(profilesRailIndex())
			o.focus = focusRows
			paneW, paneH := o.rowsPaneWidth(), o.paneHeight()
			lines := o.rowsPaneContent(paneW)
			assert.LessOrEqualf(t, len(lines), paneH,
				"%dx%d: the empty-state line wraps to %d lines in a %d-line pane, so windowPane "+
					"shows a scroll marker on a pane with nothing to scroll to", w, h, len(lines), paneH)
			for _, l := range lines {
				assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(l.text)), paneW,
					"%dx%d: the empty-state line overflows the pane: %q", w, h, stripANSI(l.text))
			}
			checked++
		}
	}
	require.Greater(t, checked, 600, "the sweep must actually visit the geometries")
}

// TestALongProfileNameCannotEvictTheProgram: without a cap on the name column one long name
// eats the whole pane and composeRowLine truncates the head instead, hiding every program on
// screen. The name yields; the program keeps a legible column.
func TestALongProfileNameCannotEvictTheProgram(t *testing.T) {
	o := NewSettingsOverlay(profilesCfg("none", config.Profile{
		Name: strings.Repeat("n", 120), Program: "claude --model opus",
	}))
	for _, w := range []int{73, 80, 100, 120} {
		o.SetSize(w, 32)
		o.SetRailIndex(profilesRailIndex())
		line := paneText(o)[0]
		assert.Containsf(t, line, "…", "%d: the over-long name must be truncated", w)
		assert.Containsf(t, line, "claude", "%d: the program must survive the long name", w)
	}
}

// TestQuestionMarkIsInertOnTheProfilesPane. `?` opens expandedHelpContent(s.cursor), which
// describes a settingRow. On this pane s.cursor points at an unrelated row, so `?` must do
// nothing at all rather than open help about a setting the user is not looking at.
func TestQuestionMarkIsInertOnTheProfilesPane(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("?"))
	assert.False(t, o.helpOpen, "? must not open help for a row this pane is not showing")
}

// TestSlashFromTheProfilesPaneSearchesSettings. `/` is the settings search; profiles are data,
// not settings, and searchResults walks s.rows. So `/` here behaves exactly as it does from a
// handoff entry: it opens the ordinary filter and takes the rail with it.
func TestSlashFromTheProfilesPaneSearchesSettings(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("/"))
	require.True(t, o.searching())
	assert.False(t, o.profilesPaneActive(), "a filter takes the pane over regardless of the rail")
	assert.NotEqual(t, railProfiles, o.selectedEntry().kind,
		"the rail follows the highlighted result out of the editor")
}

// TestEscIsLayeredOutOfTheProfilesPane: the pane adds a level to spec §15's ladder. From the
// editor, esc backs to the rail; a second esc closes.
func TestEscIsLayeredOutOfTheProfilesPane(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the first esc backs out of the pane")
	assert.Equal(t, focusRail, o.focus)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed, "the second esc closes the panel")
}

// TestLeavingTheProfilesPaneDropsItsTransientState. Moving the rail away must not leave a
// half-typed record or an armed delete behind a pane the user can no longer see — the next
// visit would resume a state they have no way to know about.
func TestLeavingTheProfilesPaneDropsItsTransientState(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	o.profileForm = newProfileForm(-1, "half", "typed")
	o.profileConfirm = true
	o.profileNote = "stale"

	o.SetRailIndex(railDefaultIndex())

	assert.Nil(t, o.profileForm)
	assert.False(t, o.profileConfirm)
	assert.Empty(t, o.profileNote)
}
