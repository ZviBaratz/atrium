package overlay

import (
	"os"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

const (
	fldName = iota
	fldConfigDir
	fldRemote
	fldPath
	fldToken
	// fldPool aliases fldToken: showToken (GH tab) and showPool (Claude tab) are
	// mutually exclusive per form instance, so whichever optional 5th field is
	// actually built lands at this same index. Never both present at once.
	fldPool = fldToken
)

// accountForm is the add/edit sub-form for one Claude, GitHub, or Antigravity
// account. It works purely in strings; the owning AccountsOverlay validates and
// builds the typed config.ClaudeAccount / config.GHAccount / config.AgyAccount on
// submit. showToken adds the GH-only Token env field and showPool adds the
// Claude-only Pool field (both at index fldToken/fldPool); at most one is present
// per instance, so nav/render/commit key off len(inputs). The Antigravity tab
// passes neither — it has no token and no pool.
type accountForm struct {
	inputs    []textinput.Model
	focus     int
	showToken bool
	showPool  bool

	picker *DirectoryPicker // non-nil only while browsing the config dir (Task 3)

	// exists-hint cache (Task 3): recompute os.Stat only when the resolved dir changes.
	statPath string
	statOK   bool
	statDone bool

	submitted bool
	canceled  bool
}

func newFieldInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.CharLimit = 256
	return ti
}

func newAccountForm(showToken, showPool bool, name, configDir, remote, path, token, pool string) *accountForm {
	// The three tabs share this form. showToken (GH only) and showPool (Claude only)
	// are passed explicitly by the caller rather than derived from one another: the
	// Antigravity tab passes both false, so it must not be told apart from Claude by
	// a bare !showToken — that once wrongly grew a Pool field on the agy form.
	inputs := []textinput.Model{
		newFieldInput("e.g. work"),
		newFieldInput("~/.claude-work  (empty = inherit ambient env)"),
		newFieldInput("comma-separated, e.g. github.com/acme"),
		newFieldInput("comma-separated, e.g. ~/work/"),
	}
	inputs[fldName].SetValue(name)
	inputs[fldConfigDir].SetValue(configDir)
	inputs[fldRemote].SetValue(remote)
	inputs[fldPath].SetValue(path)
	if showToken {
		tok := newFieldInput("comma-separated, e.g. GH_TOKEN, GITHUB_TOKEN")
		tok.SetValue(token)
		inputs = append(inputs, tok)
	}
	if showPool {
		p := newFieldInput("optional; rotation-pool name, e.g. work")
		p.SetValue(pool)
		inputs = append(inputs, p)
	}
	f := &accountForm{inputs: inputs, showToken: showToken, showPool: showPool}
	f.applyFocus()
	return f
}

// applyFocus focuses exactly one input and blurs the rest.
func (f *accountForm) applyFocus() {
	for i := range f.inputs {
		if i == f.focus {
			f.inputs[i].Focus()
			f.inputs[i].CursorEnd()
		} else {
			f.inputs[i].Blur()
		}
	}
}

// HandleKeyPress edits the focused field; returns true when the form is done
// (submitted or canceled). While the directory picker is open (f.picker != nil),
// key presses are routed to it instead and the form itself never finishes.
// HandlePaste inserts pasted text into the focused field, or into the config-dir
// picker's filter when it is open. It never reports done: the key switch maps
// "enter" to submit and "esc" to cancel, so a routed paste of either word would
// commit or discard the record instead of typing it. A config dir is a path, and a
// path is the thing users paste.
func (f *accountForm) HandlePaste(msg tea.PasteMsg) {
	if f.picker != nil {
		f.picker.HandlePaste(msg.Content)
		return
	}
	f.inputs[f.focus], _ = f.inputs[f.focus].Update(msg)
}

