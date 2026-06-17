# Session Note Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional freeform note to a session, surfaced on its list row, so a parked session answers "why is this here?" at a glance without attaching.

**Architecture:** A new cosmetic `note` field on `session.Instance` (mirroring `displayName` exactly: trimmed, mutable any time, decoupled from git/tmux), persisted via `InstanceData` with `omitempty`. Rendering adds a `noteSeg` producer and a status-aware branch in `InstanceRenderer.Render`: a paused row shows the note in line 2's (frozen) version-control slot with age on the right; a running row keeps its live VC line and gets the note on its own indented third line; an empty note changes nothing. Entry/editing folds into the existing `RenameOverlay`, extended to two fields (name + note); pausing opens that overlay focused on the note.

**Tech Stack:** Go, Bubble Tea / Bubbles (`textinput`), lipgloss, `go-runewidth`, testify. Build/test via `just`.

## Global Constraints

- Go toolchain may not be on `PATH`: run `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test` (and same for `build`). CI uses `go.mod`'s version.
- Tests must be hermetic: never read/write the real data dir; no real tmux/git server calls in unit tests.
- All row width math uses `runewidth.StringWidth`; every rendered line must total exactly the row width `W`.
- The note glyph must be a **single-cell** glyph (`runewidth.StringWidth(glyph) == 1`) and **must not be an emoji** — wide/ZWJ glyphs desync Bubble Tea's incremental renderer (the known row-ghosting bug).
- Commits: Conventional Commits, lowercase. End each commit message with:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- License header conventions unchanged (AGPL-3.0 project).
- Verify every task with `just build` **and** `just test` before considering it done.

---

### Task 1: Domain field + persistence

**Files:**
- Modify: `session/instance.go` (struct field ~line 60; `ToInstanceData` ~line 188; `FromInstanceData` ~line 247; new accessors after `SetDisplayName` ~line 1104)
- Modify: `session/storage.go` (`InstanceData` struct ~line 14)
- Test: `session/note_test.go` (create)

**Interfaces:**
- Produces: `(*session.Instance).Note() string`, `(*session.Instance).SetNote(string)`, and `InstanceData.Note string` (JSON key `note`, omitempty). Later tasks rely on `Note()`/`SetNote()`.

- [ ] **Step 1: Write the failing test**

Create `session/note_test.go`:

```go
package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstance_SetNoteTrims(t *testing.T) {
	i := &Instance{Title: "t"}
	i.SetNote("  blocked on review  ")
	require.Equal(t, "blocked on review", i.Note())
	i.SetNote("   ")
	require.Equal(t, "", i.Note(), "whitespace-only note clears")
}

func TestToInstanceData_CarriesNote(t *testing.T) {
	i := &Instance{Title: "t"}
	i.SetNote("park me")
	require.Equal(t, "park me", i.ToInstanceData().Note)
}

func TestInstanceData_NoteJSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(InstanceData{Title: "t", Note: "waiting on CI"})
	require.NoError(t, err)
	require.Contains(t, string(b), `"note":"waiting on CI"`)

	// omitempty: an empty note is not written.
	b, err = json.Marshal(InstanceData{Title: "t"})
	require.NoError(t, err)
	require.NotContains(t, string(b), `"note"`)

	// A legacy state.json with no note key decodes to "".
	var d InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t"}`), &d))
	require.Equal(t, "", d.Note)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | grep -A2 note_test`
Expected: FAIL — `i.SetNote` / `i.Note` / `InstanceData.Note` undefined (compile error).

- [ ] **Step 3a: Add the struct field**

In `session/instance.go`, immediately after the `displayName string` field (~line 60):

```go
	// note is an optional freeform annotation surfaced on the session's row
	// (e.g. "blocked on review"). Like displayName it is cosmetic, mutable at
	// any time, and decoupled from the git branch / tmux session.
	note string
```

- [ ] **Step 3b: Add the accessors**

In `session/instance.go`, immediately after `SetDisplayName` (~line 1104):

```go
// Note returns the freeform annotation shown on the session's row, or "" when unset.
func (i *Instance) Note() string { return i.note }

// SetNote sets the freeform annotation. Whitespace is trimmed; an empty value clears it.
// Like SetDisplayName it works at any time and is independent of the git branch and tmux
// session.
func (i *Instance) SetNote(note string) { i.note = strings.TrimSpace(note) }
```

(`strings` is already imported — `SetDisplayName` uses `strings.TrimSpace`.)

- [ ] **Step 3c: Wire serialization**

In `ToInstanceData` (~line 188), add to the `InstanceData{...}` literal, right after `DisplayName: i.displayName,`:

```go
		Note: i.note,
