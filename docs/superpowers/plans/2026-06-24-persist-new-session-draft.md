# Persist the new-session form draft across Escape — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Escaping the new-session form keeps the in-progress draft (in-memory, this run only) so reopening with `n`/`N` restores it exactly; double-tap Ctrl+R discards it for a fresh form.

**Architecture:** On cancel, the `home` model stashes the live create-form `TextInputOverlay` into a new `stashedDraft` field instead of nil-ing it — but only when the form is dirty. The bare `n`/`N` open path reuses the stash instead of building fresh. A double-tap Ctrl+R inside the overlay sets a `ClearRequested` signal; the app rebuilds a fresh form (the app owns the config the pickers need).

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`), testify. Build/test via `just`.

## Global Constraints

- Conventional Commits, lowercase (`feat: …`, `fix: …`).
- Tests must stay hermetic — never touch the real data dir. App tests use the existing `TestMain`/`newCreateFormHome` setup in `app/`; overlay tests construct overlays directly. Do not add tests that reach the user's `~/.atrium`/`~/.claude-squad`.
- `go` is not on the Bash-tool PATH. Run tests with `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test` (and the same `GO=` prefix for `just build`).
- Verify with `just build` **and** `just test` before claiming completion.
- In-memory only: never serialize `stashedDraft` to `state.json`.

---

### Task 1: `IsDirty()` on the overlay

**Files:**
- Modify: `ui/overlay/textInput.go` (add method near the other accessors, ~after line 224 `SetTitleValue`)
- Test: `ui/overlay/textInput_test.go`

**Interfaces:**
- Produces: `func (t *TextInputOverlay) IsDirty() bool` — true when the title or prompt textarea holds non-whitespace text.

- [ ] **Step 1: Write the failing test**

Add to `ui/overlay/textInput_test.go`:

```go
func TestSessionCreateOverlay_IsDirty(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
	assert.False(t, o.IsDirty(), "a fresh form is not dirty")

	o.SetTitleValue("draft")
	assert.True(t, o.IsDirty(), "a typed title makes the form dirty")

	o.SetTitleValue("")
	o.SetPrompt("some prompt")
	assert.True(t, o.IsDirty(), "a typed prompt makes the form dirty")

	o.SetPrompt("   ")
	o.SetTitleValue("  ")
	assert.False(t, o.IsDirty(), "whitespace-only is not dirty")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: FAIL — `o.IsDirty undefined`.

- [ ] **Step 3: Write minimal implementation**

Add to `ui/overlay/textInput.go` after `SetTitleValue` (~line 224):

```go
// IsDirty reports whether the create form holds user-entered free text (title or
// prompt). The draft-stash logic uses it so an untouched form is still discarded
// on Escape; picker-only changes do not count as dirty.
func (t *TextInputOverlay) IsDirty() bool {
	return strings.TrimSpace(t.titleInput.Value()) != "" ||
		strings.TrimSpace(t.textarea.Value()) != ""
}
```

(`strings` is already imported.)

- [ ] **Step 4: Run test to verify it passes**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/overlay/textInput.go ui/overlay/textInput_test.go
git commit -m "feat: add IsDirty to the new-session form overlay"
```

---

### Task 2: Double-tap Ctrl+R → `ClearRequested` + footer hint

**Files:**
- Modify: `ui/overlay/textInput.go` (struct fields ~line 163; `HandleKeyPress` ~line 629; footer in `renderCreateForm` ~line 1118; new accessor)
- Test: `ui/overlay/textInput_test.go`

**Interfaces:**
- Produces: `func (t *TextInputOverlay) ClearRequested() bool` — true once a second consecutive Ctrl+R has armed-then-confirmed a clear (create form only).
- Behavior: first Ctrl+R arms (footer shows `⌃R again to clear`); any other key disarms (footer back to `⌃R clear`); second consecutive Ctrl+R sets `ClearRequested`.

- [ ] **Step 1: Write the failing tests**

Add to `ui/overlay/textInput_test.go`:

```go
func ctrlR(o *TextInputOverlay) { o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) }

func TestSessionCreateOverlay_DoubleCtrlRClears(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")

	ctrlR(o)
	assert.False(t, o.ClearRequested(), "one Ctrl+R only arms")

	ctrlR(o)
	assert.True(t, o.ClearRequested(), "a second consecutive Ctrl+R requests the clear")
}

func TestSessionCreateOverlay_CtrlRDisarmsOnOtherKey(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
	o.FocusTitle()

	ctrlR(o) // arm
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	ctrlR(o) // this is now a first press again, not a confirm
	assert.False(t, o.ClearRequested(), "an intervening key disarms the clear")
}

