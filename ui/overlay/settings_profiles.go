package overlay

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// profilesChangedKey is what the editor reports to home when cfg.Profiles changed. It is the
// config.json key itself, so it routes through applySettingChange exactly as a row edit does —
// keeping the panel's existing persist path the only writer — and reaches the one live-apply
// case a profile has: re-resolving the launch command GetProgram derives from it.
const profilesChangedKey = "profiles"

// profileDefaultBadge marks the profile default_program names. It rides the badge column, so
// composeRowLine drops it first on a narrow pane (spec §10) and profilesContextLine carries the
// same fact as a sentence — the badge is scannable, the sentence is the fallback.
const profileDefaultBadge = "default"

// profilesPaneActive reports whether the right pane is currently the Profiles editor. A filter
// takes the pane over regardless of which rail entry is marked, so the search arm wins.
func (s *SettingsOverlay) profilesPaneActive() bool {
	return !s.searching() && s.selectedEntry().kind == railProfiles
}

// clampProfileCursor pulls the cursor back inside cfg.Profiles after the list shrinks under it.
//
// It is the accounts overlay's clampCursor (ui/overlay/accounts.go), and it exists for exactly
// one case: deleting the LAST record leaves the cursor one past the end, and the very next
// render — or the next d — indexes out of range and panics. Deleting a middle record needs no
// clamp; the cursor correctly lands on what was the next profile.
func (s *SettingsOverlay) clampProfileCursor() {
	s.profileCursor = clamp(s.profileCursor, 0, max(0, len(s.cfg.Profiles)-1))
}

// resetProfileTransients drops the editor's per-visit state: an open form, an armed delete and
// the one-keypress note. Moving the rail off Profiles — or deep-linking past it — must not
// leave a half-typed record or an armed "y deletes" behind a pane the user can no longer see.
//
// profileDetecting is deliberately NOT cleared: a shell probe already in flight still lands,
// and its merge is what the user asked for.
func (s *SettingsOverlay) resetProfileTransients() {
	s.profileForm = nil
	s.profileConfirm = false
	s.profileNote = ""
}

// handleProfilesKey routes a key while the Profiles pane has focus (spec §9).
//
// Every arm starts from a clean slate: lastErr and the one-keypress note are cleared first, so a
// refusal or a detection result lives exactly until the next key rather than lingering over a
// pane it no longer describes.
//
// `?` and `r` are deliberately unbound. Both read s.rows[s.cursor], which on this pane points at
// whatever settingRow the cursor last sat on — help about a setting the user is not looking at,
// or a reset of one they cannot see.
func (s *SettingsOverlay) handleProfilesKey(msg tea.KeyMsg) (closed bool, changedKey string) {
	if s.profileConfirm {
		// Checked BEFORE the clear below, because the prompt has to survive its own render.
		return false, s.handleProfileConfirmKey(msg)
	}
	s.lastErr, s.profileNote = "", ""
	switch msg.String() {
	case "esc", "ctrl+c", "tab", "shift+tab":
		// Layered: back to the rail first, close from there. Advertised as "esc back".
		s.focus = focusRail
	case "up", "k":
		if s.profileCursor > 0 {
			s.profileCursor--
		}
	case "down", "j":
		if s.profileCursor < len(s.cfg.Profiles)-1 {
			s.profileCursor++
		}
	case "/":
		s.startSearch()
	case "n":
		s.profileForm = newProfileForm(-1, "", "")
	case "e", "enter":
		if len(s.cfg.Profiles) > 0 {
			p := s.cfg.Profiles[s.profileCursor]
			s.profileForm = newProfileForm(s.profileCursor, p.Name, p.Program)
		}
	case "d":
		if len(s.cfg.Profiles) > 0 {
			s.armProfileDelete()
		}
	case "D":
		s.requestProfileDetect()
	}
	return false, ""
}

