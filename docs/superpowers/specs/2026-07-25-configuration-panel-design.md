# Configuration panel redesign — a two-pane category browser

**Date:** 2026-07-25
**Status:** approved design, ready for an implementation plan
**Surface:** `ui/overlay/settings.go` (973 lines) + `app/app_layout.go`'s `applySettingChange`
**Related issues:** #374 (command palette), #376 (keybindings layer), #477 (per-repo
`carry_files`/`link_paths`), #454 (per-session model/effort), #370 (the UX Program epic —
which did not file this)

---

## 1. The problem, with evidence

The settings panel is schema-driven and correct, but it is **one column of 37 rows in
three sections — General 4, Appearance 9, Behavior 24**. "Behavior" is a junk drawer:
notifications, git/PR behavior, worktree seeding, the project scan, the autoyes daemon,
list ordering, updates, tmux, and safety toggles, in declaration order, with nothing to
tell them apart.

Every defect below was confirmed by driving the real binary under an isolated `HOME` +
`TMUX_TMPDIR` (see `atrium-tui-eyeball-isolation` for the recipe), not inferred from code.

| # | Defect | Evidence |
|---|---|---|
| D1 | Sections are unbalanced to the point of uselessness — 24 of 37 rows share one label | `newSettingRows` |
| D2 | No orientation: no scrollbar, no position, no sticky section header. Scrolled to the bottom, the word "Behavior" is off-screen — you cannot tell where you are or that more exists | 100×32 capture at the list tail |
| D3 | Linear nav only (`↑/↓`, `j/k`). Reaching the last row took **36 keypresses**. No PgUp/PgDn, Home/End, section jump, or search | `HandleKeyPress` |
| D4 | No search, although the repo already has a shared fuzzy `Picker` (`ui/overlay/picker.go` + `internal/fuzzy`) built for exactly this by #373 | — |
| D5 | **Help crowds out the list.** At 80×24, selecting *Account clustering* (443-char description) collapses the body to **8 visible rows while its help takes 8 lines**. Longer help is truncated with `…`, losing the content outright | 80×24 capture; `renderFooter`'s `maxDescLines` |
| D6 | No modified-vs-default signal and no reset. The `*bool`/`*int` tri-state fields *know* they are unset; nothing surfaces it | `settingRow` has no default/reset |
| D7 | Inert rows render as live. *Finished turns* is ignored while Notifications is off; *Notify command* only applies in `desktop` mode; *Agent OOM margin* is Linux-only; *Fast-forward local base* only matters with *Update base on create* on. Each caveat is buried at the end of a paragraph | row descriptions |
| D8 | Enum values are cycled blind — `‹ desktop ›` never shows the alternatives, and **every `←`/`→` press persists to disk and live-applies**, so discovering four options writes four of them | `cycleEnum` |
| D9 | `applyNote` is a free-text suffix appended after `·`. Its four values mix two concepts: three timings plus `modifies your local branch`, which is a caution, not a timing | `settingRow.applyNote` |
| D10 | `SelectRow(key)` — the deep-link hook — is exported and called **only by tests**. No notice or dialog jumps the user to the relevant setting | `grep SelectRow` |
| D11 | The configuration surface is fragmented with no cross-links: settings (`,`), accounts (`@`), profiles (JSON-only, **not editable in the TUI at all**), keybindings (planned #376), and `atrium doctor` — which holds exactly the context rows like `max_sessions` and `agent_oom_margin` need | — |
| D12 | The box is pinned at 64 cells wide, wasting a third of a 100-column terminal | `boxWidth` |

## 2. Goals / non-goals

**Goals.** Make the panel scannable (never present more than ~6 rows at once),
understandable (one-line summary always visible, full detail on demand, no truncation),
and honest (what you changed, when it takes effect, whether it currently does anything).
Close the profiles gap. Keep every existing capability.

**Non-goals.** Per-repo or per-session config (#477/#454 — we design the *seam* only, see
§5). Rewriting the accounts overlay — the Accounts category hands off to it. A
commit/cancel boundary — live-apply stays, because it is a feature (theme preview) and the
whole `applySettingChange` design rests on it. Category-level reset (`r` on the rail is a
silent no-op). Keybinding editing (#376).

## 3. Shape: a two-pane category browser

htop-style. Left rail of categories; right pane shows only the highlighted category's
rows. The rail *live-previews* — moving the rail cursor immediately re-renders the right
pane, so there is no hidden state and no drill-in feeling on a wide terminal.

```
╭ Settings ──────────────────────────────────────────────────────────────────╮
│  All settings        │  Notifications      ‹off› bell desktop osc     live  │
│  Sessions            │  Finished turns     ‹ same ›        needs Notif…     │
│  Worktrees & git     │  Notify command     (built-in)      needs desktop    │
│  Appearance          │• Notify when focused [ ] off                   live  │
│  Session list        │                                                      │
│  Notifications     ▸ │                                                      │
│  Automation          │                                                      │
│  Input               │                                                      │
│  Projects            │                                                      │
│  Updates             │                                                      │
│  Advanced            │                                                      │
│  Profiles            │                                                      │
│  Accounts          → │                                                      │
├──────────────────────┴──────────────────────────────────────────────────────┤
│  How Atrium signals a background session that finishes or blocks.           │
│  The selected, attached and muted sessions stay silent.               1/4   │
│  ⇥ pane · / search · r reset · ? more · ↵ edit · esc back                   │
╰─────────────────────────────────────────────────────────────────────────────╯
```

`•` = changed from default · `▸` = current category · `→` = hands off to another overlay

## 4. Taxonomy — 13 rail entries, all 37 keys placed exactly once

| Category | Rows | Keys |
|---|---|---|
| **All settings** | 37 | the flat list, preserved for auditing and muscle memory (not the default landing) |
| **Sessions** | 4 | `default_program`, `max_sessions`, `auto_attach`, `session_context_bar` |
| **Worktrees & git** | 6 | `branch_prefix`, `update_base_on_create`, `fast_forward_local_base`, `carry_files`, `link_paths`, `pr_create_draft` |
| **Appearance** | 5 | `theme`, `splash`, `glyph_set`, `hint_bar`, `os_chrome` |
| **Session list** | 5 | `session_sort`, `group_mode`, `model_indicator`, `effort_indicator`, `permission_indicator` |
| **Notifications** | 4 | `notifications`, `notifications_finished`, `notify_command`, `notify_when_focused` |
| **Automation** | 4 | `auto_yes`, `daemon_poll_interval`, `smart_dispatch_auto`, `trust_worktrees_root` |
| **Input** | 3 | `mouse`, `kill_double_tap_confirm`, `record_prompt_history` |
| **Projects** | 2 | `project_search_roots`, `project_search_depth` |
| **Updates** | 2 | `auto_update`, `show_release_notes_after_update` |
| **Advanced** | 2 + 1 | `tmux_config_override`, `agent_oom_margin`, + a read-only resolved-`config.json`-path row |
| **Profiles** | — | the new record editor (§9) |
| **Accounts** | — | `Enter` hands off to the existing `@` overlay |

4 + 6 + 5 + 5 + 4 + 4 + 3 + 2 + 2 + 2 = **37**. No key orphaned, none duplicated.

**All settings is a pseudo-category, not an assignment.** Each row still belongs to exactly
one real category (guard 1); All settings is a *view* that renders every row grouped under
category headers — the shape of today's list. It is the one rail entry whose rows pane
scrolls, and it windows around its cursor like today's body does.

Two naming decisions worth defending. **Automation** is "let Atrium act without asking
me" — it is what makes the old Behavior section cohere, absorbing the autoyes pair
(`auto_yes` + its poll interval), the auto-create toggle, and the pre-accepted trust
dialog. **All settings** exists so the redesign takes nothing away: the flat 37-row view
is where the modified-markers earn their keep for auditing a config.

### The rail-height invariant

The rail must fit **unscrolled at 80×24**, the project's degradation floor. Budget:
`24 − (border 2 + padding 2 + title 1 + blank 1 + separator 1 + help 3 + hint 1) = 13`
rows. 13 entries fit exactly. This is a tested invariant, not a coincidence: **a 14th
category has to earn its way in by merging two others.** If a future category is
unavoidable, the rail windows like today's body does — but the test should force that
decision to be deliberate.

## 5. Schema

`settingRow` gains eight fields. Existing fields (`key`, `label`, `kind`, `get`,
`editGet`, `set`, `options`) keep their *roles*; `section` becomes `category` and
`description`/`applyNote` are replaced.

Two things change that are easy to miss. **`kind` gains `kindReadOnly`** for the
config-path row: no `set`, no `reset`, and `Enter`/`Space`/`←`/`→` are no-ops on it.
And **six labels are rewritten** by §6 — `Max sessions`→`Session limit`,
`Session context bar`→`In-session status bar`, `Poll interval (ms)`→`Auto-yes poll
interval`, `Session sort`→`Sort within group`, plus minor rewording. Keys are stable;
labels are not, so any test that locates a row by rendered label needs updating (guard 13).

```go
type settingRow struct {
    key      string          // stable identifier applySettingChange switches on
    category settingCategory // enum, not a string — see the vocabulary below
    label    string
    kind     settingKind
    scope    settingScope    // scopeGlobal for all 37 today — the #477/#454 seam

    summary string   // one line, ≤ 74 cells, always visible
    detail  string   // optional; multi-paragraph, shown on `?`
    timing  applyTiming // when a change takes effect — a closed enum

    get     func(*config.Config) string
    editGet func(*config.Config) string
    set     func(*config.Config, string) error
    options func(*config.Config) []string
    gloss   map[string]string // enum only: one line per option

    // defaultDisplay returns the display string of the built-in default, or is nil
    // for rows whose default is machine-derived and therefore has nothing to diverge
    // from (see below). A row is "modified" iff get(cfg) != defaultDisplay().
    defaultDisplay func() string
    reset          func(*config.Config) // restore the built-in default
    activeWhen     func(*config.Config) bool // nil = always active
}
```

**`applyTiming` is a closed enum: `timingLive`, `timingNewSessions`, `timingRestart`.**
No empty value, exhaustively rendered. A restricted vocabulary makes the badge's
correctness structural rather than a matter of copy review (the lesson from #450). The
old fourth value, `modifies your local branch`, was never a timing — it moves to a
separate `settingRow.caution` field, which the footer renders after the summary and
before the timing note.

It cannot simply become a sentence in `detail`: PR A does not render `detail`, so
parking it there would delete a warning that #168 added deliberately, and it is the
only setting whose effect escapes the session worktree. `caution` keeps the enum about
scheduling while the warning stays on the surface PR A actually draws.
`fast_forward_local_base`'s `detail` still carries the fuller version for PR B.

Derived from `applySettingChange` and each field's point of use:

- **`timingNewSessions` (9):** `default_program`, `branch_prefix`, `carry_files`,
  `link_paths`, `session_context_bar`, `tmux_config_override`, `update_base_on_create`,
  `fast_forward_local_base`, `agent_oom_margin`
- **`timingRestart` (3):** `auto_update`, `trust_worktrees_root`, `daemon_poll_interval`
- **`timingLive` (25):** everything else

**`scope`** is `scopeGlobal` for all 37 rows today. The renderer and navigation must stay
scope-agnostic so a later per-repo layer (#477) adds an override column and a scope
switcher without reshaping the schema. Do not build the layer; do not special-case
`scopeGlobal` anywhere.

**`defaultDisplay` must stay pure — no exec, no filesystem.** `config.DefaultConfig()`
runs agent detection (four binary probes), so it must not be called on panel open or the
render path (#380's anti-jank concern). Each row returns its default from constants and
accessor defaults instead: `"plain"` for `glyph_set`,
`fmt.Sprintf("auto (%d)", config.DefaultSessionCap())` for `max_sessions`, and so on.

**Two rows declare `defaultDisplay: nil` and are never marked modified:**
`default_program` (defaults to the first *detected* agent profile) and `branch_prefix`
(defaults to `<os-username>/`). Their defaults are machine-derived, so there is no fixed
value to diverge from, and a marker there would be a lie. This is deliberate — do not
"fix" it by calling `DefaultConfig()`.

### `activeWhen` — the inert predicates

| Row | Active when | Reason chip when inert |
|---|---|---|
| `notifications_finished` | `GetNotifications() != off` | `needs Notifications` |
| `notify_when_focused` | `GetNotifications() != off` | `needs Notifications` |
| `notify_command` | `GetNotifications() == desktop` | `needs desktop mode` |
| `fast_forward_local_base` | `GetUpdateBaseOnCreate()` | `needs Update base on create` |
| `daemon_poll_interval` | `AutoYes` | `needs Auto-yes` |
| `agent_oom_margin` | `runtime.GOOS == "linux"` | `Linux only` |
| `group_mode` | **not an `activeWhen` predicate** — see below | `nothing to cluster` |

**An inert row is dimmed and carries a reason chip, but stays fully editable** — a user
may configure ahead of enabling the parent. Inert means "changing this has no effect right
now", never "you may not touch this".

**Every chip degrades rather than vanishing.** `needs Update base on create` is 27 cells and
the Worktrees pane leaves 24 at the 80-column floor, on a bool row with no enum alternatives
to shrink — so an inert chip falls back to a one-word `inactive` rather than being dropped. A
dimmed row with no marker at all reads as broken, and the help pane only describes the
*selected* row. Timing badges are dropped outright, per §10: they are reference information.

> **Resolved in PR B: `group_mode` has no `activeWhen` predicate.** The predicate this spec
> originally proposed, `len(cfg.ClaudeAccounts) >= 2` with the chip `needs 2+ accounts`, was
> derived from the row's own prose and is wrong in **both** directions. `ui.List`'s real gate is
> `AccountClusteringVisible() == accountGrouped() && distinctAccountCount() > 1`
> (`ui/list.go`), where `distinctAccountCount` counts distinct
> `session.Instance.AccountClusterKey()` values over the **live session list** — and that key is
> a session's *rotation pool* when it has one, else its account, else `""`. So:
>
> - Several configured accounts sharing one pool collapse to a single cluster key, so clustering
>   is a visual no-op while the config count says it is active. (Pinned by
>   `TestAccountClusteringVisible`'s pooled subtest.)
> - Sessions with no account attribution key on `""` and still form a second cluster, so
>   clustering can be visible with fewer than two accounts configured.
>
> A `settingRow` predicate only sees `*config.Config`, so the honest gate is not expressible in
> the schema. `group_mode.activeWhen` stays `nil`
> (`TestGroupModeHasNoConfigOnlyInertPredicate`), `ui.List.AccountClusteringVisible()` is the
> single definition of the gate — `ui/list_render.go` calls it rather than repeating it — and
> `home` injects its answer via `SettingsOverlay.SetAccountClusteringVisible`. Until it does,
> the panel shows **no chip at all**: a panel that cannot see the session list must not guess.
>
> The chip shows only when the setting is **on** and the list reports nothing to cluster. "Off"
> is not inert, it is off, and a chip there would be noise on a row doing exactly what it says.
>
> Note a *third* count exists and is not a substitute: `AccountReorderEnabled` counts *clusters*
> (repo-block anchors), because a repo whose sessions span accounts renders as one.

## 6. Copy — verbatim

Every summary is ≤ 74 cells — one unwrapped line at the 80-column floor **once §10 widens
the box** to `min(96, width−2)`: at width 80 that is a 78-cell box, inner 74. Today's
`boxWidth` is capped at 64 (inner 60), so under the PR A renderer a summary near the bound
wraps onto a second footer line. That is harmless — PR A keeps today's variable-height
footer — and it resolves when PR B lands the wider box together with the fixed-height help
pane. Summaries state what the setting *does*; `detail` carries the
value grammar, cautions, and cross-references. `gloss` explains enum options individually,
which is what dissolves the 300–443-char run-on descriptions.

### Sessions

| Row | Summary | Detail / gloss |
|---|---|---|
| Default program | `Agent command new sessions launch. A profile name, or a raw command.` | **detail:** A name matching a profile launches that profile's command; anything else is passed to the shell as written. Edit the profile list under Profiles. |
| Session limit | `How many sessions Atrium will hold. Empty auto-derives from this host.` | **detail:** Empty is a soft cap of half your CPU threads (minimum 2), counting only live sessions — a create or resume past it asks for confirmation rather than refusing. A number is a hard cap on every session, paused ones included, and a create past it is refused. 0 means unlimited, with no confirmation. `atrium doctor` reports the same host capacity. |
| Attach on create | `Drop straight into a new session's pane as soon as it starts.` | — |
| In-session status bar | `Thin tmux status line inside attached sessions.` | **detail:** Sessions already running keep the status line they started with; tmux only reads its config when a server starts. |

### Worktrees & git

| Row | Summary | Detail / gloss |
|---|---|---|
| Branch prefix | `Prefix for branches Atrium creates, e.g. zvi/ makes zvi/my-feature.` | — |
| Update base on create | `Branch new sessions off the freshest remote tip, not a stale local copy.` | — |
| Fast-forward local base | `Also advance your own local base branch to origin during create.` | **caution:** modifies your local branch<br>**detail:** This is the one setting here that writes outside a session worktree — it moves your local branch. Clean fast-forward only: a diverged local base is left alone. |
| Carry files | `Gitignored files copied into each new worktree.` | **detail:** Comma-separated repo-relative paths. Copies, so later edits in a worktree do not travel back. An empty list is an explicit opt-out, not a fall back to the default `.claude/settings.local.json`. |
| Link paths | `Gitignored paths symlinked into each new worktree, e.g. node_modules.` | **detail:** Comma-separated repo-relative paths. A symlink, not a copy, so every session shares one directory. Ignore the path with a pattern that has **no trailing slash** — with one, git does not treat the symlink as ignored and it lands in pause commits. |
| Create PRs as draft | `Open PRs as drafts. Turn off to merge them with m in-app.` | — |

### Appearance

| Row | Summary | Detail / gloss |
|---|---|---|
| Theme | `Colour palette and border style.` | — |
| Splash | `Animation behind the empty session list.` | **gloss:** `random` = a different pattern each launch. |
| Glyph set | `Icon fidelity. Drop a rung if you see boxes instead of icons.` | **gloss:** `nerd` = vendor Nerd-Font icons; needs a patched font. `plain` = Unicode that renders on any font (the default). `ascii` = a 7-bit floor for terminals that show boxes even on plain. |
| Hint bar | `Show key hints on the bottom row. Off leaves the row blank.` | **detail:** The row is reserved either way, so turning hints off does not resize the panes. |
| OS chrome | `Put fleet state in the window title and taskbar progress.` | **detail:** Sends OSC 9;4. Turn it off if your shell owns the terminal title. |

### Session list

| Row | Summary | Detail / gloss |
|---|---|---|
| Sort within group | `Row order inside each repo group.` | **gloss:** `creation` = the manual order you set with J/K. `status` = floats blocked and unread sessions to the top. **detail:** Group order stays manual either way (`{` / `}`). |
| Account clustering | `Add a top-level cluster per Claude account above the repo groups.` | **detail:** A divider and tinted headers per account. Manual reordering stays available: J/K within a repo group, `{` / `}` for groups inside one cluster (a move across an account boundary is refused), `[` / `]` for whole clusters. |
| Model chip | `Per-session model chip, shown whenever the model is known.` | — |
| Effort chip | `Per-session reasoning-effort chip; claude only.` | — |
| Permission chip | `Per-session permission-mode chip: plan, accept-edits, auto.` | — |

### Notifications

| Row | Summary | Detail / gloss |
|---|---|---|
| Notifications | `How Atrium signals a background session that finishes or blocks.` | **gloss:** `off`. `bell` = rings the terminal. `desktop` = runs a notifier. `osc` = an OSC 9 escape that reaches you over SSH with no local binary. **detail:** The selected, attached and muted sessions always stay silent, and so does a focused terminal unless Notify when focused is on. |
| Finished turns | `A quieter signal for a finished turn than for a blocked session.` | **gloss:** `same` = use the Notifications mode for both. `off` = leave it to the list's unread marker. `bell` = ring the terminal. **detail:** Only rungs quieter than Notifications are offered, so a finished turn can never out-shout a session blocked on you. |
| Notify command | `Shell command run for each desktop notification.` | **detail:** `$ATRIUM_SESSION`, `$ATRIUM_STATUS` and `$ATRIUM_EVENT` are in its environment. Empty uses a built-in per-OS notifier (notify-send, terminal-notifier, or osascript). |
| Notify when focused | `Keep notifying while Atrium's own terminal is focused.` | **detail:** Off stays silent while you are watching the fleet and notifies once you switch away. A terminal that never reports focus always notifies. |

### Automation

| Row | Summary | Detail / gloss |
|---|---|---|
| Auto-yes | `Auto-accept agent prompts. A daemon keeps doing it after you quit.` | — |
| Auto-yes poll interval | `How often the auto-yes daemon checks for prompts, in milliseconds.` | **detail:** At least 100ms — below that the daemon hammers tmux in a hot loop. Applies the next time the daemon starts. |
| Smart dispatch auto-create | `Let a confident i match create the session without opening the form.` | — |
| Trust worktrees root | `Pre-accept Claude's workspace-trust dialog for every session worktree.` | — |

### Input

| Row | Summary | Detail / gloss |
|---|---|---|
| Mouse | `Clickable rows, tabs and hint bar, wheel scroll, draggable divider.` | **detail:** Off hands the mouse back to the terminal so native select-to-copy works. While on, Shift+drag is the per-gesture escape. |
| Kill double-tap | `Let a second Ctrl+X confirm the kill dialog in one motion.` | — |
| Record prompt history | `Remember submitted prompts so ↑ in an empty prompt can reuse them.` | — |

### Projects

| Row | Summary | Detail / gloss |
|---|---|---|
| Project scan roots | `Directories the background scan walks to stock the project picker.` | **detail:** Comma-separated; `~` is allowed. A changed scope re-scans the next time the create form opens. |
| Project scan depth | `How many levels below each root the scan descends. 0 turns it off.` | **detail:** Empty uses the default of 3; the maximum is 8. |

### Updates

| Row | Summary | Detail / gloss |
|---|---|---|
| Auto-update | `What the startup update check does when a new version exists.` | **gloss:** `notify` = show a hint. `auto` = install in the background. `off` = no check. |
| Release notes after update | `Show a what's-new overlay once after updating.` | — |

### Advanced

| Row | Summary | Detail / gloss |
|---|---|---|
| Tmux config override | `Path to your own tmux config for session panes.` | **detail:** Empty uses Atrium's managed conf. Sessions already running keep the config their server started with. |
| Agent OOM margin | `Raise each agent above the tmux server in the kernel's OOM ranking.` | **detail:** Linux only. A kernel OOM kill then sheds one recoverable session instead of the shared server and every session with it. Empty is on at the default margin of 300, 0 is off, a number sets the margin. The kernel fixes `oom_score_adj` at exec, so an agent already running keeps its launched value until the session is relaunched. |
| Config file | *(read-only)* the resolved `config.json` path | **detail:** Atrium reads this file at launch and rewrites it whenever you change a setting here, so an edit made by hand while the TUI is running will be overwritten. |

## 7. Navigation grammar — and the one real collision

`←`/`→` must keep cycling values: that is today's grammar and what the hint line
advertises. So it **cannot** double as "switch pane".

| Focus | Key | Action |
|---|---|---|
| Rail | `↑`/`↓`, `j`/`k` | move category; the right pane re-renders immediately |
| Rail | `→`, `Tab`, `Enter` | focus the rows pane (on **Accounts**, hand off to the `@` overlay) |
| Rail | `Esc` | close the panel |
| Rows | `↑`/`↓`, `j`/`k` | move row |
| Rows | `←`/`→` | **always the value** — cycle an enum, never a pane switch |
| Rows | `Tab` / `Shift+Tab` | switch panes |
| Rows | `Esc` | back to the rail (a second `Esc` closes) |
| Rows | `Enter` | toggle a bool, cycle an enum, open the line editor for int/text |
| Rows | `Space` | toggle a bool |
| Either | `/` | search (§8) |
| Rows | `r` | reset the row to its default |
| Rows | `?` | expand `detail` into a scrollable full-height help view |
| Editing | `Enter` save · `Esc` cancel | unchanged from today |

This diverges from the accounts overlay, where `right` switches tabs and `Esc` closes
outright. The divergence is justified: accounts rows have no cyclable value and settings
rows do. The layered `Esc` is advertised in the hint line (`esc back`) so it is
discoverable rather than surprising.

Panel-internal keys are handled on `msg.String()` and are **not** `keys` registry
entries, so they do not incur the registry/help/README drift guards. Only `,`
(`KeySettings`) is a registry key and it is unchanged.

The rail remembers the last category **in memory on `home`** across opens within a run
(the overlay is reconstructed on every `,`). Persisting it to `state.json` is a deliberate
non-goal — a fresh launch starting at the top is fine.

## 8. Search

`/` opens a filter that flattens across categories, matching **label + key + summary +
category name** through `internal/fuzzy` via the shared `Picker` primitive — which is what
#373 built it for. Results render in the right pane as a flat list with each hit's
category shown on the row; the rail dims and shows a per-category match count.

Focus and dismissal, spelled out because these are the details that get guessed wrong:

- `/` works from either pane and **moves focus to the rows pane** (the results list).
- While the filter has focus, runes go to the filter — `j`/`k` type, they do not navigate.
  `↑`/`↓` still move the result cursor, as in the existing pickers.
- Editing a matched row works exactly as it does unfiltered, and the row stays in the
  result list afterwards.
- `Esc` clears the filter and keeps the rows pane focused; a second `Esc` backs out to the
  rail; a third closes. `?` opens the expanded help for the highlighted result.
- `?` is dismissed by `Esc` or a second `?`, returning to whatever was focused before.

## 9. Profiles editor

A new category, the first TUI editing of `profiles` (today JSON-only). Rows are the
`Profile{Name, Program}` records; the form follows `accountForm.go`.

- `n` new · `e`/`Enter` edit · `d` delete · `D` detect
- `D` reuses the already-exported `config.MergeDetectedProfiles(config.DetectAgentProfiles())`,
  so the TUI and `atrium profiles detect` cannot drift. Detection never modifies an
  existing profile.
- **One guard required:** `default_program` must not be left pointing at a deleted
  profile. Refuse the delete with a message naming the setting, or repoint it — the plan
  picks one and tests it.
- Live sessions are safe: `Instance.Program` (session/instance.go:144) stores its own
  resolved command string, so deleting a profile cannot break a running session.
- `default_program`'s enum options are derived from `Profiles`, so its row must re-read
  after any profile edit. Note the existing `rawDefaultProgram` capture in
  `newSettingRows` — a hand-edited raw command in `default_program` must stay a cycle
  option, and a profiles edit must not destroy it.

> **Resolved in PR D.** Six decisions this section left open:
>
> - **Profiles is a fourth `railKind` (`railProfiles`), not an 11th category.** A category means
>   `settingRow`s, which a list of records cannot be — and §4's rail budget has zero headroom at
>   13. The entry owns a focusable pane but no rows, which splits an invariant PR B could state
>   as one fact: `railHandoff` was *exactly* the no-rows case, so "→ rows" and "⇥ pane" shared a
>   discriminator. They no longer do. `→ rows` requires `settingRow`s; `⇥ pane` requires a pane
>   the forward key can focus, which the editor has.
> - **Deleting the profile `default_program` names is refused, not repointed.** The setting lives
>   in another category, so a silent repoint would change what every new session launches from a
>   pane that cannot show the change. **A rename does carry the setting along**, because the
>   record it names still exists and following it preserves exactly what launches; a delete has
>   no successor that preserves anything. The refusal's wording is **conditional**: with exactly
>   one profile, `default_program`'s enum has a single option and `cycleEnum` returns early with
>   no error, no chip and no reset — so "change it under Sessions first" would name an action the
>   panel makes impossible, and it says "add another with n first" instead.
> - **`d` asks `y / n` first**, diverging from this section's bare key list. Deleting a record is
>   the first irreversible action in the panel — `r` restores a default, an enum cycle is
>   reversible — and the sibling record editor over the same config file already confirms.
> - **Profiles are not searchable.** `/` matches label + key + summary + category over the
>   setting schema; a profile is data, not a setting. `/` from the editor opens the ordinary
>   filter and takes the rail with it, exactly as it does from a handoff entry.
> - **`D` runs detection as a `tea.Cmd`, not inline.** `config.DetectAgentProfiles` probes claude
>   through `config.GetClaudeCommand`, which spawns a login shell under a ten-second timeout, so
>   a synchronous call would freeze the update loop and every session's poll with it. The overlay
>   records a request and `home` fulfils it through the same `detectAgents` seam the startup
>   agent check uses; the merge stays `(*config.Config).MergeDetectedProfiles`.
> - **The merge happens in `home`, unconditionally, and the overlay only reports it.** The probe
>   outlives the keypress, so gating the merge on the panel still being open made one set of
>   keystrokes produce three outcomes — dropped, merged into a different overlay instance, or
>   merged with nothing on screen. `NoteProfilesDetected` returns whether the editor's pane
>   showed the outcome; when it did not, `home` surfaces it as a notice.
>
> One consequence worth recording so it is not later "fixed": after renaming or deleting the
> record whose name `rawDefaultProgram` captured, that captured string is prepended to
> `default_program`'s options as a *raw command*. It looks stale and is exactly what the capture
> promises — the config genuinely held it at panel open, and `GetProgram` passes an unmatched
> value to the shell. Narrowing the capture trades a cosmetic oddity for a real irrecoverable
> value.

## 10. Layout and degradation

- Box width becomes `min(96, width−2)` with today's floor, up from a fixed 64.
- Rail width = longest category label + marker + gap.
- Below a **derived** threshold (rail + minimum row width + separators, ≈72 cells) the
  panel falls back to **single-pane drill-in**: rail only, `Enter` opens the category's
  rows, `Esc` returns. Same mental model, one pane at a time. The threshold must be
  computed from the parts, not hardcoded as a magic number.
- The help pane is **fixed-height (3 lines) below the separator** and can never shrink the
  row list — the direct fix for D5.
- Rail height per §4's invariant; the rows pane windows around its cursor if a category
  ever outgrows the budget (All settings always does).

### Row line composition

A row line is five columns, in this order:

```
[selection 1][modified 1][label ...][value ...][timing-or-reason badge, right-aligned]
```

The selection mark and the modified marker are **separate single-cell columns** — a
selected row that is also modified shows both, so the modified marker must not reuse the
`SelectionMark` cell.

**Truncation priority, when the rows pane is narrow (two-pane at the threshold, where the
pane is ≈49 cells and the longest label is 26):** drop the badge first, then truncate the
value with a tail ellipsis, and **never truncate the label** — a half-written label makes
the row unidentifiable, while a truncated value is still recoverable from the help pane. A
truncated value must be shown in full in the help pane.

New glyphs (modified marker, rail caret, handoff arrow, scrollbar) each cost **four
sites**: the `Glyphs` struct, all three rungs in `ui/theme/registry.go`, the
`assertGlyphWidths` map in `theme_test.go`, and `TestGlyphsForFidelityRungs`. Every cell
glyph must be width 1.

## 11. Code structure

`settings.go` is already 973 lines and this roughly doubles the content, so it splits
along the repo's refactoring-program lines:

| File | Contents |
|---|---|
| `settings.go` | the type, constructor, exported API (`SetSize`, `HandleKeyPress`, `OpenAt`) |
| `settings_schema.go` | category enum + all 37 row declarations + copy (mostly data) |
| `settings_nav.go` | focus model, cursors, search, key grammar |
| `settings_render.go` | two-pane renderer, single-pane fallback, help pane, badges |

## 12. Deep links

Promote `SelectRow(key)` — exported, test-only today — into
`OpenAt(category, key)`, and wire two real call sites so the capability is proven rather
than speculative:

- the session-cap dialog → `max_sessions`
- the "manual reorder is off" notice → `session_sort`

> **Resolved in PR C: the signature is `OpenAt(key string) bool`.** `settingCategory` is
> unexported, so `app` cannot name one. More importantly, guard 1 pins that every key belongs
> to exactly one category, so a category parameter is a second source of truth whose only
> possible contribution is to disagree with the row's own — and the only sane resolution of a
> disagreement is to trust the row. `OpenAt` derives the category and syncs the rail to it,
> which is what `TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused` asserts over all 38 rows.
> It also clears the panel's transient state (an open editor, the `?` view, an active
> filter), because a deep link into an already-open panel must not land the cursor somewhere
> the user cannot see.
>
> **PR C found six `,`-teaching messages, not two.** Every one of them names the setting in
> its own copy, so leaving any of them landing on the remembered rail entry would have made
> two indistinguishable classes of `,`-notice. The full list:
>
> | # | site | triggered by | key |
> |---|------|--------------|-----|
> | 1 | the session-cap dialog (`overCapMessage`) | `ctrl+s` over the cap | `max_sessions` |
> | 2 | "session reorder is off while sorting by status" | `J` / `K` | `session_sort` |
> | 3 | "cluster reorder needs account grouping" | `[` / `]` | `group_mode` |
> | 4 | the setup-skipped welcome notice | skipping the welcome | `default_program` |
> | 5 | `warnMissingProgram`, the not-on-PATH branch | `programCheckedMsg` | `default_program` |
> | 6 | `warnMissingProgram`, the nothing-set branch | `programCheckedMsg` | `default_program` |
>
> Two things the bullets above got wrong. There are **two** reorder refusals, not one: `J`/`K`
> reorders within a repo group and `[`/`]` moves an account cluster, and they refuse for
> different reasons pointing at different keys. A count anchored on "the manual reorder notice"
> collapses them and silently drops `group_mode`. And (1) is not a notice at all — dialog copy
> reaches no notice path, so its `,` is armed by `pendingConfirmSettingKey` at the
> `confirmOverCap` site instead.
>
> That leaves five notices (2–6) routed through `settingNotice`, across four call sites —
> `warnMissingProgram` builds one call's text in two branches.
> `TestEveryCommaNoticeGoesThroughSettingNotice` keeps the rule structural rather than
> counted: it is an AST scan for `,`-advertising literals reaching `flashNotice` or
> `handleInfoNotice`, so a seventh message added later fails it without this table being
> updated.

The cap dialog carries a second obligation: `overCapMessage` currently ends with
`(set max_sessions in config.json to change this)`, which sends the user to a text editor
for a setting the panel now owns. With the deep link in place that literal should teach the
key instead. Both branches of `overCapMessage` (single and batch) carry it, and the dialog
wraps at 46 cells — price the replacement in *rendered lines* before choosing the wording.

Further call sites (notification notices, OOM warnings) are follow-up work, not this spec.

## 13. Acceptance criteria / test invariants

Structural guards — these *are* the ACs:

1. **Every scalar `Config` field has exactly one row in exactly one category.** The panel
   twin of the existing `config.TestReadmeDocumentsEveryConfigField`. Exempt: the four
   list-of-record keys (`profiles`, `claude_accounts`, `gh_accounts`, `agy_accounts`) and
   the deprecated `nerd_font`.
2. **On a fresh `config.DefaultConfig()`, no row renders a modified marker.** Cheap, and it
   catches every wrong `defaultDisplay`. The two `nil`-default rows are exempt by
   construction.
3. Every row has a non-empty `summary` of **≤ 74 cells**, and a `timing` from the closed
   enum.
4. **The rail fits unscrolled at 80×24** (§4's invariant).
5. **Selecting the longest-help row does not reduce the visible row count** — a direct
   regression test for D5, the defect that motivated the redesign.
6. No horizontal overflow at 80 or 100 columns. **Measure unstyled content width:** a
   bordered lipgloss block pads every line to the same width, so a post-render width
   assert is a tautology that can never fail (see `atrium-accounts-reorder-grouping`).
7. Inert transitions: Notifications `off` ⇒ Finished-turns inert and dimmed; set `desktop`
   ⇒ Notify-command active. An inert row is still editable.
8. `r` restores the default **and** reports the changed key, so `applySettingChange`
   persists and live-applies it exactly like an edit.
9. Search finds a row by key, by label, and by a word from its summary; `Esc` clears the
   filter without closing.
10. Single-pane fallback engages below the derived threshold and both panes are reachable.
11. `OpenAt(category, key)` lands the cursor on that row with the rows pane focused.
12. Profiles: new/edit/delete/detect round-trip through `config.SaveConfig`; deleting the
    profile named by `default_program` is guarded; a raw `default_program` command survives
    a profiles edit.
13. Every existing behavior test in `ui/overlay/settings_test.go` (930 lines) and
    `app/settings_test.go` still passes, adapted only where a selector changed. **`SelectRow`
    is used by ~5 existing tests** — keep it or migrate all call sites deliberately.

Tests must stay hermetic (`HOME` to a temp dir) per CLAUDE.md.

## 14. Staging

| PR | Contents | Risk |
|---|---|---|
| **A** | Schema fields + the §4 taxonomy + the §6 copy verbatim, rendered by the **existing single-column renderer** (10 sections instead of 3) + README reference gains a category column. Guards 1–3. | Low — no layout change |
| **B** | Two-pane renderer, nav grammar, degradation, orientation, and the full visibility layer (modified marker, timing badge, inert dimming, inline enum alternatives, `?` help). Guards 4–7, 10. | Medium |
| **C** | Search + `r` reset + `OpenAt` deep links + the Accounts handoff row. Guards 8, 9, 11. | Low |
| **D** | Profiles editor. Guard 12. | Medium |

PR A is deliberately shaped to land the highest-value change — a coherent taxonomy and
comprehensible copy — with almost no rendering risk. B is where the layout work
concentrates; the visibility layer ships with it because a two-pane renderer without
modified/inert/timing signals would be a regression in honesty.

## 15. Risks

- **Copy accuracy.** The §6 table asserts behavior in 37 places. Three planned literals in
  #399 turned out false because they were written before reading the code. The copy here
  was written against `settings.go`, `config/accessors.go` and the README reference table,
  but the implementer should re-verify each claim against the code it describes rather than
  trusting this document.
- **The `group_mode` inert predicate** is the one predicate derived from prose rather than
  from code — see the callout in §5.
- **Test churn.** ~100 tests touch this surface. Stage A keeps the renderer, so most churn
  lands in B, where selectors change.
- **`←`/`→` muscle memory** is preserved by design, but `Esc` gains a level. The hint line
  must say `esc back` in the rows pane and `esc close` on the rail — differing hints per
  focus, not one static string.