func TestSessionCreateOverlay_ClearHintInFooter(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "⌃R clear")

	ctrlR(o)
	assert.Contains(t, o.Render(), "⌃R again to clear")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: FAIL — `o.ClearRequested undefined`.

- [ ] **Step 3: Add the struct fields**

In `ui/overlay/textInput.go`, in the `TextInputOverlay` struct, after the `smartDispatch` / `submitOnEnter` lines (~line 164), add:

```go
	clearArmed     bool // first Ctrl+R seen; a second consecutive press confirms a clear
	clearRequested bool // a confirmed double-tap Ctrl+R; the app rebuilds a fresh form
```

- [ ] **Step 4: Intercept Ctrl+R in `HandleKeyPress`**

In `ui/overlay/textInput.go`, at the very top of `HandleKeyPress` (immediately after the `func` line ~629, before `switch msg.Type {`), add:

```go
	// Double-tap Ctrl+R clears the form (create form only): the first press arms,
	// any other key disarms, a second consecutive press requests the clear. The app
	// performs the rebuild — it owns the config/profiles the pickers need.
	if t.isCreateForm && msg.Type == tea.KeyCtrlR {
		if t.clearArmed {
			t.clearArmed = false
			t.clearRequested = true
		} else {
			t.clearArmed = true
		}
		return false, false
	}
	t.clearArmed = false
```

- [ ] **Step 5: Add the accessor**

Add near the other predicate accessors (e.g. after `IsCreateForm`, ~line 766):

```go
// ClearRequested reports whether a confirmed double-tap Ctrl+R has asked to reset
// the create form. The app consumes it by rebuilding a fresh overlay.
func (t *TextInputOverlay) ClearRequested() bool { return t.clearRequested }
```

- [ ] **Step 6: Add the footer hint**

In `renderCreateForm`, replace the footer block at ~lines 1118-1122:

```go
	help := createFormHelp
	if t.isTextarea() {
		help = promptFocusHelp
	}
	b.WriteString(tiHintStyle().Render(help) + "\n")
```

with:

```go
	help := createFormHelp
	if t.isTextarea() {
		help = promptFocusHelp
	}
	clearHint := "⌃R clear"
	if t.clearArmed {
		clearHint = "⌃R again to clear"
	}
	b.WriteString(tiHintStyle().Render(help+" · "+clearHint) + "\n")
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add ui/overlay/textInput.go ui/overlay/textInput_test.go
git commit -m "feat: double-tap ctrl+r to clear the new-session form"
```

---

### Task 3: Stash the dirty draft on cancel

**Files:**
- Modify: `app/app.go` (add `stashedDraft` field after `textInputOverlay`, ~line 231)
- Modify: `app/app_session.go` (`cancelPromptOverlay`, ~line 847)
- Test: `app/draft_test.go` (new)

**Interfaces:**
- Consumes: `overlay.TextInputOverlay.IsDirty()` (Task 1), `IsCreateForm()`.
- Produces: `home.stashedDraft *overlay.TextInputOverlay` — non-nil after Escaping a dirty create form; nil after Escaping a clean one.

- [ ] **Step 1: Write the failing tests**

Create `app/draft_test.go`:

```go
package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runes is a small helper to type text into the focused field.
func draftRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestDraft_EscapeStashesDirtyForm(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n")) // open, focus on title
	require.NotNil(t, h.textInputOverlay)
	h.handleKeyPress(draftRunes("my-draft"))
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, h.textInputOverlay, "the live overlay is cleared on cancel")
	require.NotNil(t, h.stashedDraft, "a dirty form is stashed")
	assert.Equal(t, "my-draft", h.stashedDraft.GetTitle())
	assert.Equal(t, stateDefault, h.state)
}

func TestDraft_EscapeDiscardsCleanForm(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n")) // open, type nothing
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, h.stashedDraft, "an untouched form leaves no stash")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: FAIL — `h.stashedDraft undefined`.

- [ ] **Step 3: Add the field**

In `app/app.go`, after the `textInputOverlay` field (~line 231):

```go
	// stashedDraft keeps a dirty new-session form across Escape so reopening with
	// n/N restores it. In-memory only (this run) — never persisted to state.json.
	stashedDraft *overlay.TextInputOverlay
