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
# dispatch-case LINES, not names — scoped to dispatchAction, see below
awk '/^func \(m \*home\) dispatchAction/,/^}/' app/app_update.go | grep -c 'case keys.Key'
grep -c '^func Test' app/dispatch_coverage_test.go   # the site-4 guards
awk '/^type Config struct/,/^}/' config/types.go | grep -cE '`json:'   # Config fields
```

The dispatch count is *lines*, and several cases carry two or three names
(`case keys.KeyMoveUp, keys.KeyMoveDown:`), so it is always below the number of
actions — that is expected, not a shortfall.

**Two of these must be scoped, and both were wrong here before they were fixed.** The
dispatch recipe used to grep the whole of `app/app_update.go`, which returns one more
than the scoped count because `keyAllowedWhileBusy` — site **7** — switches over key
names too; the number therefore summed two different switches. Counting json tags across
the whole of `config/types.go` overshoots for the same reason, because `Profile`,
`RepoScript`, `ClaudeAccount`, `AgyAccount` and `GHAccount` live there and carry their
own.

A recount recipe that answers a different question than the claim above it is how the
wrong number got here in the first place — twice — so if you change a recipe, change the
guard (`keys/skill_counts_test.go`) with it. They are checked against each other only in
the sense that both must equal the tree.

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

At last count: **62 registry entries** and **51 dispatch-case lines**, with a dozen-odd
drift guards in `keys/*_test.go` and **4** in `app/dispatch_coverage_test.go`.

Those two numbers, and the `Config` field count below, are checked against the tree by
`TestSkillCountsMatchTheTree` (`keys/skill_counts_test.go`), so they cannot rot the way
they had. It exists because they were wrong twice in one PR: 58 when the tree held 60,
then "corrected" to 60 by adding 2 to the stale number instead of counting. **Recount;
never adjust.** The guard-counts either side of it are deliberately approximate — pinning
a number no decision hangs on just taxes every unrelated test someone adds.

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
written the obvious assertion: 62 entries against 51 case *lines* is not 11
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

45 json-tagged fields on `Config` itself at last count.

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

## Adding a UI state — 7 sites, three of them fixture files

`app/app.go`'s state enum (which bumps `numStates`), plus a nullable overlay pointer
field. Then six more, and the count is the thing to get right: adding the enum and
stopping at `statemachine_test.go` is a hard CI fail, not an omission you find later.

1. **`frameStates()`, in `app/frameparity_test.go`** — *not* `app/statemachine_test.go`,
   which only *consumes* it. `TestFrameStatesCoverEveryState` requires
   `len(frameStates()) == numStates`, so bumping the enum fails there immediately.
   Seven tests fan out from that one entry (frame parity, both colour fingerprints,
   the bounds sweep, the background-message state machine, both no-colour checks).

   Give it a **`wire` func** that arms the overlay production would keep, or the state
   is swept half-constructed and the interesting dereference never happens. Prefer the
   real opener over assigning the overlay field by hand: an overlay that comes with
   sibling state (the palette's row table, the custom-commands row table) is only
   half-armed otherwise, which is the dereference the sweep exists to find. And seed it
   with **real content** — an overlay wired empty renders its one-line empty state, so
   every width and height guard downstream holds nothing.
2. **A new golden under `app/testdata/frames/<name>.txt`.** `compareGolden` hard-fails
   on a missing file, and creates it for you: `CS_UPDATE_GOLDEN=1` is an env var, not a
   flag.
3. + 4. **Re-baseline `app/testdata/colours.txt` and `colours-light.txt`**, each under
   its own `-run` target. They iterate `frameStates()` in *slice* order and write one
   block per state, so **append your entry last**: inserted mid-slice it rewrites every
   block after it and the diff becomes unreadable.
   ```
   CS_UPDATE_GOLDEN=1 go test ./app/ -run TestFrameParity
   CS_UPDATE_GOLDEN=1 go test ./app/ -run TestFrameColourFingerprint
   CS_UPDATE_GOLDEN=1 go test ./app/ -run TestLightFrameColourFingerprint
   ```
5. `app/app_layout.go` — `menuVisible()`'s case list if the state hides the hint bar,
   and a `SetSize` block so the overlay is sized responsively.
6. `app/frame_restore_test.go` — see below.
7. The `View()` arm in `app/app.go`, and a router in `app/app_update.go`'s
   `handleKeyPress` prelude.

Situational, and worth knowing which: **`app/view_bounds_test.go`'s overlay map has no
tripwire** and is deliberately fixture-specific — `TestViewFitsTerminalBoundsEveryState`
(which lives in `frameparity_test.go`, not here) carries the breadth. Add an entry only
when your state needs a pathological fixture the generic sweep cannot produce — an
unbounded list, user-authored text with no natural width.

- `SetSize` semantics are the usual defect — but check which way round before "fixing"
  one. **lipgloss v2 counts the border and padding INSIDE `Width`**, so `Width(w)`
  renders exactly `w` columns (`style.go`: `width -= horizontalBorderSize`). That
  inverted the v1 behaviour this line used to describe, and it inverted silently; the
  in-tree statement of it is `ui/theme/panel.go`'s comment ("Width and Height are the
  box's TOTAL size, borders included … the upgrade guide does not mention it").
  **Copy `commandPalette.go`** — `Width(p.width)` beside `inner := p.width - 6` is the
  self-consistent pair. `cmdLogOverlay.go` carries that same comment over
  `Width(c.width + 2)` *and* the same `inner := c.width - 6`, which cannot both be
  right: it renders two columns wider than its declared width while truncating content
  two columns narrow. It does not overflow, so nothing fails — but it is not the form
  to copy. (`textOverlay.go`'s `+2` is fine: its `boxWidth()` is *defined*
  border-exclusive and capped against the terminal.) The live defect class is the
  opposite one: hand-subtracting the frame a second time and rendering every box two
  columns narrow.
- Charge **every** non-list row to the height budget, including the conditional ones
  (`paletteChrome`'s trailing `+1` for "… N more"). A row that appears only sometimes is
  a row no golden and no bounds sweep renders — `frameStates()`' wire only *opens* an
  overlay — so the overflow costs the bottom border invisibly. Better still, give a
  conditional line an existing row to take over rather than one of its own.
- **Truncate the footer**, never let it wrap. `commandPalette.go` does; `cmdLogOverlay.go`
  does not. A wrap costs a row the height budget never counted, and `PlaceOverlay` takes
  it off the bottom border.
- `truncate.StringWithTail(s, w, "…")` replaces a character at **exactly** `w`, not only
  above it. Guard it with a `lipgloss.Width(s) <= w` check, or a fixed marker built to
  fit its budget reads back as `(repo…`.
- Add it to `app/frame_restore_test.go` if it hides the hint bar (`menuVisible`),
  or exempt it there with a reason — the walk over `numStates` fails otherwise.
  Its `opens` entry is also the **only** place in the tree that presses an opener key
  and asserts the state changed, which is what site 4 above cannot prove. Hiding the
  bar hands its row to the panes; closing without recomputing the
  layout leaves the frame a line taller than the terminal, and the alt-screen
  renderer never erases it. `view_bounds_test` cannot see this: it only measures
  a *freshly armed* overlay, never one that has been closed. The recompute itself
  is guarded once, in `Update` — it compares `menuVisible` before and after every
  message — so a state left by an async message is covered as well as one closed
  by a key, and no `dismiss*` helper needs a `recomputeLayout()` of its own.
- Overlay states must be handled **before** the global quit/esc keys in
  `app/app_update.go`'s prelude, or `q` quits while the user is typing. Each
  branch there carries its ordering constraint as a comment; keep that up.
- `ui/menu_scan_test.go`'s enum-count tripwire (`require.Equal(t, 5, int(StateVisual))`)
  pins **`ui.MenuState`**, not `app.state` — so an app state does *not* trip it, whatever
  the neighbouring prose implies. It fires only if the new surface also needs its own
  hint-bar variant, which a modal overlay does not: those hide the bar entirely.

## Changing user-visible copy

**A copy change is a width change.** Two real defects came from edits that only
added words: an all-limited sentence pushed to 80 columns against a 74-column
inner width, and a gutter whose 2 columns were added to rows rather than taken
from them. Both wrapped at the *default* terminal size, and a wrap costs a row the
height budget cannot see.

- Bound the line the way the surrounding package already does (a fallback ladder,
  not a shorter string — names vary). **Hints ladder; refusals do not.** A hint's
  tail is optional detail, so `fitHint` dropping it on a narrow terminal is the
  right trade. An error's tail is the *reason*, and a ladder hands the short rung
  to the default 80-col terminal — the good spelling would exist only for the
  developer with a wide window. #541's three batch refusals were therefore cut to
  one spelling that fits everywhere, not laddered. A second, mechanical reason to
  prefer shortening: a ladder built from `fmt.Sprintf` at a call site cannot join
  `hintLadders` (`ui/overlay/hints_test.go`), so it ships without the ordering
  guard — and `fitHint` returns the *first* rung that fits, so a mis-ordered ladder
  skips rungs invisibly at every width.
- Prefer a bound you can *prove* over one you measured, and **the way to prove one is
  usually to drop the input, not to widen the fixture.** Two of #541's three refusals
  became provably ≤32 cells only by deleting an interpolated value with no ceiling:
  `sc.Limit` from the `max_sessions` message, and the batch total from the
  over-the-limit message (it is `variantCountMax` × `len(profiles)`, and nothing caps
  `config.Profiles`). What remains in each is either a compile-time constant or a count
  an earlier return already bounds. A message whose width depends on unbounded input has
  no worst case to assert — and, worse, no fixture can *reach* the width that breaks it,
  so a render guard stays green over it — a reword that reintroduces the value can *fit*
  at the total a fixture can build, which is why the absence has to be asserted and not
  measured. Note which half of the fact you are shedding: keep the number the user has to
  act on, drop the one they can recover from the panel or the row in front of them. The
  deletion tends to pay for itself — dropping #541's total freed eight cells, which went
  back into naming what the surviving number counts (`the 20-session limit`, not `the 20
  limit`).
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
