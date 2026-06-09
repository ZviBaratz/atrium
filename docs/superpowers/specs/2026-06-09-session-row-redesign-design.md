# Session-row redesign — composable fields + refined layout

**Date:** 2026-06-09
**Status:** Approved

## Motivation

The per-session list row has accreted six signal types and now reads as busy:
line 1 is `agent-icon + name … state-glyph + state-word`; line 2 (dim) is
`branch + behind/ahead/dirty + PR#  …  +adds −dels + age`. The state **word**
(`working` / `ready` / `waiting`) is pure redundancy — `stateParts` derives the
glyph, color, and word from one switch, so the word never says anything the
color-coded glyph does not.

Two goals, one of them structural:

1. **Visible:** quiet the row without dropping information — relocate the state
   signal to a leading gutter, drop the redundant word, and free line 1's right
   edge.
2. **Structural (the real investment):** rebuild the renderer around an
   **ordered list of field descriptors** so a later increment can expose field
   *selection and ordering* as config without a rewrite. This session ships a
   fixed layout; the architecture is what makes the next steps cheap.

**Design principle:** the row is a composition of independent field segments
over a generic layout engine — not a hand-laid pair of lines.

## Scope

**In scope (this session):**
- Composable field-renderer refactor (the foundation).
- Refined two-line default layout: leading status gutter, state word removed,
  line-1 right edge reserved for the `AUTO` badge only.
- `·` middot separators on line 2; branch glyph dropped (indentation + `zvi/`
  prefix already read as "branch").

**Out of scope (later increments, in order):**
- A `Compact rows` setting (single-line layout) — next.
- Config-driven field selection + ordering — after that.

No config fields are added this session.

## Visual spec

```
 ▎⠋ ✳ Review                                  AUTO
      zvi/review · ↑11 · #86          +4927 −2133 · 2d

   ● ✳ Account configuration
      zvi/account-config · #86         +3631 −2038 · 59m

   ◆ ✳ PR Review
      zvi/pr-review · ↑220             +2830 −325 · 2d
```

**Leading identity columns (shared by both lines):**
- `▎` selection accent bar — col 0, only on the selected row (unchanged).
- **State gutter** — col 1, the glyph + color from the existing `stateParts`,
  relocated from line 1's right edge. The state word is removed.
- Agent icon, then the name (line 1).
- Line 2 indents its content to align under the name; the branch glyph is
  dropped.

**State → gutter glyph/color** (identical to today's mapping, minus the word):

| Status | Glyph | Color |
|---|---|---|
| Running / Loading | spinner frame | Working |
| Ready (unread) | filled `●` | Success |
| Ready (seen) | hollow `○` | SuccessDim |
| NeedsInput | `◆` | Attention (name also recolors, as today) |
| Paused | paused glyph | FgDim |
| default (uninitialized) | blank | — |

**Line composition:**
- **Line 1 left:** state gutter, agent icon, name (the flex field — absorbs
  leftover width, truncates with `…`).
- **Line 1 right:** `AUTO` badge when autoyes is on and not paused; otherwise
  empty.
- **Line 2 left:** indent, branch (flex), then `· behind`, `· ahead`, `· dirty`,
  `· #PR` as present.
- **Line 2 right:** `+adds −dels`, then `· age`.

Nothing is removed except the state word. Behind/ahead/dirty, PR badge, diff
stat, and age all remain.

## Architecture

### Segment model + generic layout engine

A row is built from segments, not hand-laid strings.

```go
// rowSeg is one rendered piece of a row line: its plain text (for width math)
// and its styled form (ANSI escapes add no columns, so plain width == rendered
// width). flex marks the single segment per line that absorbs leftover width.
type rowSeg struct {
    plain  string
    styled string
    flex   bool
}
```

Each field becomes a **pure producer** returning one or more segments from an
`*session.Instance` (+ theme, selection, background): `gutterSeg`, `agentSeg`,
`nameSeg`, `branchSeg`, `gitCtxSegs`, `prSeg`, `diffSegs`, `ageSeg`, `autoSeg`.

One generic engine lays them out:

```go
// composeLine renders one row line to exactly width columns: it sums the fixed
// segment widths, gives the leftover to the single flex segment (truncated with
// "…"), then joins left segments, a background-aware gap, and the right-aligned
// segments. bg is baked into the gap so the selected-row fill survives ANSI
// resets.
func composeLine(width int, bg lipgloss.TerminalColor, left, right []rowSeg) string
```

`InstanceRenderer.Render` shrinks to: derive `bg`, build the four segment lists
(line1 left/right, line2 left/right), call `composeLine` twice, and join with
the selection marker. The future selection/ordering feature replaces the
hardcoded lists with config-sourced ones — the budgeting is already generic.

### Why this approach

The rejected alternative — extract each field into a named helper but keep line
composition inline — is a lighter change but leaves field *order* implicit in
the composition code, so the ordering feature would need a second refactor. The
segment model pays that cost once, now.

### Invariants the engine must preserve

All three are already load-bearing in today's `Render` and are the usual sources
of row-rendering bugs:

1. **Background baked into every segment and gap.** An ANSI reset at the end of
   any styled span also clears the background; the selected-row fill must be on
   each piece, not wrapped around the line.
2. **`theme.SanitizeWidth` on the display name.** Emoji/ZWJ clusters can measure
   narrower than they render; without sanitizing, the row overflows and wraps,
   desyncing Bubble Tea's incremental renderer.
3. **`runewidth` for all width math, and the "drop the glyph rather than leave
   it dangling" rule** when a field's budget falls below 1 (a lone branch/PR
   glyph with no text reads as a rendering bug).

## Files

- **`ui/row.go`** (new) — `rowSeg`, `composeLine`, and the field producers. Also
  relieves `ui/list.go` (already ~1230 lines); row composition is a clean
  boundary to lift out.
- **`ui/list.go`** — `InstanceRenderer.Render` reduced to segment-list
  declaration + two `composeLine` calls. `stateParts` drops its `word` return
  (no remaining caller).
- **`ui/row_test.go`** (new) — table tests for `composeLine` (flex truncation,
  gap sizing, right-alignment, background-baking) and the pure field producers.
- **Existing row tests** (`ui/list_test.go`, `ui/overhaul_test.go`, and the
  sanitize/scroll tests that assert rendered output) — re-point expected strings
  to the new layout. They remain the regression net proving each line totals
  exactly its budget width `W`.
- **No `config/` changes** this session.

`ui/pr.go`'s `prBadgeColor` is unchanged and becomes `prSeg`'s color source;
`prStateWord` / `reviewWord` (diff-tab helpers) are untouched.

## Testing

- `composeLine` unit tests: a flex segment truncates to fit; fixed segments are
  never truncated; the right group is flush-right; the gap is at least one
  column; the background is present on every emitted span (assert no bare reset
  before line end on a selected row).
- Field-producer tests: each returns the expected plain text and width across
  states (e.g. `gutterSeg` over all `session.Status` values; `diffSegs` empty
  when the diff is empty; `prSeg` empty when `HasPR` is false).
- Width-totaling regression: every rendered line measures exactly `W`
  (`runewidth.StringWidth`) for selected and unselected rows, across narrow and
  wide panels — the property the existing tests encode.
- Hermetic: no real config/state/tmux writes; the renderer takes its inputs from
  in-memory `Instance` fixtures, as the current tests do.

## Verification

`just build` and `just test` both pass before the work is considered done, per
the project's correctness contract.
