# Atrium [![Website](https://img.shields.io/badge/website-zvibaratz.github.io%2Fatrium-2ea44f)](https://zvibaratz.github.io/atrium/) [![CI](https://github.com/ZviBaratz/atrium/actions/workflows/build.yml/badge.svg)](https://github.com/ZviBaratz/atrium/actions/workflows/build.yml) [![GitHub Release](https://img.shields.io/github/v/release/ZviBaratz/atrium)](https://github.com/ZviBaratz/atrium/releases/latest) [![Go Report Card](https://goreportcard.com/badge/github.com/ZviBaratz/atrium)](https://goreportcard.com/report/github.com/ZviBaratz/atrium) [![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE.md) [![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/ZviBaratz/atrium/badge)](https://securityscorecards.dev/viewer/?uri=github.com/ZviBaratz/atrium)

Atrium is a terminal command center for orchestrating multiple AI coding agents — [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Gemini](https://github.com/google-gemini/gemini-cli), and other local agents including [Aider](https://github.com/Aider-AI/aider) — each in its own isolated git worktree, so you can drive several tasks at once from a single panel.

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

```
Usage:
  atrium [flags]
  atrium [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  debug       Print debug information like config paths
  doctor      Check the environment for common misconfigurations
  help        Help about any command
  profiles    Manage agent profiles (e.g. `profiles detect`)
  reset       Reset all stored instances
  update      Download, verify, and install the latest release
  version     Print the version number of atrium

Flags:
  -y, --autoyes          [experimental] If enabled, all instances will automatically accept prompts for claude code & aider
  -h, --help             help for atrium
  -p, --program string   Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')
```

Run the application with:

```bash
atrium
```
NOTE: The default program is `claude` and we recommend using the latest version.

<br />

<b>Using Atrium with other AI assistants:</b>
- For [Codex](https://github.com/openai/codex): Set your API key with `export OPENAI_API_KEY=<your_key>`
- Launch with specific assistants:
   - Codex: `atrium -p "codex"`
   - Aider: `atrium -p "aider ..."`
   - Gemini: `atrium -p "gemini"`
- Make this the default, by modifying the config file (locate with `atrium debug`)

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
| `@` | accounts (Claude / GitHub) |
| `L` | command log — the tmux / git / gh commands Atrium runs |
| `ctrl-l` | force a full redraw of the screen |
| `q` | quit |

#### Filtering

Press `/` to filter the session list incrementally. A query is split on
whitespace into terms combined with **AND**; each term is either a predicate over
cached session state or a plain substring matched (case-insensitively) against a
session's name, branch, or note. Predicate values match by **prefix**, so the
list narrows as you type rather than blinking empty mid-word.

| Term | Matches |
|------|---------|
| `status:<name>` | sessions whose status prefixes `<name>` — `running`, `ready`, `loading`, `paused`, `needsinput`, `pending` |
| `dirty` | sessions with uncommitted changes |
| `behind` | sessions behind their base branch |
| `behind:<expr>` | `behind:3` (exactly 3), `behind:>0`, `behind:>=2`, `behind:<5`, `behind:<=1` |
| `pr:<state>` | PR state prefixing `<state>` — `open`, `merged`, `closed`, or `none` (no PR) |
| `account:<name>` | Claude account name prefixing `<name>`; `account:none` for sessions with no resolved account |
| `note:<text>` | sessions whose note prefixes `<text>` |
| `effort:<level>` | reasoning-effort level prefixing `<level>` — `low`, `medium`, `high`, `xhigh`, `max`; `effort:none` for sessions with no resolved effort |
| `<text>` | plain substring in the session's name, branch, or note |

Worked examples (each is exercised verbatim against the parser by
`session.TestReadmeFilterExamples`):

- `status:need dirty` — sessions that need input **and** have uncommitted changes.
- `behind:>0 pr:open` — sessions behind their base **and** with an open PR.
- `account:work note:release` — `work`-account sessions whose note starts with `release`.
- `effort:max dirty` — sessions running at `max` effort **and** with uncommitted changes.
- `auth` — any session with `auth` in its name, branch, or note.

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

The session you're currently on — the selected row, or one you're attached to —
never notifies, so only agents you've navigated away from can interrupt you.

```json
{
  "notifications": "desktop",
  "notify_command": "notify-send \"Atrium\" \"$ATRIUM_SESSION $ATRIUM_STATUS\""
}
```

`notify_command`, when set, runs via `sh -c` for each desktop notification with
`$ATRIUM_SESSION` (the session's display name), `$ATRIUM_STATUS`
(`Ready`/`NeedsInput`), and `$ATRIUM_EVENT` (`finished`/`needs_input`) in its
environment — the session name rides in the environment, never interpolated into
the command, so it can't break argument parsing. Use it for `terminal-notifier`,
webhooks (`curl`), or any custom notifier. A failing command is logged, never
fatal. Both settings are also editable live from the Settings panel (`,`).

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
opt out. Entries must be gitignored in the project — anything else is skipped
with a warning, because pausing a session commits its worktree and a
non-ignored file would leak into the session branch.

Carried files are re-seeded from the original checkout whenever the worktree
is created, including on resume after a pause — edits made to them inside a
session do not survive a pause/resume cycle.

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
- The resolved account is **pinned at session creation** and shown as a badge in
  the session list (dim for the default account, accented for a routed one). It
  is injected once at launch and is not re-resolved on restart or `--continue`;
  editing `claude_accounts` affects only newly created sessions.
- When more than one account is configured, the new-session form shows an
  **Account** picker, preset to the auto-routed account, to override the choice.
- Omitting `claude_accounts` disables the feature entirely (no badge, no
  injection), so existing configs are unaffected.

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

#### Configuration reference

Every `config.json` key, its default, and where it is documented above. Most are
also editable live from the Settings panel (`,`); the three marked **†** are
JSON-only and have no Settings row. A test
(`config.TestReadmeDocumentsEveryConfigField`) fails the build if a new field is
added without a row here.

| Key | Type | Default | Notes |
|-----|------|---------|-------|
| `default_program` | string | `"claude"` | launch command when no matching profile ([Profiles](#profiles)) |
| `auto_yes` | bool | `false` | auto-accept all prompts (experimental; the `-y` flag) |
| `daemon_poll_interval` | int | `1000` | autoyes daemon poll interval, milliseconds |
| `branch_prefix` | string | `"<user>/"` | prefix for created git branches |
| `profiles` | array | detected | named program configs ([Profiles](#profiles)) |
| `tmux_config_override` | string | `""` | path to a custom tmux config for sessions |
| `auto_attach` | bool | `true` | attach to a new session as soon as it starts ([Auto-attach](#auto-attach)) |
| `show_release_notes_after_update` | bool | `true` | "what's new" overlay once after an update |
| `kill_double_tap_confirm` | bool | `true` | a second `ctrl-x` confirms the kill dialog |
| `theme` | string | `"tokyo-night"` | color palette + border style |
| `splash` | string | random | empty-state splash pattern (`""`/`"random"` = fresh each launch) |
| `glyph_set` | string | `"plain"` | icon fidelity rung: `nerd` (vendor Nerd-Font icons, needs a patched font), `plain` (Unicode that renders on any font — the default), `ascii` (7-bit floor for terminals where even plain Unicode shows tofu) |
| `nerd_font` | bool | `false` | *deprecated* — superseded by `glyph_set`; still read for back-compat (`true` → `glyph_set: nerd` when `glyph_set` is unset) |
| `session_context_bar` | bool | `true` | thin tmux status line inside attached sessions |
| `hint_bar` | bool | `true` | always-on bottom key-hint bar |
| `os_chrome` | bool | `true` | fleet state in the terminal title + OSC 9;4 taskbar progress |
| `record_prompt_history` | bool | `true` | remember submitted prompts for reuse in the create form and quick-send |
| `mouse` | bool | `true` | mouse capture (clickable rows/tabs/hint bar, wheel, divider drag); `false` frees native select-to-copy |
| `max_sessions` | int | unlimited | opt-in cap on concurrent sessions |
| `trust_worktrees_root` | bool | `false` | pre-accept Claude's workspace-trust for the worktrees root |
| `carry_files` | array | `[".claude/settings.local.json"]` | gitignored files copied into each worktree ([Carried files](#carried-files)) |
| `pr_create_draft` | bool | `true` | `c` opens a draft PR |
| `update_base_on_create` | bool | `true` | branch off the freshest remote base tip |
| `fast_forward_local_base` | bool | `false` | also fast-forward the local base branch on create |
| `claude_accounts` | array | `[]` | per-session `CLAUDE_CONFIG_DIR` routing ([Claude accounts](#claude-accounts)) |
| `gh_accounts` | array | `[]` | per-session `GH_CONFIG_DIR` routing ([GitHub CLI accounts](#github-cli-accounts)) |
| `auto_update` | string | `"notify"` | startup update behavior: `notify` / `auto` / `off` ([Auto-update](#auto-update)) |
| `project_search_roots` **†** | array | `["~"]` | directories the background repo scan walks for the project picker |
| `project_search_depth` **†** | int | `3` | levels below each root the scan descends (`0`/negative disables it) |
| `model_indicator` | string | `"on"` | per-session model chip: `on` / `off` |
| `permission_indicator` | string | `"on"` | per-session permission-mode chip: `on` / `off` |
| `effort_indicator` | string | `"on"` | per-session reasoning-effort chip: `on` / `off` |
| `session_sort` | string | `"creation"` | within-group order: `creation` / `status` |
| `group_mode` | string | `"repo"` | list grouping: `repo` / `account` |
| `smart_dispatch_auto` **†** | bool | `false` | let a confident `i` match create the session without the form |
| `notifications` | string | `"off"` | background-session signal: `off` / `bell` / `desktop` ([Notifications](#notifications)) |
| `notify_command` | string | built-in | shell command for `desktop` notifications ([Notifications](#notifications)) |

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
