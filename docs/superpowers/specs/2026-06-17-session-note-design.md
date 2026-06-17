# Session note — annotate why a session is parked

**Date:** 2026-06-17
**Status:** Draft (awaiting review)

## Motivation

Some sessions aren't finished and aren't abandoned — they're *parked*: waiting
for a review to land, a teammate to reply, CI to go green. Today the only way to
remember *why* a parked session is sitting there is to attach to it and re-read
the context. Scrolling past a handful of these and re-deriving "what am I waiting
for on this one?" is a recurring, low-grade tax.

A free-text **note** on a session removes that tax: a glance at the list answers
"why is this here?" without attaching. It also reinforces Atrium's
frequent-attach model rather than fighting it — the note is precisely what lets
you *deliberately not* attach to a session you're intentionally ignoring.

The motivating verb is "pause with a note," but the underlying primitive is more
general and cleaner: **a note is plain session metadata**, not an artifact of the
pause keystroke. Modelling it that way avoids welding an annotation to an action
and sidesteps a "can't edit without resume→re-pause" trap.

**Design principle:** the note is general, optional session metadata; it costs
nothing when unset, and it surfaces where the screen has room and the question is
live.

## Scope

**In scope:**
- A persisted `Note string` field on `Instance`, general (not gated to `Paused`),
  migration-safe (`omitempty`; old `state.json` decodes to `""`).
- Display **only when non-empty** — an empty note changes nothing on the row.
- **Status-aware placement** (see Visual spec): on a paused row the note takes
  line 2's version-control slot (frozen, low-value while parked); on a running
  row — the uncommon case — it drops to its own indented line so live VC signal
  is preserved.
- Distinct, beautiful styling: a leading single-cell glyph + a muted accent
  color separate from branch and `displayName`.
- Editing folded into the **rename overlay** — one dialog edits both name and
  note. Pausing opens that overlay focused on the note field, so "pause, jot why"
  is one fluid motion; the pause itself is never blocked.
- Soft length cap of ~80 characters on the note input.

**Out of scope (explicit non-goals — name them so the note stays a note):**
- Reminders, notifications, or snooze-until-time.
- Auto-resume when a condition (PR merged, CI green) is met.
- A dedicated `Blocked`/`Waiting` status.
- Structured fields (reviewer, PR link, due date) — it is one free-text line.
- Multiline / markdown notes.

These are deliberately excluded; the examples that motivate the feature
("waiting until the review lands") tempt toward a reminder system, and that is a
different, much larger feature.

## Visual spec

