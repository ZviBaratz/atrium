# Atrium [![Website](https://img.shields.io/badge/website-zvibaratz.github.io%2Fatrium-2ea44f)](https://zvibaratz.github.io/atrium/) [![CI](https://github.com/ZviBaratz/atrium/actions/workflows/build.yml/badge.svg)](https://github.com/ZviBaratz/atrium/actions/workflows/build.yml) [![GitHub Release](https://img.shields.io/github/v/release/ZviBaratz/atrium)](https://github.com/ZviBaratz/atrium/releases/latest) [![Go Report Card](https://goreportcard.com/badge/github.com/ZviBaratz/atrium)](https://goreportcard.com/report/github.com/ZviBaratz/atrium) [![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE.md) [![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/ZviBaratz/atrium/badge)](https://securityscorecards.dev/viewer/?uri=github.com/ZviBaratz/atrium)

Atrium is a terminal command center for orchestrating multiple AI coding agents — [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Antigravity](https://antigravity.google/docs/cli/reference), [Gemini](https://github.com/google-gemini/gemini-cli), and other local agents including [Aider](https://github.com/Aider-AI/aider) — each in its own isolated git worktree, so you can drive several tasks at once from a single panel.

![Atrium Screenshot](assets/screenshot.png)

### Highlights
- Complete tasks in the background (including yolo / auto-accept mode!)
- Manage instances and tasks in one terminal window
- Review changes before applying them, pause sessions to pick their branches up elsewhere
- Each task gets its own isolated git workspace, so no conflicts

### Demos

Per-flow screencasts — create→attach→detach, diff review, pause/resume — are
generated from committed [vhs](https://github.com/charmbracelet/vhs) tapes in
[`docs/demos/`](docs/demos/). Render them with `just gifs`; they are deliberately
slow-paced so each step is followable.

<br />

### Installation

Atrium installs as `atrium` on your system. The installer also sets up `atr` as a short alias.

#### Quick install (curl)

```bash
curl -fsSL https://raw.githubusercontent.com/ZviBaratz/atrium/main/install.sh | bash
```

This puts the `atrium` binary in `~/.local/bin`. To use a custom name for the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/ZviBaratz/atrium/main/install.sh | bash -s -- --name <your-binary-name>
```

#### go install

Requires Go 1.25 or newer (older toolchains fetch it automatically unless
`GOTOOLCHAIN=local` is set):

```bash
go install github.com/ZviBaratz/atrium@latest
```

#### Updating

```bash
atrium update          # download, verify, and install the latest release
atrium update --check  # just see whether one exists
```

Atrium also checks for new releases when it starts (cached: the network is
hit at most once a day, and at most once an hour after a failed check) and
shows a hint when one is available. The running app and your sessions are
never touched — an installed update takes effect the next time you start
`atrium`. Set `"auto_update": "auto"` in `config.json` to install updates
automatically in the background (auto mode may also check at startup while a
found update is still pending install), or `"off"` to disable the startup
check.
Source builds that are not at an exact release tag (`go install`, dev
checkouts) report a dev version and never self-update.

### Prerequisites

- [tmux](https://github.com/tmux/tmux/wiki/Installing) 3.2 or newer — Atrium starts
  every session with `tmux new-session -e`, which older versions reject
- [git](https://git-scm.com/downloads)
- [gh](https://cli.github.com/) — optional, for pushing branches and opening PRs

Run `atrium doctor` to check them.

### Usage

Run the application with:

```bash
atrium
```

NOTE: The default program is `claude` and we recommend using the latest version.

Atrium also ships a set of subcommands. `atrium <command> --help` prints the full
detail for any of them; a test (`TestReadmeDocumentsEveryCommand`) fails the build
if one is missing from this table.

| Command | What it does |
|---------|--------------|
| *(none)* | Start the TUI |
| `ls` | List sessions, as a table or as JSON for scripts |
| `peek` | Print what a session's pane is showing, without attaching |
| `send` | Queue a prompt for a session |
| `new` | Create one or more sessions without a TUI |
| `guide` | Print what an agent running inside a session can do |
| `doctor` | Check core dependencies (tmux, git, gh) and agent CLI heuristic versions |
| `reap` | List tmux servers Atrium left behind, and stop them on request |
| `profiles` | Manage agent profiles (e.g. `profiles detect`) |
| `debug` | Print debug information like config and log paths |
| `update` | Download, verify, and install the latest release |
| `reset` | Reset all stored instances |
| `version` | Print the version number of atrium |
| `completion` | Generate the autocompletion script for the specified shell |
| `help` | Help about any command |

Global flags:

| Flag | Effect |
|------|--------|
| `-p, --program` | Program to run in new instances (e.g. `aider --model ollama_chat/gemma3:1b`) |
| `-y, --autoyes` | [experimental] Automatically accept prompts |
| `-v, --verbose` | Print the log file path on exit |

<br />

<b>Using Atrium with other AI assistants:</b>
- For [Codex](https://github.com/openai/codex): Set your API key with `export OPENAI_API_KEY=<your_key>`
- Launch with specific assistants:
   - Codex: `atrium -p "codex"`
   - Aider: `atrium -p "aider ..."`
   - Gemini: `atrium -p "gemini"`
   - Antigravity: `atrium -p "agy"`
- Make this the default, by modifying the config file (locate with `atrium debug`)

<br />

#### Scripting Atrium

`ls`, `peek`, `send` and `new` are the headless surface: four primitives — list
the fleet, read a screen, send a message, create a session — that let a script or
an orchestrator agent drive Atrium without a TTY. None of them start the TUI, hold
its lock, or need a terminal.

**`atrium ls`** prints a table; `--json` emits an array for `jq`. It reads stored
state and never touches tmux, so it works with no tmux server running at all.

```bash
atrium ls
atrium ls --json | jq -r '.[] | select(.status == "needs-input") | .title'
```

Status, diff figures and queue depth are **last-known** values recorded by the
running TUI, not live probes. Each entry carries `updated_at` so a consumer can
judge how stale they are; with no Atrium running they stop advancing. A running
TUI records every status change as it happens, so `status` is current to within
seconds; diff figures refresh on a slower sweep.

To ask how long a session has held its status, subtract `status_changed_at` — not
`updated_at`, which is one shared instant dating the whole snapshot, nor
`created_at`, which is the age of the worktree.

| Field | Notes |
|-------|-------|
| `title` | The session's identity, and what `peek`/`send` accept |
| `display_name` | Cosmetic label; falls back to `title` |
| `path`, `worktree`, `branch` | Repo path, isolated worktree, branch |
| `status` | `running`, `ready`, `loading`, `paused`, `needs-input`, `pending` |
| `program`, `model`, `permission_mode`, `effort`, `account` | What is running, and how |
| `tmux_name` | The tmux session name, for scripts that want tmux directly |
| `queued_prompts` | Prompts waiting to be delivered |
| `auto_yes`, `direct`, `unread`, `muted`, `note` | Session flags and annotation |
| `isolated` | Created dependency-isolated, so it got none of the [`link_paths`](#linked-paths) symlinks. Records the choice made at creation, not what `link_paths` says now |
| `created_at`, `updated_at` | RFC 3339, or `null` when unrecorded |
| `status_changed_at` | When `status` last changed, RFC 3339; `null` for a session not yet observed by a build that records it |
| `diff` | `added`, `removed`, `files_changed`, `commits`, `behind`, `dirty`, and `unpushed` (`null` when not yet computed) |

The schema evolves additively: fields may be added, never removed or repurposed.

**`atrium peek`** captures a session's pane. It is read-only — it never attaches,
sends keys, or otherwise disturbs the session — but it does need a live tmux
server, so a paused session cannot be peeked.

```bash
atrium peek fix-auth              # the visible pane, as plain text
atrium peek fix-auth --lines 200  # the last 200 lines, reaching into scrollback
atrium peek fix-auth --color      # keep ANSI colors
```

**`atrium send`** queues a prompt. Delivery matches the TUI's own quick-send
(`s`): the prompt reaches the agent strictly when it next goes idle, and is never
injected mid-turn.

```bash
atrium send fix-auth "rebase onto main and re-run the tests"
git log --oneline -5 | atrium send fix-auth -    # prompt from stdin
atrium send fix-auth "ship it" --wait 30s        # block until it is queued
```

Delivery is **asynchronous**. `send` writes the message to a spool directory
(`outbox/` in the data dir) and the running Atrium picks it up within about a
second. It deliberately never writes `state.json`: that file has exactly one
writer at any instant — the TUI, which holds `tui.lock` for its whole life, or
the autoyes daemon in the window where no TUI is alive — and both rewrite it
whole, so an outside append would be clobbered rather than merged.

This makes `send` durable rather than conditional. With no Atrium running it
warns on stderr and still exits 0: the message stays queued and is delivered the
next time one starts. Pass `--wait` to block until it has actually been queued,
and fail if it has not — including when Atrium picked the message up but could
not deliver it, for instance because the session was killed in the meantime.

Undelivered messages expire after 24 hours, on the grounds that a day-old prompt
describes a tree that has moved on. Sending to a paused session is allowed — the
queue is persisted, so the prompt waits for the resume.

**`atrium new`** creates a session — worktree, branch and agent — from a script,
a CI job, or an agent working through a queue of issues, and with `--variants`
creates several of them from one prompt.

```bash
atrium new fix-auth                              # in the current repo
atrium new fix-auth "start on the parser"        # with a first prompt
atrium new fix-auth --path ~/src/web --profile codex
atrium new fix-auth --branch release/2.0         # base it on an existing branch
atrium new fix-auth --program codex              # one agent, without a profile
atrium new fix-auth --wait 60s                   # block, then print the branch
atrium new bake "fix the parser" --variants claude:2,codex:1   # a bake-off
```

`--program`, `--profile` and `--variants` all name what to run, so exactly one of
them may be passed; with none, the session runs whatever the TUI's own
new-session key would run. `--branch` chooses the *start point* only: the session
still gets its own branch, derived from the title like any other.

It is a producer on the same terms as `send`, and for the same reason: the TUI is
the only thing that creates sessions, so `new` spools a request to
`outbox/create/` and the running Atrium executes it through the same core the
new-session key reaches — the creation itself, minus the parts that belong to
someone at the keyboard, which is why a background create moves no cursor and
jumps nothing to the head of the recent-paths list. Everything that core enforces
still applies — the session cap, the tmux version floor, and above all the title
check.

Queued create requests expire on the same 24-hour horizon as queued prompts, and
for the same reason: a day-old request names a branch point the tree has moved on
from. An expired one is discarded with a rejection receipt rather than built, so a
`--wait` blocked on it is told what happened.

That check matters more from a script than from the keyboard. A session's branch
and tmux names derive from its title, so choosing a title is choosing a branch; a
title whose derived names are already taken is **refused**, never silently
suffixed, because a caller that asked for `fix-auth` and got `fix-auth-2` would
push to a branch it never named. Run inside a session's worktree — where an agent
usually is — `new` targets that worktree's *repo*, and says so on stderr.

`--variants` is the one exception, and it is an exception because it is asked for.
It is the headless half of the create form's fan-out: `--variants claude:2,codex:1`
requests three sessions sharing one prompt, one base branch and one repo, which is
the bake-off primitive — race three claudes, or claude against codex, then keep the
best diff. N sessions cannot share one branch, so the title becomes a *stem* and the
variants are named `bake-1`, `bake-2`, `bake-3`, skipping any name a session or a
local branch in the target repo already owns. Nothing is silent about it: each
derived title is printed as it is queued, and `--wait` names the branch each one was
given. Asking which names are taken means asking a repository, so a fan-out of two
or more needs a git target; a fan-out of one derives nothing, keeps the bare title,
and is exactly `--profile claude`.

Each variant is spooled as an ordinary request carrying its own final title and
program, sharing one batch id — so every gate, receipt and recovery path treats it
as the single create it is, and only the session cap reads the batch as a batch. The
cap is charged for the whole batch: it fits, or it is refused whole, each member
answered with its own receipt, rather than creating variants until the cap closes.
The charge is live, so if something else takes the room after part of a batch is
already built — somebody at the keyboard, another script — the rest is refused
together rather than trickling in, and each receipt counts what was still queued
rather than what was asked for. One member is left out of both the count and the
refusal: a variant Atrium is recovering from an interrupted build, which is answered
at its own gate instead. Two things a fan-out cannot promise, both because the drain
builds one spooled session at a time:
a `--wait` has to be sized for every build in series, and with no `--branch` each
variant starts from the target's HEAD at *its own* creation time — so pass
`--branch` when the comparison depends on a common start point.

Two of the TUI's gates ask the user a question rather than refusing: crossing the
host-derived session cap, and routing to a fully rate-limited account pool. A
headless request has nobody to ask, so it is refused with the reason; `--force`
answers both in advance. An explicit `max_sessions` is not one of them — it
refuses in the TUI too, and `--force` does not reach it.

`--wait` blocks until the session actually exists and then prints its branch and
worktree, read back from what Atrium recorded rather than derived from the title.
Without it the command is honestly fire-and-forget: it prints what it queued, and
`atrium ls` shows the session once it exists. That is not the same as watching for the
*result* — a request the drain refuses leaves no session and no row, and the refusal is
written as a receipt that only `--wait` reads.

Both spools are drained by the TUI's poll loop, which is **suspended while you
are attached to a session** — Atrium has handed the terminal to tmux and its
event loop is parked until you detach. Nothing is lost, and the wait is bounded by
that one attach rather than by a relaunch: the drain runs on the first poll tick
after the detach. But "within about a second" assumes a TUI watching the list
rather than one sitting inside a pane, and that is the common case for the workflow
`new` exists for, since an agent handing off runs *inside* a session.

So it is reported rather than left to be inferred. `send` and `new` warn on stderr
when they spool into a parked Atrium, naming what holds the terminal and what will
release it — a session to detach from, or a terminal-mode [custom
command](#custom-commands) to let finish — and `--wait` says the same at its
deadline instead of listing every reason it might not have landed. One gap remains,
deliberately: the warning goes to the pane of the session that ran the command, so
an agent in session B handing off while you watch session A prints where nobody is
looking.

None of the four holds Atrium's lock or writes `state.json`, so running them on a
loop alongside a live Atrium is safe. `ls` and `peek` only read it; `send` and `new`
add one file each — the request they spool — and then read `tui.lock` and
`handover.lock` to work out what to warn you about: whether a TUI is there at all,
and whether the one that is has its terminal handed to a session. Both probes ask
for a *shared* lock, briefly and without blocking, and neither creates the file it
reads — so a held lock changes the warning rather than the outcome, and two of these
running at once cannot mistake each other for Atrium. All four append to the shared,
rotating `atrium.log` in the data directory.

A queued request is state, so `atrium reset` discards both spools along with
everything else it wipes. Without that, a create request made before the reset
would still be there afterwards, and — with no session left for its title to
collide with — the next Atrium would build it. Each discarded request leaves the
same rejection receipt any other refusal would, so a `--wait` blocked on one is
told the reset took it rather than reading the file's disappearance as success.

If a title exists in more than one repo, `peek` and `send` will report the
ambiguity and list the candidates; `--path <repo>` picks one. (`ls` takes no title
at all — it lists every session, so it has nothing to disambiguate. `new` takes
`--path`, but to choose where the session is created, not to disambiguate.)

All four exit 0 on success and 1 on failure, with the reason on stderr.

**`atrium guide`** is the fifth, and the only one written for a reader rather than a
script: it prints what an agent running *inside* a session can do — the four commands
above, the rule that Atrium owns the worktree and reclaims it, how to hand off to the
next session, and which commands belong to the person at the keyboard rather than to
the agent. It is static text, takes no locks and reads no state.

It exists because the surface above was undiscoverable to the agents it was built for.
The `SessionStart` brief Atrium injects into a session (`sessionBriefTemplate`) spends
one clause naming `guide`, and the page carries everything that clause has no room for
— the brief is re-delivered on every session start, every `/clear` and every
compaction, so it is the wrong place to put a manual. Where a fact already has an owner
the page names the owner instead of restating it: `atrium new --help` is what answers
when a queued create actually lands.

That pointer reaches **claude sessions only**, and by more than one gate. `ensureHookSettings`
injects the settings file solely for an agent whose adapter declares hook support, which claude
alone does; it also skips injection when the claude binary's `--help` does not advertise
`--settings`, and the `SessionStart` entry is added only for a session with a worktree and a
branch — so a *direct* (non-git) session gets no brief either, including the paragraph on this
page written for it. Any of them can run `atrium guide` perfectly well; they are simply never
told to. [#773](https://github.com/ZviBaratz/atrium/issues/773) tracks closing the adapter gap.

The page also spells the binary `atrium` throughout, rather than the name it was installed
under (`install.sh --name`). [#775](https://github.com/ZviBaratz/atrium/issues/775) tracks that.

<br />

#### Keybindings

Press `?` in the app for the same cheatsheet, live. This table mirrors it group
for group; a test (`keys.TestReadmeDocumentsEveryBinding`) fails the build if the
in-app keymap and this section ever drift apart, so it stays complete.

> **Modern terminals report more keys than they used to.** Atrium asks the
> terminal for *key disambiguation* (the Kitty keyboard protocol, plus xterm's
> `modifyOtherKeys`), which terminals that support it — Ghostty, Kitty, WezTerm,
> Alacritty, foot, iTerm2, Rio, Contour — use to distinguish combinations that
> older terminals collapsed onto the same control code: <kbd>ctrl+m</kbd> from
> <kbd>enter</kbd>, <kbd>ctrl+i</kbd> from <kbd>tab</kbd>, <kbd>ctrl+h</kbd> from
> <kbd>backspace</kbd>, and <kbd>ctrl+[</kbd> from <kbd>esc</kbd>. The most
> visible effect is a good one: <kbd>esc</kbd> takes effect immediately, with no
> wait to see whether an escape sequence follows. No binding below relies on the
> old conflation, so nothing here changes; a terminal without the protocol simply
> never enables it and behaves exactly as before.
>
> The one thing it *adds* is in the composers: on such a terminal
> <kbd>shift+enter</kbd> inserts a newline in the new-session prompt and the
> quick-send box, with no terminal reconfiguration, while <kbd>enter</kbd> keeps
> sending or advancing. <kbd>ctrl+j</kbd> does the same everywhere and always
> will, and <kbd>alt+enter</kbd> — what a Claude-Code-style `/terminal-setup`
> remap makes <kbd>shift+enter</kbd> send — still works too. A footer names
> <kbd>⇧↵</kbd> only once the terminal has actually answered the capability
> query, so it errs towards <kbd>⌃J</kbd>: under tmux, or on an xterm that speaks
> only `modifyOtherKeys`, <kbd>shift+enter</kbd> may work while the hint stays
> quiet about it. The new-session form's footer also drops the clause to fit a
> narrow terminal, so the quick-send box is the one to read the answer off.
> `atrium doctor` reports what can be told from outside the app.

##### Navigate
| Key | Action |
|-----|--------|
| `↑/k` `↓/j` | move selection |
| `u` / `b` | jump to next unread / blocked session |
| `tab` / `shift-tab` | next / prev pane |
| `1` / `2` / `3` | jump to preview / diff / terminal |
| `shift-↑` `shift-↓` | scroll the active pane |
| `<` / `>` | shrink / grow the session list (or drag the divider) |
| `\` | cycle layout presets (monitor / default / review / focus) |
| `esc` | exit scroll mode / clear filter |

##### Manage
| Key | Action |
|-----|--------|
| `n` | new session (form, name first) |
| `N` | new session (form, project first) |
| `i` | smart new (describe it; auto-routes to a project) |
| `R` | rename session (label only) |
| `A` | auto-name session (via its agent) |
| `M` | mute / unmute this session's notifications (see [Notifications](#notifications)) |
| `/` | filter sessions (see [Filtering](#filtering)) |
| `v` | multi-select: `space` marks, `p`/`r`/`x` act on the marked set |

##### Handoff
| Key | Action |
|-----|--------|
| `↵/o` | attach to the selected session |
| `ctrl-q` | toggle attach/detach (detach when in, attach from the list) |
| `ctrl-x` | kill the selected/attached session (press it twice to confirm — every keyed confirmation takes its own key twice) |
| `U` | undo the last kill: rebuild its branch, worktree and agent (see [Undoing a kill](#undoing-a-kill)) |
| `ctrl-pgup/pgdn` | in a session: cycle to prev / next session in the repo group |
| `shift-pgup/pgdn` | in a session: scroll the agent's scrollback (see [Scrolling an attached session](#scrolling-an-attached-session)) |
| `s` | send a message (without attaching) |
| `C` | diff tab: comment on a line or range → queue it to the agent (↑↓/j/k move, shift+↑↓/J/K extend, enter comment, esc exit) |
| `Q` | manage queued prompts (list / cancel) |
| `H` | claude sessions: list the checkpoints it took before each prompt, then attach to rewind one (`Esc Esc`) — see [Checkpoints](#checkpoints) |
| `a` | approve the agent's prompt (`↵` picks its default); on idle claude, accept the suggested prompt |
| `d` | start / stop the repo's `run_command` (dev server) on this session's port (see [Run commands](#run-commands)) |
| `p` | pause: stop the agent, commit changes, free the worktree |
| `ctrl-p` | pause all active sessions in the current view |
| `P` | commit & push branch |
| `c` | create a PR for the pushed branch (gh) |
| `m` | merge the session's PR (squash) |
| `w` | open the session's PR in the browser |
| `r` | resume a paused session |
| `ctrl-r` | resume all paused sessions in the current view |
| `y` | copy branch name to clipboard (works over SSH — see [Clipboard](#clipboard)) |
| `Y` | copy the active tab's content — the raw diff, or the pane with styling stripped |
| `f` | copy/open URLs & paths from the preview |

##### Groups
| Key | Action |
|-----|--------|
| `J` / `K` | reorder within a repo group |
| `{` / `}` | move a whole group up / down |
| `[` / `]` | move an account cluster up / down |
| `←` / `→` | collapse / expand group |
| `Z` | collapse / expand all |

##### Other
| Key | Action |
|-----|--------|
| `?` | toggle this cheatsheet |
| `ctrl-k` | command palette — find any action by name and run it |
| `!` | custom commands — your own verbs over the selected session ([Custom commands](#custom-commands)) |
| `,` | settings |
| `@` | accounts (Claude / GitHub / Antigravity) |
| `L` | command log — the tmux / git / gh commands Atrium runs |
| `ctrl-l` | force a full redraw of the screen |
| `q` | quit |

#### Remapping keys

Atrium's keys are not entirely its own. It runs inside and around tmux, so every
chord it takes is one your terminal, your multiplexer or your shell might already
want — and two of them are read as raw bytes while you are attached to an agent:
`ctrl-q` (which is XOFF unless you have run `stty -ixon`) and `ctrl-x` (an
ordinary editing key in any shell). The `keybindings` section in `config.json` is
the way out:

```json
{
  "keybindings": {
    "attach_toggle": "ctrl+g",
    "up": ["up", "w"],
    "pause_all": "disabled"
  }
}
```

A value is one key, a list of keys, or `"disabled"` to unbind the action
entirely. An unbound action leaves the hint bar; it keeps its `?` cheatsheet row,
with the key column blank, so you can still see the action exists. Either way it
stays runnable from the command palette (`ctrl-k`), which is what makes unbinding
safe. `attach_toggle` is the one action that cannot be unbound — it is the only
way out of an attached pane other than tmux's own prefix — and it and `kill` take
a single `ctrl+<letter>` key rather than a list, because a session pane reads them
as one raw byte.

Some keys are worth avoiding: `ctrl+m`, `ctrl+i`, `ctrl+j` and `ctrl+h` are the
ones the disambiguation note above lists as historically conflated with `enter`,
`tab`, a newline and `backspace`, and `shift+enter`, `ctrl+enter` and
`ctrl+shift+enter` are `enter` on the same terminals. Atrium can tell them apart only on one that speaks the
protocol; on any other the chord never arrives and the action is simply dead, so
an override onto one is applied with a warning. For `attach_toggle` and `kill` it
is refused outright — a session pane reads raw bytes and cannot distinguish them
at all. The warning covers the keys Atrium has had reason to name, so treat its
silence as "not on that list" rather than as a guarantee: `alt+enter` and
`shift+tab` do have their own legacy encodings, but plenty of other chords do not.

Every surface — hint bar, cheatsheet, palette, and any message that names a key —
is generated from the keymap, so a remap shows up everywhere at once. No
`keybindings` section means today's keys, unchanged.

Keys are spelled the way a terminal reports them: `+` joins a chord (`ctrl+g`,
`alt+enter`, `shift+tab`), a shifted letter is just the capital (`K`, not
`shift+k`), and the space bar is `space`. Note that the cheatsheet *displays* a
chord with a hyphen (`ctrl-x`) but a binding is written with a plus (`ctrl+x`).
An override replaces an action's keys rather than adding to them, so rebinding
`up` to `w` drops the arrow too unless you list both.

A mistake is reported, never fatal: the offending line is skipped, that action
keeps its default key, every other override still applies, and the reason is
shown at startup and by `atrium doctor`. Names both sides where it can — a
collision with a key another action still holds tells you to rebind that one too.

Some keys are refused. `ctrl+c`, `esc` and `ctrl+l` are handled before the keymap
and are the way out when a remap goes wrong; `` ` `` belongs to the screensaver;
`ctrl+[` *is* `esc` on most terminals; and `ctrl+pgup` / `ctrl+pgdown` are the
attach layer's session cycling. `attach_toggle` and `kill` must be `ctrl` plus a
single letter, because the attach layer reads them as one byte, and
`attach_toggle` cannot be disabled — it would leave you with no way out of a pane
but tmux's own prefix.

##### Action names

| Action | Default | Action | Default |
|--------|---------|--------|---------|
| `accounts` | `@` | `mute` | `M` |
| `approve` | `a` | `new` | `n` |
| `attach_toggle` | `ctrl-q` | `new_pick_project` | `N` |
| `auto_name` | `A` | `next_blocked` | `b` |
| `checkpoints` | `H` | `next_tab` | `tab` |
| `collapse_all` | `Z` | `next_unread` | `u` |
| `collapse_group` | `←` | `open` | `↵/o` |
| `command_log` | `L` | `open_pr` | `w` |
| `command_palette` | `ctrl-k` | `pause` | `p` |
| `copy_branch` | `y` | `pause_all` | `ctrl-p` |
| `copy_content` | `Y` | `prev_tab` | `shift-tab` |
| `create_pr` | `c` | `push_branch` | `P` |
| `custom_commands` | `!` | `queue` | `Q` |
| `diff_comment` | `C` | `quit` | `q` |
| `down` | `↓/j` | `rename` | `R` |
| `expand_group` | `→` | `resume` | `r` |
| `filter` | `/` | `resume_all` | `ctrl-r` |
| `grow_list` | `>` | `run_command` | `d` |
| `help` | `?` | `scroll_down` | `shift-↓` |
| `hints` | `f` | `scroll_up` | `shift-↑` |
| `kill` | `ctrl-x` | `send` | `s` |
| `layout_preset` | `\` | `settings` | `,` |
| `merge_pr` | `m` | `shrink_list` | `<` |
| `move_account_down` | `]` | `smart_new` | `i` |
| `move_account_up` | `[` | `tab_diff` | `2` |
| `move_down` | `J` | `tab_preview` | `1` |
| `move_group_down` | `}` | `tab_terminal` | `3` |
| `move_group_up` | `{` | `toggle_mark` | `space` |
| `move_up` | `K` | `undo_kill` | `U` |
| `multi_select` | `v` | `up` | `↑/k` |



#### Filtering

Press `/` to filter the session list incrementally. A query is split on
whitespace into terms combined with **AND**; each term is either a predicate over
cached session state or a plain substring matched (case-insensitively) against a
session's name, branch, or note. Predicate values match by **prefix**, so the
list narrows as you type rather than blinking empty mid-word — except `model:`,
which matches a substring so a family name like `opus` reaches past the vendor
prefix in `claude-opus-4-8`.

| Term | Matches |
|------|---------|
| `status:<name>` | sessions whose status prefixes `<name>` — `running`, `ready`, `loading`, `paused`, `needsinput`, `pending` |
| `dirty` | sessions with uncommitted changes |
| `muted` | sessions whose notifications are silenced (`M` key) |
| `unread` | sessions with a finished turn not yet acknowledged |
| `behind` | sessions behind their base branch |
| `behind:<expr>` | `behind:3` (exactly 3), `behind:>0`, `behind:>=2`, `behind:<5`, `behind:<=1` |
| `pr:<state>` | PR state prefixing `<state>` — `open`, `merged`, `closed`, or `none` (no PR) |
| `account:<name>` | Claude account name prefixing `<name>`; `account:none` for sessions with no resolved account |
| `note:<text>` | sessions whose note prefixes `<text>` |
| `effort:<level>` | reasoning-effort level prefixing `<level>` — `low`, `medium`, `high`, `xhigh`, `max`; `effort:none` for sessions with no resolved effort |
| `model:<name>` | resolved model *containing* `<name>` — `model:opus` matches `claude-opus-4-8`. The one predicate that matches a substring, not a prefix, so a family name reaches past the vendor prefix |
| `mode:<name>` | permission mode prefixing `<name>` — matched against the label the row's chip shows: `plan`, `accept-edits`, `auto`, `bypass`, or the raw mode name if it has no label. `mode:none` — or equivalently `mode:default` — matches sessions showing no mode chip, which is what the default (manual) mode renders as |
| `<text>` | plain substring in the session's name, branch, or note |

Worked examples (each is exercised verbatim against the parser by
`session.TestReadmeFilterExamples`):

- `status:need dirty` — sessions that need input **and** have uncommitted changes.
- `behind:>0 pr:open` — sessions behind their base **and** with an open PR.
- `account:work note:release` — `work`-account sessions whose note starts with `release`.
- `effort:max dirty` — sessions running at `max` effort **and** with uncommitted changes.
- `model:opus behind` — `opus`-family sessions **and** behind their base branch.
- `mode:plan status:need` — planning sessions **and** waiting on you.
- `auth` — any session with `auth` in its name, branch, or note.
- `muted unread` — sessions that are silenced **and** have a new unread turn.
- `unread pr:open` — unacknowledged turns **and** an open PR.

Press `esc` to clear the committed filter.

##### Mouse
The mouse mirrors the keyboard — every click runs the same action its key would, nothing is mouse-only:

- **Click** a session row to select it, a repo header to fold/unfold it, a tab to switch to it, or any **hint-bar entry** to run that key.
- **Double-click** a session row to attach (like `↵`).
- **Wheel** over the list moves the selection; over a pane it scrolls that pane.
- **Drag** the divider between the list and the preview to resize the split.
- **Shift+drag** bypasses capture and selects text with your terminal's own selection — the escape hatch when you want to copy from the screen.

Set `mouse` to `false` to turn mouse capture off completely, handing every mouse event back to the terminal (see below).

### Configuration

Atrium stores its configuration in `~/.atrium/config.json`. You can find the exact path by running `atrium debug`. Installs that predate the rename keep using their existing `~/.claude-squad` directory automatically.

The diagnostic log sits beside it, at `~/.atrium/atrium.log` — `atrium debug` prints that path too. It is capped at 16 MiB and keeps one previous generation as `atrium.log.1`, so it costs at most 32 MiB. Atrium starts normally when it cannot write the log; it says so on exit and `atrium debug` reports why.

#### Mouse

Mouse capture is on by default: clickable session rows, repo headers, tabs, and hint-bar entries; wheel scrolling; and a draggable list/preview divider. If your terminal's native select-to-copy matters more than in-app clicking, hold **Shift** while dragging to select text past the capture, or turn the mouse off entirely:

```json
{
  "mouse": false
}
```

With `mouse` off, Atrium never enables mouse reporting, so selection, copy, and paste behave exactly as they would in any non-mouse program. The setting is also togglable live from the Settings panel (`,`).

#### Scrolling an attached session

Atrium's sessions run on their own tmux server with their own config, so your
`~/.tmux.conf` — prefix, copy-mode keys, scroll chords — does not apply inside
one. Scrollback is bound prefix-free instead:

| Key | Action |
|-----|--------|
| `shift-↑` / `shift-↓` | scroll one line — hold to scroll continuously |
| `shift-pgup` / `alt-pgup` | scroll back a page (enters tmux copy mode) |
| `shift-pgdn` / `alt-pgdn` | scroll forward; at the bottom it exits copy mode for you |
| `g` / `G`, `j` / `k`, `ctrl-u` / `ctrl-d` | while scrolled: top / bottom, line, half page |
| `?` / `/`, then `n` / `N` | while scrolled: search up / down, next / previous match |
| `v`, `y` | while scrolled: start a selection, copy it (to the system clipboard) |
| `q` | leave the scrollback and return to the agent |

`shift-↑/↓` is the same chord that scrolls the preview pane from the session list,
and moves the same one line per press. Copy mode is vi-keyed, and the wheel scrolls
the same history when `mouse` is on, the same distance per notch as in the preview.
Note that `ctrl-b` — tmux's default prefix, and the key Claude Code uses to
background a task — is not needed for any of this.

Scrolling reaches `history-limit` lines back (10000).

**Agents that draw on the alternate screen** — Claude Code with fullscreen
rendering (`/tui fullscreen`, or `CLAUDE_CODE_NO_FLICKER=1`), and anything else
vim-like — accumulate no tmux history at all, so there is nothing for copy mode to
show. Atrium detects that and hands the chord to the agent instead, letting it
scroll its own view; in Claude Code, `pgup`/`pgdn` and `ctrl-home`/`ctrl-end` do
that out of the box, and `shift-↑/↓` can be mapped to `scroll:lineUp`/`lineDown`
in `keybindings.json` so the same chord works in both kinds of pane. The trade-off
is elsewhere: the preview pane's `shift-↑/↓` and `atrium peek --lines` read tmux
history too, so against a fullscreen agent they see only the visible screen.

#### Undoing a kill

`ctrl-x` used to be the one thing Atrium did that you could not take back: it
removes the session's worktree and runs `git branch -D`, which leaves the branch's
commits reachable from nothing — and `git gc`, which your own work in the project
triggers, prunes unreachable objects. Press `U` and the session comes back:
its branch, its worktree, and its agent, resumed into the conversation it was
having.

That works because the teardown does two things first. It commits anything
uncommitted, exactly as pause does — and the restore unwinds that commit again, so
the worktree comes back as you left it, with no artifact in the history. Then it
points a ref at the branch head under `refs/atrium/undo/`, which is what keeps the
commits alive after `branch -D` and immune to `git gc`. Those refs live outside
`refs/heads`, so `git branch` never lists them, and `git push` and `git fetch` do
not touch them.

Killing several sessions at once in multi-select mode is one action, so `U`
reverses it as one and brings the whole batch back.

Records expire after **7 days**, and expiry is what releases the retained commits
to git. `atrium reset` clears them all. To see what is currently held:

```bash
git -C <your repo> for-each-ref refs/atrium/undo/
```

**What does not come back.** The terminal scrollback is gone — the pane is
destroyed and rebuilt. So are gitignored files that lived in the worktree: a
local `.env`, a build cache, downloaded dependencies. They were never in a commit
to restore from, and only the paths named by
[`carry_files`](#carried-files) and [`link_paths`](#linked-paths) are re-seeded — the
latter unless the session was created dependency-isolated, which gets none of them by
design and refills them with its setup script or its agent's own install.
The agent's conversation usually returns — Atrium resumes it the same way it does
after a pause, for the agents that support it. When Atrium can see there is no
transcript to resume, the session comes back with a fresh agent and the notice
after the restore says so. For agents whose transcripts it cannot locate
(everything but Claude Code today) the agent settles its own resume, so the notice
stays quiet rather than guess.

Atrium refuses rather than guessing when the world has moved on: if you have
already created a session with the same name, if the branch has been recreated
pointing somewhere else, or if the project directory is gone. Every refusal names
the ref that still holds the commits, so `git branch <name> <ref>` recovers them
by hand.

#### Checkpoints

Claude Code snapshots every file it is about to edit, just before each of your
prompts, and lets you roll back to one from inside the session with `/rewind` or
`Esc Esc`. From the session list you cannot normally see any of that without
attaching. Press `H` on a Claude session and Atrium lists those checkpoints: when
each was taken, the prompt it precedes, and how many files the session had touched
by that point.

It reads them straight out of Claude's own transcript — the same file the model and
context chips come from — so there is nothing to enable and no extra process to
run. `r` re-reads it, `↵` attaches to the session so you can press `Esc Esc` and
pick a checkpoint, `esc` closes.

**Atrium lists; Claude restores.** The rewind itself stays inside the session on
purpose. Claude's checkpoint covers every file the session has touched *wherever it
lives*, not only the ones under the worktree — its own plan and memory files, a
scratch directory in `/tmp`, occasionally another checkout entirely — and rolling
back deletes files created since. Doing that from the fleet view, one keypress away
from the wrong row, is a worse trade than walking you to the surface that shows you
the changes first and can rewind the conversation along with the code. A row that
reads `12 files, 3 outside` is telling you exactly that: three of them are not in
this worktree.

Two things the list cannot promise. Claude keeps the last **100** checkpoints per
session and sweeps a session's file backups on its own retention schedule
(`cleanupPeriodDays`, 30 by default) while leaving the transcript records in place,
so an old row can outlive the copies it would restore from — the overlay says so
when the backups are already gone. And checkpoints only cover Claude's own file
edits: changes made by a `bash` command it ran, by a subagent, or by you in another
window are not in them. Git remains the permanent history; this is local undo.

Non-Claude sessions have no equivalent surface, so `H` there just says so.

#### Auto-attach

By default, Atrium attaches you to a new session as soon as it starts, so you land directly in the agent. Detach with `ctrl-q` to return to the session list. When you create a session with the `N` form and provide an initial prompt, auto-attach is skipped — the session stays in the list so the prompt is delivered automatically once the agent is ready, and you can attach with `↵`/`o` whenever you like.

To disable auto-attach and always return to the list after creating a session, set `auto_attach` to `false`:

```json
{
  "auto_attach": false
}
```

#### Auto-update

`auto_update` controls the startup release check: `"notify"` (default) shows a
hint when a newer release exists, `"auto"` downloads and installs it in the
background (applied on the next launch), and `"off"` disables the check. The
explicit `atrium update` command works regardless of this setting. Alongside
the transient hint, a persistent `⇡` badge in the Sessions panel border shows
the pending update (or restart) state until the next launch.

#### Notifications

Because each agent runs inside Atrium's own tmux server, an agent's own terminal
bell never reaches you — so Atrium can emit its own signal when a **background**
session finishes a turn or blocks on a prompt. `notifications` selects how:

- `"off"` (default) — no notifications.
- `"bell"` — rings the terminal bell once per edge on Atrium's own terminal.
- `"desktop"` — fires a desktop notification. With `notify_command` unset, Atrium
  uses a built-in per-OS notifier (`notify-send` on Linux, `terminal-notifier` or
  `osascript` on macOS); a missing notifier is a silent no-op.
- `"osc"` — writes an OSC 9 desktop notification to Atrium's own stdout, so the
  escape is emitted by **your** terminal and reaches you over SSH with no local
  notifier binary. Supported by iTerm2, kitty, WezTerm, Ghostty, ConEmu, and foot;
  terminals that don't recognize it simply show nothing.

All three events — a turn finishing, a session blocking on you, and an agent that
ended its turn by **asking you a question** — use that one mode by default.
`notifications_finished` splits them into an **attention ladder**, so the agent
that actually needs you is never out-shouted by one that merely stopped:

- `"same"` (default) — a finished turn uses the `notifications` mode too, exactly
  as before.
- `"off"` — a finished turn sends nothing out-of-band. This is quieter, not silent:
  the row still carries its unread marker, and `u` still jumps to it.
- `"bell"` — a finished turn just rings the terminal, while a session blocking on
  you gets the fuller `notifications` mode (and `b` jumps to it).

The rung applies to a **plain** finished turn only. A blocked session and a session
that stopped to ask both always use `notifications` itself, and only rungs quieter
than every mode are offered, so a finished turn can never outrank either.
`"desktop"` and `"osc"` are deliberately not accepted here — they are peers of each
other, so neither is "one rung quieter" than the other; anything unrecognized reads
as `"same"`. `notifications: "off"` remains the master switch: it silences all
three events whatever `notifications_finished` says.

**A question is never silenced by a queued prompt.** A finished turn on a session
with a follow-up queued stays quiet — it is about to be auto-continued, and ringing
would ping you for work you queued to run unattended. But a turn that ended by
asking is not auto-continued: Atrium holds the queue rather than answering the
question for you (the prompt goes out once you've looked at the row), so that turn
notifies like any other session waiting on you. Detection is claude-only, and reads
the last thing the agent wrote; an agent that asks by saying "let me know" rather
than with a question mark is not detected, and behaves as a plain finished turn.

The session you're currently on — the selected row, or one you're attached to —
never notifies, so only agents you've navigated away from can interrupt you.
**While Atrium's terminal is focused nothing notifies at all** (you're already
watching the fleet); set `notify_when_focused` to `true` to keep notifying even
then. A terminal that doesn't report focus — a plain `tmux` without `focus-events
on`, or one without DECSET 1004 — is never treated as focused, so it always
notifies exactly as before. You can also **mute an individual session** with `M`;
a muted session never notifies, and the mute persists across restarts.

```json
{
  "notifications": "desktop",
  "notifications_finished": "bell",
  "notify_command": "notify-send \"Atrium\" \"$ATRIUM_SESSION $ATRIUM_STATUS\""
}
```

That pair reads as: a desktop popup when an agent is waiting on you, a bell when
one merely finished its turn.

`notify_command`, when set, runs via `sh -c` for each desktop notification with
`$ATRIUM_SESSION` (the session's display name), `$ATRIUM_STATUS`
(`Ready`/`NeedsInput`), and `$ATRIUM_EVENT` (`finished`/`needs_input`/`asked`) in its
environment — the session name rides in the environment, never interpolated into
the command, so it can't break argument parsing. Use it for `terminal-notifier`,
webhooks (`curl`), or any custom notifier. A failing command is logged, never
fatal. These settings are also editable live from the Settings panel (`,`).

#### Clipboard

Copy actions (`y` for the branch name, and hint mode's `f` copy) use **two
paths** so a copy lands in your local clipboard whether Atrium runs on your
machine or on a remote host:

- **OSC 52** — Atrium emits the copied text to your terminal as a clipboard
  escape sequence. This is the SSH-friendly path: it needs no clipboard binary
  on the remote, so a copy from an agent running over SSH still reaches the
  clipboard of the terminal in front of you. Your terminal must support (and
  usually enable) OSC 52 clipboard writes.
- **System clipboard utility** — Atrium also shells out to `xclip`/`xsel`/
  `wl-copy` (Linux), `pbcopy` (macOS), or the Windows clipboard. This is the
  local fallback for terminals that ignore OSC 52.

The OSC 52 leg always goes out, so a copy is never reported as failed: a missing
clipboard utility is recorded in the log rather than shown as an error, because
claiming a copy failed while the escape was on its way would be wrong more often
than right. If a copy does not land, the two things to check are that your
terminal has OSC 52 writes enabled (see the tmux note below) and that a clipboard
utility is installed — run with `-v` to get the log file path, where a missing one
is noted.

> **Running Atrium inside your own outer tmux?** tmux swallows OSC 52 by default,
> so the escape never reaches your terminal. Enable clipboard passthrough in that
> **outer** tmux (the one you started before launching Atrium):
>
> ```tmux
> # ~/.tmux.conf
> set -g set-clipboard on
> ```
>
> This applies only to an outer tmux you control — Atrium's own per-session tmux
> server is internal and already handled.

#### OS chrome (window title & taskbar progress)

When Atrium is one tab or window among many, its signal otherwise stops at its own
panel borders. With `os_chrome` on (the default), Atrium surfaces the fleet in the
terminal's own chrome:

- **Window title** — `atrium · 2 need you · 5 running`, updated as statuses change
  (zero segments are omitted; a fully idle fleet is a bare `atrium`).
- **Taskbar progress** (OSC 9;4) — an indeterminate bar while any agent is working,
  cleared when none are, and an error state when a session dies. Rendered by
  Ghostty 1.2+, Windows Terminal, ConEmu, and kitty; other terminals ignore it.

Set `os_chrome` to `false` when your shell or multiplexer owns the title:

```json
{
  "os_chrome": false
}
```

Also editable live from the Settings panel (`,`).

#### Profiles

Profiles let you define multiple named program configurations and switch between them when creating a new session. When more than one profile is defined, the session creation overlay shows a profile picker that you can navigate with `←`/`→`.

Profiles are edited in the Settings panel (`,` → **Profiles**): `n` adds one, `e` or `↵` edits the highlighted record, `d` deletes it, and `D` probes for installed agent CLIs and appends any that are missing. A profile named by `default_program` cannot be deleted until that setting points elsewhere; renaming it carries the setting along.

On first run, Atrium probes for installed agent CLIs (`claude`, `codex`, `gemini`, `aider`, `agy`) and seeds a profile for each one it finds. After installing a new agent, press `D` in the panel's Profiles category, or run:

```bash
atrium profiles detect
```

to add it as a profile — existing profiles and your default program are never modified.

To configure profiles by hand instead, add a `profiles` array to your config file and set `default_program` to the name of the profile to select by default:

```json
{
  "default_program": "claude",
  "profiles": [
    { "name": "claude", "program": "claude" },
    { "name": "codex", "program": "codex" },
    { "name": "aider", "program": "aider --model ollama_chat/gemma3:1b" }
  ]
}
```

Each profile has two fields:

| Field     | Description                                              |
|-----------|----------------------------------------------------------|
| `name`    | Display name shown in the profile picker                 |
| `program` | Shell command used to launch the agent for that profile  |

If no profiles are defined, Atrium uses `default_program` directly as the launch command (the default is `claude`).

#### Carried files

Git worktrees materialize only tracked files, so gitignored local config — most
commonly `.claude/settings.local.json` (hooks, output style, MCP allowlists) —
never reaches a fresh session worktree on its own. The `carry_files` list names
repo-relative gitignored files that Atrium copies from the original checkout
into each newly created session worktree:

```json
{
  "carry_files": [".claude/settings.local.json"]
}
```

The default is `[".claude/settings.local.json"]`; set an empty list (`[]`) to
opt out. Entries must be gitignored — anything else is skipped with a warning,
because pausing a session commits its worktree and a non-ignored file would leak
into the session branch.

"Gitignored" is decided **in the session's worktree**, which is what pause stages —
not in your own checkout. A `.gitignore` edit you have not committed never reaches
the worktree, so an entry covered only by such an edit is skipped. Two ways to fix
it: commit the rule on the branch the session starts from, or add it to
`.git/info/exclude`, which every worktree of the repo shares and which keeps the
rule out of the branch entirely. The same applies to [linked paths](#linked-paths).

Skips are recorded in the log rather than shown in the TUI, so a missing carried
file is quiet — `atrium --verbose` prints the log's path on exit.

Carried files are re-seeded from the original checkout whenever the worktree
is created, including on resume after a pause — edits made to them inside a
session do not survive a pause/resume cycle.

#### Linked paths

Some gitignored paths should not be copied at all. An installed dependency tree
like `node_modules` is large and slow to duplicate per session, and the tooling
resolves through a symlink perfectly well — so `link_paths` names repo-relative
paths that Atrium **symlinks** into each new worktree, pointing at the original
checkout's copy:

```json
{
  "link_paths": ["node_modules", "container/agent-runner/node_modules"]
}
```

The default is `[]` (off). The link target is absolute, so it stays valid however
deep the worktree sits under the data dir, and a path that does not exist in the
original checkout yet (no `npm install` run) is skipped silently.

Entries must be gitignored, and — unlike `carry_files` — the pattern must not end
in a slash:

```gitignore
node_modules      # ignores the directory *and* the symlink — use this
node_modules/     # directories only: the symlink would NOT be ignored
```

Git stores a symlink as a file, so a directory-only pattern leaves the link
un-ignored, which would commit it into the session branch on pause and show it in
the session diff. Atrium checks this the way git will see it in the worktree and
skips the entry with a warning rather than creating a link that leaks. As with
[carried files](#carried-files), the rule has to reach that worktree — committed on
the branch the session starts from, or in `.git/info/exclude`.

Links are re-created whenever the worktree is materialized, including on resume
after a pause. On Windows, creating a symlink requires Developer Mode or an
elevated process; without it the entry is skipped with a warning and the session
still starts.

Unlike a carried file, a linked path is **shared and writable, not a per-session
copy** — it is the original checkout's tree under another name. Writes through it
land in your own working copy and are visible to every other session at once, so
an agent that runs `npm install` (or `rm -rf node_modules`) inside one session
changes the dependency tree for all of them. That is exactly why linking beats
copying for a large tree: link paths whose contents you are content to share, and
prefer `carry_files` when a session needs its own copy of a *file*.

##### Dependency-isolated sessions

Sharing is right for the sessions that only read the tree, and wrong for the one
whose job is to change it — an upgrade session's `npm install` rewrites the tree
your other sessions (and anything else running from that checkout) resolve
through. So the choice is **per session**, made when the session is created.

Wherever `link_paths` is configured, the new-session form grows a **Dependencies**
row with two settings:

- **shared** (the default, and today's behaviour) — the paths are symlinked, as above.
- **isolated** — this worktree gets **none** of them. No symlink, and no empty
  directory either: the worktree looks exactly like a fresh clone where nobody has
  run `npm install` yet, which is the state every tool already handles. Whatever the
  session installs there is a real, private directory that no other session and no
  part of your checkout can see, and it is deleted with the worktree on pause or
  kill.

The choice is fixed for the session's life and survives a restart and a
pause/resume — the links are re-seeded on every worktree materialization, so a
resumed isolated session stays isolated. It has no effect on `carry_files`, which
are already per-session copies, and none on a **direct** (non-git) session, which
has no worktree at all — the row renders inert there, like the base-branch row.

An isolated session starts with nothing installed. If the repo has a
[setup script](#setup-scripts), that script runs immediately afterwards and
installs into the now-private tree; without one, the agent's first `npm install`
does the same thing, just later. That applies to **every** materialization, not
just the first: the private tree is ordinary worktree content, so pausing deletes
it and resuming starts empty again. A setup script is what makes that automatic,
and it is why an isolated session is worth pausing less casually than a shared one.

The path still has to be gitignored *in the session's worktree*, exactly as a
linked one does — otherwise the tree the session installs is untracked rather than
ignored, and pause commits it onto the branch. Atrium logs a warning at session
start when it is not. A directory-only rule (`node_modules/`) is fine here, unlike
for a symlink.

Nothing in the session list marks a session isolated; the choice is visible in
`atrium ls --json` as `isolated`.

#### Setup scripts

`carry_files` and `link_paths` move *files* into a new worktree. Neither can install
dependencies, apply a migration, or generate an `.env` — so an agent asked to build or
run the project starts by doing that itself, in a tree where nothing is installed.
`repo_scripts` is the step that can: a shell script Atrium runs once the worktree
exists and **before the agent program launches into it**. The same entry also declares
the repo's [managed port range](#managed-ports) and its [run command](#run-commands).

Configuration is per repository, but it lives in your own `config.json` rather than in
the repo. Entries are routed by the matcher [Claude accounts](#claude-accounts) use —
case-insensitive substrings, `origin` remote first and then a path, first match wins,
and an entry with no rules at all is the catch-all:

```json
{
  "repo_scripts": [{
    "name": "web",
    "remote_matches": ["acme/web"],
    "path_matches": ["projects/web"],
    "setup_script": "npm ci && npm run db:migrate",
    "port_range": "3000-3099",
    "session_env": { "GOLANGCI_LINT_CACHE": "/tmp/lint-{{.Session.Title}}" }
  }]
}
```

One difference from the account sections is worth knowing, because it decides what you
write in `path_matches`: the path tested here is the **repository root** (a direct
session's own directory, when there is no repo), not the session's worktree and not the
directory you happened to start from. A worktree lives under
`~/.atrium/worktrees/<branch>_<nonce>` and carries none of your project's path, so
matching on it is not something you can usefully do — and a trailing slash on a rule
that spells the root exactly (`/projects/web/`) will never match, because the root has
none.

Nothing here is read from the repository itself. That is deliberate: a setup script
committed alongside a project would run whatever its author wrote the moment you opened
a clone of it.

`setup_script` runs through `sh -c` with the worktree as its working directory, after
carried files and linked paths are in place. It gets the same `$ATRIUM_*` environment
as a [custom command](#custom-commands) — `$ATRIUM_WORKTREE`, `$ATRIUM_BRANCH`,
`$ATRIUM_REPO`, and the rest — and the same template placeholders and `quote` helper,
so a path never has to be interpolated into a shell string by hand. Prefer the
environment: `$ATRIUM_WORKTREE` needs no escaping at all, while a bare
`{{.Session.Name}}` is the session's *renameable* label going straight into a shell
command — wrap it in `{{ quote .Session.Name }}` if you interpolate it.

A non-zero exit does **not** destroy the session. The worktree, the branch and the
agent all survive; the failure opens a modal carrying the tail of what the script
printed, and the run is recorded in the command log (`L`). What you lose is whatever
the script installs, so the session comes up cold rather than not at all.

It runs once per **worktree**, not once per session. Pausing removes the worktree and
resuming recreates it — empty of `node_modules`, of generated files, of everything
gitignored — so the script runs again on resume. Write it to be idempotent. (A park
that left its worktree on disk skips both the recreation and the script.)

`session_env` is exported into the setup script *and* into the agent's own pane, which
is the point: a per-session value only some steps can see is not a per-session value.
Names are refused if Atrium already injects them (anything starting with `ATRIUM_`, plus
`CLAUDE_CONFIG_DIR` and `GH_CONFIG_DIR`). Values are Go templates over the same session
context. They reach the pane as `tmux new-session -e` arguments, briefly visible to
`ps` — do not put secrets there.

A **direct** (non-git) session is the one asymmetric case. It has no worktree of its
own — it runs in your real directory — so it gets `session_env` but never runs
`setup_script`: installing into your own checkout is not Atrium's to do. It also has no
branch, so a `session_env` value spelling `{{.Session.Branch}}` renders empty there
rather than failing.

One interaction to know about: a script that runs `npm install` under a path listed in
[`link_paths`](#linked-paths) is writing into your own checkout's tree, shared by every
other session at once — so for an ordinary session, linking and installing are
alternatives rather than a pair. They *are* a pair for a session created with
Dependencies set to **isolated**
([dependency-isolated sessions](#dependency-isolated-sessions)): that worktree gets no
links, so the script installs into a tree of its own. That is the combination to reach
for when a session's job is to change dependencies.

#### Managed ports

`port_range` hands each session of that repo one TCP port of its own, so two sessions
can run the same dev server at once without you resolving the collision by hand. It is
spelled `lo-hi`, inclusive, and both ends must be 1024 or above.

The port reaches a session three ways, all carrying the same number:

| Where | Spelling |
|-------|----------|
| The agent's pane, and the setup script | `$ATRIUM_PORT` |
| `setup_script` and `session_env` templates | `{{.Session.Port}}` |
| [Custom commands](#custom-commands) | either |

So `npm run dev -- --port $ATRIUM_PORT` in the agent's own shell does the right thing
for whichever session it is typed in, and the row shows `:3001` — a link, on a terminal
that supports them, to `http://localhost:3001`.

A session keeps its port for as long as the session itself lasts: allocated when the
worktree is materialized, kept across a pause, released on kill. It survives an Atrium
restart too, so a server you started stays reachable at the number the row shows.

That a *paused* session still holds its port is deliberate, and it is the one place the
design costs you something. The number is what your browser tab, your bookmark and any
template that rendered it are already aimed at, and a resume restarts the session's dev
server on it — so handing it to the next session created would mean the resumed session
either comes back on a number nobody was told about or lands on one another session now
owns. So a parked session occupies a port until it is killed. Size the range for the
sessions you park as well as the ones you run.

Two sessions are never handed the same port: Atrium tracks what its own sessions hold
and, separately, refuses a port anything else is already listening on. That second check
is a snapshot — a port free when the session is created can be taken before your server
binds it — so it is a filter against what is already running, not a reservation.

If nothing in the range is free the session still starts, without `$ATRIUM_PORT`, and
says so in a modal. Widen the range or free a port by killing another session — pausing
one will not, for the reason two paragraphs up.

While the script runs the session's row says so, and the preview names it in place of
the generic "Setting up workspace…" it shows for every other session that has not come
up yet — on a resume as well as on a first start. Entries the validator refuses are
dropped rather than applied — one bad template does not cost you the others — and it
says which and why: a modal at startup, and `atrium doctor` under **Repo scripts**.

Quitting while a script is running ends it. Atrium kills the script's whole process
group rather than just the `sh` it started, so a half-finished `npm ci && npm run build`
does not keep running after the app is gone — and anything that script had backgrounded
goes with it. A process left behind by a script that already *finished* is not touched:
Atrium stops waiting on it after a moment and launches the agent, so a
`dev-server &` never holds the session up.

#### Run commands

`setup_script` is a step to *wait for*. `run_command` is a process to *keep*: the dev
server, the watcher, the thing that binds the port above. Add it to the same
`repo_scripts` entry:

```json
{
  "repo_scripts": [{
    "remote_matches": ["acme/web"],
    "port_range": "3000-3099",
    "run_command": "npm run dev -- --port $ATRIUM_PORT"
  }]
}
```

Press <kbd>d</kbd> on a session to start it, and <kbd>d</kbd> again to stop it. Nothing
starts on its own: a session is not always one you want a server for, and one server per
session would mean one port per session too.

It runs in a **tmux session of its own**, named after the session's with a `_run` suffix,
beside the agent's and the terminal tab's. That is what buys the properties a background
goroutine could not: it survives an Atrium restart, you can `tmux -L atrium attach -t
<name>_run` to read its full scrollback, and it is torn down with the session it belongs
to. It gets the worktree as its working directory and the same environment the agent's
pane does — `$ATRIUM_PORT` and every `session_env` value — so a server that reads a port
from the environment and one that takes it as a flag both work.

The row says which state it is in, on the same chip as the port: `:3001` for a session
holding a port with nothing running, `▸:3001` once the server is up. A repo that declares
a `run_command` with no `port_range` shows `▸dev` instead — a state worth showing even
with no number to show with it.

Pausing stops it, because pausing removes the worktree the server is running in. Resuming
starts it again, on the same port — the number is kept across a pause precisely so that
nothing renumbers underneath a browser tab you left open. Renaming a session leaves the
server alone: it keeps the tmux name it was started under, so it stays attachable and
stays torn down with the session.

A **direct** (non-git) session never hosts one, for the same reason it never runs
`setup_script`: its directory is your own checkout, and a second dev server writing over
the build you are already running there is not Atrium's to start.

One thing to know: if the command exits on its own — a crash, a typo, a port that turned
out to be taken — its tmux session goes with it, and so does its output. A command that
dies *at once* is reported as an error the moment you press <kbd>d</kbd>, quoting what
was run; one that dies later just returns the chip to its idle state, with nothing left
to read. Either way the place to find out why is the terminal tab, where you can run it
by hand in the same worktree with the same `$ATRIUM_PORT`.

#### Claude accounts

Route each session to a specific Claude Code account by injecting a per-session
`CLAUDE_CONFIG_DIR`, chosen by matching the worktree's git `origin` remote (or, for
a non-git/direct session, its directory path). This is useful when different repos
must run under different Claude accounts (e.g. personal vs. work), since MCP
connectors and auth are stored per `CLAUDE_CONFIG_DIR`. Add a `claude_accounts`
list to your config file:

```json
{
  "claude_accounts": [
    { "name": "personal", "config_dir": "~/.claude" },
    {
      "name": "quantivly",
      "config_dir": "~/.claude-quantivly",
      "remote_matches": ["quantivly/", "github-quantivly:"],
      "path_matches": ["/quantivly/"]
    }
  ]
}
```

- `remote_matches` are case-insensitive substrings tested against the origin URL.
- `path_matches` are case-insensitive substrings tested against the target
  **directory path** — the routing signal for **direct (non-git) sessions** (which
  have no remote), such as a container directory that holds several repos but is not
  itself one, and also a route for git repos whose remote matches nothing.
- Matching is evaluated per account in list order (within an account, remote first
  then path); the first account that hits either rule wins. Because list order
  dominates, an earlier account's `path_matches` beats a later account's
  `remote_matches`.
- The **first account with no `remote_matches` and no `path_matches`** is the
  catch-all default, used when no route matches. It is optional: with no such
  account, non-matching sessions inherit the current environment.
- Order is **match priority**, not just display order: press `J` / `K` (or
  `shift+↑` / `shift+↓`) in the `@` accounts overlay to move the row under the
  cursor up or down one slot — the cursor follows the row, and a press at either
  end, or on a tab with fewer than two accounts, does nothing. This is how you
  change which account is the catch-all, or break a tie between two accounts
  whose rules both match; it works the same on all three tabs (Claude, GitHub,
  Antigravity). The `default` / `unreachable` / `routed` badges update live as
  rows move, and the new order is saved to `config.json` immediately.
- The resolved account is **pinned at session creation** and shown as a badge in
  the session list (dim for the default account, accented for a routed one). The
  `CLAUDE_CONFIG_DIR` it injects is set once at launch and is never re-resolved —
  no config edit can move a running session to a different login, on restart or
  `--continue`. The **badge and the account cluster** are re-derived from that
  directory, at launch and whenever the `@` panel commits an edit, so renaming an
  account (or moving it into a pool) adopts its existing sessions instead of
  leaving them grouped under a name the config no longer has. Deleting an account
  from the config leaves its sessions' badges as they were — the last known truth
  about the login they run under.
- On a list pane too narrow to hold the badge and the session name, the badge is
  the one that goes — the name is how you tell one row from another at all, and
  without it a narrow row also loses the permission-mode chip and the agent icon
  off the right edge. Nothing is hidden by this that you cannot get back without
  resizing: `account:<name>` filtering reads the same pinned name the badge shows.
  (Account grouping helps too, but only partly — a pooled session's divider names
  its **pool**, where the badge names the member.) Where both account badges are on
  one row, the `agy:` one yields first — and across a band as many columns wide as
  this badge is wider than that one (none, where it is the narrower), the row gives
  up both.
- When more than one account is configured, the new-session form shows an
  **Account** picker, preset to the auto-routed account, to override the choice.
- `expect_account` (optional) is the email address that `config_dir` is supposed to
  be logged in as. `name` is only a label: running `/login` inside a config dir
  re-points it at a different Claude account in place, and nothing about the routing
  notices — every badge and pool keeps the old name while the sessions bill a login
  you never picked, with a usage figure on a webpage as the only symptom. Setting it
  gives that drift something to fail against:

  ```json
  { "name": "personal", "config_dir": "~/.claude", "expect_account": "me@example.com" }
  ```

  **Creating or resuming** a session whose config dir holds a *different* login is
  **refused**, naming both logins and the directory to fix — a wrong launch cannot be
  undone by noticing it afterwards, and a resumed session re-injects the same
  directory, so it is gated identically. And `atrium doctor`'s **Claude account
  identities** section marks the account verified rather than unpinned.

  Only a confirmed mismatch refuses. An account with no `expect_account` is never
  blocked, and a directory with no login recorded is allowed through — `claude` will
  prompt for login in the pane, which cannot silently mis-bill, and refusing would
  strand you mid-onboarding; a pinned account logs a warning when it launches
  unverified that way. Comparison is on email, case- and whitespace-insensitive.

  Separately, and with no configuration at all, `doctor` warns when two accounts you
  believe are separate turn out to hold the **same** login, naming which one the
  combined work is billing. There is no field for `expect_account` in the `@` accounts
  overlay; edits there preserve it.
- Atrium's own short background calls — the one-shot used by auto-naming (`N`) and
  by smart dispatch's routing (`i`) — are billed the same way. Auto-naming runs on
  the **session's own account**, since the session it is naming already has one.
  The routing call has no session yet (proposing the project *is* its job, so it
  runs before one is chosen), so it uses the **catch-all** — the account a session
  that matched no route would get, and the ambient environment when no catch-all is
  configured. Neither call can see the account's `CLAUDE.md` or `settings.json`:
  they run under a throwaway home holding nothing but a link to that account's
  credentials.
- Omitting `claude_accounts` disables the feature entirely (no badge, no
  injection), so existing configs are unaffected.

##### Rotation pools

Give two or more accounts the same `pool` name to spread sessions across them
instead of pinning every session in a repo to one account:

```json
{
  "claude_accounts": [
    {
      "name": "work-1",
      "config_dir": "~/.claude-work",
      "remote_matches": ["quantivly/"],
      "pool": "work"
    },
    { "name": "personal", "config_dir": "~/.claude" },
    { "name": "work-2", "config_dir": "~/.claude-work2", "pool": "work" }
  ]
}
```

- Matching still resolves to a single **account** first, exactly as above; if
  that account carries a `pool`, the session routes to the whole **pool**
  instead — `route → pool → next available member`. Only one member needs its
  own route rules; the rest just share its `pool` name (`work-2` above has none
  of its own, and still rotates in).
- Selection is **round-robin per new session**, not per workload: each pool
  keeps a rotation cursor that advances by one every time a session is created,
  so an idle session and one mid-task both count as "one turn" — the cursor
  never skips back to whichever member looks less busy.
- Adjacent members of one pool render as a bracketed group in the `@` overlay's
  Claude tab (`┌`/`│`/`└` in a dim gutter column to the row's left); the
  per-row `pool:<name>` chip stays either way. Brackets are per contiguous
  run: a pool split into two clusters gets two brackets *and* the split
  note, since the trigger is non-contiguity, not "no brackets" — `pool
  '<name>' is split — J/K to group its members`. One consequence of
  reordering: a pool's rotation cursor is an index into its members in config
  order, so reordering members *within* a pool can repeat or skip one member
  once — harmless for round-robin.
- An account can be flagged rate-limited by hand — Atrium has no way to read
  Anthropic's own limits — from the `@` accounts overlay: press `l` on a Claude
  account to toggle it limited/available. Each Claude row ends in a mark for its
  current state — `●` available, `⊘` limited (`*` and `x` on the ASCII glyph
  rung) — and the panel's second hint line names the limited one. Rotation skips
  a limited member and cycles only through the rest. The flag is indefinite: it
  stays until you press `l` again to clear it (a per-account reset time that
  auto-expires is a planned follow-up).
- The new-session form's **Account** picker lets you override routing per
  session: pick the `<pool> ⇄` entry to rotate across the pool, or a specific
  member (shown indented under it) to pin that account for this one session —
  which bypasses availability, so it works even on a flagged member.
- If **every** member of the routed pool is flagged limited, creating a session
  shows a confirm ("all `<pool>` accounts are rate-limited … create anyway on
  `<member>`?") instead of silently spawning on a limited account. Declining
  creates nothing; accepting pins the member whose limit resets soonest — which,
  while flags are indefinite-only, is the first pool member. This applies to
  **smart auto-dispatch too**, which opts out of the form, not out of the account
  gates: declining there creates nothing but does not preserve the line you
  typed, since only a create form is kept as a restorable draft. A pool of one
  never raises it — there is nothing to rotate to.
- Press `t` in the `@` accounts overlay to preview where an input (remote URL
  and/or path) would route right now, without creating anything. When the
  matched account belongs to a pool, the `Claude →` line names the member a
  session would actually take, and a block beneath it (`pool '<name>' ⇄`)
  lists the pool's members with their available/limited chip, marking the
  one it picked `← next` — and, when getting there meant skipping a limited
  member, naming why, e.g. `work-1 limited → rotating to work-2`. If every
  member is limited, the `Claude →` line instead shows
  `⚠ all '<pool>' accounts limited`, and the block marks the member that
  accepting the confirm would pin, with `← on confirm` — the one whose limit
  resets soonest, which, while flags are indefinite-only, is the first pool
  member. The preview only reads availability and the
  rotation cursor; it never advances it, so the same input can preview a
  different member after a real session moves the cursor. A pool with fewer
  than two members — including no pool at all — previews unchanged, with no
  pool block.
- **Setting up a second account:** each pool member needs its own
  `CLAUDE_CONFIG_DIR` with its own login. Point Claude at a fresh directory and
  log in once — `CLAUDE_CONFIG_DIR=~/.claude-work2 claude`, then run `/login`
  inside it — and use that same directory as the member's `config_dir`.
- Two members whose `config_dir` resolves to the **same** directory are the
  same login under the hood, so rotating between them is a silent no-op (every
  session lands on the one account regardless of the cursor). `atrium doctor`
  flags this — a pool with two members sharing a `config_dir` prints a warning
  naming both.
- **Renaming a pool** means retyping the same `pool` name on each of its members
  (a pool is just that shared string — there is no pool entity to rename). Open
  sessions follow the rename, and the cluster keeps the position `[` / `]` gave it.
  What does not follow is state keyed by the *old* name: `atrium doctor` reports
  any leftover rotation cursor or cluster slot, all of them harmless.

#### GitHub CLI accounts

`gh` keeps a single **global active account** per host, so in a multi-agent setup a
session on a work repo and a session on a personal repo fight over it — and an agent
running `gh auth switch` to fix its own auth silently breaks every other running
session. Atrium avoids this by injecting a per-session `GH_CONFIG_DIR`, chosen by the
same remote/path matching as `claude_accounts` but configured independently so gh
routing can differ from Claude-login routing. Add a `gh_accounts` list:

```json
{
  "gh_accounts": [
    { "name": "personal", "config_dir": "~/.config/gh" },
    {
      "name": "quantivly",
      "config_dir": "~/.config/gh-quantivly",
      "remote_matches": ["quantivly/", "github-quantivly:"],
      "path_matches": ["/quantivly/"]
    }
  ]
}
```

- `config_dir` is a `gh` config directory (containing `hosts.yml`) whose **active
  account** is the one you want for matching repos. Create one per account, e.g.
  `GH_CONFIG_DIR=~/.config/gh-quantivly gh auth login`; when `gh` stores tokens in the
  OS keyring, the separate dirs share those tokens and differ only in which account is
  active. Verify with `GH_CONFIG_DIR=~/.config/gh-quantivly gh auth status`.
- Matching rules (`remote_matches`, `path_matches`, list order, the optional rule-less
  catch-all) work exactly as for `claude_accounts` above.
- The resolved dir is injected into the agent's tmux session (so the agent's own `gh`
  — and any HTTPS git credential-helper — pick the right account) **and** into
  Atrium's own `gh` calls (PR create/merge/view). It is pinned at session creation;
  editing `gh_accounts` affects only newly created sessions.
- Omitting `gh_accounts` (or a non-matching session with no catch-all) leaves `gh` on
  its ambient global account, exactly as before.

> **Commit identity & SSH keys are still handled outside Atrium.** `gh_accounts`
> routes the *GitHub CLI account* (`GH_CONFIG_DIR`) and `claude_accounts` routes the
> *Claude Code account* (`CLAUDE_CONFIG_DIR`); neither sets git commit identity.
> `user.email` / `user.signingkey` and the SSH key used to fetch/push are selected by
> your machine's git config from the repo's remote org — e.g. a remote-based
> `includeIf "hasconfig:remote.*.url:…"` (git ≥ 2.36), so a work repo's worktree
> resolves to the work identity and key regardless of its path under
> `~/.atrium/worktrees/`. Atrium carries no commit-identity logic; it relies on that
> system, which keys off the same remote signal as `remote_matches` above.

#### Antigravity accounts

Route each session to a specific [Antigravity](https://antigravity.google/docs/cli/reference)
(`agy`) configuration by isolating the CLI's config directory
(`~/.gemini/antigravity-cli`), chosen by the same remote/path matching as
`claude_accounts` but configured independently. This keeps a work agent's
Antigravity auth and settings separate from a personal one. Add an `agy_accounts`
list:

```json
{
  "agy_accounts": [
    { "name": "personal", "config_dir": "~/.agy" },
    {
      "name": "quantivly",
      "config_dir": "~/.agy-quantivly",
      "remote_matches": ["quantivly/", "github-quantivly:"],
      "path_matches": ["/quantivly/"]
    }
  ]
}
```

- Matching rules (`remote_matches`, `path_matches`, list order, the optional rule-less
  catch-all) work exactly as for `claude_accounts` above. The resolved dir is pinned
  at session creation, so re-routing a repo affects only newly created sessions;
  renaming an account in place (same `config_dir`) is re-derived onto existing
  sessions, as for `claude_accounts`.
- The pinned account is shown on the session's row as an `agy:<name>` badge, on
  sessions that run `agy` and pinned a `config_dir`. An account is resolved for every
  session, but only the Antigravity CLI's launch reaches for it — so a `claude`
  session carries no badge even under a catch-all `agy` account, and neither does an
  account with no `config_dir` (which inherits the ambient config and isolates
  nothing). The badge reports the *pinned route*, not that the isolation is live:
  where bwrap does not apply — macOS, or Linux without bubblewrap, per the bullet
  below — the route is still shown, because whether `bwrap` is on `PATH` is decided
  at launch and not recorded on the session. On a list pane too narrow to hold the
  badge and the session name, the badge is the one that goes — the name is how you
  find the session at all — and it goes *before* the Claude account badge, which has
  the `account:` filter to fall back on where this one has nothing. So widen the pane
  to read it back: the `@` panel's routing preview is
  *not* a substitute, because it resolves the current config for a repo, which is
  what a **new** session there would get, not what an existing session pinned.
- Unlike Claude accounts there is no pool and no default/fallback state, so the badge
  has no dim form; and account clustering (`[` / `]`, the `account` group mode) keys
  on the Claude account alone, so an `agy` badge is never folded into a cluster
  divider.
- Isolation is implemented with [bwrap](https://github.com/containers/bubblewrap)
  (bubblewrap), which bind-mounts the account's `config_dir` over
  `~/.gemini/antigravity-cli` for that session only. **This is Linux-only** — bwrap
  is a Linux user-namespace tool, so the routing is a no-op on macOS. If `bwrap` is
  not installed, the session still starts but runs against the ambient config (a
  warning is logged); install bubblewrap to get isolation.
- Omitting `agy_accounts` (or a non-matching session with no catch-all) leaves `agy`
  on its ambient config, exactly as before.

#### Custom commands

Your own verbs over the selected session. Atrium knows the worktree, the branch, the
title and the repo; `custom_commands` is how you spend that on a shell command without
attaching or copy-pasting a path.

`!` opens the menu, and each entry's own key runs it. `?` lists them too, so the keys
you configure are documented where every built-in key is.

```jsonc
"custom_commands": [
  {
    "key": "g",                                       // one printable character
    "description": "lazygit in this worktree",         // shown in the menu and in ?
    "command": "lazygit -p {{ quote .Session.Worktree }}",
    "output": "terminal"                              // required; takes the screen
  },
  {
    "key": "c",
    "description": "just ci",
    "command": "just ci",
    "output": "background",
    "confirm": true                                   // ask y/n first
  },
  {
    "key": "f",
    "description": "git fetch --all",
    "context": "repo",                                // the repository, not the worktree
    "command": "git fetch --all",
    "output": "background"
  }
]
```

| Field | Required | Values | Notes |
|-------|----------|--------|-------|
| `key` | yes | one printable character | what runs it from inside the menu. Any character is available, including `q` — the menu handles keys before the global quit. The space bar is not (it arrives as `space`), nor is a combining mark. |
| `description` | yes | string | all the menu and `?` can show, so it is what identifies the command. |
| `command` | yes | Go template | run as `sh -c`. See the placeholders below. |
| `output` | yes | `background`, `terminal` | `background` runs it detached, naming itself on the progress row, and reports the exit status when it finishes. `terminal` gives it the screen until it exits, the way attaching to a session does — for lazygit, an editor, a pager, or a build you mean to watch. There is deliberately no default: the modes behave differently enough that an implicit one would be a surprise. |
| `context` | no | `session` (default), `repo` | `session` runs in the agent's working directory and needs a started, unpaused session. `repo` runs in the repository root, which is available whatever the session is doing. |
| `confirm` | no | bool | ask before running. The dialog names the command and the directory. |

The context reaches your command two ways, and either is fine:

| Template | Environment | What it is |
|----------|-------------|------------|
| `{{ .Session.Title }}` | `$ATRIUM_TITLE` | the immutable title the branch and tmux session are named from |
| `{{ .Session.Name }}` | `$ATRIUM_SESSION` | the display label (the one `R` renames) |
| `{{ .Session.Branch }}` | `$ATRIUM_BRANCH` | the session's branch; empty for a direct session |
| `{{ .Session.Worktree }}` | `$ATRIUM_WORKTREE` | the isolated worktree, once the session has started |
| `{{ .Session.Port }}` | `$ATRIUM_PORT` | the session's [managed port](#managed-ports); empty unless its repo declares a `port_range` |
| `{{ .Repo.Path }}` | `$ATRIUM_REPO` | the repository root |
| `{{ .Repo.Name }}` | `$ATRIUM_REPO_NAME` | the repository's name, as the session list groups by |

The environment needs no quoting: `lazygit -p "$ATRIUM_WORKTREE"` cannot break argument
parsing however odd the path is. A template renders into a shell string, so quote a
value that might contain a space with the `quote` function —
`lazygit -p {{ quote .Session.Worktree }}`.

Each row says what it will do before you press it: `(terminal)` for a command that takes
the screen, `(repo)` for one that runs in the repository root rather than the worktree.
`?` lists the same markers.

A row that cannot run is dimmed with the reason rather than hidden, and pressing its key
says the same thing in the menu's footer: a `session`-context row on a paused session, or
any command that reads a value this session does not have (`gh pr checks
{{ .Session.Branch }}`, or `gh pr checks "$ATRIUM_BRANCH"`, on a session with no branch).
Both forms are covered — a command is refused whether it names the value as a placeholder
or as a variable — because an empty expansion is how `rm -rf "$ATRIUM_WORKTREE"/build`
becomes `rm -rf /build`. The check errs toward refusing: a name inside single quotes, or
in a comment, still counts.

One custom command runs at a time, whichever mode it is in; a second gets a notice.

A `terminal` command owns the screen while it runs, so `Ctrl+C` stops the command and leaves
Atrium running — as does `Ctrl+\`. `Ctrl+Z` still suspends the pair, and `fg` brings both
back. One caveat, because the shell sends those keys to every process Atrium started, not
only to your command: if something else was already working in the background — an
auto-name, an open-PR — interrupting your command interrupts that too, and you may see it
report a failure you did not cause. Nothing is lost; run it again. Your other sessions
keep being serviced throughout — queued prompts are still delivered and auto-yes still
answers — but the session list is not redrawn until the command exits, and it is swept
fresh on return.

Every run is recorded in the command log (`L`), under its key and description rather
than its rendered text — so a token in a command never lands in the log. A failure
raises a notice: a `background` command's output is in its record, and a `terminal`
command's went to the screen you were watching, so its record carries the exit status
alone.

A malformed entry is **dropped, not bound**: the rest still work. Atrium reports the
ones it refused in a modal at startup, and `atrium doctor` prints the same list, so a
placeholder typo or two entries claiming the same key is a message rather than a
command silently missing from the menu.

#### Configuration reference

Every `config.json` key, its default, and where it is documented above. Nearly all
are also editable live from the Settings panel (`,`). The exceptions are the three
account lists — `claude_accounts`, `gh_accounts`, `agy_accounts` — which the
one-value-per-row panel cannot express and which are managed from the Accounts
overlay instead, and the two deprecated keys whose successors own the row instead:
`nerd_font` (superseded by `glyph_set`) and `kill_double_tap_confirm` (superseded by
`double_tap_confirm`).
`profiles`, `custom_commands` and `repo_scripts` are lists of records too: the panel
gives `profiles` a record editor of its own under Profiles rather than a row, and the
other two are edited in `config.json` directly. `keybindings` is the same case one
step further — a whole keymap, and one a bad row would cost you the key you were
editing with. A test
(`config.TestReadmeDocumentsEveryConfigField`) fails the build if a new field is
added without a row here.

The panel groups these keys into ten categories — Sessions, Worktrees & git,
Appearance, Session list, Notifications, Automation, Input, Projects, Updates, and
Advanced — shown in the Category column below. A key with no panel row carries
`—` instead; `profiles` names its editor.

| Key | Category | Type | Default | Notes |
|-----|----------|------|---------|-------|
| `default_program` | Sessions | string | `"claude"` | launch command when no matching profile ([Profiles](#profiles)) |
| `auto_yes` | Automation | bool | `false` | auto-accept all prompts (experimental; the `-y` flag) |
| `daemon_poll_interval` | Automation | int | `1000` | autoyes daemon poll interval, milliseconds |
| `branch_prefix` | Worktrees & git | string | `"<user>/"` | prefix for created git branches |
| `profiles` | Profiles | array | detected | named program configs ([Profiles](#profiles)) |
| `custom_commands` | — | array | `[]` | your own verbs over the selected session: a key, a shell template, and where it runs. `!` opens the menu ([Custom commands](#custom-commands) documents every field) |
| `keybindings` | — | object | `{}` | remap the keymap: action name → key, list of keys, or `"disabled"` ([Remapping keys](#remapping-keys)) |
| `tmux_config_override` | Advanced | string | `""` | path to a custom tmux config for sessions |
| `auto_attach` | Sessions | bool | `true` | attach to a new session as soon as it starts ([Auto-attach](#auto-attach)) |
| `show_release_notes_after_update` | Updates | bool | `true` | "what's new" overlay once after an update |
| `double_tap_confirm` | Input | bool | `true` | a second press of the key that opened a confirmation confirms it, so `ctrl-x` `ctrl-x` kills and `P` `P` pushes in one motion. It gates the shortcut, not the dialog: the box and its warning are shown either way, and `y` still confirms. Armed on the kill, push, create-PR, merge, batch-pause, batch-resume and over-capacity-resume dialogs |
| `kill_double_tap_confirm` | — | bool | `true` | *deprecated* — superseded by `double_tap_confirm`; still read for back-compat (it decides when `double_tap_confirm` is unset, so an explicit `false` is not silently undone) |
| `theme` | Appearance | string | `"auto"` | color palette + border style. The default, `auto`, follows the terminal's own background (dark → `tokyo-night`, light → `tokyo-night-day`, no answer → `tokyo-night`). Name a palette to pin one: `tokyo-night` / `catppuccin-mocha` for a dark terminal, `tokyo-night-day` / `catppuccin-latte` for a light one, `unicode` (tokyo-night with square borders). A palette named explicitly never auto-switches |
| `splash` | Appearance | string | random | empty-state splash pattern (`""`/`"random"` = fresh each launch; `"off"` = no animation, just the wordmark) |
| `glyph_set` | Appearance | string | `"plain"` | icon fidelity rung: `nerd` (vendor Nerd-Font icons, needs a patched font), `plain` (Unicode that renders on any font — the default), `ascii` (7-bit floor for terminals where even plain Unicode shows tofu) |
| `image_preview` | Appearance | string | `"auto"` | how an agent-produced image opens when you uppercase-hint its path: `auto` (real pixels on kitty and Ghostty when Atrium is not inside tmux, block glyphs everywhere else — the default; running kitty *inside* tmux gets glyphs, because tmux does not forward the graphics protocol), `kitty` (attempt pixels even where the terminal isn't recognised — a terminal with the graphics protocol but not Unicode placeholders answers and then draws blanks or boxes, which nothing can detect, so switch to `glyph` if that happens; does **not** yet make tmux work either — the payload is not wrapped in tmux's passthrough envelope), `glyph` (never attempt pixels), `off` (no overlay; hinting an image path just copies it) |
| `nerd_font` | — | bool | `false` | *deprecated* — superseded by `glyph_set`; still read for back-compat (`true` → `glyph_set: nerd` when `glyph_set` is unset) |
| `session_context_bar` | Sessions | bool | `true` | thin tmux status line inside attached sessions |
| `hint_bar` | Appearance | bool | `true` | always-on bottom key-hint bar |
| `os_chrome` | Appearance | bool | `true` | fleet state in the terminal title + OSC 9;4 taskbar progress |
| `record_prompt_history` | Input | bool | `true` | remember submitted prompts for reuse in the create form and quick-send |
| `mouse` | Input | bool | `true` | mouse capture (clickable rows/tabs/hint bar, wheel, divider drag); `false` frees native select-to-copy |
| `max_sessions` | Sessions | int | auto (½ CPU threads) | session cap. Unset = host-aware soft cap on *live* sessions: a create or a resume that crosses it warns once, and a startup that would relaunch past it leaves the overflow paused instead (`r` / `ctrl+r` brings them back); `N` = hard cap on *every* session, paused included, refused when creating; `0` = unlimited (no warning) |
| `agent_oom_margin` | Advanced | int | `on (300)` | Linux only: raise each agent's `oom_score_adj` this far above the shared tmux server's so a kernel OOM kill sheds one recoverable session, not the server (every session). Unset = on (default margin); `N` = margin; `0` = off |
| `trust_worktrees_root` | Automation | bool | `false` | pre-accept Claude's workspace-trust for the worktrees root |
| `carry_files` | Worktrees & git | array | `[".claude/settings.local.json"]` | gitignored files copied into each worktree ([Carried files](#carried-files)) |
| `link_paths` | Worktrees & git | array | `[]` | gitignored paths symlinked into each worktree, e.g. `node_modules` ([Linked paths](#linked-paths)) |
| `repo_scripts` | — | array | `[]` | per-repository setup script, run command, port range and session environment, routed by remote/path ([Setup scripts](#setup-scripts)) |
| `pr_create_draft` | Worktrees & git | bool | `true` | `c` opens a draft PR |
| `update_base_on_create` | Worktrees & git | bool | `true` | branch off the freshest remote base tip |
| `fast_forward_local_base` | Worktrees & git | bool | `false` | also fast-forward the local base branch on create |
| `claude_accounts` | — | array | `[]` | per-session `CLAUDE_CONFIG_DIR` routing ([Claude accounts](#claude-accounts)) |
| `gh_accounts` | — | array | `[]` | per-session `GH_CONFIG_DIR` routing ([GitHub CLI accounts](#github-cli-accounts)) |
| `agy_accounts` | — | array | `[]` | per-session `agy` config directory routing via bwrap ([Antigravity accounts](#antigravity-accounts)) |
| `auto_update` | Updates | string | `"notify"` | startup update behavior: `notify` / `auto` / `off` ([Auto-update](#auto-update)) |
| `project_search_roots` | Projects | array | `["~"]` | directories the background repo scan walks for the project picker |
| `project_search_depth` | Projects | int | `3` | levels below each root the scan descends (`0`/negative disables it) |
| `model_indicator` | Session list | string | `"on"` | per-session model chip: `on` / `off` |
| `permission_indicator` | Session list | string | `"on"` | per-session permission-mode chip: `on` / `off` |
| `effort_indicator` | Session list | string | `"on"` | per-session reasoning-effort chip: `on` / `off` |
| `context_indicator` | Session list | string | `"percent"` | per-session transcript chip: `percent` / `count` / `bar` / `cost` / `off` (the occupancy modes fall back to a count when the model's window is unknown; `cost` is a list-rate estimate, not a bill) |
| `session_sort` | Session list | string | `"creation"` | within-group order: `creation` / `status` |
| `group_mode` | Session list | string | `"repo"` | list grouping: `repo` / `account` |
| `smart_dispatch_auto` | Automation | bool | `false` | let a confident `i` match create the session without the form |
| `notifications` | Notifications | string | `"off"` | background-session signal: `off` / `bell` / `desktop` / `osc` (SSH-friendly OSC 9) ([Notifications](#notifications)) |
| `notifications_finished` | Notifications | string | `"same"` | quieter rung for a *plain finished turn* only, so a blocked session — or one that stopped to ask — is never out-shouted: `same` / `off` / `bell` ([Notifications](#notifications)) |
| `notify_command` | Notifications | string | built-in | shell command for `desktop` notifications ([Notifications](#notifications)) |
| `notify_when_focused` | Notifications | bool | `false` | keep notifying while Atrium's terminal is focused; `false` stays silent while you're watching ([Notifications](#notifications)) |

##### `NO_COLOR`

Atrium follows [no-color.org](https://no-color.org): setting `NO_COLOR` to **any
non-empty value** — `1`, `true`, `yes`, even `0` — renders the whole UI without
colour, `theme` notwithstanding. Setting it to nothing (`NO_COLOR=`) is not a
request and leaves colour on.

Bold, italic and underline are kept, so the hierarchy that colour was carrying
survives. The thin tmux status band inside attached sessions goes monochrome too;
tmux draws that itself, so it is handled separately rather than by the renderer.

Note that many tools read `NO_COLOR` as a boolean and so ignore `NO_COLOR=yes`.
Atrium does not.

### FAQs

#### Failed to start new session

If you get an error like `failed to start new session: timed out waiting for tmux session`,
check tmux first: Atrium starts every session with `tmux new-session -e`, and a tmux older
than 3.2 rejects that flag inside the pty, so the only symptom is this timeout. Run
`atrium doctor` — it reports a too-old tmux explicitly. (Creating a session is refused up
front with a clearer message; resuming an existing one still reaches this timeout.)

If tmux is fine, update the underlying program (ex. `claude`) to the latest version.

#### Atrium is using a lot of CPU

Atrium's cost has two halves, and they need different tools.

**Its own work** — rebuilding the frame, classifying panes — is captured with a
signal. Send `SIGUSR1` to a running `atrium` and it writes CPU, heap and goroutine
profiles into the temp directory — that is `$TMPDIR` when it is set, which on
macOS is a per-user path like `/var/folders/…/T/`, not `/tmp`. (The log itself
lives in the data dir, not here: it is capped and rolls itself over, while nothing
prunes a profile.)

```bash
kill -USR1 "$(pgrep -x atrium)"     # start sampling (30s, then it stops itself)
kill -USR1 "$(pgrep -x atrium)"     # optional: stop early
go tool pprof "$(ls -t "${TMPDIR:-/tmp}"/atrium-cpu-*.pprof | head -1)"
```

Each finished run logs the exact path it wrote to, so `grep pprof ~/.atrium/atrium.log`
is the authoritative answer if the glob above finds nothing. Run `atrium debug` if
you are not sure where the log is — an install predating the rename keeps using
`~/.claude-squad`.

Set `ATRIUM_PPROF_SECONDS` (1–300, default 30) to change the sampling window. The
trigger is always armed — no flag, no restart — because the instance worth
profiling is the one already running. Quitting Atrium mid-run closes the profile
first, so a capture cut short by an exit is still readable.

**The subprocesses it launches** (git, tmux, gh) are invisible to that profile:
Atrium is blocked waiting on them, so they contribute nothing to its own CPU
samples. Press <kbd>L</kbd> for the command log, whose header attributes CPU by
command verb. To see both halves at once, on Linux:

```bash
# fields 14-17 of /proc/<pid>/stat: utime stime cutime cstime, in 10ms ticks
awk '{print "own:", $14+$15, " children:", $16+$17}' /proc/"$(pgrep -x atrium)"/stat
```

Taking that reading twice, a known interval apart, gives the split between
Atrium's own CPU and its subprocesses'.

### Security & verifying releases

Releases ship with SLSA build provenance and a keyless Sigstore signature over
the checksums file, plus a per-archive SBOM. To confirm a download is genuine:

```bash
gh attestation verify atrium_<version>_<os>_<arch>.tar.gz --repo ZviBaratz/atrium
```

See [SECURITY.md](SECURITY.md) for full verification steps (including `cosign`)
and how to report a vulnerability privately.

### How It Works

1. **tmux** to create isolated terminal sessions for each agent
2. **git worktrees** to isolate codebases so each session works on its own branch
3. A simple TUI interface for easy navigation and management

### License

[AGPL-3.0](LICENSE.md)

Atrium is a derivative work of [Claude Squad](https://github.com/smtg-ai/claude-squad) and remains licensed under the AGPL-3.0. See [NOTICE](NOTICE) for attribution.
