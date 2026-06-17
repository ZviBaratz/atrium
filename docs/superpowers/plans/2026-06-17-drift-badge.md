# Drift Badge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent `⚠ stale` badge to the Sessions-panel border, shown only when the startup drift hint could not be delivered, so hint-bar-off users still get a passive drift signal.

**Architecture:** Mirror the existing update-badge pattern. Add a `Warn` glyph to the theme, a `driftBadge` slot on `List` rendered alongside `updateBadge` in the single panel-border badge, and a one-line set in the `driftFoundMsg` handler's existing "toast dropped" branch.

**Tech Stack:** Go, Bubble Tea, lipgloss; `mattn/go-runewidth` (glyph width).

## Global Constraints

- `go` is not on the Bash-tool PATH. Use `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go`. Package tests: `$GO test ./path/ -run Name -v`. Full suite: `GO=$GO just test`. Build: `GO=$GO just build`.
- Commits: Conventional Commits, lowercase; body ends with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- The drift badge is a **fallback**: set ONLY when the toast was dropped (handler `cmd == nil` branch). Never set when the toast is shown. No explicit clear path — `List` is fresh per launch.
- Badge text is exactly `⚠ stale` (glyph `Warn` + `" stale"`). The `Warn` glyph is `⚠` (U+26A0, verified `runewidth.StringWidth == 1`).
- Stage only the files each task names — never `git add -A`.

---

### Task 1: Add the `Warn` theme glyph

**Files:**
- Modify: `ui/theme/theme.go` (add `Warn` field to `Glyphs`)
- Modify: `ui/theme/registry.go` (set `Warn: "⚠"` in both glyph blocks, near each `Ahead:` at lines 37 and 114)
- Modify: `ui/theme/theme_test.go` (add `Warn` to the glyph-width map, ~line 56)

**Interfaces:**
- Produces: `theme.Glyphs.Warn string` — set to `"⚠"` in every theme.

- [ ] **Step 1: Add `Warn` to the glyph-width test**

In `ui/theme/theme_test.go`, in the `cells` map (the block starting ~line 50), add a line after `"Ahead":         g.Ahead,`:

```go
			"Warn":          g.Warn,
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go; $GO test ./ui/theme/ -run TestPanel -v 2>&1 | head; $GO build ./ui/theme/ 2>&1 | head`
Expected: compile error — `g.Warn undefined (type Glyphs has no field or method Warn)`.

- [ ] **Step 3: Add the `Warn` field to the `Glyphs` struct**

In `ui/theme/theme.go`, in the `Glyphs` struct, add after the `Ahead` line (line 45):

```go
	Warn          string // heuristic-drift / stale-data warning
```

- [ ] **Step 4: Set `Warn` in both registry glyph blocks**

In `ui/theme/registry.go`, add `Warn: "⚠",` immediately after each `Ahead:         "⇡",` line (lines 37 and 114):

```go
		Warn:          "⚠",
```

- [ ] **Step 5: Run the theme tests to verify they pass**

Run: `$GO test ./ui/theme/ -v 2>&1 | tail -15`
Expected: PASS (the glyph-width test now also checks `Warn` and `⚠` is width 1).

- [ ] **Step 6: Commit**

```bash
git add ui/theme/theme.go ui/theme/registry.go ui/theme/theme_test.go
git commit -m "feat(theme): add Warn glyph (⚠)" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Add the `driftBadge` slot to `List`

**Files:**
- Modify: `ui/list.go` (add `driftBadge` field ~line 212, `SetDriftBadge` setter ~line 234, `joinBadges` helper, and combine in the `String()` render at line 667)
- Test: `ui/list_drift_badge_test.go` (create)

**Interfaces:**
- Consumes: `theme.PanelWithBadge` (existing).
- Produces: `func (l *List) SetDriftBadge(text string)` — sets the drift badge shown in the panel border alongside the update badge.

- [ ] **Step 1: Write the failing test**

Create `ui/list_drift_badge_test.go`:

```go
package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDriftBadgeRendersInPanel(t *testing.T) {
	l, _ := newFilterList(t, "alpha")
	l.SetSize(80, 24)
	l.SetDriftBadge("⚠ stale")
	require.Contains(t, l.String(), "stale")
}