// requestProfileDetect asks home to run agent detection off the update loop.
//
// It cannot run inline: config.DetectAgentProfiles probes claude through
// config.GetClaudeCommand, which spawns a login shell sourcing the user's rc file under a
// ten-second timeout (config/detect.go). A synchronous call would freeze the update loop — and
// with it every session's poll — for as long as that takes. home runs it as a tea.Cmd through
// the same detectAgents seam the startup agent check already uses, so the panel and
// `atrium profiles detect` cannot probe differently, and hands the result to
// NoteProfilesDetected.
//
// The latch is what stops a held key spawning a shell per repeat; NoteProfilesDetected releases
// it. It sets no note: profilesHelp derives "Detecting installed agents…" from the flag instead,
// because handleProfilesKey clears the note on every key — so a note would vanish the moment the
// user pressed j while waiting, and a second D would visibly REMOVE the only explanation on
// screen rather than repeat it.
func (s *SettingsOverlay) requestProfileDetect() {
	if s.profileDetecting {
		return
	}
	s.profileDetecting, s.profileDetectPending = true, true
}

// TakeProfileDetect reports whether the Profiles editor has asked for a detection run,
// consuming the request. home calls it after every key press, exactly as it reads Handoff on
// close: an overlay cannot run a command itself.
func (s *SettingsOverlay) TakeProfileDetect() bool {
	if !s.profileDetectPending {
		return false
	}
	s.profileDetectPending = false
	return true
}

// NoteProfilesDetected records a completed detection's outcome for the editor's help pane and
// reports whether the user will actually see it there.
//
// home does the merging and owns the wording; this half exists only so the result is REPORTED
// where the user is looking. When the editor's pane is not on screen — the rail moved, a filter
// is up, the panel closed — it returns false and home surfaces the outcome as a notice instead.
// Without that split the merge could rewrite config.json with nothing whatever on screen: the
// probe outlives the keypress, and syncCursorToRail clears the note on the way past.
//
// The cursor follows the first added record, so D and n agree about where you end up.
func (s *SettingsOverlay) NoteProfilesDetected(added []string, text string) (shown bool) {
	s.profileDetecting = false
	if len(added) > 0 {
		for i, p := range s.cfg.Profiles {
			if p.Name == added[0] {
				s.profileCursor = i
				break
			}
		}
	}
	s.clampProfileCursor()
	if !s.profilesPaneActive() {
		return false
	}
	s.profileNote = text
	return true
}

// armProfileDelete refuses when the highlighted record is the one default_program names, and
// otherwise arms the confirmation.
//
// Refusing is spec §9's guard 12, taken over the repoint alternative. default_program lives in
// another category, so a silent repoint would change what every new session launches from a
// pane that cannot show the change; and there is no successor record that preserves the launch
// command, unlike a rename (see commitProfile). It is also the panel's existing voice for a
// value it will not silently rewrite — project_search_depth refuses a value past the accessor's
// clamp rather than echoing back a number the accessor ignores.
//
// The message leads with the setting's own label, because the help pane caps prose at
// helpHeight() lines with a tail ellipsis and that label is the one word the user needs to find
// the row.
//
// The one-profile wording is not politeness. default_program's options are the profile names
// plus the captured raw command, and cycleEnum returns early on a single-option enum with no
// error, no inert chip and no reset — a silent dead key. seededDefaultConfig points
// default_program at Profiles[0], so a machine with one agent installed lands in exactly that
// state on first run, and "change it under Sessions first" would send that user to a row the
// panel makes impossible to change. Name the action that actually works instead.
func (s *SettingsOverlay) armProfileDelete() {
	if s.cfg.Profiles[s.profileCursor].Name != s.cfg.DefaultProgram {
		s.profileConfirm, s.profileConfirmIdx = true, s.profileCursor
		return
	}
	if len(s.cfg.Profiles) == 1 {
		s.lastErr = "Default program points at your only profile — add another with n first."
		return
	}
	s.lastErr = "Default program points at this profile — change it under Sessions first."
}

