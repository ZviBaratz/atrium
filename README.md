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

- [tmux](https://github.com/tmux/tmux/wiki/Installing)
- [gh](https://cli.github.com/)

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
| `doctor` | Check core dependencies (tmux, git, gh) and agent CLI heuristic versions |
| `profiles` | Manage agent profiles (e.g. `profiles detect`) |
| `debug` | Print debug information like config paths |
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

`ls`, `peek` and `send` are the headless surface: three primitives — list the
fleet, read a screen, send a message — that let a script or an orchestrator agent
drive Atrium without a TTY. None of them start the TUI, take its lock, or need a
terminal.

**`atrium ls`** prints a table; `--json` emits an array for `jq`. It reads stored
state and never touches tmux, so it works with no tmux server running at all.

```bash
atrium ls
atrium ls --json | jq -r '.[] | select(.status == "needs-input") | .title'
```

Status, diff figures and queue depth are **last-known** values recorded by the
running TUI, not live probes. Each entry carries `updated_at` so a consumer can
judge how stale they are; with no Atrium running they stop advancing.

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
| `created_at`, `updated_at` | RFC 3339, or `null` when unrecorded |
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

`ls` and `peek` only ever read: they will not create, rewrite, or clean up
anything in the data directory, so running `atrium ls` on a loop alongside a live
Atrium is safe.

If a title exists in more than one repo, any of the three commands will report
the ambiguity and list the candidates; `--path <repo>` picks one.

All three exit 0 on success and 1 on failure, with the reason on stderr.

<br />

#### Keybindings

Press `?` in the app for the same cheatsheet, live. This table mirrors it group
for group; a test (`keys.TestReadmeDocumentsEveryBinding`) fails the build if the
in-app keymap and this section ever drift apart, so it stays complete.

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
| `ctrl-x` | kill the selected/attached session (twice to confirm) |
| `ctrl-pgup/pgdn` | in a session: cycle to prev / next session in the repo group |
| `s` | send a message (without attaching) |
| `C` | diff tab: comment on a line or range → queue it to the agent (↑↓/j/k move, shift+↑↓/J/K extend, enter comment, esc exit) |
| `Q` | manage queued prompts (list / cancel) |
| `a` | approve the agent's prompt (`↵` picks its default); on idle claude, accept the suggested prompt |
| `p` | pause: commit changes + free the worktree |
| `ctrl-p` | pause all active sessions in the current view |
| `P` | commit & push branch |
| `c` | create a PR for the pushed branch (gh) |
| `m` | merge the session's PR (squash) |
| `w` | open the session's PR in the browser |
| `r` | resume a paused session |
| `ctrl-r` | resume all paused sessions in the current view |
| `y` | copy branch name to clipboard (works over SSH — see [Clipboard](#clipboard)) |
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
| `,` | settings |
| `@` | accounts (Claude / GitHub / Antigravity) |
| `L` | command log — the tmux / git / gh commands Atrium runs |
| `ctrl-l` | force a full redraw of the screen |
| `q` | quit |

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

#### Mouse

Mouse capture is on by default: clickable session rows, repo headers, tabs, and hint-bar entries; wheel scrolling; and a draggable list/preview divider. If your terminal's native select-to-copy matters more than in-app clicking, hold **Shift** while dragging to select text past the capture, or turn the mouse off entirely:

```json
{
  "mouse": false
}
```

With `mouse` off, Atrium never enables mouse reporting, so selection, copy, and paste behave exactly as they would in any non-mouse program. The setting is also togglable live from the Settings panel (`,`).

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

Both events — a turn finishing, and a session blocking on you — use that one mode
by default. `notifications_finished` splits them into an **attention ladder**, so
the agent that actually needs you is never out-shouted by one that merely stopped:

- `"same"` (default) — a finished turn uses the `notifications` mode too, exactly
  as before.
- `"off"` — a finished turn sends nothing out-of-band. This is quieter, not silent:
  the row still carries its unread marker, and `u` still jumps to it.
- `"bell"` — a finished turn just rings the terminal, while a session blocking on
  you gets the fuller `notifications` mode (and `b` jumps to it).

A blocked session always uses `notifications` itself, and only rungs quieter than
every mode are offered, so a finished turn can never outrank a blocked one.
`"desktop"` and `"osc"` are deliberately not accepted here — they are peers of each
other, so neither is "one rung quieter" than the other; anything unrecognized reads
as `"same"`. `notifications: "off"` remains the master switch: it silences both
events whatever `notifications_finished` says.

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
(`Ready`/`NeedsInput`), and `$ATRIUM_EVENT` (`finished`/`needs_input`) in its
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

A copy only reports failure when **both** paths are unavailable, and the message
names the next step (install a clipboard utility, or use a terminal with OSC 52
support).

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

On first run, Atrium probes for installed agent CLIs (`claude`, `codex`, `gemini`, `aider`) and seeds a profile for each one it finds. After installing a new agent, run:

```bash
atrium profiles detect
```

to add it as a profile — existing profiles and your default program are never modified.

To configure profiles by hand, add a `profiles` array to your config file and set `default_program` to the name of the profile to select by default:

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
copying for a large tree, but it is the one place a session is deliberately not
isolated: link paths whose contents you are content to share, and prefer
`carry_files` when a session needs its own copy.

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
  Antigravity). The `default` / `catch-all (unreachable)` / `routed` badges
  update live as rows move, and the new order is saved to `config.json`
  immediately.
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

  Setting it does two things. **Creating or resuming** a session whose config dir
  holds a *different* login is **refused**, naming both logins and the directory to
  fix — a wrong launch cannot be undone by noticing it afterwards, and a resumed
  session re-injects the same directory, so it is gated identically. And `atrium
  doctor`'s **Claude account identities** section marks the account verified rather
  than unpinned.

  Only a confirmed mismatch refuses. An account with no `expect_account` is never
  blocked, and a directory with no login recorded is allowed through — `claude` will
  prompt for login in the pane, which cannot silently mis-bill, and refusing would
  strand you mid-onboarding. Comparison is on email, case- and whitespace-insensitive.

  Separately, and with no configuration at all, `doctor` warns when two accounts you
  believe are separate turn out to hold the **same** login, naming which one the
  combined work is billing. There is no field for `expect_account` in the `@` accounts
  overlay; edits there preserve it.
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
  account to toggle it limited/available. Rotation skips a limited member and
  cycles only through the rest. The flag is indefinite: it stays until you press
  `l` again to clear it (a per-account reset time that auto-expires is a planned
  follow-up).
- The new-session form's **Account** picker lets you override routing per
  session: pick the `<pool> ⇄` entry to rotate across the pool, or a specific
  member (shown indented under it) to pin that account for this one session —
  which bypasses availability, so it works even on a flagged member.
- If **every** member of the routed pool is flagged limited, creating a session
  from the new-session form shows a confirm ("all `<pool>` accounts are
  rate-limited … create anyway on `<member>`?") instead of silently spawning on
  a limited account. Declining leaves the draft in place and creates nothing;
  accepting pins the member whose limit resets soonest — which, while flags are
  indefinite-only, is the first pool member. Smart auto-dispatch skips this
  all-exhausted gate, so a fully-limited pool can still spawn silently there
  (#483).
- Press `t` in the `@` accounts overlay to preview where an input (remote URL
  and/or path) would route right now, without creating anything. When the
  matched account belongs to a pool, the `Claude →` line names the member a
  session would actually take, and a block beneath it (`pool '<name>' ⇄`)
  lists the pool's members with their available/limited chip, marking the
  one it picked `← next` — and, when getting there meant skipping a limited
  member, naming why, e.g. `work-1 limited → rotating to work-2`. If every
  member is limited, the `Claude →` line instead shows
  `⚠ all '<pool>' accounts limited`, and the block marks the member that
  accepting the create-form's confirm would pin, with `← on confirm` — the
  one whose limit resets soonest, which, while flags are indefinite-only, is
  the first pool member. The preview only reads availability and the
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
  at session creation; editing `agy_accounts` affects only newly created sessions.
- Isolation is implemented with [bwrap](https://github.com/containers/bubblewrap)
  (bubblewrap), which bind-mounts the account's `config_dir` over
  `~/.gemini/antigravity-cli` for that session only. **This is Linux-only** — bwrap
  is a Linux user-namespace tool, so the routing is a no-op on macOS. If `bwrap` is
  not installed, the session still starts but runs against the ambient config (a
  warning is logged); install bubblewrap to get isolation.
- Omitting `agy_accounts` (or a non-matching session with no catch-all) leaves `agy`
  on its ambient config, exactly as before.

#### Configuration reference

Every `config.json` key, its default, and where it is documented above. Nearly all
are also editable live from the Settings panel (`,`). The exceptions are the four
keys whose value is a *list of records* — `profiles`, `claude_accounts`,
`gh_accounts`, `agy_accounts` — which the one-value-per-row panel cannot express
(the accounts are instead managed from the Accounts overlay), and the
deprecated `nerd_font`, which `glyph_set` supersedes. A test
(`config.TestReadmeDocumentsEveryConfigField`) fails the build if a new field is
added without a row here.

The panel groups these keys into ten categories — Sessions, Worktrees & git,
Appearance, Session list, Notifications, Automation, Input, Projects, Updates, and
Advanced — shown in the Category column below. The five keys with no panel row carry
`—` instead.

| Key | Category | Type | Default | Notes |
|-----|----------|------|---------|-------|
| `default_program` | Sessions | string | `"claude"` | launch command when no matching profile ([Profiles](#profiles)) |
| `auto_yes` | Automation | bool | `false` | auto-accept all prompts (experimental; the `-y` flag) |
| `daemon_poll_interval` | Automation | int | `1000` | autoyes daemon poll interval, milliseconds |
| `branch_prefix` | Worktrees & git | string | `"<user>/"` | prefix for created git branches |
| `profiles` | — | array | detected | named program configs ([Profiles](#profiles)) |
| `tmux_config_override` | Advanced | string | `""` | path to a custom tmux config for sessions |
| `auto_attach` | Sessions | bool | `true` | attach to a new session as soon as it starts ([Auto-attach](#auto-attach)) |
| `show_release_notes_after_update` | Updates | bool | `true` | "what's new" overlay once after an update |
| `kill_double_tap_confirm` | Input | bool | `true` | a second `ctrl-x` confirms the kill dialog |
| `theme` | Appearance | string | `"tokyo-night"` | color palette + border style |
| `splash` | Appearance | string | random | empty-state splash pattern (`""`/`"random"` = fresh each launch) |
| `glyph_set` | Appearance | string | `"plain"` | icon fidelity rung: `nerd` (vendor Nerd-Font icons, needs a patched font), `plain` (Unicode that renders on any font — the default), `ascii` (7-bit floor for terminals where even plain Unicode shows tofu) |
| `nerd_font` | — | bool | `false` | *deprecated* — superseded by `glyph_set`; still read for back-compat (`true` → `glyph_set: nerd` when `glyph_set` is unset) |
| `session_context_bar` | Sessions | bool | `true` | thin tmux status line inside attached sessions |
| `hint_bar` | Appearance | bool | `true` | always-on bottom key-hint bar |
| `os_chrome` | Appearance | bool | `true` | fleet state in the terminal title + OSC 9;4 taskbar progress |
| `record_prompt_history` | Input | bool | `true` | remember submitted prompts for reuse in the create form and quick-send |
| `mouse` | Input | bool | `true` | mouse capture (clickable rows/tabs/hint bar, wheel, divider drag); `false` frees native select-to-copy |
| `max_sessions` | Sessions | int | auto (½ CPU threads) | session cap. Unset = host-aware soft cap on *live* sessions, warning once when a create or a resume crosses it; `N` = hard cap on *every* session, paused included, refused when creating; `0` = unlimited (no warning) |
| `agent_oom_margin` | Advanced | int | `on (300)` | Linux only: raise each agent's `oom_score_adj` this far above the shared tmux server's so a kernel OOM kill sheds one recoverable session, not the server (every session). Unset = on (default margin); `N` = margin; `0` = off |
| `trust_worktrees_root` | Automation | bool | `false` | pre-accept Claude's workspace-trust for the worktrees root |
| `carry_files` | Worktrees & git | array | `[".claude/settings.local.json"]` | gitignored files copied into each worktree ([Carried files](#carried-files)) |
| `link_paths` | Worktrees & git | array | `[]` | gitignored paths symlinked into each worktree, e.g. `node_modules` ([Linked paths](#linked-paths)) |
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
| `session_sort` | Session list | string | `"creation"` | within-group order: `creation` / `status` |
| `group_mode` | Session list | string | `"repo"` | list grouping: `repo` / `account` |
| `smart_dispatch_auto` | Automation | bool | `false` | let a confident `i` match create the session without the form |
| `notifications` | Notifications | string | `"off"` | background-session signal: `off` / `bell` / `desktop` / `osc` (SSH-friendly OSC 9) ([Notifications](#notifications)) |
| `notifications_finished` | Notifications | string | `"same"` | quieter rung for a *finished turn* only, so a blocked session is never out-shouted: `same` / `off` / `bell` ([Notifications](#notifications)) |
| `notify_command` | Notifications | string | built-in | shell command for `desktop` notifications ([Notifications](#notifications)) |
| `notify_when_focused` | Notifications | bool | `false` | keep notifying while Atrium's terminal is focused; `false` stays silent while you're watching ([Notifications](#notifications)) |

### FAQs

#### Failed to start new session

If you get an error like `failed to start new session: timed out waiting for tmux session`, update the
underlying program (ex. `claude`) to the latest version.

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
