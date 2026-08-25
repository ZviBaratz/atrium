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

## Adding a keybinding — 10 sites, every one guarded

At last count: **63 registry entries** and **52 dispatch-case lines**, with a dozen-odd
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
| 2b | `keys/registry.go` — the `Entry`'s `Action`, its name in `config.json` (empty for a `DocOnly` entry) | `TestRegistry_EveryDispatchedEntryHasAnAction`, `TestRegistry_NoDuplicateActions`, `TestRegistry_ActionNamesAreSnakeCase`, `TestActionVocabulary_Golden` |
| 2c | `keys/registry.go` — the `Entry`'s `Effect`: what pressing the key can change | `TestEveryRegistryEntryDeclaresAnEffect`, `TestKeyEffects_Golden`, `TestBusyAllowlistNeverAdmitsAMutation` |
| 3 | `keys/help_layout.go` — a `HelpRow` in `HelpGroups`, or a `Mentions` entry | `TestHelpGroups_CoverEveryBinding` (fails structurally, before any rendering) |
| 4 | `app/app_update.go` — `case keys.KeyX:` in `dispatchAction` | `TestEveryRegistryActionHasADispatchCase` — see below |
| 5 | `keys/registry_test.go` — the string→action pair in the golden inventory | itself (`TestGlobalKeyStringsMap_GoldenInventory`) |
| 6 | `README.md` — **two** tables: `#### Keybindings` and `##### Action names`, backtick-wrapped, each in its own section | `TestReadmeDocumentsEveryBinding` **and** `TestReadmeDocumentsEveryAction` |
| 7 | `app/palette_gates.go` — a `paletteGates` entry (`global()` / `needsSelection` / `perSession`) | `TestEveryPaletteActionHasAGate` **and** `TestPaletteGatesAgreeWithDispatch` (both directions) |
| 8 | `app/app_update.go` `keyAllowedWhileBusy` — *only if* it must work during an async action | `TestBusyAllowlistNeverAdmitsAMutation` one way only — see below |

Site 2b has the longest memory: an `Action` is the name a user's `config.json` binds
to, so unlike every other identifier here it can be added to but never renamed.
`TestActionVocabulary_Golden` is what makes a rename fail rather than silently stop
honoring an override that used to work.

Site 2c is the newest, and the only site whose *omission* is caught rather than
merely its drift: `EffectUnset` is the zero value and is invalid, so an `Entry` that
says nothing about what its key can change fails
`TestEveryRegistryEntryDeclaresAnEffect` instead of defaulting into "safe". Read the
`Effect` doc comment before choosing — it carries the taxonomy and the two rules that
are not guessable from a key's label:

- **Classify the handler, not the name.** `approve` (`a`) is `EffectMutate` and
  changes nothing in Atrium: it taps Enter on the agent's own tool-permission
  dialog. Read as observing, a read-only mode lets a bystander authorize an `rm -rf`.
- **An opener carries the worst effect reachable through the surface it opens**,
  unless another gate on this same classification already covers that surface. The
  command palette is `EffectObserve` for exactly that reason (`runPaletteAction`
  re-enters `dispatchAction`, so its rows are classified); `!`, `H` and `v` are not,
  because their surfaces reach something no `Entry` owns: shell verbs for `!`; an
  attach *and* a fork that opens the create form for `H`; and, for `v`, the bare `x`
  that `handleMultiSelectState` matches literally before it resolves the rest
  through `GlobalKeyStringsMap` (so its pause/resume/kill really are classified —
  only `x` is not). The tab keys are `EffectMutate` for the same rule: the terminal
  tab starts a shell in the worktree the first time it renders.

`EffectView` — persists view state (fold, split, preset, list order) and nothing
else — exists because the busy-gate deliberately admits six keys that write
`state.json`, so a two-value split makes the site-8 guard below fail on its first run
over correct code. `keys/effect_test.go`'s golden is the authority on which key is
which; don't restate the classification anywhere else.

Site 6's second table is the one that surprises: `##### Action names` is a **two-column**
split of the whole vocabulary, so adding one action reflows every row after the midpoint
rather than appending. Regenerate it from the existing pairs, and sort on the action name
with the backticks stripped — `` `new_pick_project` `` sorts before `` `new` `` otherwise,
because `_` precedes a backtick.

**Prose that names the key is not on this list, because it should not exist.** A sentence
like "press r to resume" reads the label from the registry (`keys.LabelOf`), and
`TestNoProseNamesAKeyLiterally` (`app/prose_keys_test.go`) scans `app/`, `ui/` and
`ui/overlay/` for the literal form. Its allowlist is only for an overlay's own keys, which
no `Entry` owns and no override can move.