// handleProfileConfirmKey routes the delete confirmation. y or ↵ deletes; n, esc or ctrl+c backs
// out; every other key is ignored, so a stray press can neither confirm nor silently disarm
// (the accounts overlay's rule).
func (s *SettingsOverlay) handleProfileConfirmKey(msg tea.KeyMsg) (changedKey string) {
	switch msg.String() {
	case "y", "enter":
		s.profileConfirm = false
		i := s.profileConfirmIdx
		s.cfg.Profiles = append(s.cfg.Profiles[:i], s.cfg.Profiles[i+1:]...)
		if s.profileCursor > i {
			// The cursor was below the deleted record, so it stays on whatever it was on.
			s.profileCursor--
		}
		s.clampProfileCursor()
		return profilesChangedKey
	case "n", "esc", "ctrl+c":
		s.profileConfirm = false
	}
	return ""
}

// The form's fields, as slice indices. profileFieldCount closes the block so nav wraps on the
// real length rather than a literal, exactly as accountForm keys off len(inputs).
const (
	fldProfileName = iota
	fldProfileProgram
	profileFieldCount
)

// The field labels, shared by the renderer and its width guard. They are the label column of
// the form's two lines, so profileProgramLabel — the longer — sets that column's width.
const (
	profileNameLabel    = "Name"
	profileProgramLabel = "Program"
)

// profileForm is the add/edit sub-form for one config.Profile. It works purely in strings; the
// panel validates and builds the record on submit, exactly as AccountsOverlay does for
// accountForm.
//
// editIndex is -1 for a new profile and the cfg.Profiles index for an edit — the single
// sentinel that drives both validateProfile's self-exclusion and commitProfile's
// append-vs-replace.
type profileForm struct {
	inputs    [profileFieldCount]textinput.Model
	focus     int
	editIndex int
}

// newProfileForm builds the form, seeded for an edit or empty for a new record.
func newProfileForm(editIndex int, name, program string) *profileForm {
	f := &profileForm{editIndex: editIndex}
	f.inputs[fldProfileName] = newFieldInput("e.g. codex")
	f.inputs[fldProfileProgram] = newFieldInput("e.g. claude --model opus")
	f.inputs[fldProfileName].SetValue(name)
	f.inputs[fldProfileProgram].SetValue(program)
	f.applyFocus()
	return f
}

// applyFocus focuses exactly one input and blurs the rest, leaving the cursor at the end so a
// seeded field can be appended to rather than overtyped (accountForm.applyFocus's contract, and
// what lets a test tab into a field and type).
func (f *profileForm) applyFocus() {
	for i := range f.inputs {
		if i == f.focus {
			f.inputs[i].Focus()
			f.inputs[i].CursorEnd()
			continue
		}
		f.inputs[i].Blur()
	}
}

// setWidth sizes both inputs and re-windows any seeded value against the new width.
//
// The re-window is not optional. textinput only recomputes its visible window from Update or
// setCursor, and newProfileForm calls SetValue + CursorEnd while Width is still 0 — so an EDIT
// form emits its whole value regardless of Width until the user types. Measured: a 60-column
// terminal rendered a 66-cell line into a 54-cell pane, which lipgloss soft-wraps, growing the
// box and clipping the pinned hint. settings.go's startEdit avoids this by setting Width before
// CursorEnd; this form cannot, because it does not know the pane width until render.
//
// SetCursor(Position()) is the no-op that forces that recompute.
func (f *profileForm) setWidth(w int) {
	for i := range f.inputs {
		if f.inputs[i].Width == w {
			continue
		}
		f.inputs[i].Width = w
		f.inputs[i].SetCursor(f.inputs[i].Position())
	}
}

func (f *profileForm) name() string    { return strings.TrimSpace(f.inputs[fldProfileName].Value()) }
func (f *profileForm) program() string { return strings.TrimSpace(f.inputs[fldProfileProgram].Value()) }

