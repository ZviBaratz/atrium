# Test-routing preview: pool- and availability-aware

**Date:** 2026-07-25
**Status:** Approved
**Branch:** `zvi/item-b`
**Follow-up to:** `2026-07-23-account-pools-rotation-design.md` (PR #458) and
`2026-07-24-accounts-reorder-grouping-design.md` (PR #475, item A). This is item B,
the other half of the post-merge feedback on pools.

## Summary of decisions (all confirmed with the user)

- **Extract, don't replicate.** The availability-aware "pick the next member" logic
  moves out of `app/app_session.go` into two pure functions in `config`
  (`SelectPoolMember`, `SoonestResetMember`). Both the creation path and the preview
  call them. A second copy inside the overlay would drift from creation, which is the
  exact defect class this change exists to close.
- **The preview mirrors the *confirm's* answer on all-limited**, not the rotate
  branch's. Those two disagree today (see "The disagreement" below) and the confirm is
  what a user on the create-form path actually gets.
- **Bracketed block**, reusing item A's `poolGutter`, so the preview and the grouped
  `@` list read alike.
- **Height-capped with a counted overflow line.** The preview has no windowing today;
  a pool block adds a line per member. The cap keeps the `esc back` hint reachable and
  never hides the decisive member.
- **Read-only.** The preview reads the rotation cursor and never writes it. It is a
  side-effect-free "what would happen now".
- **Dormant** for a no-pool / single-account config: byte-identical to today.
- **Claude branch only.** The GH and Antigravity branches of `renderPreview` are
  untouched — pools and availability are Claude-only concepts.

## Motivation

Two post-merge notes on the account-pools work, both about the `@` overlay's `t`
(test routing) preview:

> 3. "When testing routing, … I see it resolving to zvi.baratz correctly, but there's
>    no pool information."
> 4. "I marked zvi.baratz as limited, but the routing test still shows it as selected.
>    It might be good to account for that (ideally with clear decision information)."

**This is a preview gap, not a rotation bug.** Real session creation already skips
limited members and rotates correctly (`app/app_session.go:1387-1433`, covered by
`app/rotation_test.go`). The preview simply never learned about pools: `renderPreview`
(`ui/overlay/accounts.go:450`) resolves via `o.cfg.ResolveClaudeAccount(remote, path)`
— the pre-pool flat resolver returning `(name, dir, isDefault)`. It has no pool, no
availability, no rotation cursor. So it confidently reports the *first-matching*
account while creation would use a different one, and it does so in the one screen
whose entire purpose is answering "which account would a new session here use?".

That makes the preview worse than absent: it is a wrong answer delivered with the
authority of a dedicated tool.

## The disagreement this design had to resolve

Exploration turned up that Atrium currently has **two different answers** for a
fully-limited pool:

| Path | Code | Answer |
|---|---|---|
| `startNewSession`'s rotate branch | `chosen := start` (`app_session.go:1409`) | the **cursor's own** member |
| `confirmAllExhausted` | `soonestResetMember(...)` (`:1835`) | the **soonest-to-reset** member |

On the create-form path `gateAllExhausted` fires first, stages the confirm, and pins
the soonest-to-reset member — so **the confirm's answer is the one users get**, and
that is what the preview reflects. The rotate branch's `start` fallback is a defensive
backstop, and `SelectPoolMember` preserves it verbatim (`idx = start` when
`allLimited`) so this change moves no app behaviour at all.

### Related gap, deliberately out of scope

Smart auto-dispatch calls `startNewSession` directly (`app_session.go:304`) and never
runs `gateAllExhausted`. On a fully-limited pool it therefore falls through to the
`start` fallback with no confirm — the comment at `:1409` calling that branch
"defensive" is not accurate for that caller. Returning `allLimited` from the shared
selector makes the condition *detectable* at both call sites, which is the
precondition for fixing it; the fix itself is a separate PR. Filed as #483.

This is also why the preview's all-limited sentence (`previewDecisionLine`, "Design"
§2 below) names *"the form"* rather than "creating" bare: the
confirm it describes is real on the create-form path and does not exist on smart
auto-dispatch's, so the copy must not imply it does. The two decisions are linked —
#483 is the reason the sentence needs the qualifier, not a coincidence.

> **Closed by #483.** Auto-dispatch now runs `gateAllExhausted` before spawning, so
> the qualifier this section argued for is no longer true and the sentence has gone
> back to "creating asks to confirm". The two decisions being linked is what made
> that a copy change as well as a behaviour change. The sample in §2 below still
> shows the qualified wording — it records what this design shipped, not what the
> code says today.

## Design

### 1. The extraction — `config/rotation.go`

`config` imports only `atrium/log`; `app` and `ui/overlay` both import `config`. A
pure selector there creates no cycle.

```go
// SelectPoolMember returns the index of the member a new session would use and
// whether every member is currently limited. It scans from the normalized cursor,
// wrapping in config order, and returns the first available member. When no member
// is available it returns the cursor's own member (idx == the normalized cursor)
// with allLimited true — the fallback startNewSession has always applied. Empty
// members returns (-1, false); callers guard.
func SelectPoolMember(members []ClaudeAccount, avail map[string]AccountAvailability,
                      cursor int, now time.Time) (idx int, allLimited bool)

// SoonestResetMember returns the index of the member whose limit resets soonest.
// Members with a parseable Until sort by that time; indefinite or unparseable sort
// last; all-indefinite returns 0.
func SoonestResetMember(members []ClaudeAccount,
                        avail map[string]AccountAvailability) int
```

`now` is an explicit parameter, matching `config.AccountAvailable(av, now)` — no clock
inside a pure helper, so tests pin a fixed instant.

Four call sites move onto them:

| Site | Today | After |
|---|---|---|
| `app_session.go:1408-1416` | cursor normalization + wrap scan | one `SelectPoolMember` call |
| `app_session.go:1819-1825` | **a second copy** of the availability scan | one `SelectPoolMember` call |
| `app_session.go:1835` | `soonestResetMember` (private) | `config.SoonestResetMember` |
| `accounts.go` `renderPreview` | *(nothing — the gap)* | both |

What stays in `app`: reading state and the clock, the `chosen+1` cursor-advance
convention, the `len(members) > 1` persist guard, the pool-stamp suppression, and the
pin bypass. The selector returns an index and never writes state.

**One asymmetry is preserved deliberately.** `gateAllExhausted` keeps its
`len(members) < 2` early return: a singleton pool whose one account is limited must
not raise the confirm, whereas `SelectPoolMember` reports `allLimited = true` for it.
The guard, not the selector, encodes "a pool of one has nothing to rotate".

### 2. What the preview renders

Multi-member pool with a skip:

```
Claude → work-2 (~/.claude-work2)
         pool 'work' ⇄
         ┌ work-1       ⛔ limited
         └ work-2       ● available  ← next
         work-1 limited → rotating to work-2
GitHub → ~/.config/gh-work [GH_TOKEN]
Antigravity → inherit ambient env
```

All members limited:

```
Claude → ⚠ all 'work' accounts limited
         pool 'work' ⇄
         ┌ work-1       ⛔ limited    ← on confirm
         └ work-2       ⛔ limited
         the form asks to confirm, then uses work-1 (first member)
```

Decisions encoded in that layout:

- **The headline keeps its shape.** `Claude → <account> (<dir>)` is unchanged in form;
  what changes is that the account named is the rotation-chosen member rather than the
  first-matching one. On all-limited the headline becomes the warning, because there is
  no member creation would use *without asking first*.
- **`⇄` is the established rotation glyph** — the create-form picker already labels a
  rotating pool entry `work ⇄` (`ui/overlay/accountPicker.go:38`).
- **Chips are the list's exact literals**, `● available` (DimStyle) and `⛔ limited`
  (DangerStyle), so the same account reads the same way in both surfaces.
- **`← next` / `← on confirm`** distinguishes "this is what happens" from "this is what
  happens if you say yes".
- **The decision line only appears when there is a decision to explain** — a skip, or
  the all-limited confirm. A pool where the cursor's own member is available needs no
  sentence; the `← next` marker is the whole answer.
- **`(first member)` vs `(resets soonest)`** is chosen by whether the pinned member has
  a parseable `Until`. Flags are indefinite-only today, so `SoonestResetMember` returns
  0 and "resets soonest" would be a false literal — the README already documents that
  the confirm's pick "while flags are indefinite-only, is the first pool member".

### 3. Dormancy

The Claude branch splits on `len(members) < 2` from `ResolveClaudePool`:

- **fewer than two members** — no pool, a singleton pool, an ungrouped account, zero
  accounts, or the synthetic `("default", [{Name: "default"}], true)` sentinel — falls
  through to **today's exact `ResolveClaudeAccount` block, unchanged**, including its
  four-branch switch over the sentinel ambiguity. Nothing about that path is re-derived
  through a different resolver, so its eight existing tests stay green unmodified.
- **two or more members** — the pool block. A pool with two or more members is always a
  real declared pool of real named accounts, so the sentinel case cannot arise there.

### 4. Height cap

The preview has no windowing (`rowWindow` serves the list only), and its chrome is
exactly 18 lines: border 2 + `Padding(1,2)` 2 + title/blank 2 + 12 body lines
("Test routing", blank, two labelled inputs = 4, blank, three result lines, blank,
hint). At a 24-row terminal a pool block therefore affords **4** member lines before
the `esc back` hint is pushed off — reachable with a 5-member pool, not a pathological
one.

`previewMemberWindow(n, chosen, budget)` returns a contiguous window anchored so the
decisive member is always inside it, plus a hidden count rendered as
`… N more members not shown`. Anchoring is the point: a cap that could hide the
`← next` row would defeat the feature it is protecting. Because the window can omit
members at either end, the notice says "not shown" rather than "more" — it is a count,
not a direction. The gutter is computed over the whole member slice and indexed
absolutely, so a windowed first row shows `│` rather than a false `┌`, exactly as the
list behaves under scroll.

### 5. Read-only

`renderPreview` reads `o.state.GetAccountRotation(pool)` and
`o.state.GetAccountAvailability()`; it never calls `SetAccountRotation`. Rendering is
idempotent, which matters because Bubble Tea re-renders on every keystroke: a preview
that advanced the cursor would rotate the pool once per typed character. A test
renders three times and asserts the cursor is unmoved.

## Backward compatibility

- No config or state schema change. No new key in `config.json` or `state.json`.
- No app behaviour change: the extraction is refactor-only, pinned by
  `app/rotation_test.go`'s existing tests, which stay green unmodified.
- A config with no `pool` anywhere renders the preview byte-identically to today.
- No keymap change — `t`, `handlePreviewKey` and the registry are untouched.

## Out of scope

- The auto-dispatch path skipping `gateAllExhausted` (filed separately).
- Per-account reset times: flags remain indefinite-only, so `Until` is exercised by the
  pure tests but not reachable from the UI yet.
- The GH and Antigravity preview branches.
- Any change to rotation itself, the picker, or the list.