```

(`overlay` is already imported in `app/app.go`.)

- [ ] **Step 4: Stash on cancel**

In `app/app_session.go`, change `cancelPromptOverlay` (~line 847). Replace:

```go
func (m *home) cancelPromptOverlay() tea.Cmd {
	m.textInputOverlay = nil
	m.state = stateDefault
```

with:

```go
func (m *home) cancelPromptOverlay() tea.Cmd {
	// Keep a dirty create form as a draft so a deliberate Escape-to-check-something
	// is non-destructive; everything else (clean form, quick-send, smart-dispatch)
	// is discarded as before.
	if m.textInputOverlay != nil && m.textInputOverlay.IsCreateForm() && m.textInputOverlay.IsDirty() {
		m.stashedDraft = m.textInputOverlay
	}
	m.textInputOverlay = nil
	m.state = stateDefault
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add app/app.go app/app_session.go app/draft_test.go
git commit -m "feat: stash a dirty new-session form on escape"
```

---

### Task 4: Restore the stash on the bare `n`/`N` open

**Files:**
- Modify: `app/app_session.go` (`openCreateFormSeeded`, ~lines 501-580)
- Test: `app/draft_test.go`

**Interfaces:**
- Consumes: `home.stashedDraft` (Task 3); `overlay.GetSelectedPath()`, `GetTitle()`.
- Produces: bare `n`/`N` reuses `stashedDraft` as the live overlay and clears the field; smart-dispatch (`prefill != nil`) and seeded (`seedPath != ""`) opens still build fresh.

- [ ] **Step 1: Write the failing test**

Add to `app/draft_test.go`:

```go
func TestDraft_ReopenRestoresStash(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	h.handleKeyPress(draftRunes("my-draft"))
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, h.stashedDraft)

	h.handleKeyPress(draftRunes("n")) // reopen
	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, "my-draft", h.textInputOverlay.GetTitle(), "the draft is restored")
	assert.Nil(t, h.stashedDraft, "the stash is consumed into the live overlay")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: FAIL — restored title is empty (a fresh overlay was built).

- [ ] **Step 3: Restructure `openCreateFormSeeded`**

In `app/app_session.go`, replace the body of `openCreateFormSeeded` from after the max-sessions guard (the line `m.newSessionPath = seedPath`, ~line 507) through the end of the function (~line 580 `return tea.Batch(cmds...)`) with:

```go
	// Restore a stashed draft only on the bare n/N entry (no seed path, no prefill):
	// the smart-dispatch and seeded paths carry explicit intent that must win.
	restore := prefill == nil && seedPath == "" && m.stashedDraft != nil

	if restore {
		m.newSessionPath = m.stashedDraft.GetSelectedPath()
	} else {
		m.newSessionPath = seedPath
	}
	if m.newSessionPath == "" {
		m.newSessionPath = m.defaultNewSessionPath()
	}
	target := m.newSessionPath
	// Scope the duplicate-title check to the target's repo group from the first
	// keystroke; the async validity check re-points it as the picker moves.
	m.newSessionGroup = git.RepoGroupKey(m.ctx, target)
	m.resetTitleCheck()
	m.state = statePrompt

	var isGit bool
	if restore {
		m.textInputOverlay = m.stashedDraft
		m.stashedDraft = nil
		valid, direct, _ := targetValidity(m.ctx, target)
		isGit = valid && !direct
		// Re-run the inline duplicate verdict for the restored title.
		m.refreshTitleError()
	} else {
		var ov *overlay.TextInputOverlay
		ov, isGit = m.newSessionFormOverlay()
		m.textInputOverlay = ov
		if prefill != nil {
			ov.SetPrompt(prefill.Prompt)
			if prefill.Title != "" {
				ov.SetTitleValue(prefill.Title)
			}
			if prefill.Path != "" {
				ov.SelectPath(prefill.Path)
			}
			if prefill.Confident {
				// Project and title are trusted; land on the Permissions chip — the one
				// decision smart dispatch defers. Falls back to Create on non-claude.
				ov.FocusMode()
			} else {
				ov.FocusTitle()
			}
			// A pre-filled title needs the same duplicate verdict a keystroke triggers.
			m.refreshTitleError()
		} else if focusTitle {
			m.textInputOverlay.FocusTitle()
		}
		// Open the account picker on the auto-routed account for the contextual target.
		remoteURL := ""
		if isGit {
			remoteURL = git.GetRemoteURL(m.ctx, target)
		}
		if name, _, _ := m.appConfig.ResolveClaudeAccount(remoteURL, target); name != "" {
			m.textInputOverlay.PreselectAccount(name)
		}
	}

	// Branch plumbing only applies to a git target: seed the fetched-once set and
	// kick the background fetch plus the initial (undebounced) branch search.
	m.fetchedPaths = map[string]bool{}
	cmds := []tea.Cmd{tea.WindowSize()}
	if !m.scanInFlight && time.Since(m.lastScanAt) > projectScanTTL {
		cmds = append(cmds, m.startProjectScan())
	}
	if isGit {
		m.fetchedPaths[target] = true
		cmds = append(cmds,
			m.runBranchFetch(target),
			m.runBranchSearch("", m.textInputOverlay.BranchFilterVersion()))
	}
	// Verify a pre-filled or restored title against orphan branches in the target repo,
	// the same async check a keystroke schedules.
	if title := m.textInputOverlay.GetTitle(); title != "" {
		cmds = append(cmds, m.scheduleTitleCheck(title, target))
	}
	return tea.Batch(cmds...)
```

This preserves the fresh and prefill behavior exactly (the `if title != ""` schedule is a no-op for a fresh empty form, and equals the old `prefill.Title` schedule for the smart-dispatch path) and adds the restore branch.

- [ ] **Step 4: Run test to verify it passes**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: PASS. The existing `TestOpenCreateForm_*` and smart-dispatch tests must also still pass (run the whole suite).

- [ ] **Step 5: Commit**

```bash
git add app/app_session.go app/draft_test.go
git commit -m "feat: restore a stashed draft when reopening the new-session form"
```

---

### Task 5: Apply Ctrl+R clear in the app, and clear the stash on create

**Files:**
- Modify: `app/app_keys.go` (`handlePromptState`, after the `shouldClose` block ~line 104)
- Modify: `app/app_session.go` (`createSessionFromForm` success path, ~line 715)
- Test: `app/draft_test.go`

**Interfaces:**
- Consumes: `overlay.ClearRequested()` (Task 2); `home.openCreateForm` (existing); `home.stashedDraft` (Task 3).

- [ ] **Step 1: Write the failing tests**

Add to `app/draft_test.go`:

```go
func TestDraft_DoubleCtrlRRebuildsFresh(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(draftRunes("n"))
	h.handleKeyPress(draftRunes("my-draft"))
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	h.handleKeyPress(draftRunes("n")) // reopen with the restored draft
	require.Equal(t, "my-draft", h.textInputOverlay.GetTitle())

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // arm
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlR}) // confirm

	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, "", h.textInputOverlay.GetTitle(), "the form is rebuilt fresh")
	assert.Nil(t, h.stashedDraft, "the stash is dropped on clear")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: FAIL — after the two Ctrl+R presses the title is still `my-draft` (no rebuild wired).

- [ ] **Step 3: Wire the clear in `handlePromptState`**

In `app/app_keys.go`, immediately after the closing `}` of the `if shouldClose {` block (after line 104, before the `// If the target directory changed` comment at line 106), add:

```go
	// A confirmed double-tap Ctrl+R rebuilds the form fresh and drops any draft.
	if m.textInputOverlay.ClearRequested() {
		m.stashedDraft = nil
		return m, m.openCreateForm(true)
	}

```

(`m.openCreateForm(true)` calls `openCreateFormSeeded("", true, nil)`; with the stash now nil it builds a fresh title-focused form.)

- [ ] **Step 4: Clear the stash on successful create**

In `app/app_session.go`, in `createSessionFromForm`'s success path (~line 715), change:

```go
	m.textInputOverlay = nil
	m.state = stateDefault
```

to:

```go
	m.textInputOverlay = nil
	m.stashedDraft = nil
	m.state = stateDefault
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add app/app_keys.go app/app_session.go app/draft_test.go
git commit -m "feat: clear the new-session draft on ctrl+r and on create"
```

---

### Task 6: Full verification

- [ ] **Step 1: Build**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build`
Expected: builds to `./bin/atrium`, no errors.

- [ ] **Step 2: Full test suite**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test`
Expected: all packages PASS.

- [ ] **Step 3: Lint**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just lint`
Expected: clean (or only pre-existing warnings unrelated to these files).

---

## Self-review notes (spec coverage)

- Stash on Escape, dirty-only → Task 3. Free-text dirtiness → Task 1.
- Restore on reopen incl. focus (stashed overlay reused as-is) → Task 4.
- `n`/`N` equivalence when a draft exists → Task 4 (restore skips the focus override; both `n` and `N` hit the same restore branch).
- Double-tap Ctrl+R clear, arming + disarm-on-other-key + footer hint → Task 2; app rebuild → Task 5.
- Auto-clear on create → Task 5; in-memory-only (no persistence) → no state.json changes anywhere.
- Out of scope (quick-send / smart-dispatch overlays unaffected) → Task 3 guards on `IsCreateForm()`; Task 4 restore guards on `prefill == nil && seedPath == ""`.