// handleProfileFormKey routes a key while the record form is open — the editor's own Esc level,
// above the panel's three (spec §15's ladder, extended by spec §9's editor).
//
// Enter validates before committing and, on failure, leaves the form open with the message in
// the help pane: a rejected save must be fixable in place rather than thrown away, and every
// subsequent Enter must still reach validation (the accounts overlay writes that as resetting
// its own `submitted` flag; here the flag does not exist, so the property is structural).
//
// Everything the switch does not name goes to the focused input, which is why j/k/d/n/D are
// letters in a form — the rule the settings line editor and the `/` filter also follow.
func (s *SettingsOverlay) handleProfileFormKey(msg tea.KeyMsg) (changedKey string) {
	f := s.profileForm
	switch msg.String() {
	case "esc", "ctrl+c":
		s.profileForm = nil
		s.lastErr = ""
		return ""
	case "tab":
		f.focus = wrapIndex(f.focus, +1, len(f.inputs))
		f.applyFocus()
		return ""
	case "shift+tab":
		f.focus = wrapIndex(f.focus, -1, len(f.inputs))
		f.applyFocus()
		return ""
	case "enter":
		if err := s.validateProfile(); err != "" {
			s.lastErr = err
			return ""
		}
		changed := s.commitProfile()
		s.profileForm = nil
		s.lastErr = ""
		return changed
	default:
		f.inputs[f.focus], _ = f.inputs[f.focus].Update(msg)
		return ""
	}
}

// validateProfile rejects an empty name, a name another record already uses, and an empty
// program — in that order, for the reason below. It returns "" when valid, matching AccountsOverlay.validate — a string rather than an
// error because it is rendered prose, not a wrapped failure.
//
// A program is required where the accounts form lets a config dir be blank: an empty program is
// not "inherit the ambient default", it is a session that launches nothing.
//
// `i != f.editIndex` is the self-exclusion: an unrenamed edit is not a duplicate of itself,
// while renaming ONTO another record still fails. With editIndex -1 the exclusion never fires,
// so a new record is checked against every existing one.
//
// ORDER MATTERS: name-empty, then duplicate-name, then program-empty. A user fills the form top
// to bottom, so at the moment they type a colliding name the program field is still empty —
// checking the program first answers a question they have not reached yet and hides the one
// thing wrong with what they HAVE typed. The more specific error wins.
func (s *SettingsOverlay) validateProfile() string {
	f := s.profileForm
	name := f.name()
	if name == "" {
		return "A profile needs a name."
	}
	for i, p := range s.cfg.Profiles {
		if i != f.editIndex && p.Name == name {
			return "A profile named " + strconv.Quote(name) + " already exists."
		}
	}
	if f.program() == "" {
		return "A profile needs a program — the shell command that launches the agent."
	}
	return ""
}

// commitProfile writes the form back into cfg.Profiles and reports the changed key.
//
// A rename carries default_program with it. That pointer is a NAME, so leaving it behind would
// silently change what new sessions launch: it would stop matching any profile and
// config.GetProgram would fall through to running the old name as a raw shell command. Deleting
// that record has no successor that preserves anything, which is why delete refuses instead —
// see armProfileDelete.
//
// The whole struct is replaced, so any config.Profile field this form does not show would be
// destroyed. Profile is {Name, Program} today and the form shows both; the moment it grows a
// third field, carry it across here — the lesson AccountsOverlay.commit records for
// ExpectAccount.
//
// Unlike resetRow there is no before/after comparison: this is one deliberate Enter rather than
// a repeatable key, so an unchanged save costs one write instead of a rewrite per keypress.
func (s *SettingsOverlay) commitProfile() string {
	f := s.profileForm
	p := config.Profile{Name: f.name(), Program: f.program()}
	if f.editIndex < 0 {
		s.cfg.Profiles = append(s.cfg.Profiles, p)
		s.profileCursor = len(s.cfg.Profiles) - 1
		return profilesChangedKey
	}
	if s.cfg.Profiles[f.editIndex].Name == s.cfg.DefaultProgram {
		s.cfg.DefaultProgram = p.Name
	}
	s.cfg.Profiles[f.editIndex] = p
	return profilesChangedKey
}

