# Atrium [![Website](https://img.shields.io/badge/website-zvibaratz.github.io%2Fatrium-2ea44f)](https://zvibaratz.github.io/atrium/) [![CI](https://github.com/ZviBaratz/atrium/actions/workflows/build.yml/badge.svg)](https://github.com/ZviBaratz/atrium/actions/workflows/build.yml) [![GitHub Release](https://img.shields.io/github/v/release/ZviBaratz/atrium)](https://github.com/ZviBaratz/atrium/releases/latest) [![Go Report Card](https://goreportcard.com/badge/github.com/ZviBaratz/atrium)](https://goreportcard.com/report/github.com/ZviBaratz/atrium) [![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE.md) [![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/ZviBaratz/atrium/badge)](https://securityscorecards.dev/viewer/?uri=github.com/ZviBaratz/atrium)

Atrium is a terminal command center for orchestrating multiple AI coding agents — [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Gemini](https://github.com/google-gemini/gemini-cli), and other local agents including [Aider](https://github.com/Aider-AI/aider) — each in its own isolated git worktree, so you can drive several tasks at once from a single panel.

![Atrium Screenshot](assets/screenshot.png)

### Highlights
- Complete tasks in the background (including yolo / auto-accept mode!)
- Manage instances and tasks in one terminal window
- Review changes before applying them, pause sessions to pick their branches up elsewhere
- Each task gets its own isolated git workspace, so no conflicts

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

```bash
go install github.com/ZviBaratz/atrium@latest
```

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
  help        Help about any command
  reset       Reset all stored instances
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

#### Menu
The menu at the bottom of the screen shows available commands:

##### Instance/Session Management
- `n` - Create a new session (form focused on the name)
- `N` - Create a new session (form focused on the project picker)
- `ctrl-x` - Kill the selected session (press twice to confirm)
- `↑/k`, `↓/j` - Navigate between sessions
- `/` - Filter sessions

##### Actions
- `↵/o` - Attach to the selected session
- `ctrl-q` - Toggle attach/detach (detach when attached, attach from the list)
- `s` - Send a message to the selected session without attaching
- `p` - Pause: commit changes and free the worktree
- `P` - Commit and push the session branch
- `r` - Resume a paused session
- `y` - Copy the session's branch name to the clipboard
- `?` - Show help menu

##### Navigation
- `tab` / `shift-tab` - Cycle the preview / diff / terminal panes
- `1` / `2` / `3` - Jump straight to a pane
- `shift-↓/↑` - Scroll the active pane
- `,` - Settings
- `q` - Quit the application

### Configuration

Atrium stores its configuration in `~/.atrium/config.json`. You can find the exact path by running `atrium debug`. Installs that predate the rename keep using their existing `~/.claude-squad` directory automatically.

#### Auto-attach

By default, Atrium attaches you to a new session as soon as it starts, so you land directly in the agent. Detach with `ctrl-q` to return to the session list. When you create a session with the `N` form and provide an initial prompt, auto-attach is skipped — the session stays in the list so the prompt is delivered automatically once the agent is ready, and you can attach with `↵`/`o` whenever you like.

To disable auto-attach and always return to the list after creating a session, set `auto_attach` to `false`:

```json
{
  "auto_attach": false
}
```

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
