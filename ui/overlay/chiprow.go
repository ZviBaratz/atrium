package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// claudeFieldNA is the dim placeholder the claude-only fields (model and
// permission mode) render while the form's effective program is not Claude
// Code; their enabled state is driven together by syncClaudeFieldsEnabled.
const claudeFieldNA = "  n/a — the selected profile is not Claude Code"

// noOverrideChip is the label of the first chip in every claude override field
// (model, effort, permission mode) — the no-op choice that composes no flag. It
// reads "inherit", not "default", and the distinction is load-bearing: "default"
// is a real member of claude's --permission-mode enum
// (agent.ClaudePermissionModes' superset), the session list *suppresses* the mode
// chip for a detected "default" (ui/list_render.go), and README documents
// `mode:default` as "matches sessions showing no mode chip". So "default" already
// means a specific rendered state everywhere else in Atrium; using it here for
// "send no flag" was the one place the word meant something different. "inherit"
// says what the chip does — defer to whatever resolves the value (claude's own
// default, or a profile/settings.json pin) — and is 7 cells, exactly the width of
// "default", so every chip row keeps the width it already had: model 41,
// permissions 38, effort 42, the last sitting exactly on the 42 inner cells an
// 80-col terminal yields (see modelField.go). One const, three fields, so the word
// can never drift between them.
//
// "inherit" was in fact the original label: #116 renamed it to "default" because
// Atrium cannot know which default claude will resolve. That objection is answered
// rather than ignored — the focused hint now names the nearest source Atrium can
// see (noOverrideHint), so the chip no longer has to carry that meaning alone. The
// round trip is recorded here so a third flip has to argue with both decisions.
const noOverrideChip = "inherit"

// chipRow is the state machine behind the chip-style fields: a horizontal row
// of options with a wrapping cursor, focus, and an inert state. By convention
// the first chip is the no-op (noOverrideChip) choice, so selected returns "" for
// it. ModeField is a pure chip row; ModelField layers its free-text custom
// mode on top.
type chipRow struct {
	options  []string
	labels   []string // display labels; nil = use options as-is (len must equal len(options) when set)
	cursor   int
	focused  bool
	disabled bool
	// pinned / pinLabel / pinMixed describe where the flag's effective value comes
	// from across the selected claude programs, for the focused no-op-chip hint (see
	// noOverrideHint). pinned reports that every selected program pins the flag;
	// pinLabel is that pin's display label, or "" when Atrium cannot name the value
	// (outside its enum snapshot, or a field whose values are unbounded); pinMixed is
	// set when the programs disagree or only some pin. Set by syncClaudeFieldsEnabled
	// via SetProfilePin. pinned is deliberately separate from pinLabel: "a pin Atrium
	// cannot name" must not render as "no pin".
	pinned   bool
	pinLabel string
	pinMixed bool
}

// Focus gives the row focus.
func (c *chipRow) Focus() { c.focused = true }

// Blur removes focus from the row.
func (c *chipRow) Blur() { c.focused = false }

// SetDisabled toggles the inert state (the effective program is not claude).
func (c *chipRow) SetDisabled(disabled bool) { c.disabled = disabled }

// Disabled reports whether the row is inert.
func (c *chipRow) Disabled() bool { return c.disabled }

// SetProfilePin records where the flag's effective value comes from across the
// selected claude programs, for the focused no-op-chip hint. pinned is true when
// every selected program pins the flag; label is that pin's display label, or ""
// when Atrium cannot name the value (pass "" and pinned=true for a pin outside the
// enum snapshot, so the hint says a pin exists without echoing it); mixed is true
// when the selected programs disagree or only some pin. Recomputed by
// syncClaudeFieldsEnabled at construction and after every variant-control change.
func (c *chipRow) SetProfilePin(label string, pinned, mixed bool) {
	c.pinLabel = label
	c.pinned = pinned
	c.pinMixed = mixed
}

// noOverrideHint returns the focused hint for the no-op chip: it names the nearest
// source Atrium can see with zero I/O, so the user knows what "inherit" defers to
// before selecting a real value.
//
// It says "program", not "profile", because the pin can come from either: with no
// profiles configured the selected program is the `default_program` config key (see
// claudePrograms), and naming a profile that does not exist would misdescribe the
// one thing this hint exists to name. "varies by profile" keeps its wording — it
// needs two disagreeing programs, which only a variant picker can select, and that
// exists only when profiles do.
//
// echoValue controls whether a single shared pin names its value. The enum fields
// (effort, permission mode) pass true, which is bounded because
// syncClaudeFieldsEnabled supplies a label only for a value inside Atrium's own
// enum: the widest is "accept-edits", so "Permissions  program pins accept-edits"
// is 38 of the 42 cells available. An out-of-enum pin arrives pinned with no label
// and falls through to "program pins it" — that is what keeps an unvalidated
// --effort token (see agent.EffortFlag) from overflowing the row. The model field
// passes false outright: model values are unbounded (64-char, vendor-prefixed IDs)
// and VariantPicker never renders a profile's program, so echoing one buys nothing.
func (c *chipRow) noOverrideHint(echoValue bool) string {
	switch {
	case c.pinMixed:
		return "varies by profile"
	case !c.pinned:
		return "claude's default"
	case echoValue && c.pinLabel != "":
		return "program pins " + c.pinLabel
	default:
		return "program pins it"
	}
}

// wrapIndex moves cur by delta within [0,n), wrapping at both ends. A
// non-positive n (no options) returns 0, keeping callers panic-free since
// "% 0" would panic where the old clamp checks were silently safe.
func wrapIndex(cur, delta, n int) int {
	if n <= 0 {
		return 0
	}
	return ((cur+delta)%n + n) % n
}

// moveCursor cycles the chips with the arrow keys (Up/Down accepted alongside
// Left/Right, matching the profile picker), wrapping at both ends so one keypress
// reaches the opposite end. Every other key is a no-op — in particular Esc is
// never consumed, staying the form's close key.
func (c *chipRow) moveCursor(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		c.cursor = wrapIndex(c.cursor, -1, len(c.options))
	case tea.KeyRight, tea.KeyDown:
		c.cursor = wrapIndex(c.cursor, +1, len(c.options))
	}
}

// selected returns the cursor chip, or "" when the row should contribute
// nothing: disabled, or sitting on the first (no-op) chip.
func (c *chipRow) selected() string {
	if c.disabled || c.cursor == 0 {
		return ""
	}
	return c.options[c.cursor]
}

// render renders the chip row (the profile-picker idiom): the cursor chip is
// highlighted when focused, plain when not, every other chip dim, with dim "·"
// separators.
func (c *chipRow) render() string {
	var s strings.Builder
	for i, opt := range c.options {
		displayLabel := opt
		if c.labels != nil {
			displayLabel = c.labels[i]
		}
		label := " " + displayLabel + " "
		switch {
		case i == c.cursor && c.focused:
			s.WriteString(ppSelectedStyle().Render(label))
		case i == c.cursor:
			s.WriteString(label)
		default:
			s.WriteString(mfDimStyle().Render(label))
		}
		if i < len(c.options)-1 {
			s.WriteString(mfDimStyle().Render("·"))
		}
	}
	return s.String()
}