// profilesPaneContent is the rows pane for the Profiles entry: the record form while one is
// open, else one line per profile.
//
// The lines are composed by composeRowLine, the same function the settings rows use, so the
// editor inherits spec §10's truncation ladder rather than reimplementing it: the badge yields
// first, then the program, and the name column is capped (see profileLabelWidth). The styling
// splits the same way renderRowLine's default arm does — head dim, value bright, badge faint —
// so a profile's command is not dimmer than a setting's value one pane over.
func (s *SettingsOverlay) profilesPaneContent(width int) []paneLine {
	if s.profileForm != nil {
		return s.profileFormLines(width)
	}
	t := theme.Current()
	if len(s.cfg.Profiles) == 0 {
		// An empty pane reads as a broken panel — the obligation a handoff note carries. Name
		// both keys that fill it.
		//
		// No header here even in single-pane mode: this line already names the pane's subject,
		// so a "Profiles" heading above it repeats itself — and it costs a line the narrowest
		// geometry does not have. Measured: at 40x10 the pane is 3 lines and this text wraps to
		// 2, so the header would push it to 4 and windowPane would draw a scroll marker on a
		// pane with nothing to scroll to.
		return wrappedPaneLines(
			"No profiles yet — press n to add one, or D to detect agents.",
			width, t.FaintStyle())
	}
	var lines []paneLine
	if !s.twoPane() {
		// Single-pane drill-in hides the rail, so the pane has to name itself — otherwise the
		// user is looking at an unlabelled list of names and commands, which is D2 (no
		// orientation) reintroduced at narrow widths. rowsPaneContent does exactly this for a
		// category, and dispatching to this function jumps over that branch.
		lines = append(lines, paneLine{
			text:   t.DimStyle().Bold(true).Render("Profiles"),
			rowIdx: -1,
		})
	}
	labelW := s.profileLabelWidth(width)
	for i, p := range s.cfg.Profiles {
		// Both panes always show their cursor; only the STYLE differs, so exactly one
		// accent-bright marker is on screen at a time (renderRowLine's rule).
		sel := " "
		selected := i == s.profileCursor
		rowStyle := t.FgStyle()
		if selected {
			sel = t.Glyphs.SelectionMark
			if s.focus == focusRows {
				rowStyle = t.AccentStyle()
			}
		}
		badge := ""
		if p.Name == s.cfg.DefaultProgram {
			badge = profileDefaultBadge
		}
		parts := composeRowLine(width, labelW, sel, " ",
			ansi.Truncate(p.Name, labelW, "…"), p.Program, badge)
		text := t.DimStyle().Render(parts.head) +
			t.FgStyle().Render(parts.value+parts.gap) +
			t.FaintStyle().Render(parts.badge)
		if selected {
			// Accent wins over the split: the row under the cursor must read as one unit.
			text = rowStyle.Render(parts.plain())
		}
		lines = append(lines, paneLine{text: text, rowIdx: i})
	}
	return lines
}

// profileLabelWidth is the name column: the longest profile name, capped so the program column
// keeps rowMinValueCells.
//
// A profile name is user data of unbounded length, unlike the fixed schema labels spec §10 says
// never to truncate — so this column is capped and the name tail-ellipsized, with the full name
// and program in the help pane. Without the cap one long name eats the pane and composeRowLine
// truncates the head instead, hiding every program on screen.
func (s *SettingsOverlay) profileLabelWidth(width int) int {
	w := 0
	for _, p := range s.cfg.Profiles {
		if n := ansi.StringWidth(p.Name); n > w {
			w = n
		}
	}
	return clamp(w, 1, max(1, width-rowMarkerCells-rowLabelGap-rowMinValueCells))
}

