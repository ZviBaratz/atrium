# Accounts overlay: reorder rows + grouped pool display

**Date:** 2026-07-24
**Status:** Approved
**Branch:** `zvi/follow-up`
**Follow-up to:** `2026-07-23-account-pools-rotation-design.md` (PR #458, merged `666c49b`)

## Summary of decisions (all confirmed with the user)

- **Reordering is real routing precedence, not cosmetics.** `J`/`K` permutes
  `cfg.*Accounts` in place and reports `dirty`, so the app saves it to
  `config.json`. Reordering *is* how a user picks the catch-all and the match
  priority. No new config or state schema — the feature only permutes a slice
  that already exists.
- **The consequence is made visible by the badges already there.** The overlay
  renders `default` on the first rule-less account and
  `catch-all (unreachable)` on any later one (`ui/overlay/accounts.go:561`), so
  moving a rule-less account above another swaps those badges on the keypress.
  The legend's `↑/↓ move` becomes `↑/↓ select` so that "move" names only the new
  key.
- **Grouping is a read-out of the real order, never a re-sort.** Adjacent
  same-pool rows get a `┌`/`│`/`└` gutter. Display order therefore always equals
  config order, which (a) keeps the row index equal to the config index, so the
  five cursor-as-index sites need no mapping layer, and (b) makes it impossible
  for grouping and free reorder to fight each other.
- **A split pool gets a note, not a silent re-sort:** `pool 'work' is split —
  J/K to group its members`. Reorder is the fix, and the note is what makes that
  discoverable.
- **Keys: `J` / `K`, with `shift+↑` / `shift+↓` as aliases.** This is the
  vocabulary the app already has: in the session list `J`/`K` reorders the
  selected row within its container (README:257). `j`/`k` already move the cursor
  in this overlay, so shift+same-key = "move the row, not the cursor". `{`/`}`
  stays free as the consistent key for a future atomic pool move.
- **Reorder applies to all three tabs** (Claude, GitHub, Antigravity). Order is
  first-match precedence in every section (`matchRouteIndex`), and every tab
  already renders the order-dependent catch-all badge — so GH and Antigravity
  currently show the user that order matters while offering no way to change it.
- **The pool gutter is Claude-only**, because only `ClaudeAccount` has a `Pool`
  field.
- **One account moves per press, always.** No group-aware moves, no second move
  unit. A press that splits a bracket is visible the instant it happens and is
  undone by the opposite key.

## Motivation

Two post-merge notes on the account-pools PR:

1. "We should be able to move accounts up and down in the table."
2. "Might be worth considering showing account pooling as grouped."

Both are about the same table. The `@` accounts overlay renders
`cfg.{Claude,GH,Agy}Accounts` as a flat list in config order and offers no way to
change that order. But the order is load-bearing: routing is **first-match in
config order** (`config/accounts.go:123` `matchRouteIndex` — the remote is tested
against `remote_matches`, then the path against `path_matches`, per account, in
order), and **the first rule-less account is the catch-all**. The overlay already
*shows* this — that is exactly what the `default` and
`catch-all (unreachable)` badges mean — so today it tells the user the order
matters and then denies them the edit. Changing precedence means hand-editing
`config.json`.

Pooling has a related legibility problem. A pool's members are identifiable only
by reading a repeated `pool:work` chip down the rows; nothing shows that two rows
are one rotation unit. The session list already clusters by pool
(`accountKey` in `ui/list.go:85`), so the overlay is the odd one out — but the
list's clustering is *cosmetic* ordering persisted to `state.json`, whereas the
overlay's order is *config* and drives routing. The two mechanisms must not be
confused.

## Why grouping is a read-out and not a re-sort

The tempting design — always render rows grouped by pool, whatever the config
order — was rejected for three concrete reasons:

1. **It would break the index identity.** `rows()` is a 1:1 projection of the
   config slice today, and the cursor is used as a direct config index at five
   sites: the `l` availability toggle (`accounts.go:172`), `openForm` (`:199`,
   `:203`, `:207`), and the delete in `handleConfirmKey` (`:293-297`). A
   presentation-only re-sort makes the row index a lie at every one of them: edit
   or delete would hit the wrong account unless a mapping layer is threaded
   through all five.
2. **It would make the badge dishonest.** The `default` /
   `catch-all (unreachable)` distinction is a statement about *config order*. Under
   a re-sort, `default` could render on a row that is not visually first among the
   rule-less rows, which is precisely the confusion this overlay exists to prevent.
3. **It would fight reorder.** The user asked for both features. If the display
   re-sorts, a row the user moves can snap back to where the grouping wants it —
   two features actively undoing each other.

Reading the grouping out of the real order dissolves all three. It also gives the
reorder key a purpose beyond routing: *tidying* a config so its pools read as
units. That is why a split pool is worth a note rather than a silent fix — the
note is a prompt to use the key.

## Design

Everything is local to `ui/overlay/accounts.go` plus one new pure-helper file.
`handleListKey` dispatches on `msg.String()` and is **not** in
`keys/registry.go`, so this adds no registry entry and no keymap mutation guards.

### Reorder

A new case in `handleListKey`:

```go
case "K", "shift+up":   return false, o.moveAccount(-1)
case "J", "shift+down": return false, o.moveAccount(+1)
```

`moveAccount(delta int) (dirty bool)` swaps the element at `o.cursor` with its
neighbour in the active tab's slice and moves `o.cursor` with it, so the cursor
tracks the account rather than the position. It returns `false` — no swap, no
save — at either boundary and whenever the tab has fewer than two rows. The
`switch o.tab` over the three slices mirrors the delete in `handleConfirmKey`.

Nothing in `app/` changes. `handleAccountsState` (`app/app_accounts.go:14`)
already calls `config.SaveConfig` on `dirty` and drops `m.stashedDraft`. Dropping
the draft is correct here rather than incidental: a stashed create-form draft
cached its account list at build time, and the picker's entries are built from
config order (`ui/overlay/accountPicker.go:24`), so a reorder genuinely
invalidates it.

### Pool gutter

Two pure functions in a new file `ui/overlay/accounts_pool.go`:

- `poolGutter(accts []config.ClaudeAccount) []string` — one gutter cell per
  account: `"┌ "` at a run head, `"│ "` in the middle, `"└ "` at the tail, `"  "`
  for a non-member. Returns `nil` when no run of two or more adjacent same-pool
  accounts exists, which is what keeps a pool-free config rendering exactly as it
  does today (no gutter column at all, no shifted layout).
- `splitPools(accts []config.ClaudeAccount) []string` — pool names whose members
  are not all adjacent, in first-appearance order.

`Pool == ""` never forms a run: two adjacent unpooled accounts are not a pool.

Both are computed over the **whole** slice before windowing, so a tail row still
shows its `└` when the run head has scrolled off the top. This is the same
pre-scan discipline the catch-all badge already needs
(`accounts.go:499-505`, and `TestAccountsOverlay_CatchAllBadgeSurvivesWindowScroll`).

`renderList` inserts the gutter cell between the cursor marker and the name, on
the Claude tab only. The per-row `pool:x` chip **stays**: the chip and the gutter
say different things — the chip is "this account's pool" (the only signal for a
pool with one member present), the gutter is "these rows are adjacent members of
one pool". Keeping the chip also keeps every row self-describing under scroll.
Common-case row width with the gutter is 76 of the 80 inner columns at the
overlay's 84-column cap.

### Notes and legend

A second conditional note joins the existing "unmatched repos inherit the ambient
account" (`accounts.go:540`), Claude tab only:

```
pool 'work' is split — J/K to group its members
```

`rowWindow`'s chrome allowance for this new line is conditional: it stays `12`
by default and becomes `13` only when the Claude tab actually has a split pool
to report. The pre-existing unmatched-repos note keeps its unconditional
budget — it predates this feature and has always cost a row regardless of
whether it renders, so leaving it alone is what keeps a config that already
existed rendering unchanged. The new note gets no such exemption: `12` rows
*is* the existing behaviour (`24 - 12` at an 80×24 terminal), so a static `13`
would be the change — it costs every config, including one with no `pool` key
anywhere, a visible row it used to have (30 routed accounts at 80×24: 12 rows
becomes 11). Conditioning the new line's allowance on the note it actually
budgets for isn't a cosmetic widening; it's what makes a pool-free config
byte-identical to today's, as promised below.

The legend becomes two lines, reflowed so neither wraps at an 80-column terminal
(74 inner columns), which is why `l limited` moves to the second line:

```
↑/↓ select · J/K reorder · tab switch · n new · e edit · d delete     (65 cols)
l limited · t test routing · esc close                               (38 cols)
```

`J/K reorder` is advertised only when the active tab has two or more accounts and
`l limited` only on the Claude tab — the overlay's existing "never name a dead
key" convention (`accounts.go:549-551`).

## Consequences documented rather than coded

- **Rotation cursor.** `state.GetAccountRotation(pool)` is an index into
  `PoolMembers`, which is config order. Reordering members within a pool can
  therefore repeat or skip one member once. Harmless for round-robin, and
  identical to what deleting a pool member already does today. No reset — a reset
  would itself cause a repeat.
- **Account picker.** `buildAccountEntries` follows config order, so reordering
  also tidies the new-session picker's entry order.

## Backward compatibility

- No schema change of any kind. The feature permutes the existing account slice.
- With fewer than two accounts on a tab, `J`/`K` is inert and unadvertised.
- With no pools configured, `poolGutter` returns `nil`, no gutter column is
  rendered, no split note can appear, and the table is byte-identical to today's.
- Reorder never rewrites the config on its own; only an explicit keypress does.
  Opening the overlay changes nothing.

## Out of scope

- Test-routing pool/availability awareness (follow-up item B, separate PR).
- Atomic pool moves (`{`/`}`) — deliberately left unbuilt and unbound.
- Any change to the session list's `[`/`]` account-cluster reorder, which is a
  different mechanism (cosmetic ordering in `state.json`).