func (f *accountForm) HandleKeyPress(msg tea.KeyPressMsg) (done bool) {
	if f.picker != nil {
		switch msg.String() {
		case "enter":
			f.inputs[fldConfigDir].SetValue(f.picker.GetSelectedPath())
			f.picker = nil
		case "esc", "ctrl+c":
			f.picker = nil
		case "tab":
			f.picker.CompletePrefix()
		default:
			f.picker.HandleKeyPress(msg)
		}
		return false
	}
	switch msg.String() {
	case "enter":
		f.submitted = true
		return true
	case "esc", "ctrl+c":
		f.canceled = true
		return true
	case "ctrl+o":
		if f.focus == fldConfigDir {
			f.openPicker()
		}
		return false
	case "tab":
		f.focus = (f.focus + 1) % len(f.inputs)
		f.applyFocus()
		return false
	case "shift+tab":
		f.focus = (f.focus - 1 + len(f.inputs)) % len(f.inputs)
		f.applyFocus()
		return false
	default:
		f.inputs[f.focus], _ = f.inputs[f.focus].Update(msg)
		return false
	}
}

// openPicker opens the directory picker seeded with the current config-dir value
// (resolved, so a "~"-prefixed value starts browsing from the real path) and the
// home directory as a fallback candidate.
func (f *accountForm) openPicker() {
	cur := config.ClaudeAccount{ConfigDir: f.ConfigDir()}.ResolvedConfigDir()
	p := NewDirectoryPicker([]string{cur, "~"})
	p.SetLabel("Config dir")
	p.SetVisibleRows(5)
	p.Focus()
	f.picker = p
}

// configDirHint reports whether the resolved config dir exists, cached by path so
// the os.Stat runs only when the value changes (not on every render/keystroke).
func (f *accountForm) configDirHint() string {
	v := f.ConfigDir()
	if v == "" {
		return ""
	}
	resolved := config.ClaudeAccount{ConfigDir: v}.ResolvedConfigDir()
	if !f.statDone || resolved != f.statPath {
		info, err := os.Stat(resolved)
		f.statOK = err == nil && info.IsDir()
		f.statPath = resolved
		f.statDone = true
	}
	if f.statOK {
		return theme.Current().DimStyle().Render("  (exists)")
	}
	return theme.Current().DangerStyle().Render("  (not found)")
}

func (f *accountForm) Name() string            { return strings.TrimSpace(f.inputs[fldName].Value()) }
func (f *accountForm) ConfigDir() string       { return strings.TrimSpace(f.inputs[fldConfigDir].Value()) }
func (f *accountForm) RemoteMatches() []string { return parseList(f.inputs[fldRemote].Value()) }
func (f *accountForm) PathMatches() []string   { return parseList(f.inputs[fldPath].Value()) }

func (f *accountForm) TokenEnv() []string {
	if !f.showToken {
		return nil
	}
	return parseList(f.inputs[fldToken].Value())
}

// Pool returns the Claude-only rotation-pool field's value, or "" when this form
// is a GH-tab edit (showPool false, the field was never built).
func (f *accountForm) Pool() string {
	if !f.showPool {
		return ""
	}
	return strings.TrimSpace(f.inputs[fldPool].Value())
}

func (f *accountForm) Submitted() bool { return f.submitted }
func (f *accountForm) Canceled() bool  { return f.canceled }

// parseList splits a comma-separated field, trims each token, and drops empties
// (a stray " " token would otherwise substring-match any path with a space).
// Returns nil (not []string{}) so the omitempty config fields stay dormant.
func parseList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Render draws the field list, or the directory picker sub-view when it is open.
func (f *accountForm) Render(inner int) string {
	t := theme.Current()
	if f.picker != nil {
		f.picker.SetWidth(inner)
		return t.DimStyle().Render("Browse config dir") + "\n\n" + f.picker.Render()
	}
	labels := []string{"Name", "Config dir", "Remote match", "Path match"}
	switch {
	case f.showToken:
		labels = append(labels, "Token env")
	case f.showPool:
		labels = append(labels, "Pool")
	}
	var b strings.Builder
	for i := range f.inputs {
		label := t.DimStyle().Render(labels[i])
		if i == f.focus {
			label = t.AccentStyle().Render(labels[i])
		}
		hint := ""
		if i == fldConfigDir {
			hint = f.configDirHint()
		}
		b.WriteString(label + hint + "\n" + f.inputs[i].View() + "\n")
	}
	return b.String()
}