```

In `FromInstanceData` (~line 247), add to the `&Instance{...}` literal, right after `displayName: data.DisplayName,`:

```go
		note: data.Note,
```

In `session/storage.go`, add to the `InstanceData` struct right after the `DisplayName` field (~line 14):

```go
	// Note is an optional freeform annotation shown on the session's row (e.g.
	// "blocked on review"). omitempty keeps old state files compact; absence
	// decodes to "" (no note).
	Note string `json:"note,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | tail -5`
Expected: PASS (whole suite green).

- [ ] **Step 5: Commit**

```bash
git add session/instance.go session/storage.go session/note_test.go
git commit -m "feat(session): add persisted freeform note field

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Theme note glyph

**Files:**
- Modify: `ui/theme/theme.go` (`Glyphs` struct, ~line 37)
- Modify: `ui/theme/registry.go` (nf constant block ~line 21; `nfGlyphs()` ~line 40; `unicodeFallback` Glyphs literal ~line 124)
- Test: `ui/theme/theme_test.go` (create, or append if it exists)

**Interfaces:**
- Produces: `theme.Theme.Glyphs.Note string` — a single-cell annotation marker, populated for every theme. Later tasks read `p.th.Glyphs.Note`.

- [ ] **Step 1: Write the failing test**

Create `ui/theme/theme_test.go` (if the file already exists, append the function):

```go
package theme