func TestUpdateAndDriftBadgesCombine(t *testing.T) {
	l, _ := newFilterList(t, "alpha")
	l.SetSize(80, 24)
	l.SetUpdateBadge("⇡ v0.7.1")
	l.SetDriftBadge("⚠ stale")
	out := l.String()
	require.Contains(t, out, "v0.7.1")
	require.Contains(t, out, "stale")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `$GO test ./ui/ -run 'TestDriftBadge|TestUpdateAndDrift' -v 2>&1 | head`
Expected: compile error — `l.SetDriftBadge undefined`.

- [ ] **Step 3: Add the field, setter, and join helper**

In `ui/list.go`, after the `updateBadge string` field (line 212), add:

```go
	// driftBadge is the persistent agent-heuristic drift indicator inset in the
	// panel's top border ("⚠ stale"). Set only when the startup drift hint could
	// not be shown (hint bar off / overlay), so it reaches users who'd miss the
	// toast; like updateBadge it must survive overlays and hint_bar:false.
	driftBadge string
```

After the `SetUpdateBadge` method (ends ~line 236), add:

```go
// SetDriftBadge sets the plain-text drift badge ("⚠ stale") shown in the
// Sessions panel border as a fallback when the startup drift hint can't render.
func (l *List) SetDriftBadge(text string) {
	l.driftBadge = text
}

// joinBadges combines the panel-border badges into the single slot,
// space-separated, skipping empties so either badge may be absent.
func joinBadges(badges ...string) string {
	parts := make([]string, 0, len(badges))
	for _, b := range badges {
		if b != "" {
			parts = append(parts, b)
		}
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: Combine the badges in the render**

In `ui/list.go`, the `String()` method's final return (line 667) currently reads:

```go
	return zone.Mark(listPanelZoneID, theme.Current().PanelWithBadge("Sessions", l.updateBadge, content, l.width, l.height, true))
```

Replace `l.updateBadge` with the joined badges:

```go
	return zone.Mark(listPanelZoneID, theme.Current().PanelWithBadge("Sessions", joinBadges(l.updateBadge, l.driftBadge), content, l.width, l.height, true))
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `$GO test ./ui/ -run 'TestDriftBadge|TestUpdateAndDrift' -v 2>&1 | tail`
Expected: PASS.

- [ ] **Step 6: Run the full ui package to confirm no regression**

Run: `$GO test ./ui/ 2>&1 | tail -3`
Expected: `ok  github.com/ZviBaratz/atrium/ui`.

- [ ] **Step 7: Commit**

```bash
git add ui/list.go ui/list_drift_badge_test.go
git commit -m "feat(ui): add drift badge slot to the sessions panel border" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Set the drift badge when the hint is dropped

**Files:**
- Modify: `app/app_driftcheck.go` (add `driftBadgeText()` helper)
- Modify: `app/app_update.go` (set the badge in the `driftFoundMsg` handler's `cmd == nil` branch)
- Modify: `app/app_driftcheck_test.go` (extend the two existing `driftFoundMsg` handler tests)

**Interfaces:**
- Consumes: `theme.Current().Glyphs.Warn` (Task 1); `m.list.SetDriftBadge` (Task 2).
- Produces: `func driftBadgeText() string` (package `app`) → `"⚠ stale"`.

- [ ] **Step 1: Extend the existing handler tests**

In `app/app_driftcheck_test.go`, the package imports must include `ui` (already imported) and `strings`. Add `"strings"` to the import block if absent.

In `TestDriftFoundMsg_NoAckWhenHintDropped`, after the existing `m.Update(driftFoundMsg{agents: agents})` call and the ack assertion, the list must be sized so the badge renders. Replace the body's `m := &home{...}` list field and add a size call + badge assertion. Concretely, after the existing final `}` of the ack check, add:

```go
	m.list.SetSize(80, 24)
	if out := m.list.String(); !strings.Contains(out, "stale") {
		t.Errorf("drift badge not shown after hint was dropped; panel:\n%s", out)
	}