// profileFormLines renders the record form inside the rows pane: one line per field, the label
// in the pane's own label column and the input in its value column.
//
// Label BESIDE input rather than accountForm's label-above-input, because this form lives in a
// pane whose height floor is settingsMinBody (3): stacked it needs five lines and would be
// clipped on a short terminal, where Program — the field that decides what launches — is the
// line that disappears. Beside, the whole form is exactly three lines at every geometry the
// panel supports.
func (s *SettingsOverlay) profileFormLines(width int) []paneLine {
	t := theme.Current()
	f := s.profileForm
	labelW := ansi.StringWidth(profileProgramLabel)
	// The trailing -1 is the cursor cell: textinput.Model.View() renders Width + 1, on both the
	// value and the placeholder path. Without it every form line is one cell over the pane at
	// every geometry, which lipgloss soft-wraps.
	f.setWidth(max(10, width-rowMarkerCells-labelW-rowLabelGap-1))

	heading := "New profile"
	if f.editIndex >= 0 {
		heading = "Edit profile"
	}
	lines := []paneLine{{text: t.DimStyle().Bold(true).Render(heading), rowIdx: -1}}
	for i, label := range []string{profileNameLabel, profileProgramLabel} {
		style := t.DimStyle()
		if i == f.focus {
			style = t.AccentStyle()
		}
		// Pad the PLAIN label before styling, so the padding carries the style — the order
		// renderRowLine uses. (padRight measures with lipgloss.Width, which ignores escape
		// bytes, so padding a styled string would also align correctly; this is about the
		// rendered result, not about miscounting.)
		lines = append(lines, paneLine{
			text: strings.Repeat(" ", rowMarkerCells) +
				style.Render(padRight(label, labelW)) +
				strings.Repeat(" ", rowLabelGap) +
				f.inputs[i].View(),
			rowIdx: -1,
		})
	}
	return lines
}

// profilesHelp is the help pane's prose for the editor, and whether it is a warning.
//
// It replaces settingRow.footerText() for this pane because s.cursor still points at whatever
// settingRow it last sat on, and selectedRow() is unguarded — describing that row here would put
// an unrelated setting's summary under a list of profiles, the same lie railHandoff's blank
// prose avoids.
func (s *SettingsOverlay) profilesHelp() (prose string, danger bool) {
	switch {
	case s.profileConfirm:
		// Armed by armProfileDelete. It outranks everything below deliberately: a detection can
		// still land while a confirmation is up, and a background result must not displace the
		// question the user is being asked. The note is not lost — it shows once y or n answers.
		return "Delete profile " + strconv.Quote(s.cfg.Profiles[s.profileConfirmIdx].Name) +
			"? This cannot be undone.", true
	case s.profileDetecting:
		// Derived from the in-flight flag, NOT from profileNote — which handleProfilesKey clears
		// on every key. The probe outlives the key that started it, so a j pressed while it runs
		// must not erase the only thing on screen explaining why nothing has happened yet. It is
		// also what makes a second D a visible no-op rather than a key that removes feedback.
		return "Detecting installed agents…", false
	case s.profileNote != "":
		return s.profileNote, false
	case s.profileForm != nil:
		// The form fills the pane and the hint row names its keys; a third voice would crowd
		// out the validation message that lands here on a rejected save.
		return "", false
	case len(s.cfg.Profiles) == 0:
		return "Without a profile, Default program is run as the launch command itself.", false
	default:
		// Spec §10: a truncated value must be shown in full here, or the truncation loses
		// information rather than deferring it.
		return s.cfg.Profiles[s.profileCursor].Program, false
	}
}

// profilesContextLine is the editor's position readout, with the default-program fact as its
// body — the sentence behind the badge composeRowLine drops first on a narrow pane.
func (s *SettingsOverlay) profilesContextLine(width int) string {
	n := len(s.cfg.Profiles)
	if n == 0 || s.profileForm != nil {
		return ""
	}
	body := ""
	if s.cfg.Profiles[s.profileCursor].Name == s.cfg.DefaultProgram {
		body = "Default program launches this profile."
	}
	return rightAligned(body, fmt.Sprintf("%d/%d", s.profileCursor+1, n), width)
}

// profilesHintLadder is the editor's key hints, widest wording first. `/ search` outranks
// "⇥ pane" in the ladder so the filter stays advertised at the 80-column floor, where the
// widest rung does not fit.
func (s *SettingsOverlay) profilesHintLadder() []string {
	if s.profileConfirm {
		return []string{"y delete · n cancel · esc cancel", "y delete · n cancel", "y / n"}
	}
	return []string{
		"↑/↓ move · n new · ↵ edit · d delete · D detect · / search · ⇥ pane · esc back",
		"↑/↓ move · n new · ↵ edit · d delete · D detect · / search · esc back",
		"↑/↓ · n new · ↵ edit · d delete · D detect · esc back",
		"n new · ↵ edit · d delete · esc back",
		"esc back",
	}
}