Plus, situationally: `ui/menu.go`'s context hint sets (`defaultHintKeys` and
friends) if the key should appear in the bar — guarded in the reverse direction
only, by `ui/menu_scan_test.go`.

**Site 8 is guarded in one direction only, and it is the direction that catches the
cheaper mistake.** `TestBusyAllowlistNeverAdmitsAMutation` (`app/key_effect_test.go`)
rejects a key added to `keyAllowedWhileBusy` whose `Effect` is `EffectMutate` — the
allowlist can no longer quietly grow a key that races the in-flight action it was
meant to wait for. Nothing tells you a key *belongs* there, because "must work
mid-action" is not a property the registry can hold; that judgement is still yours.

Two absences in that switch are load-bearing and must not be "fixed". `KeyUndoKill`
is out because the gate is the only thing making a restore single-flight, and a
second press would recreate a branch the first one already claimed
(`TestBusyGateStillExcludesUndo` pins it). `KeyQuit` is *in*, despite being
`EffectMutate`, because it is the escape hatch from a wedged action — carried as a
named entry in `busyGateMutationExempt`, with the reason, rather than by softening
its classification. The tab keys are in it too, admitted for navigation and
`EffectMutate` because the terminal tab starts a shell — but what makes admitting
them safe is a gate *outside* this switch: `shellStartRefused`
(`app/app_frames.go`) withholds that shell while an action is in flight, which is
also the only thing that could cover the two routes to it that involve no keypress
(#701). Read the map itself rather than a count here, and read its note before
adding a reason — an exemption is the one way to silence this guard.

Site 7 is mandatory for every *dispatchable* action, and it is the one that fails
in a file you were not editing — the palette reaches every action, so an un-gated
one defaults into "always fine" by omission. Two things to get right, both of which
`TestPaletteGatesAgreeWithDispatch` will tell you about: the gate must not be
*stricter* than the handler (a false dim makes the action unreachable from the
palette for a reason that is simply wrong), and where the handler checks several
preconditions the gate should check them **in the handler's order**, or the row
explains itself with the wrong one. A new chip-sized reason also belongs in
`TestPaletteGateReasonsFitTheRow`'s list, which is hand-maintained.

**Site 4 was the gap until #374 closed it.** The count mismatch is why nobody had
written the obvious assertion: 63 entries against 52 case *lines* is not 11
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

53 json-tagged fields on `Config` itself at last count.

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

There are **two** glyph tables, and which one you are in decides which guards fire:
the `Glyphs` struct (declared in `ui/theme/theme.go`, filled per rung in
`ui/theme/registry.go`) and the agent identity table (`ui/theme/agent.go`). Both are
rung-aware, both project into the `?` legend, and until #674/#673 the second was
neither. A theme carries both — `Glyphs` exported, the agent table behind
`AgentGlyph`/`AgentKeys`.

1. **It must measure width 1.** Guarded by `TestGlyphWidths`,
   `TestAgentGlyphWidths`, `TestNoteGlyphIsSingleCellEverywhere` in
   `ui/theme/theme_test.go`. A 2-cell glyph is not cosmetic — it breaks the column
   math and the view-bounds invariant, which is exactly what `TestGlyphWidths` says
   it guards.

   "Across every palette × glyph-set" is the shape of the *invariant*, not of any one
   of those three sweeps, and the axes they actually walk differ:
   `TestGlyphWidths` is every palette × every rung, `TestAgentGlyphWidths` every rung
   on the default palette (`themeAtRung`), `TestNoteGlyphIsSingleCellEverywhere` every
   palette on whichever rung is current. Every rung is the property to preserve in the agent
   sweep rather than to assume — it used to measure only the one table `Get()` returns,
   and a rung a sweep never visits is a rung nothing measures at all.

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
   `app/help_legend_test.go`'s `TestLegendCoversRowVocabulary` covers **both** tables:
   it reflects over the live `Glyphs` struct, so a new field forces a decision, and
   walks `theme.AgentKeys()` (`assertLegendCoversAgents`), so a new agent does too.
   The agent half is #673's; before it, agent identity was in no legend at all and
   #672 added a sixth glyph without tripping anything.

   Two things that half does differently, both worth copying into the next guard of
   this shape. It asserts the glyph **and its label together** — a bare containment
   check is what the `Glyphs` exclusion map has to apologise for twice, where a mark
   passes because some unrelated entry happens to paint it. And it runs on more than
   one rung, because the legend is a projection of the *active* tables.
3. **Prefer plain single-cell non-PUA Unicode.** There is no reliable way to probe
   for a patched font; the three-rung ladder (`nerd`/`plain`/`ascii`) in
   `ui/theme/registry.go` is the answer, not detection.
4. **It needs an ascii form too — and the table will not tell you it is missing.**
   `asciiGlyphs()` and `asciiAgentGlyphs()` are both built *from* the plain table, so
   a glyph added to plain alone is silently Unicode on the rung whose entire purpose
   is to have none. That is #674 exactly: the agent table had no rung, and a user on
   `glyph_set: ascii` got a status column of `* ? ~ Y` with `✻ ❖ ✦ ≡ ✜` still painted
   beside it. `TestASCIIAgentGlyphsDoNotCollide` fails on an inherited Unicode mark
   in the agent table; on the `Glyphs` struct that check exists for `ContextRamp`
   alone (`TestContextRampRungs`), so a new *field* is still on you.

   The agent table is **two** rungs, not three (`agentGlyphsFor`): plain and nerd
   share one, because the nerd rung exists to overlay vendor PUA icons and this table
   deliberately carries none.

   **`asciiGlyphs()`' tolerable-collision argument does not extend to the agent
   table.** Those four reuses are argued from "no screen shows both meanings"; the
   agent glyph is pinned to the far right of a session row, so it shares one frame
   with the status gutter, the git chips, the fold markers and the context meter.
   Case-insensitively, too — `X` and `V` are the case-twins of Muted/MarkChecked and
   Behind/FoldOpen, and case height is the weakest distinction a font can make, on
   exactly the fonts this rung exists for. The rule the values were derived by is written
   into `asciiAgentGlyphs()`' comment; what `TestASCIIAgentGlyphsDoNotCollide` executes is
   the constraint set that rule serves (7-bit, no case-insensitive collision, distinct
   within the table), not the derivation — a different letter meeting those is a review
   conversation, not a build break.

## Adding a UI state — 5 sites, one of them production code

`app/app.go`'s state enum (which bumps `numStates`), plus a nullable overlay pointer
field. Then one production-code site, two test-table entries and two fixture steps
below. The list was longer before #801: `viewContent`, `handleKeyPress`'s prelude,
`menuVisible` and the paste switch each hand-enumerated the enum again, the
per-overlay `SetSize` blocks hand-kept the same fact keyed by overlay *field*
(nil-guarded blocks that named no state), and those five readers now select
through `surfaceSpecs`.

That consolidated the five READERS, not every per-state fact. A state that interacts
with the mouse or with the bar's own content still has hand-kept sites the registry
deliberately does not cover: `hintBarClickState` (`app/app_msgs.go` — its test now
walks the enum, so an unclassified state fails; #852 is what landing in neither of
its old lists looked like), the `ui.MenuState` writers (a separate enum, set
imperatively on entry and on every exit path), the help/info mouse arm in
`handleMouse` (wheel-scroll and click-outside-dismiss name the two `textOverlay`
states literally), and the per-state resize exits in `Update`'s `WindowSizeMsg` arm
(hint mode *leaves* on any resize; the screensaver only when the new size drops
below the splash floor, `ui.SplashFits` — state changes, which the `size` column
cannot express: it resizes overlays). Of those, only the click gate has a
guard that forces the decision; the rest are still found by reading.

1. **A `surfaceSpec` entry in `app/surfaces.go`** — the production-code site. The entry is
   the state's whole surface as data: `render` (what `viewContent` composites over
   the frame; nil for a state that renders in the frame itself), `keys` (the handler
   `handleKeyPress` routes to — every entry's handler runs before the global esc/quit
   handling, so the ordering that used to be a per-guard comment is structural now),
   `barVisible` (`menuVisible`'s bit), `size` (the overlay's resize policy; the walk
   runs every entry's closure, which nil-checks its own overlay field — one closure
   per FIELD, so a state sharing another's overlay leaves size nil and says so),
   `paste` (nil means a paste is inert there), and `fixture` (the golden's name).
   `TestEverySurfaceSpecIsComplete` fails a forgotten or misplaced slot, a duplicate
   fixture name, and a fixture with no golden; `TestSurfaceSpecHasNotGrownAField`
   beside it makes a new column a decision for every state rather than a silent zero.

   **The guard proves the entry exists, not that it is right.** A `keys` func aimed at
   the wrong handler passes it, and a per-surface suite that only calls its handler
   directly cannot notice — the checkpoints timeline is the state that could have
   shipped that way, which is why `TestCheckpointsKeyRoutesThroughUpdate` presses its
   key through `home.Update` instead; copy that shape for the new state. And still
   **press the keys yourself** in the running TUI.
2. **A `{state, wire}` entry in `frameStates()`, `app/frameparity_test.go`** — *not*
   `app/statemachine_test.go`, which only *consumes* it. The name column is derived
   from `surfaceSpecs`' fixture, so the entry is just the state and its wiring.
   `TestFrameStatesCoverEveryState` requires one entry per state, so bumping the enum
   fails there immediately. Seven tests fan out from that one entry (frame parity,
   both colour fingerprints, the bounds sweep, the background-message state machine,
   both no-colour checks).

   Give it a **`wire` func** that arms the overlay production would keep, or the state
   is swept half-constructed and the interesting dereference never happens. Prefer the
   real opener over assigning the overlay field by hand: an overlay that comes with
   sibling state (the palette's row table, the custom-commands row table) is only
   half-armed otherwise, which is the dereference the sweep exists to find. And seed it
   with **real content** — an overlay wired empty renders its one-line empty state, so
   every width and height guard downstream holds nothing.
3. **A new golden under `app/testdata/frames/<fixture>.txt`.** `compareGolden`
   hard-fails on a missing file, and creates it for you: `CS_UPDATE_GOLDEN=1` is an
   env var, not a flag.
4. **Re-baseline `app/testdata/colours.txt` and `colours-light.txt`**, each under
   its own `-run` target. They iterate `frameStates()` in *slice* order and write one
   block per state, so **append your entry last**: inserted mid-slice it rewrites every
   block after it and the diff becomes unreadable.
   ```
   CS_UPDATE_GOLDEN=1 go test ./app/ -run TestFrameParity
   CS_UPDATE_GOLDEN=1 go test ./app/ -run TestFrameColourFingerprint
   CS_UPDATE_GOLDEN=1 go test ./app/ -run TestLightFrameColourFingerprint
   ```
5. `app/frame_restore_test.go` — see below.

Situational, and worth knowing which: **`app/view_bounds_test.go`'s overlay map has no
tripwire** and is deliberately fixture-specific — `TestViewFitsTerminalBoundsEveryState`
(which lives in `frameparity_test.go`, not here) carries the breadth. Add an entry only
when your state needs a pathological fixture the generic sweep cannot produce — an
unbounded list, user-authored text with no natural width.

- `SetSize` semantics are the usual defect in a `size` closure — but check which way
  round before "fixing" one. **lipgloss v2 counts the border and padding INSIDE
  `Width`**, so `Width(w)` renders exactly `w` columns (`style.go`:
  `width -= horizontalBorderSize`). That inverted the v1 behaviour this line used to
  describe, and it inverted silently; the in-tree statement of it is
  `ui/theme/panel.go`'s comment ("Width and Height are the box's TOTAL size, borders
  included … the upgrade guide does not mention it").
  **Copy `commandPalette.go`** — `Width(p.width)` beside `inner := p.width - 6` is the
  self-consistent pair, and `cmdLogOverlay.go` carries the same one since #638
  corrected its v1-era `+2`. (`textOverlay.go`'s `+2` is fine: its `boxWidth()` is
  *defined* border-exclusive and capped against the terminal.) The live defect class
  is hand-subtracting the frame a second time and rendering every box two columns
  narrow.
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
- Add it to `app/frame_restore_test.go` if its `barVisible` is false, or exempt it
  there with a reason — the walk over `numStates` fails otherwise. Its `opens` entry
  also presses the opener key and asserts the state changed — for every bar-hiding
  state at once, which is what the keybinding table's site 4 cannot prove. Hiding
  the bar hands its row to the panes; closing without recomputing the layout leaves
  the frame a line taller than the terminal, and the alt-screen renderer never
  erases it. `view_bounds_test` cannot see this: it only measures a *freshly armed*
  overlay, never one that has been closed. The recompute itself is guarded once, in
  `Update` — it compares `menuVisible` before and after every message — so a state
  left by an async message is covered as well as one closed by a key, and no
  `dismiss*` helper needs a `recomputeLayout()` of its own.
- Overlay states must be handled **before** the global quit/esc keys, or `q` quits
  while the user is typing. That ordering is structural now — `handleKeyPress` runs
  every registered `keys` handler before its globals — so it cannot be got wrong per
  state; what `q` or esc *means inside* the surface is the entry's own comment in
  `surfaceSpecs`, and keeping that rationale beside the entry is still on you.
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
