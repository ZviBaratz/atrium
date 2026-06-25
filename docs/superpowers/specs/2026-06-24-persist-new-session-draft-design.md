# Persist the new-session form draft across Escape

## Problem

The new-session form (opened with `n` or `N`) is destroyed on Escape:
`cancelPromptOverlay()` sets `m.textInputOverlay = nil` (`app/app_session.go`),
and the next open builds a brand-new overlay via `NewSessionCreateOverlay`. So
the common "Escape to quickly check something, then come back" round trip loses
everything the user had typed — title, multi-line prompt, and every picker
selection.

This is a polish gap for a tool whose core loop is composing prompts to dispatch
agents.

## Goal

Escaping the new-session form and reopening it restores the in-progress draft
exactly as it was left, so a deliberate Escape-to-check-something is non-
destructive. Provide an explicit, accident-proof way to discard the draft and
start fresh.

## Non-goals

- **Cross-restart persistence.** The draft is in-memory only, for the current
  Atrium run. It is never written to `state.json`. This deliberately avoids
  stale drafts resurfacing days later (pointing at deleted repos / renamed
  profiles).
- **Separate drafts for `n` vs `N`.** `n` and `N` open the *same* form
  (`NewSessionCreateOverlay`); they differ only in initial focus (title vs
  project picker). There is one shared draft, not two.
- **The other `TextInputOverlay` users.** The quick-send reply box
  (`submitOnEnter`) and the smart-dispatch input (`smartDispatch`) reuse the
  same overlay type but are out of scope; only the create form (`isCreateForm`)
  stashes. Escape on those keeps today's discard behavior.
- **Field-level snapshotting.** We keep the whole live overlay object rather
  than serializing each field and re-injecting (see Tradeoffs).

## Behavior

### Stash on Escape
When the create form is Escaped:
- If the form is **dirty** — `title` or the prompt `textarea` has non-whitespace
  content — the home model keeps the overlay in a new `stashedDraft` field
  instead of discarding it.
- If the form is **not dirty**, it is discarded exactly as today
  (`m.textInputOverlay = nil`), so an untouched form always reopens fresh.

Dirtiness is defined by the free-text fields only. Changing only a picker
(repo/branch/profile/…) without typing a title or prompt does not make the form
dirty; escaping such a form discards it. This keeps the rule simple and
predictable.

### Restore on reopen
The next `n` / `N`:
- If `stashedDraft != nil`, reuse it as the overlay instead of building a fresh
  one. Title, prompt, repo, branch, profile, model, mode, account, and the
  **last focused field** are all preserved.
- Re-run the title-uniqueness check against the restored title (so validation
  state is correct after restore).
- Do **not** apply the `n`/`N` focus override; keep the stashed focus. `n` and
  `N` are therefore equivalent while a draft exists. The title/directory focus
  distinction applies only to a fresh form.
- If `stashedDraft == nil`, build fresh and apply today's `n`→title /
  `N`→directory focus.

### Clear (double-tap Ctrl+R)
Inside the create form:
- First `Ctrl+R` **arms** the clear and shows a footer hint
  (`⌃R again to clear`). The unarmed footer shows `⌃R clear`.
- A second **consecutive** `Ctrl+R` confirms: the form is rebuilt as a brand-new
  create form (identical to a pre-feature fresh open) and the stash is dropped.
- **Any other keypress disarms** the clear (footer returns to `⌃R clear`).
- No timer is involved — it is strictly "press Ctrl+R twice in a row," which
  keeps the behavior deterministic and unit-testable.

The rebuild happens app-side, because reconstructing the pickers needs config,
profiles, and candidate repo paths that the overlay does not hold. The overlay
only signals intent via a `ClearRequested` flag.

### Auto-clear
- A successful create clears the stash (`m.stashedDraft = nil`).
- Quitting Atrium clears it implicitly (in-memory only).

## Implementation

### `app/app.go`
Add one field to `home`:
```go
stashedDraft *overlay.TextInputOverlay // dirty new-session form kept across Esc, this run only
```

### `app/app_session.go`
- `cancelPromptOverlay()`: when the current overlay is the create form
  (`isCreateForm`) and `IsDirty()`, set `m.stashedDraft = m.textInputOverlay`;
  otherwise nil it as today. Continue resetting UI/menu state as before.
- Open path (where `m.textInputOverlay = ov` is set, ~line 519): if
  `m.stashedDraft != nil`, reuse it (and re-run the title check); skip the
  `n`/`N` focus override. Else build fresh as today.
- Successful-create path: set `m.stashedDraft = nil`.

### `ui/overlay/textInput.go`
- `IsDirty() bool` —
  `strings.TrimSpace(titleInput.Value()) != "" || strings.TrimSpace(textarea.Value()) != ""`.
- `HandleKeyPress`: intercept `Ctrl+R` before the `default` case (so it does not
  reach the focused field). First press sets `clearArmed = true`; a second
  consecutive press sets `clearRequested = true`. Every other branch of the
  switch resets `clearArmed = false`.
- `ClearRequested() bool` accessor.
- Footer rendering: while `clearArmed`, show `⌃R again to clear`; otherwise show
  `⌃R clear`. Only for the create form.

### `app/app_keys.go`
In `handlePromptState`, after delegating the key to the overlay: if
`m.textInputOverlay.ClearRequested()`, rebuild a fresh create form via the same
code path as a no-stash open and set `m.stashedDraft = nil`.

## Tradeoffs

**Whole-overlay stash vs field snapshot.** We keep the live `TextInputOverlay`
object. The cost: a restored draft carries the candidate-repo / profile / account
lists captured when it was first opened, so if config changed mid-run the lists
could be stale. Within a single run this is effectively never observable, and it
avoids a large amount of per-field getter/setter/re-injection code. Accepted
deliberately.

## Testing

All app-level tests stay hermetic (temp `HOME`, per existing `TestMain` in
`app/app_test.go`).

### `ui/overlay/textInput.go` unit tests
- `IsDirty()` true when title set, true when prompt set, false when both empty
  (and false for whitespace-only).
- Double-tap: one `Ctrl+R` does not set `ClearRequested`; two consecutive do; a
  `Ctrl+R` then another key then `Ctrl+R` does not (disarm works).
- Footer shows the armed hint only while armed.

### `app/` integration tests
- Open create form → type title + prompt → Escape → `stashedDraft != nil`.
- Reopen → title/prompt/focus restored.
- Open → Escape with empty form → `stashedDraft == nil` (fresh on reopen).
- Restored draft → `Ctrl+R` ×2 → fresh empty form and `stashedDraft == nil`.
- Successful create clears `stashedDraft`.
- Quick-send / smart-dispatch overlays are unaffected by Escape (no stash).

## Verification

`just build` and `just test` must pass before the work is considered complete.