```

In `TestDriftFoundMsg_AckRecordedWhenHintShown`, after the existing ack assertion, add:

```go
	m.list.SetSize(80, 24)
	if out := m.list.String(); strings.Contains(out, "stale") {
		t.Errorf("drift badge should NOT be set when the hint was shown; panel:\n%s", out)
	}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `$GO test ./app/ -run TestDriftFoundMsg -v 2>&1 | tail -20`
Expected: `TestDriftFoundMsg_NoAckWhenHintDropped` FAILS (`drift badge not shown after hint was dropped`) — the handler doesn't set the badge yet. `driftBadgeText` is not referenced yet so this is a logic failure, not a compile error.

- [ ] **Step 3: Add the `driftBadgeText` helper**

In `app/app_driftcheck.go`, add the `theme` import (grouped with the other `github.com/ZviBaratz/atrium/...` imports):

```go
	"github.com/ZviBaratz/atrium/ui/theme"
```

Add at the end of the file:

```go
// driftBadgeText is the persistent Sessions-panel badge shown as a fallback when
// the startup drift hint could not be delivered. Short so the panel can degrade
// it word-by-word when narrow ("⚠ stale" -> "⚠"), like updateBadgeText.
func driftBadgeText() string {
	return theme.Current().Glyphs.Warn + " stale"
}
```

- [ ] **Step 4: Set the badge in the dropped-toast branch**

In `app/app_update.go`, the `case driftFoundMsg:` handler's `cmd == nil` branch currently reads:

```go
		if cmd == nil {
			return m, nil
		}
```

Replace it with:

```go
		if cmd == nil {
			// Toast dropped (hint bar off, or a modal owns the screen). Surface the
			// drift via the persistent panel badge instead — the durable fallback
			// for users who'd otherwise never see it. Don't ack: leave it re-armed.
			if m.list != nil {
				m.list.SetDriftBadge(driftBadgeText())
			}
			return m, nil
		}
```

- [ ] **Step 5: Run the handler tests to verify they pass**

Run: `$GO test ./app/ -run TestDriftFoundMsg -v 2>&1 | tail`
Expected: both PASS (dropped → badge shown; shown → no badge).

- [ ] **Step 6: Run the full app package**

Run: `$GO test ./app/ 2>&1 | tail -3`
Expected: `ok  github.com/ZviBaratz/atrium/app`.

- [ ] **Step 7: Commit**

```bash
git add app/app_driftcheck.go app/app_update.go app/app_driftcheck_test.go
git commit -m "feat(app): show drift panel badge when the startup hint can't render" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Full suite, vet, fmt, lint, build, smoke**

```bash
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just vet
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just fmt-check
PATH="$HOME/go/bin:/home/zvi/.local/share/mise/installs/go/latest/bin:$PATH" GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just lint
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build
```

Expected: all pass; `golangci-lint` 0 issues. (If lint cites a path under a *different* worktree, run `golangci-lint cache clean` and re-run — that's a stale shared-cache artifact, not this branch.)

---

## Self-Review

**Spec coverage:**
- Glyph `Warn: "⚠"` + completeness test → Task 1. ✓
- `driftBadgeText()` returning `⚠ stale` → Task 3. ✓
- `List.driftBadge` slot + `SetDriftBadge` + combined render → Task 2. ✓
- Wiring in the `cmd == nil` branch only, guarded `m.list != nil` → Task 3. ✓
- Fallback-only behavior (no badge when toast shown) → Task 3 tests. ✓
- Tests: app dropped/shown, ui render+combine, theme glyph → Tasks 1-3. ✓
- Non-goals (no config flag, no clear path, no toast/doctor change) → none implemented. ✓

**Placeholder scan:** No TBD/TODO/vague directives; every code step shows complete code. ✓

**Type consistency:** `Glyphs.Warn` (Task 1) used by `driftBadgeText` (Task 3). `SetDriftBadge`/`joinBadges`/`driftBadge` (Task 2) used by the handler and tests (Task 3). `driftBadgeText()` consistent across helper and call site. ✓