Note set, **paused** session — the note takes the line-2 VC slot; age stays
right (you still see how long it's been parked):

```
   ⏸ auth-refactor                              ◯ sonnet
      ✎ blocked on Benoit's review · box#123              2d
```

Note set, **running** session (uncommon) — line 2 keeps live VC signal; the note
gets its own indented line below it, full width, never truncated against chips:

```
 ▎⠋ ✳ migration-spike                           ◯ opus  AUTO
      zvi/migration · ↑3 · #142          +812 −96 · 5m
        ✎ risky — double-check the down-migration before merge
```

**No note set** — the row is byte-for-byte what it is today. Zero cost.

**Styling:**
- A single leading glyph marks the annotation. It **must be single-cell**
  (validate `runewidth.StringWidth == 1`); **no emoji** — wide/ZWJ glyphs desync
  Bubble Tea's incremental renderer and reintroduce the known row-ghosting bug.
  Add it to the theme's `Glyphs` (e.g. `Note: "✎"`) so it is themeable.
- The note text uses a muted accent color distinct from the branch color and the
  name color, optionally italic, so it reads unmistakably as "annotation" and is
  never confused with identity or git state.
- The note text is width-sanitized (`theme.SanitizeWidth` / `runewidth`) exactly
  as the display name is, and truncates with `…` if it ever exceeds its budget.

## Architecture

### Domain — `session/instance.go`

- Add an unexported `note string` field to `Instance`, guarded by the existing
  `mu` RWMutex like the other mutable cosmetic fields.
- `Note() string` and `SetNote(s string)` (trim whitespace, like
  `SetDisplayName`). `SetNote("")` clears it.
- `ToInstanceData` writes `Note`; `FromInstanceData` reads it. No status coupling
  — the field round-trips regardless of `Status`.

### Persistence — `session/storage.go`

- Add `Note string `json:"note,omitempty"`` to `InstanceData`, alongside `Model`.
- `omitempty` keeps existing state files clean and makes the change
  migration-free: absent → `""`. No version bump needed (numeric `Status` values
  and the additive field preserve forward/backward compatibility).

### Rendering — `ui/row.go` + `ui/list.go`

Builds on the existing segment model (`rowSeg`, `composeLine`, `nameSeg`,
`gitChips`, `changeSegs`, `ageSeg`).

- New producer `noteSeg(inst, theme, …) rowSeg` — the styled, glyph-prefixed,
  width-sanitized note as a **flex** segment. Returns the zero `rowSeg` (emits
  nothing) when `Note() == ""`.
- `InstanceRenderer.Render` gains a status-aware branch, entered only when the
  note is non-empty:
  - **Paused + note:** line 2 left becomes `[noteSeg]` (flex) in place of
    branch + git-context + PR; line 2 right keeps `ageSeg`. Reuses `composeLine`
    unchanged — it is just a different left segment list.
  - **Running (or any non-paused) + note:** lines 1 and 2 render exactly as today;
    a **third line** is appended — an indented, mostly-flex `composeLine` call
    with `[noteSeg]` left and an empty right group. This is the only place row
    height grows, and only for annotated active sessions.
  - **No note:** the existing two-line path is taken verbatim — no new
    allocation, no behavior change.

The width-totaling invariant (every emitted line measures exactly the panel
width via `runewidth`) and the background-baking invariant both carry over
because every new line goes through `composeLine`.

### Entry & editing — `ui/overlay/renameOverlay.go`, `app/app_update.go`

- Extend `RenameOverlay` to carry **two** `textinput.Model`s: the existing name
  field and a new note field (cap ~80 chars). `Tab` cycles focus between them
  (the overlay already handles `tab` for its mode toggle; extend that handling).
  Add an `initialFocus` parameter to the constructor: rename opens focused on the
  name, pause opens focused on the note.
- `IsSubmitted`/`Value` gain a note accessor (e.g. `NoteValue()`); on submit the
  handler applies `SetDisplayName` *and* `SetNote`, then persists via
  `UpdateInstance`. An empty note field clears the note.
- **Pause flow** (`KeyPause`, `app/app_update.go`): pause executes immediately and
  unchanged — the session is parked first, so instant-pause semantics are
  preserved. Then the rename overlay opens focused on the note field, pre-filled
  with the current note. `Esc`/empty-`Enter` dismisses with no note. This honors
  "pause *with* a note" without making pause modal-blocking.
- **Rename flow** is unchanged except the overlay now also shows/edits the note
  (focused on the name field).
- No new `app` state: note editing reuses `stateRename`. No new keybinding:
  entry is via pause (note-focused) and rename (name-focused).

### Why this shape

- **General field, status-aware surface** — the data model is decoupled from
  pause (clean invariants, editable any time), while display spends pixels only
  where there's room (paused line 2) or accepts one extra line only when an
  active session is genuinely annotated. This was chosen over (a) gating the
  field to `Paused` (forces resume→re-pause to edit) and (b) always rendering the
  note inline on line 2 (truncates to uselessness on busy running rows).
- **Editing in the rename overlay** — reuses the proven lightweight overlay and
  its state, avoids a second text-input component and a new `app` state, and
  keeps the two free-text labels (name, note) edited side by side where their
  distinct roles are obvious.

## Files

- **`session/instance.go`** — `note` field; `Note()`/`SetNote()`; round-trip in
  `ToInstanceData`/`FromInstanceData`.
- **`session/storage.go`** — `Note` field on `InstanceData` (`omitempty`).
- **`ui/row.go`** — `noteSeg` producer.
- **`ui/list.go`** — status-aware note rendering in `InstanceRenderer.Render`
  (paused line-2 substitution; non-paused third line; no-note path unchanged).
- **`ui/overlay/renameOverlay.go`** — second (note) input field, `initialFocus`
  param, `Tab` focus cycling, 80-char cap, note accessor.
- **`app/app_update.go`** — pause opens note-focused rename overlay after pausing;
  rename/submit persists `SetNote` alongside `SetDisplayName`.
- **theme `Glyphs`** — add a single-cell `Note` glyph.
- **Tests** (below) — `ui/row_test.go`, `ui/overlay/*_test.go`,
  `session/storage_test.go` (or instance round-trip test).

## Testing

- **Persistence round-trip:** an `Instance` with a note → `ToInstanceData` →
  JSON → `FromInstanceData` preserves the note; an empty note serializes to an
  absent key (`omitempty`); a legacy `state.json` with no `note` key loads as
  `""`. Hermetic (temp `HOME`, no real data dir).
- **Rendering:**
  - Paused + note: line 2 shows the glyph + note (left) and age (right); branch
    and git chips are absent; the line totals exactly the panel width.
  - Running + note: lines 1–2 unchanged; a third indented line carries the note;
    all three lines total the panel width.
  - Empty note: output is byte-for-byte identical to the current renderer
    (regression guard — assert against the existing expected strings).
  - Long note: truncates with `…` within its budget; width math holds.
  - Width safety: a note containing an emoji/ZWJ cluster is sanitized and does
    not overflow the line.
- **Overlay:** the note field accepts input and caps at 80 chars; `Tab` moves
  focus name↔note; submit returns both values; an emptied note field clears the
  note; `Esc` cancels without mutating either field. Constructor `initialFocus`
  places the cursor on the requested field.
- **Pause integration:** pausing sets `Paused` *and* opens the overlay focused on
  the note; dismissing with no input leaves the note empty and the session paused
  (pause is never rolled back by skipping the note).

## Verification

`just build` and `just test` both pass before the work is considered done, per
the project's correctness contract. `session/tmux` real-server tests self-skip
when `tmux` is absent, as usual.
