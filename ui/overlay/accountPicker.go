package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/config"

	tea "charm.land/bubbletea/v2"
)

// accountEntry is one selectable line in the picker: a pool "⇄" entry (member
// nil) that rotates the pool, or a concrete account (member set) that pins.
// pool is the cluster key and is set on both an entry's ⇄ line and its members.
type accountEntry struct {
	label  string
	pool   string
	member *config.ClaudeAccount
}

// buildAccountEntries groups accounts by pool (first-appearance order): each
// multi-member pool contributes a "<pool> ⇄" rotating entry followed by its
// members; ungrouped accounts (Pool == "") contribute one entry each, in config
// order. With no pools this is one entry per account, identical to before.
func buildAccountEntries(accounts []config.ClaudeAccount) []accountEntry {
	var order []string
	byPool := map[string][]int{}
	for i, a := range accounts {
		if a.Pool == "" {
			continue
		}
		if _, seen := byPool[a.Pool]; !seen {
			order = append(order, a.Pool)
		}
		byPool[a.Pool] = append(byPool[a.Pool], i)
	}
	var entries []accountEntry
	for _, p := range order {
		entries = append(entries, accountEntry{label: p + " ⇄", pool: p})
		for _, idx := range byPool[p] {
			a := accounts[idx]
			entries = append(entries, accountEntry{label: "  " + a.Name, pool: p, member: &a})
		}
	}
	for i := range accounts {
		if accounts[i].Pool == "" {
			a := accounts[i]
			entries = append(entries, accountEntry{label: a.Name, pool: "", member: &a})
		}
	}
	return entries
}

// AccountPicker is an embeddable horizontal selector for choosing the Claude Code
// account a new session runs under. It mirrors ProfilePicker.
type AccountPicker struct {
	entries []accountEntry
	cursor  int
	focused bool
	width   int
	// touched flips true the first time the user drives the picker with a nav key.
	// It separates an auto-routed preselection (which the form revises as the target
	// project changes) from a deliberate override (which must stick). Programmatic
	// SelectByName never sets it.
	touched bool
}

// NewAccountPicker creates a picker over accounts; the first is selected by default.
func NewAccountPicker(accounts []config.ClaudeAccount) *AccountPicker {
	return &AccountPicker{entries: buildAccountEntries(accounts)}
}

// SelectByName preselects the pool ⇄ entry (preferred) or, absent one, the
// member account with the given name (e.g. the auto-routed one). No-op if the
// name is not present, or once the user has taken manual control (Touched) —
// a deliberate choice outranks later auto-routing.
func (ap *AccountPicker) SelectByName(name string) {
	if ap.touched {
		return
	}
	for i, e := range ap.entries { // prefer the pool ⇄ entry
		if e.member == nil && e.pool == name {
			ap.cursor = i
			return
		}
	}
	for i, e := range ap.entries { // else a concrete member
		if e.member != nil && e.member.Name == name {
			ap.cursor = i
			return
		}
	}
}

// Focus gives the picker focus.
func (ap *AccountPicker) Focus() { ap.focused = true }

// Blur removes focus from the picker.
func (ap *AccountPicker) Blur() { ap.focused = false }

// SetWidth sets the rendering width.
func (ap *AccountPicker) SetWidth(w int) { ap.width = w }

// HasMultiple returns true if there is more than one entry to choose from.
func (ap *AccountPicker) HasMultiple() bool { return len(ap.entries) > 1 }

// hasPools reports whether any entry is a pool ⇄ (rotating) entry, i.e. at least
// one multi-member pool is configured. Used to gloss the picker's hint only when
// the rotate-vs-pin distinction actually exists.
func (ap *AccountPicker) hasPools() bool {
	for _, e := range ap.entries {
		if e.member == nil {
			return true
		}
	}
	return false
}

// Touched reports whether the user has driven the picker with a nav key. The form
// uses it to decide auto-routed preselection (untouched) versus an override (touched).
func (ap *AccountPicker) Touched() bool { return ap.touched }

// HandleKeyPress moves the cursor; Up/Down mirror Left/Right (the form navigates ↑↓).
// The cursor wraps at both ends so one keypress reaches the opposite end. Any nav key
// marks the picker touched — engaging the control signals intent.
func (ap *AccountPicker) HandleKeyPress(msg tea.KeyPressMsg) bool {
	switch msg.Code {
	case tea.KeyLeft, tea.KeyUp:
		ap.touched = true
		ap.cursor = wrapIndex(ap.cursor, -1, len(ap.entries))
		return true
	case tea.KeyRight, tea.KeyDown:
		ap.touched = true
		ap.cursor = wrapIndex(ap.cursor, +1, len(ap.entries))
		return true
	}
	return false
}

// Selected returns the cursored entry (zero value when empty).
func (ap *AccountPicker) Selected() accountEntry {
	if len(ap.entries) == 0 {
		return accountEntry{}
	}
	if ap.cursor < 0 || ap.cursor >= len(ap.entries) {
		return ap.entries[0]
	}
	return ap.entries[ap.cursor]
}

// GetSelectedAccount returns the cursored account for display: the member if the
// entry pins one, else a synthetic account named for the pool ⇄ entry.
func (ap *AccountPicker) GetSelectedAccount() config.ClaudeAccount {
	e := ap.Selected()
	if e.member != nil {
		return *e.member
	}
	return config.ClaudeAccount{Name: e.pool, Pool: e.pool}
}

// Render draws the picker (label + horizontal options), mirroring ProfilePicker.
func (ap *AccountPicker) Render() string {
	var s strings.Builder
	s.WriteString(ppLabelStyle().Render("Account"))
	if ap.HasMultiple() && ap.focused {
		hint := "  ↑↓ to change"
		// When pools exist, the ⇄ entry rotates across the pool while a member entry
		// pins one — a distinction otherwise carried only by the glyph and indent, so
		// gloss whichever the cursor sits on.
		if ap.hasPools() {
			switch sel := ap.Selected(); {
			case sel.member == nil:
				hint += " · ⇄ rotates the pool"
			case sel.pool != "":
				hint += " · pins this member"
			}
		}
		s.WriteString(ppDimStyle().Render(hint))
	}
	s.WriteString("\n\n")
	for i, e := range ap.entries {
		label := " " + e.label + " "
		switch {
		case i == ap.cursor && ap.focused:
			s.WriteString(ppSelectedStyle().Render(label))
		case i == ap.cursor:
			s.WriteString(label)
		default:
			s.WriteString(ppDimStyle().Render(label))
		}
		if i < len(ap.entries)-1 {
			s.WriteString(ppDimStyle().Render(" | "))
		}
	}
	return s.String()
}