import (
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

func TestNoteGlyphIsSingleCellEverywhere(t *testing.T) {
	for _, name := range Names() {
		t.Cleanup(Set(name))
		g := Current().Glyphs.Note
		require.NotEmpty(t, g, "%s: note glyph must be set", name)
		require.Equal(t, 1, runewidth.StringWidth(g), "%s: note glyph must be single-cell (no emoji)", name)
	}
}
```

If `theme.Names()` does not exist, replace the loop with explicit `Set("tokyo-night")` and `Set("unicode")` cleanups and assert each. (Check with `grep -n "func Names" ui/theme/*.go`; if absent, use the explicit form below.)

```go
func TestNoteGlyphIsSingleCellEverywhere(t *testing.T) {
	for _, name := range []string{"tokyo-night", "unicode"} {
		t.Cleanup(Set(name))
		g := Current().Glyphs.Note
		require.NotEmpty(t, g, "%s: note glyph must be set", name)
		require.Equal(t, 1, runewidth.StringWidth(g), "%s: note glyph must be single-cell", name)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | grep -A2 NoteGlyph`
Expected: FAIL — `Glyphs.Note` undefined (compile error).

- [ ] **Step 3a: Add the struct field**

In `ui/theme/theme.go`, inside `type Glyphs struct` (after the `Dirty` field, ~line 47):

```go
	Note          string // precedes a freeform session note
```

- [ ] **Step 3b: Add the Nerd-Font constant**

In `ui/theme/registry.go`, in the const block with the other `nf*` glyphs (~after line 21):

```go
	nfNote   = 0xf249 // nf-fa-sticky_note
```

- [ ] **Step 3c: Populate both glyph sets**

In `nfGlyphs()` (~line 40), add after `Dirty: ...`:

```go
		Note:          string(rune(nfNote)),
```

In the `unicodeFallback` Glyphs literal (~line 116, after `Dirty: "*",`):

```go
			Note:          "✎",
```

Confirm no other theme defines its own `Glyphs` literal:
`grep -n "Glyphs:" ui/theme/registry.go` — every entry must be either `Glyphs: nfGlyphs()` or the one `unicodeFallback` literal. If a third literal exists, add `Note` to it too with a single-cell glyph.

- [ ] **Step 4: Run test to verify it passes**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | grep -A2 NoteGlyph`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/theme/theme.go ui/theme/registry.go ui/theme/theme_test.go
git commit -m "feat(theme): add single-cell note glyph

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Row rendering

**Files:**
- Modify: `ui/row.go` (add `noteSeg` producer near `nameSeg`, ~line 191)
- Modify: `ui/list.go` (`InstanceRenderer.Render`: line-2 note substitution + third line; final `JoinVertical`, ~lines 484–542)
- Test: `ui/list_test.go` (append)

**Interfaces:**
- Consumes: `(*session.Instance).Note()` (Task 1), `p.th.Glyphs.Note` (Task 2), and the existing `rowPaint`, `composeLine`, `ageSeg`, `nameSeg`, `flexSeg`, `SanitizeWidth`.
- Produces: `(rowPaint).noteSeg(*session.Instance) rowSeg`.

- [ ] **Step 1: Write the failing test**

Append to `ui/list_test.go`:

```go
// renderRowWith builds a single row at width 80 under the unicode theme (so the
// note glyph "✎" is stable), applying setup to the instance before rendering.
func renderRowWith(t *testing.T, setup func(i *session.Instance)) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "auth-refactor", Path: ".", Program: "echo"})
	require.NoError(t, err)
	setup(inst)
	return r.Render(inst, 1, false)
}

func TestRender_PausedNoteTakesLineTwo(t *testing.T) {
	out := renderRowWith(t, func(i *session.Instance) {
		i.SetStatus(session.Paused)
		i.SetNote("blocked on Benoit's review")
		i.SetDiffStats(&git.DiffStats{Added: 5, Removed: 2, Commits: 3})
	})
	require.Contains(t, out, "✎", "paused row shows the note glyph")
	require.Contains(t, out, "blocked on Benoit's review")
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 2, "paused-with-note stays two lines (note replaces the VC line)")
	for _, ln := range lines {
		require.Equal(t, 79, ansi.StringWidth(ln), "each line totals W (width-1 for the marker col)")
	}
}

func TestRender_RunningNoteGetsThirdLine(t *testing.T) {
	out := renderRowWith(t, func(i *session.Instance) {
		i.SetStatus(session.Running)
		i.SetNote("risky — double-check before merge")
		i.SetDiffStats(&git.DiffStats{Added: 5, Removed: 2, Commits: 3})
	})
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 3, "running-with-note gets a third line; live VC line is preserved")
	require.Contains(t, lines[2], "✎")
	require.Contains(t, lines[2], "risky — double-check before merge")
	for _, ln := range lines {
		require.Equal(t, 79, ansi.StringWidth(ln), "every line totals W")
	}
}

func TestRender_NoNoteIsUnchanged(t *testing.T) {
	out := renderRowWith(t, func(i *session.Instance) {
		i.SetStatus(session.Running)
		i.SetDiffStats(&git.DiffStats{Added: 5, Removed: 2, Commits: 3})
	})
	require.Len(t, strings.Split(out, "\n"), 2, "no note → exactly two lines, unchanged")
	require.NotContains(t, out, "✎")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | grep -E "Render_(Paused|Running|NoNote)"`
Expected: FAIL — `noteSeg` undefined and/or row line counts wrong.

- [ ] **Step 3a: Add the `noteSeg` producer**

In `ui/row.go`, after `nameSeg` (~line 191):

```go
// noteSeg is the freeform session note as a flex segment: a leading note glyph
// plus the width-sanitized note text, in a distinct accent (Purple) so it reads
// as an annotation, never confused with the branch (dim) or the name (Fg).
// Returns the zero rowSeg (renders nothing) when the instance has no note.
func (p rowPaint) noteSeg(i *session.Instance) rowSeg {
	note := strings.TrimSpace(i.Note())
	if note == "" {
		return rowSeg{}
	}
	text := p.th.Glyphs.Note + " " + theme.SanitizeWidth(note)
	return p.flexSeg(text, p.th.Palette.Purple, false)
}
```

- [ ] **Step 3b: Branch the renderer on the note**

In `ui/list.go`, inside `Render`, after the `if i.IsDirect() { ... } else { ... }` block that assigns `line2` (i.e. right after line ~535's closing `}` of that block, before the `// --- Left marker` comment at ~line 537), insert:

```go
	// Session note: when the session is paused, the note takes line 2's
	// (now-frozen) version-control slot, keeping the age on the right. When it is
	// running, line 2's live VC signal is preserved and the note gets its own
	// indented third line. No note → both branches are skipped and the row is
	// unchanged.
	var line3 string
	if note := p.noteSeg(i); note.plain != "" {
		indent := p.seg(strings.Repeat(" ", indentW), th.Palette.FgDim)
		if i.Paused() {
			var right2 []rowSeg
			if age, ok := p.ageSeg(i); ok {
				right2 = append(right2, age)
			}
			line2 = p.composeLine(W, []rowSeg{indent, note}, right2)
		} else {
			line3 = p.composeLine(W, []rowSeg{indent, note}, nil)
		}
	}
```

(`indentW`, `W`, `p`, `th`, and `line2` are all already in scope at that point.)

- [ ] **Step 3c: Emit the optional third line**

In `ui/list.go`, replace the final return of `Render` (~line 542):

```go
	return lipgloss.JoinVertical(lipgloss.Left, marker+line1, marker+line2)
```

with:

```go
	rows := []string{marker + line1, marker + line2}
	if line3 != "" {
		rows = append(rows, marker+line3)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
```

The list viewport needs no change: `List.String` derives the selected block's height from the actual emitted line count (`selH = len(lines)-at`), so a three-line row is accounted for automatically.

- [ ] **Step 4: Run test to verify it passes**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | tail -8`
Expected: PASS (new Render tests green; existing row/list tests still green — a note is only rendered when set, so no-note rows are byte-for-byte unchanged).

- [ ] **Step 5: Commit**

```bash
git add ui/row.go ui/list.go ui/list_test.go
git commit -m "feat(ui): render session note on paused and running rows

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Rename overlay — second (note) field

**Files:**
- Modify: `ui/overlay/renameOverlay.go` (whole file: add note input, focus state, accessor, constructor signature)
- Test: `ui/overlay/renameOverlay_test.go` (append)

**Interfaces:**
- Produces: `NewRenameOverlay(currentLabel, currentNote string, focusNote bool) *RenameOverlay`, `(*RenameOverlay).NoteValue() string`. `Tab`/`shift+tab` cycle focus name↔note; `ctrl+d` toggles deep rename (relocated from `Tab`). Task 5 consumes these.

- [ ] **Step 1: Write the failing test**

Append to `ui/overlay/renameOverlay_test.go`:

```go
func TestRenameOverlay_NoteFieldEditsAndReturns(t *testing.T) {
	o := NewRenameOverlay("auth-refactor", "", true) // focus the note
	for _, r := range "waiting on CI" {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.False(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}), "enter closes")
	require.True(t, o.IsSubmitted())
	require.Equal(t, "auth-refactor", o.Value(), "name field untouched")
	require.Equal(t, "waiting on CI", o.NoteValue())
}

func TestRenameOverlay_TabCyclesNameAndNote(t *testing.T) {
	o := NewRenameOverlay("name", "note", false) // focus the name
	for _, r := range "X" { // edits the name field
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // move to note
	for _, r := range "Y" { // edits the note field
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.Equal(t, "nameX", o.Value())
	require.Equal(t, "noteY", o.NoteValue())
}

func TestRenameOverlay_NoteCharLimit(t *testing.T) {
	o := NewRenameOverlay("n", "", true)
	for i := 0; i < 200; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	}
	require.LessOrEqual(t, len(o.NoteValue()), 80, "note capped at 80 chars")
}
```

(Use the imports already in the test file: `tea "github.com/charmbracelet/bubbletea"`, `require`. Add them if missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | grep -A2 RenameOverlay_Note`
Expected: FAIL — `NewRenameOverlay` arity / `NoteValue` undefined.

- [ ] **Step 3: Rewrite `renameOverlay.go`**

Replace the file body (keep the package + imports; add nothing new beyond what's shown) with:

```go
package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RenameOverlay is a lightweight two-field dialog: a session's cosmetic display
// label and its freeform note. Tab/shift+tab move focus between the two; ctrl+d
// toggles whether submitting the name also renames the underlying git branch,
// worktree, and tmux session (deep) or only the cosmetic label.
type RenameOverlay struct {
	name      textinput.Model
	note      textinput.Model
	focusNote bool // which field has focus (false = name, true = note)
	submitted bool
	canceled  bool
	width     int
	deep      bool
}

// NewRenameOverlay creates the dialog pre-filled with the current label and note.
// focusNote starts the cursor on the note field (used by the pause flow, where the
// point is to jot why the session is being parked); otherwise the name is focused.
func NewRenameOverlay(currentLabel, currentNote string, focusNote bool) *RenameOverlay {
	name := newTitleInput()
	name.SetValue(currentLabel)

	note := newTitleInput()
	note.CharLimit = 80
	note.Placeholder = "note (optional) — e.g. blocked on review"
	note.SetValue(currentNote)

	o := &RenameOverlay{name: name, note: note, focusNote: focusNote, width: 50, deep: false}
	o.applyFocus()
	return o
}

// applyFocus focuses exactly one input and blurs the other, leaving the cursor at
// the end of the focused field.
func (r *RenameOverlay) applyFocus() {
	if r.focusNote {
		r.name.Blur()
		r.note.Focus()
		r.note.CursorEnd()
	} else {
		r.note.Blur()
		r.name.Focus()
		r.name.CursorEnd()
	}
}

// HandleKeyPress processes a key press and returns true if the overlay should close.
// enter submits, esc/ctrl+c cancel, tab/shift+tab switch field, ctrl+d toggles deep,
// everything else edits the focused field.
func (r *RenameOverlay) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "enter":
		r.submitted = true
		return true
	case "esc", "ctrl+c":
		r.canceled = true
		return true
	case "tab", "shift+tab":
		r.focusNote = !r.focusNote
		r.applyFocus()
		return false
	case "ctrl+d":
		r.deep = !r.deep
		return false
	default:
		if r.focusNote {
			r.note, _ = r.note.Update(msg)
		} else {
			r.name, _ = r.name.Update(msg)
		}
		return false
	}
}

// IsDeep reports whether the user chose a deep rename (branch + worktree + tmux).
func (r *RenameOverlay) IsDeep() bool { return r.deep }

// Value returns the trimmed display label.
func (r *RenameOverlay) Value() string { return strings.TrimSpace(r.name.Value()) }

// NoteValue returns the trimmed note ("" clears it).
func (r *RenameOverlay) NoteValue() string { return strings.TrimSpace(r.note.Value()) }

// IsSubmitted reports whether the user accepted the dialog.
func (r *RenameOverlay) IsSubmitted() bool { return r.submitted }

// IsCanceled reports whether the user dismissed the dialog.
func (r *RenameOverlay) IsCanceled() bool { return r.canceled }

// SetWidth sets the width of the overlay.
func (r *RenameOverlay) SetWidth(width int) { r.width = width }

// Render renders the overlay as a bordered box.
func (r *RenameOverlay) Render() string {
	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2).
		Width(r.width)

	deepMark, labelMark := "○", "○"
	if r.deep {
		deepMark = "●"
	} else {
		labelMark = "●"
	}
	dim := theme.Current().DimStyle()
	nameLabel := dim.Render("name")
	noteLabel := dim.Render("note")
	mode := dim.Render("mode: " + labelMark + " label only\n      " + deepMark + " deep (branch + worktree)")
	hint := theme.Current().OverlayHintStyle().Render("tab switch field · ctrl+d deep · enter save · esc cancel")
	title := theme.Current().OverlayTitleStyle().Render("Rename session")
	content := title + "\n\n" +
		nameLabel + "\n" + r.name.View() + "\n\n" +
		noteLabel + "\n" + r.note.View() + "\n\n" +
		mode + "\n\n" + hint
	return style.Render(content)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | grep -A2 RenameOverlay`
Expected: the overlay package fails to **build** elsewhere now — `NewRenameOverlay` callers in `app/` pass the old single argument. That is Task 5. Run just the overlay package's tests to confirm this task's unit is correct:

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./ui/overlay/ 2>&1 | tail -5`
Expected: PASS for `ui/overlay` (the package compiles in isolation; the `app` build break is resolved in Task 5).

- [ ] **Step 5: Commit**

```bash
git add ui/overlay/renameOverlay.go ui/overlay/renameOverlay_test.go
git commit -m "feat(ui/overlay): add note field to rename overlay

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: App wiring — pause opens note editor; persist note

**Files:**
- Modify: `app/app_update.go` (3 `NewRenameOverlay` call sites: ~line 121, ~line 904, and the pause handler ~line 1103–1107; rename-submit handler ~line 635–658)
- Test: `app/app_update.go` behavior is covered by building + the existing `app` tests; add a focused unit if an `app`-level overlay test harness exists (see Step 1).

**Interfaces:**
- Consumes: `NewRenameOverlay(label, note string, focusNote bool)`, `(*RenameOverlay).NoteValue()` (Task 4); `(*session.Instance).SetNote/Note` (Task 1).

- [ ] **Step 1: Write the failing test (build-level)**

The whole repo currently fails to build because Task 4 changed `NewRenameOverlay`'s signature. That compile failure *is* the failing state. Confirm it:

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go build ./... 2>&1 | grep NewRenameOverlay`
Expected: FAIL — "not enough arguments in call to overlay.NewRenameOverlay" at the three call sites.

(If `grep -rn "func TestUpdate\|stateRename" app/*_test.go` shows an existing overlay-driving harness, add an assertion there that pausing a git session sets `state == stateRename` with the overlay's note focused; otherwise the build gate plus manual verification in the closing step suffices.)

- [ ] **Step 2: Confirm the failure**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go build ./... 2>&1 | tail -5`
Expected: build errors at `app/app_update.go`.

- [ ] **Step 3a: Update the auto-name call site (~line 121)**

```go
		m.renameTarget = msg.instance
		m.renameOverlay = overlay.NewRenameOverlay(msg.name, msg.instance.Note(), false)
		m.state = stateRename
```

- [ ] **Step 3b: Update the KeyRename call site (~line 904)**

```go
		m.renameTarget = selected
		m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName(), selected.Note(), false)
		m.state = stateRename
```

- [ ] **Step 3c: Open the note editor after pausing (~line 1103–1107)**

Replace the pause body:

```go
		if err := selected.Pause(); err != nil {
			return m, m.handleError(err)
		}
		m.tabbedWindow.CleanupTerminalForInstance(selected)
		return m, m.instanceChanged()
```

with:

```go
		if err := selected.Pause(); err != nil {
			return m, m.handleError(err)
		}
		m.tabbedWindow.CleanupTerminalForInstance(selected)
		// Pause has already happened. Offer the rename overlay focused on the note
		// field so "park this, jot why" is one motion; esc / empty-enter just leaves
		// the session paused with no note. Instant pause is preserved — the pause is
		// never rolled back by skipping the note.
		m.renameTarget = selected
		m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName(), selected.Note(), true)
		m.state = stateRename
		return m, m.instanceChanged()
```

- [ ] **Step 3d: Persist the note on submit (~line 635–658)**

In the `if m.state == stateRename { ... }` block, capture the note alongside the label and apply it. Change:

```go
		submitted := m.renameOverlay.IsSubmitted()
		value := m.renameOverlay.Value()
		deep := m.renameOverlay.IsDeep()
```

to:

```go
		submitted := m.renameOverlay.IsSubmitted()
		value := m.renameOverlay.Value()
		note := m.renameOverlay.NoteValue()
		deep := m.renameOverlay.IsDeep()
```

and change the apply block:

```go
		if submitted && target != nil {
			if deep {
				if err := m.deepRename(target, value); err != nil {
					return m, m.handleError(err)
				}
			} else {
				target.SetDisplayName(value)
				if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
					return m, m.handleError(err)
				}
			}
		}
```

to:

```go
		if submitted && target != nil {
			target.SetNote(note)
			if deep {
				if err := m.deepRename(target, value); err != nil {
					return m, m.handleError(err)
				}
			} else {
				target.SetDisplayName(value)
			}
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
		}
```

(The save now runs for both branches so the note is persisted even on a deep rename. If `deepRename` already calls `SaveInstances` internally, a second save is harmless — it writes the same in-memory instances. Confirm with `grep -n "func (m \*home) deepRename" app/*.go` and read it; if it does not save, this unified save is required.)

- [ ] **Step 4: Run build + tests to verify they pass**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build && GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | tail -8`
Expected: build succeeds; full suite PASS.

- [ ] **Step 5: Commit**

```bash
git add app/app_update.go
git commit -m "feat(app): open note editor on pause and persist session note

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Manual verification (after Task 5)

Build and run, then confirm the loop end-to-end (the spec's correctness contract is automated tests; this is the human smoke check):

```bash
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build
./bin/atrium
```

1. Select a git session, press `p` → it pauses and the rename overlay opens with the cursor in the note field. Type `blocked on review`, press Enter.
2. The paused row's line 2 shows `✎ blocked on review` with the age on the right; the branch/PR chips are gone (replaced by the note).
3. Press `r` to resume → line 2 reverts to the live VC info; the note moves to its own third line under the row.
4. Rename the session (`R` or the rename key) → the overlay shows both fields; clear the note field, Enter → the note line disappears.
5. Quit and relaunch → the note (if set) survived (it is in `state.json`).

## Self-Review notes (verification the plan covers the spec)

- Spec "persisted `Note string`, general, `omitempty`, migration-safe" → Task 1.
- Spec "display only when non-empty; status-aware placement" → Task 3 (`note.plain != ""` guard; paused vs running branch).
- Spec "single-cell glyph, no emoji, muted accent distinct from branch/name" → Task 2 (glyph + width test) and Task 3 (`Palette.Purple`).
- Spec "editing folded into rename overlay; pause opens it focused on note; ~80-char cap; empty clears" → Tasks 4 + 5.
- Spec non-goals (reminders, snooze, auto-resume, status, structured fields, multiline) → nothing in any task implements them.
