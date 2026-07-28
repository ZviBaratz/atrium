---
name: tui-drift-sites
description: Use when adding, renaming, removing or rebinding anything in Atrium's UI that has more than one representation — a keybinding, a Config field, a status/agent glyph, a hint-bar entry, or a key named in prose — and when reviewing a diff that touches keys/, config/types.go, ui/theme/, or ui/menu.go. Answers "what else must change, and which test fails if I miss it?".
---

# Atrium's drift sites

## Overview

Most UI facts in Atrium exist in more than one place on purpose — a key is a
registry entry *and* a dispatch case *and* a cheatsheet row *and* a README line.
That is fine, because most of those copies are **derived or guarded**. What is not
fine is guessing which. This skill is the enumerated map: touch X, and here is
every site plus the test that fails when you miss one.

Counts below were taken from the tree, not from memory. **Re-count before trusting
them** — one `grep -c` is cheaper than a wrong claim:

```sh
grep -cE '^\t\{Name:' keys/registry.go        # registry entries
grep -c 'case keys.Key' app/app_update.go      # dispatch-case LINES, not names
grep -c '^func Test' app/dispatch_coverage_test.go   # the site-4 guards
awk '/^type Config struct/,/^}/' config/types.go | grep -cE '`json:'   # Config fields
```

The dispatch count is *lines*, and several cases carry two or three names
(`case keys.KeyMoveUp, keys.KeyMoveDown:`), so it is always below the number of
actions — that is expected, not a shortfall.

That last one has to be scoped to the struct. Counting json tags across the whole
file gives 59, because `Profile`, `ClaudeAccount`, `AgyAccount` and `GHAccount` live
there too and carry their own — and a recount recipe that answers a different
question than the claim above it is how the wrong number got here in the first place.

Verification, hermeticity, and the `just lint` / `golangci-lint` traps are in
`CLAUDE.md` — not repeated here. The gate is `just ci`.

For the *general* discipline behind all of this — why a green Go suite is nearly
blind to a TUI, the width tautology, bubblezone's stale bounds, mutation testing —
use the **`verify-tui`** skill from the `charm-tui` plugin. This file is only
Atrium's site map.

`charm-tui` lives outside this repo, so `.claude/settings.json` can enable it but
cannot install it. Once per machine:

```
/plugin marketplace add ZviBaratz/claude-plugins
```

`enabledPlugins` does the rest. Until you run it the skill simply will not resolve.

## Adding a keybinding — 7 sites, all guarded

At last count: **58 registry entries, 48 dispatch-case lines, 12 drift guards** in
`keys/*_test.go` plus **4** in `app/dispatch_coverage_test.go`.

| # | Site | Guarded by |
|---|---|---|
| 1 | `keys/keys.go` — the `KeyName` const, with a doc comment | `revive:exported` via `just lint`; `TestKeyNames_AllRegisteredOrDeliberatelyAbsent` |
| 2 | `keys/registry.go` — the `Entry` (`WithKeys` + `WithHelp`, plus `Layer`/`DocOnly`) | `TestRegistry_NoDuplicateKeyStrings`, `TestRegistry_LayerTags`, `TestRegistry_DocumentedOnlyEntries` |
| 3 | `keys/help_layout.go` — a `HelpRow` in `HelpGroups`, or a `Mentions` entry | `TestHelpGroups_CoverEveryBinding` (fails structurally, before any rendering) |
| 4 | `app/app_update.go` — `case keys.KeyX:` in `dispatchAction` | `TestEveryRegistryActionHasADispatchCase` — see below |
| 5 | `keys/registry_test.go` — the string→action pair in the golden inventory | itself (`TestGlobalKeyStringsMap_GoldenInventory`) |
| 6 | `README.md` `#### Keybindings` — backtick-wrapped, in that section | `TestReadmeDocumentsEveryBinding` |
| 7 | `app/app_update.go` `keyAllowedWhileBusy` — *only if* it must work during an async action | manual |

Plus, situationally: `ui/menu.go`'s context hint sets (`defaultHintKeys` and
friends) if the key should appear in the bar — guarded in the reverse direction
only, by `ui/menu_scan_test.go`.

**Site 4 was the gap until #374 closed it.** The count mismatch is why nobody had
written the obvious assertion: 58 entries against 48 case *lines* is not 10
missing cases, because several cases carry two or three names at once, 3 entries
are `DocOnly`, the screensaver is deliberately absent from the registry, and
`space` is consumed by the multi-select handler rather than the switch — so
"every entry has a case" would false-positive on all of them.

What closed it was extracting the switch into `dispatchAction` and then reading
its `case` labels **out of the source** with `go/ast`.
`TestEveryRegistryActionHasADispatchCase` requires every dispatched entry to have
a case or a `dispatchExempt` reason naming the handler that owns its key instead,
and `TestEveryDispatchExemptionIsRealAndReasoned` rejects an exemption that names
a nonexistent key, one that already has a case, or offers no reason.

**It still only proves the case exists.** A case that dispatches to the wrong
handler, or to one whose guards refuse silently, passes. After adding a key,
press it — or drive it through `handleKeyPress` in a test. Do not infer from a
green suite that it does what you meant.

Two cross-layer pins worth knowing exist, because they fail in surprising places:

- `session/tmux/keys_link_test.go` ties chord strings to the attach layer's raw
  control bytes (`chord[len-1] & 0x1f`). Changing one spelling without the other
  fails here.
- `ui/key_prose_test.go` pins keys named in **free prose** (splash text, the
  empty-list hint) to the registry, each with a `site` label naming what to fix.

## Adding a `Config` field — 4 sites, 3 guarded bidirectionally

42 json-tagged fields on `Config` itself at last count.

| # | Site | Guarded by |
|---|---|---|
| 1 | `config/types.go` — the field + `json:` tag | — |
| 2 | `config/config.go` `DefaultConfig()` — the default value | manual |
| 3 | `README.md` "Configuration reference" — backtick-wrapped row | `TestReadmeDocumentsEveryConfigField` |
| 4 | `ui/overlay/settings_schema.go` — a `settingRow`, if user-editable | `TestEveryScalarConfigFieldHasARow` **and** `TestEveryRowKeyIsAConfigFieldOrReadOnly` (both directions) |

Site 4's guard is the model to copy elsewhere: reflection over
`reflect.TypeOf(config.Config{})` in **both** directions, so a new scalar field
must either get a row or be explicitly excluded with a reason. Also
`TestEveryRowHasAKnownCategoryAndScope` and `TestEveryCategoryHasALabel`.

`settings_schema.go`'s row builder **must stay pure** — no exec, no filesystem,
and in particular never `config.DefaultConfig()`, which resolves the OS user to
derive `branch_prefix`. Two rows are nil by design; a marker on either would be a
lie. Don't "fix" them.

## Adding or changing a glyph — width first, legend second

1. **It must measure width 1.** Guarded across every palette × glyph-set
   combination by `TestGlyphWidths`, `TestAgentGlyphWidths`,
   `TestNoteGlyphIsSingleCellEverywhere` in `ui/theme/theme_test.go`. A 2-cell
   glyph is not cosmetic — it breaks the column math and the view-bounds
   invariant, which is exactly what `TestGlyphWidths` says it guards.

   Distinguish what the *tests* assert from *why the rule exists*. The three tests
   above assert width 1 for column math; none of them mentions ghosting. But the
   underlying reason is the same divergence in both cases: when a glyph's
   *measured* width differs from its *rendered* width, the line overflows, wraps,
   and desyncs bubbletea's incremental alt-screen renderer into accumulating ghost
   rows. `ui/theme/agent.go` says so about **this** table — the glyphs are plain,
   single-cell, non-PUA precisely because of it, which is also why item 3 below
   matters.

   One mechanism, two mitigations, two guards: for glyphs **we choose**, pick safe
   ones and pin width 1 (the tests above). For content we **don't** choose — pane
   output, diffs — sanitize at the boundary (`SanitizeWidth`, `ui/theme/panel.go`).
   See `verify-tui`'s `measurement.md` for the mechanism in full.
2. **It must reach the `?` legend**, or be excluded with a reason.
   `app/help_legend_test.go`'s `TestLegendCoversRowVocabulary` reflects over the
   live `Glyphs` table, so a new field forces a decision. Note one exclusion
   exists purely because its glyph coincidentally appears elsewhere in the legend
   prose — the loop would pass by accident without it.
3. **Prefer plain single-cell non-PUA Unicode.** There is no reliable way to probe
   for a patched font; the three-rung ladder (`nerd`/`plain`/`ascii`) in
   `ui/theme/registry.go` is the answer, not detection.

## Adding a UI state

`app/app.go`'s state enum, plus a nullable overlay pointer field. Then:

- Add it to `app/statemachine_test.go`'s `states[]` **with a `wire` func** that
  arms the overlay production would keep, or the state is swept in a
  half-constructed shape and the interesting dereference never happens. Prefer
  wiring through the real opener over assigning the overlay field by hand: an
  overlay that comes with sibling state (the palette's row table) is only
  half-armed otherwise, which is the dereference the sweep exists to find.
- Add it to `app/view_bounds_test.go`'s overlay map if it renders a box. That is
  the test that actually holds a state to 80×24 — it asserts the *composed frame*,
  so it is the only one that sees an overlay whose own tests measured lines
  lipgloss had already padded to a uniform width. `SetSize` semantics are the
  usual defect here: lipgloss sizes the content box and draws the border *outside*
  it, so a style given `Width(w)` renders `w+2` columns.
- Overlay states must be handled **before** the global quit/esc keys in
  `app/app_update.go`'s prelude, or `q` quits while the user is typing. Each
  branch there carries its ordering constraint as a comment; keep that up.
- `ui/menu_scan_test.go` has an enum-count tripwire that fails on purpose so you
  classify whether the new state's bar carries key hints or progress text.

## Changing user-visible copy

**A copy change is a width change.** Two real defects came from edits that only
added words: an all-limited sentence pushed to 80 columns against a 74-column
inner width, and a gutter whose 2 columns were added to rows rather than taken
from them. Both wrapped at the *default* terminal size, and a wrap costs a row the
height budget cannot see.

- Bound the line the way the surrounding package already does (a fallback ladder,
  not a shorter string — names vary).
- Pin any threshold you state in a comment as an **assertion**. A comment claiming
  "fits up to 11 characters" is unverified, and one off-by-one makes it a lie.
- `git grep` the literal afterwards. A reworked sentence leaves stale copies in
  specs, plans, and cross-references that point *at* the stale copy.
- Vocabulary guards must be **exact match**. `TestRegistry_ReorderLadderVocabulary`
  exists because the reorder ladder is session → repo group → account **cluster**,
  and a `Contains("account")` check would have passed the original bug.

## Before you call it done

- Which of the sites above did you touch, and which guard proves each?
- For a keybinding: did you actually **press** it? Site 4 has no guard.
- For copy or a glyph: what does it measure at the narrowest supported width?
- `just ci`.
