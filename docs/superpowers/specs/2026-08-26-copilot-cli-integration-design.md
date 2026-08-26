# GitHub Copilot CLI Integration

**Date:** 2026-08-26
**Status:** Stage 1 LANDED (2026-08-26). All three surfaces are driven — the two dialogs, and
the busy marker on the re-driven ladder that replaced the invalid one — and each is pinned to
a verbatim width ladder in `copilot_pane_test.go`. The wall-stripping scan
(`flattenBottomBox`) is anchored on `bottomBoxBlock` rather than the whole pane. What Stage 1
did NOT close is below under NOT MEASURED; `HookSupport` waits on #773, the transcript
readers on Stage 3, and adapter-declared launch knobs on #816.
**Driven against:** GitHub Copilot CLI **1.0.80** (npm `@github/copilot`), Linux

## Motivation

Atrium orchestrates several agent CLIs, and the tier gap between claude and everything
else is wide: the transcript readers in `session/transcript` and the status-hook channel
guarded by the adapter's `HookSupport` field are claude-only. Adding GitHub Copilot CLI
is the occasion to close that gap generically rather than special-case a second agent,
because its surface maps onto Atrium's claude-tier features almost one for one.

The precipitating reason is billing. GitHub AI Credits, which an organizational Copilot
subscription carries, are spent by GitHub's own surfaces — Copilot Chat, the Copilot
coding agent, and Copilot CLI. They cannot fund OpenAI's Codex CLI: OpenAI closed the
request for Copilot-subscription login as `not_planned` (openai/codex#8361), and a
maintainer's comment there records that Copilot's models are not hosted compatibly with
the Codex harness. So Copilot CLI is the CLI that spends those credits, and `codex` is
not.

`codex` is also not where Atrium's gap is. Its adapter — the `codex` var in
`session/agent/registry.go` — is among the better-driven ones, pinned at 0.147.0 with
fixtures in `session/agent/codex_pane_test.go`.

## Scope

**In scope:** a `copilot` adapter; transcript-derived data (credits, context, model,
preview, resume detection); authoritative status and the session brief; and adapter-declared
launch knobs (issue #816's second declaration).

**Out of scope:** multi-account routing and account pools. See "Auth does not live in
COPILOT_HOME" — the mechanism that would serve them is not the one this spec builds.

## Evidence tiers

Every row below is one of three things, and they must not be run together. This repo's
bar is a live probe at a named version, because a heuristic that never matches fails
silently — the doctrine in `session/agent/registry.go`'s package header, and the reason
`sessionStartMatcher` records probed rows rather than asserting a schema.

- **DRIVEN** — observed on 1.0.80 on this machine, in an isolated `COPILOT_HOME` against
  a scratch git repo. Reproducible.
- **VENDOR** — from the CLI's own `--help` and `help <topic>` output at 1.0.80, or from
  github/copilot-cli's `changelog.md`. Authoritative as a record, but not observed.
- **NOT MEASURED** — named here so it cannot pass as covered.

## What was driven

### Install and resolution — DRIVEN

`npm install -g @github/copilot` yields `copilot --version` = "GitHub Copilot CLI
1.0.80". The binary resolves on the bare `PATH` (through a mise shim here), so
`detectAgentCommand` in `config/agents.go` needs only its plain `exec.LookPath` branch —
not the shell-profile-aware probe claude requires. Add `"copilot"` to `knownAgentBins`.

### COPILOT_HOME isolates config and state — DRIVEN

`COPILOT_HOME` overrides the directory holding configuration and state, defaulting to
`$HOME/.copilot` (VENDOR: `copilot help environment`). Pointed at a fresh empty
directory it populated, without error:

```
config.json                       settings live in settings.json; this is managed state
session-store.db  (+ -wal, -shm)  SQLite index
session-state/<uuid>/             per-session directory, see below
installed-plugins
logs/
```

It did **not** fail on absent skills, plugins, agents or instructions, and it did not
sign out. So a per-session Atrium-owned `COPILOT_HOME` is safe, and the hazard
`scripts/drive-agent.sh` documents for gemini's `GEMINI_CLI_HOME` does not transfer.
What such a session loses is the user's skills and plugins, which is a graceful
degradation, not a failure.

### Auth does not live in COPILOT_HOME — DRIVEN

Two probes with an empty `COPILOT_HOME` both completed billed turns: one with the token
variables blanked, one with a deliberately bogus `COPILOT_GITHUB_TOKEN`. The second
logged "Classic PATs are not supported. Please use fine-grained PATs or other supported
token types" and then succeeded anyway, so the credential came from elsewhere — the OS
keyring, which is outside `COPILOT_HOME`.

Two consequences, in opposite directions:

- Per-session `COPILOT_HOME` **cannot** sign a session out. This is what makes the
  hook-injection design below safe.
- Per-session `COPILOT_HOME` **cannot** route two sessions to two Copilot accounts
  either. Account routing would need a fine-grained PAT per account via
  `COPILOT_GITHUB_TOKEN`, which is a different mechanism from the one claude's
  `CLAUDE_CONFIG_DIR` accounts use. That is why accounts are out of scope here.
- Classic PATs are rejected outright, so any token Atrium injects must be fine-grained
  with the "Copilot Requests" permission.

### Hooks fire, and the output schema is NOT claude's — DRIVEN

This is the finding that justifies Stage 0 existing.

The **invocation** schema is claude-compatible. Written into `config.json` under an
Atrium-owned `COPILOT_HOME`, claude's nested shape is accepted verbatim, keyed by
Copilot's camelCase event names:

```json
{ "hooks": { "sessionStart": [ { "hooks": [ { "type": "command", "command": "…" } ] } ] } }
```

A `sessionStart` hook and an `agentStop` hook both fired.

The **output** schema is not claude-compatible, and the difference is silent. Emitting
claude's nested `hookSpecificOutput.additionalContext` fired the hook and delivered
nothing — the model, asked for the injected magic word, said it could not find one.
Emitting a flat top-level object delivered it, on both `sessionStart` and
`userPromptSubmitted`:

```json
{ "additionalContext": "…" }
```

So `buildHookSettings`' event wiring can be generalized, but its payload emitter cannot
be shared with claude's. Had this been inferred from claude's schema, the brief would
have shipped registered, documented and dead — the exact failure #773 was filed about.

### The session store is JSONL, not SQLite — DRIVEN

Each session gets `session-state/<uuid>/` holding:

```
events.jsonl              the event stream — the transcript
workspace.yaml            session identity: id, cwd, git_root, branch, name,
                          user_named, created_at, updated_at, client_name
session.db                SQLite (index/cache; not needed for any reader below)
checkpoints/index.md      checkpoints
rewind-file-snapshots/    file-history tracking
```

`events.jsonl` means no cgo SQLite dependency is required for any transcript feature.
`workspace.yaml` carries a **literal, unsanitized `cwd`** plus `git_root` and `branch`,
so the collision `ProjectDir` documents for claude — distinct working dirs flattening to
one project dir — does not arise. Session lookup for a worktree is an exact `cwd` match,
disambiguated by `updated_at`.

Event records share one envelope (`type`, `data`, `id`, `timestamp`, `parentId`) and the
types observed map onto what Atrium needs:

| Event | Carries | Serves |
|---|---|---|
| `session.start` | `version: 1`, `copilotVersion`, `context.{cwd,gitRoot,branch}` | identity; a schema version to pin |
| `assistant.turn_start` / `turn_end` | `turnId` | authoritative Working/Ready |
| `assistant.idle` | — | idle confirmation |
| `session.usage_checkpoint` | `totalNanoAiu`, `totalPremiumRequests`, `modelCacheState[].modelId` | the credits meter; resolved model |
| `session.model_change` | `newModel`, `reasoningEffort`, `contextTier` | model and effort chips |
| `session.auto_mode_resolved` | `chosenModel`, `reasoningBucket` | the model `auto` resolved to |
| `user.message` / `assistant.message` / `system.message` | `content`, `model` | transcript render; last question asked |

`session.start` carrying an explicit `version` is worth pinning: it is a stability signal
the vendor maintains, and a better drift tripwire than `VerifiedVersion` alone can be.

### Authoritative status may not need hooks at all — DRIVEN

`assistant.turn_start` / `assistant.turn_end` / `assistant.idle` in `events.jsonl` are
the same signal claude needs injected hooks to report. Tailing the file is strictly less
fragile than hook injection, since it involves no vendor config, no trust model, and no
payload schema. Prefer it, and keep hooks for what only they can do — injecting the
brief, which the probe above proves is available on `sessionStart`.

### Non-interactive output — DRIVEN

`-p/--prompt` runs one prompt and exits; `--allow-all-tools` is required for it.
`--output-format json` emits the same event vocabulary as `events.jsonl`, marks
transient records `ephemeral: true`, and terminates with one `result` record carrying
`exitCode`, `usage.premiumRequests`, `sessionDurationMs` and
`codeChanges.{linesAdded,linesRemoved,filesModified}`. `-s/--silent` prints only the
agent response.

That makes headless naming cleaner than claude's: read the last `assistant.message`'s
`content`, or take `-s` output whole. Note that a run loads the built-in GitHub MCP
server; `--disable-builtin-mcps` should cut naming latency.

The default text footer also reports the meter directly:

```
Changes    +0 -0
AI Credits 0.25 (6s)
Tokens     ↑ 13.2k (5.6k cached) • ↓ 208 (192 reasoning)
Resume     copilot --resume=<session-id>
```

### Flags relevant to Atrium — VENDOR (`copilot --help` at 1.0.80)

| Flag | Use |
|---|---|
| `--continue` | resume most recent session; `-r/--resume[=value]` takes an id, prefix or name |
| `-i, --interactive <prompt>` | start interactive **and** run a prompt — a first-prompt delivery path that bypasses composer typing entirely |
| `--model <model>` | `auto` or a model name; also `COPILOT_MODEL` |
| `--effort, --reasoning-effort` | `none minimal low medium high xhigh max` |
| `--mode <mode>` | `interactive plan autopilot`; also `--plan`, `--autopilot` |
| `--allow-all-tools` / `--allow-all` / `--yolo` | permission posture; also `COPILOT_ALLOW_ALL` |
| `--context <tier>` | `default` or `long_context` |
| `--max-ai-credits <credits>` | per-session credit cap |
| `-n, --name <name>` | name the session |
| `--no-color`, `--log-dir`, `--log-level`, `-C <dir>` | capture and placement hygiene |
| `--session-id <id>` | resume by id, or set the UUID for a new session |

`--effort`'s levels are close enough to claude's that issue #816's declaration can likely
share a field kind.

### Paste collapsing — VENDOR, needs a driven capture

The `compactPaste` setting (default `true`) renders a paste of more than ten lines as
`[Paste #N - X lines]`. That is a `PasteCollapsed` case, as claude has for its own chip
shape, and without one a queued paste would not be recognised as landed. The literal
above is VENDOR; the matcher needs the real chip captured off a pane.

### The TUI, driven at the width ladder — DRIVEN

Driven 2026-08-26 on 1.0.80 at widths 120/60/40/34/28/26/24/20, in an isolated
`COPILOT_HOME` with the work token injected via `ATR_CAP_ENV`. Captures and emitted
fixtures preserved outside the run root, since `drive-agent`'s `down` removes it. The
organization's usage page reported 12 AI credits for the whole of Stage 0.

**Composer.** The glyph is `❯` (U+276F), which the package default set already accepts, so
`InputBoxPrompts` stays nil. The composer is delimited by horizontal rules above and below
rather than a box, so there are no vertical borders on its own line.

**Busy marker — DRIVEN at all eight widths, 2026-08-26 (the second ladder).** The status
row replaces the hint row *below* the composer, so `MarkerWindow` stays 0 and the footer
anchor finds it — the claude arrangement, not codex's. At the wide rungs the row reads
`<spinner> Working · <N> B esc interrupt`, the spinner animating through `● ◉ ◎` across the
sweep and the byte counter growing throughout.

The ladder is valid on the two checks the first one failed: the byte counter grows
monotonically at every rung — 544 B, 1.0, 1.5, 1.9, 2.4, 2.9, 3.4, 3.8 KiB — and the
spinner is painted at all eight, so the turn outlived the sweep. Note the unit changes from
`B` to `KiB` between the first two rungs, which is why no marker may key on it.

| width | status row, verbatim | `Working` | `esc interrupt` |
|---|---|---|---|
| 120 | `● Working · 544 B esc interrupt` | contiguous | contiguous |
| 60 | `◉ Working · 1.0 KiB esc interrupt` | contiguous | contiguous |
| 40 | `◎ Working · 1.5 KiB esc interrupt` | contiguous | contiguous |
| 34 | `◉ Working· 1.9 KiB esc` / `interrupt` | contiguous | **split** |
| 28 | `◎      · 2.4    esc` / `WorkingKiB      interrupt` | contiguous | **split** |
| 26 | `◎      · 2.9   esc` / `WorkingKiB     interrupt` | contiguous | **split** |
| 24 | `◉     · 3.4  esc` / `WorkinKiB    interrup` / `g            t` | **split** | **split** |
| 20 | `◎    · 3.8 esc` / `WorkiKiB   interr` / `ng         upt` | **split** | **split** |

Three findings, each of which a wide capture alone would have got wrong.

**`Working` alone is the only viable marker, and its floor is 26.** The byte counter sits
*between* the two words, so `Working esc interrupt` is never contiguous at any width. `esc
interrupt` on its own stops being contiguous at 34 — one rung earlier than the width at
which the footer goes multi-column — because the single-column row simply wraps there. So a
matcher keyed on the hint would miss five of eight rungs. `Working` survives to 26; at 24
and 20 the multi-column footer splits it mid-word (`Workin`/`g`, then `Worki`/`ng`), and no
substring and no window value can reach a word that is not on screen as a word.

**Losing 34's space is not a width-monotonic story either.** At 34 the row reads
`Working·` with no space before the separator, while 40 and every wider rung read
`Working ·`. A marker of `Working ·` would therefore have passed 120, 60 and 40 and failed
at 34 — the same non-monotonic shape the approval dialog's selector shows.

**Those two lost rungs are what `LiveSpinner` would be for, and it stays nil.** The spinner
glyph is present at 24 and 20, so a frame-set detector is the only signal left there. This
ladder captured one frame per rung, which cannot establish a frame set, so that surface is
NOT MEASURED and Stage 1 records the two rungs as negative evidence rather than guessing a
predicate for them. A session at 24 columns or narrower reports idle during a live turn;
that is the disclosed cost of not driving the spinner.

**Why this section is dated and the first ladder is kept.** The first sweep's turn ended
after its width-60 rung, so its six narrow rungs captured an IDLE pane while looking like a
measurement: `Working` in exactly two of eight, an identical `Session: 1.52 AIC used` across
the other six, and the hint row where the status row belongs. Those captures are preserved
beside the valid ones under `captures-invalid-working-2026-08-26/` with a `WHY.txt`, because
this paragraph is a claim about them and deleting them would make it uncheckable. Do not
emit fixtures from that directory.

**Folder-trust gate.** Headline `Do you trust the files in this folder?`, title
`Confirm folder trust`, options `1. Yes` / `2. Yes, and remember this folder for future
sessions` / `3. No (Esc)`, selector `❯`. Note the selector is the composer's own glyph, so
the gate reads as a box line to `InputBoxVisible` — the collision that field's doc already
describes for claude and agy.

**A borderless-window assumption fails here, and a wider window cannot fix it.** Unlike
codex's overlay, this dialog is drawn *inside* box borders, and its body wraps rather than
truncating. So a wrapped headline reconstructs as `…files in this │ │ folder?…`: the border
runes and their padding sit between the fragments, and `flattenChrome` — which collapses
newlines to spaces — can never rejoin them. Raw matching of the headline fails from 40
down; the title survives to 24 and is unrecoverable at 20 (it wraps, and its first fragment
then scrolls off the top of the pane — see the height cliff below); and at 28 the title
already sits deeper than codex's `GateWindow`. Widening the window is the codex remedy and
it does not transfer.

What does work, at every driven rung, is stripping the box walls before flattening. That is
a new primitive the package does not have, and both matchers below need it, so it belongs
beside `flattenChrome` rather than inside either adapter.

**Where that scan is anchored, and why it is not the whole pane.** An earlier draft of this
paragraph proposed a stripped WHOLE-PANE scan and accepted its cost — that it can match the
same sentence quoted in the transcript, failing closed per `GateUp`'s own reasoning. That
cost does not have to be paid, because this dialog is not the shape that draft assumed.
Measured on all sixteen dialog captures: both dialogs are CLOSED round boxes whose bottom
border is the last non-empty line at every rung, with `│`-walled interior rows throughout —
gemini's shape, not codex's. So `bottomBoxBlock`, the liveness anchor already in
`chrome.go`, anchors both, and the primitive reduces to "strip the walls off that block's
lines, join, flatten". `bottomBoxBlock` returns false on all eight `working-*` panes, so
"no gate" is an anchored answer rather than a scan of transcript, and the exposure narrows
to the one `bottomBoxBlock` already discloses: quoted box art that ends the pane.

The primitive deliberately synthesises phrases across rows, which inverts
`bottomBoxBlock`'s own line-wise contract. That is the point here and the doc comment must
say so: the synthesis is bounded to one anchored box's interior, whereas the trap that
contract was written against (#713) was flattening across a WINDOW.

**A height cliff the width ladder hides.** At 20 columns the gate's box is taller than the
pane, so its top rows — including the title `Confirm folder trust` — scroll off. The title
is present at 24 and absent at 20, and it is not truncated, it is gone. Every literal a
matcher keys on must therefore sit LOW in the dialog: the headline and the option labels do,
the title does not. `Do you trust the files in this folder?` and `Yes, and remember this
folder for future sessions` both survive all eight rungs.

**Approval prompt, and the reason it must never be auto-tapped.** An action outside the
trusted directory raises a dialog reading `This action may read or write the following
path outside your allowed directory list.` then `Do you want to allow this?`, with:

```
  1. Yes
❯ 2. Yes, and add these directories to the allowed list
  3. No (Esc)
```

The pre-selected option is the second one. So Enter on this dialog does not approve one
action — it **widens the allowed-path list**. Autoyes tapping Enter here would silently
extend a copilot agent's filesystem reach past its worktree for the rest of the session,
which is a sandbox widening performed by a convenience feature. `NoAutoTap` is therefore
mandatory, and for a strictly worse reason than the one it carries on codex, where Enter
approves a single command.

Its literals wrap inside borders exactly as the gate's do — raw matching of the headline
fails from 28 down, the option label from 40 down — and the same wall-stripping scan
recovers both at every rung. `No (Esc)` and the `↑/↓ to navigate · enter to select · esc to
cancel` footer survive every width but are shared with the trust gate, so neither can
discriminate.

The option label is the specific one, and it must be keyed WITHOUT the selector. The space
between selector and number is not stable and not monotonic in width: `❯ 2.` at 120, 60, 34
and 28, `❯2.` at 40, 26, 24 and 20. So `Yes, and add these directories to the allowed list`
is the literal and the `❯ 2. ` prefix is not — and a matcher that included the prefix would
have passed a 120-column check and failed at 40 while passing again at 34, which is the
shape of drift a single wide capture cannot see.

Unlike the gate, this dialog's box fits the pane at every driven rung — its top border is on
screen at all eight, where the gate's is gone at 20 — so its title `Allow directory access`
survives too. That is box height rather than a property of titles, so the matcher keys on
the headline and the option label regardless.

**`session.usage_checkpoint` is not an account total, and must never be shown as one.**
Summing the event stream under one `COPILOT_HOME` gave 10.91 credits for the drive while
the organization's usage page reported 12 for Stage 0 as a whole. The reading was not
wrong, it was *scoped*: `totalNanoAiu` is cumulative per session, the sessions live under
whichever config directory produced them, and the pre-flight probes ran under separate
temporary homes that no such sum can see. Rounding accounts for the rest.

So a cost feature built on this event carries a per-session figure. Atrium may render that
honestly — this session's spend — but a chip implying account or budget consumption would
be asserting something the source cannot answer, which is the failure `atrium doctor`
exists to avoid. The authority for billed spend is the organization's usage page, and
nothing local substitutes for it.

**Two values the pane surfaces for free.** The status row carries `Session: <N> AIC used`,
a live AI-credit reading, and the footer carries the resolved model name. Both are
scrapeable without opening the session store, which is worth knowing before building a
reader for either.

**A measurement artifact worth recording, and how it was actually cleared.** The first
working-state ladder appeared to show the busy marker vanishing below 40 columns. It had
not: the turn ended partway through the ladder, and the narrow rungs captured an idle pane.
A ladder is only valid for a transient state if the state outlives the sweep. The wrong
reading was the more interesting-looking one.

Writing that down did not remove it. For as long as the correction existed only in prose,
the run directory held the invalid ladder and nothing else — so the lesson sat beside the
artifact it condemns, which is worse than no correction, because the next reader trusts the
prose and cites the capture. What cleared it was re-driving, not re-writing: the ladder in
the busy-marker section above was swept in one turn whose byte counter grows at every rung,
and the invalid captures were moved to `captures-invalid-working-2026-08-26/` in the same
pass, so the two can no longer be confused for each other.

The remedy generalises past this ladder. `Working` present at every rung was the check the
first sweep lacked, and it would still have passed a sweep whose turn ended between two
rungs that both happened to be painted. The check that cannot be faked is the byte counter:
a stalled counter says the turn is over even while the row is still on screen. Any future
ladder over a transient state needs a monotone quantity the state itself advances, not just
a marker the state leaves behind.

## NOT MEASURED

- **`LiveSpinner`'s frame set**, and therefore whether a busy predicate can be written for
  the two rungs where `Working` splits mid-word (24 and 20). The re-driven ladder in the
  busy-marker section above closed that section's other three questions — the floor is 26,
  `esc interrupt` breaks at 34, and `Working ·` is non-monotonic — but it captured one
  spinner frame per rung, and one frame cannot establish a set. Stage 1 records 24 and 20 as
  negative evidence instead of guessing a predicate for them.
- **`ResumeProbe`'s needle.** `--continue` and `-r, --resume` both appear in `--help`, but
  the needle must be pinned to the listing rather than the bare word, and that has not
  been chosen.
- **Hook payloads on stdin.** Only hook *output* was probed. What each event delivers is
  unmeasured; Atrium's state writers may not need it.
- **The paste chip.** `compactPaste` is documented and its placeholder shape is VENDOR; no
  pane was driven with a paste over the ten-line threshold.
- **Resume behaviour in a directory with nothing to resume.** The question
  `drive-agent`'s `resume` verb exists to answer, and the row it would add to
  `RESUME_TABLE`.
- **Whether the injected token's account is the one billed.** The pre-flight proves the
  token authenticated, and the org's own usage reporting is the only place attribution can
  be confirmed. `session.usage_checkpoint` records amounts, not the payer.

## Design consequences

1. **`HookSupport bool` becomes a capability.** #773's acceptance criterion 2 already
   asks for this. The field must say which mechanism, where its settings go, what the
   event names are, and — the driven finding above — which output schema the payload
   emitter uses.
2. **Prefer `events.jsonl` over hooks for status.** Hooks earn their risk only for the
   brief.
3. **Cost is credits, not tokens at list price.** `session/agent/pricing.go` is Anthropic
   list pricing, and its own header warns against calling its output spend. Copilot
   reports `totalNanoAiu` and `totalPremiumRequests` — a real meter. Surface those; do
   not route copilot through that table.
4. **Generalize, do not special-case twice.** The readers in `session/model.go`,
   `session/usage.go`, `session/cost.go`, `session/asked.go`, `session/checkpoints.go`,
   `session/effort.go` and `session/permissionmode.go` each test for `agent.KeyClaude`.
   Two-key tests would leave the third agent in the same hole. Ask the adapter for the
   capability instead.
5. **Root resolution moves into the transcript adapter.** `applyDefaults` resolves
   `$CLAUDE_CONFIG_DIR` else `~/.claude`, and `ProjectDir` calls the claude adapter
   directly. Copilot's root is `$COPILOT_HOME` else `~/.copilot`.
6. **`-i` is a better first-prompt path than typing.** Atrium delivers a queued first
   prompt by typing into the composer and reading it back. `-i <prompt>` hands it over at
   launch. Worth evaluating against the existing delivery guarantees.
7. **Copilot CLI owns worktrees and sessions too.** It ships a worktree command and a
   multi-session UI. Atrium owns the worktree, so the brief's ownership assertion —
   pinned by `TestSessionBriefAssertsWorktreeOwnership` — matters more for this agent
   than for claude, which has no such command.

## Prerequisite: bill the organization, not a personal entitlement

Stage 0's probes ran on a credential the OS keyring supplied, which was not the
organization's. Until that is corrected, every capture run spends the wrong budget.

The obvious route — mint a fine-grained PAT — is the wrong one, and the reason is worth
recording because the vendor README recommends it without the caveats.

### Why the PAT route does not serve Atrium

`Copilot Requests` is an **Account**-level fine-grained permission whose only access level
is **Read**. It authenticates inference and grants no repository access whatever, so it
does not cover the built-in GitHub MCP server, the delegate-to-GitHub command, or gist
sharing.

It also cannot be owned by an organization. github/copilot-cli#223 is open: the permission
does not appear when the token's resource owner is an org, because a Copilot license
attaches to a user rather than to an organization. The workaround its thread converges on
is to set the resource owner to one's own account, where the permission appears under the
Account tab once a repository scope has been chosen.

That workaround does not survive contact with this use case. A PAT whose resource owner is
a personal account cannot read an organization's private repositories, which is what
Atrium's worktrees are. Several commenters on that issue report exactly this: inference
works and private-org repository context does not.

### Entitlement is two independent settings, and one API call reads both

A 403 "unauthorized: not authorized to use this Copilot feature" is not a token problem
and not a seat problem — it is most likely the organization's CLI policy, which is
separate from the IDE policy and defaults to unset. Both halves are readable, and reading
them beats clicking through settings pages:

```sh
# Requires a token for an org member; read:org was sufficient here.
gh api orgs/<org>/copilot/billing
gh api orgs/<org>/copilot/billing/seats --jq '.seats[] | .assignee.login'
```

The billing object's `cli` field is the gate. Observed on the organization this work
targets: `plan_type` business, `ide_chat` enabled, and `cli` **unconfigured** — with the
intended account holding an assigned seat whose `last_activity` was never. So the seat and
the plan were in place and the CLI feature was simply never turned on. An owner enabling
the CLI policy is the whole fix.

Recording it because the failure presents as an auth problem and is not one. Nothing in
the CLI's message names the policy field, and the token, the seat and the plan can all be
correct while the feature stays off.

### The AI-credits budget is a second, independent gate

The CLI policy is not the only thing that can produce "Access denied by policy settings"
with a 403 `unauthorized: not authorized to use this Copilot feature`. A metered-usage
budget does too, and it is invisible to every Copilot endpoint:

```sh
gh api orgs/<org>/settings/billing/budgets --jq '.budgets[]
  | "sku=\(.budget_product_sku) amount=\(.budget_amount) block=\(.prevent_further_usage)"'
```

Observed on the organization this work targets: an `ai_credits` budget of **0** at
organization scope with `prevent_further_usage` true, alongside an enabled CLI policy and
an assigned Business seat. Copilot CLI meters AI credits per interaction, so the budget
denied every request.

The load-bearing fact, and the one that makes this trap work: **a zero budget is not
limited to blocking paid overage — it blocks the included allowance too, immediately.** So
a plan showing thousands of unused included credits and a zero blocking budget are
perfectly consistent, and the budget wins. Setting a zero AI-credits budget in the belief
that included credits will still be drawn is a widely reported mistake, not an exotic one.

Two things make the diagnosis harder than it should be. IDE chat is unaffected, so Copilot
plainly works for other members and the CLI's failure reads as CLI-specific. And the error
is phrased as authorization rather than billing — the CLI reports `unauthorized: not
authorized to use this Copilot feature`, which points at policy, while the same block
surfaces in editors as a budget message.

Order the checks accordingly, cheapest and most decisive first: the budget, then the
policy, then the seat, then the token. Three of the four were already correct here and the
budget was the gate. Note also that a zero budget with blocking is frequently a deliberate
cost control rather than an oversight, so raising it is a spending decision and not a
toggle to flip on someone's behalf.

### The interactive login is the supported path

The credential the keyring supplied during Stage 0 authenticated inference and is shared
across sessions. Sessions are interactive tmux panes, so a one-time login covers every
session afterwards. Reserve tokens for headless automation and for account routing, per
below.

### Account routing is possible, but advisory rather than structural

An earlier draft claimed Atrium must never inject a `gh` token into a copilot session.
That was wrong: `copilot login --help` lists "OAuth tokens from the GitHub CLI (gh) app"
among supported types, and Stage 0's probes authenticated while carrying one. Only classic
PATs are refused outright.

So can Atrium route personal and work accounts the way it does for claude? Three measured
facts say yes mechanically, no structurally.

**`COPILOT_HOME` does not scope credentials.** The login subcommand's help states the token
goes to the system credential store, falling back to a plaintext file under the config
directory only when no store is available. Stage 0 confirmed the consequence: a fresh empty
`COPILOT_HOME` authenticated from a credential it did not contain. The isolation
`CLAUDE_CONFIG_DIR` gives — a session that *cannot* read another account's credentials —
has no equivalent.

**Token injection fails open.** A well-formed but invalid `COPILOT_GITHUB_TOKEN` logged
"Failed to fetch PAT user login (401)" and the session then succeeded on the stored
credential, exit zero. An Atrium injecting a stale token therefore gets a session silently
billed to whichever account the credential store holds, with nothing visible in the pane.

**There is no positive identity signal.** A run with a valid token logged nothing naming
the authenticated account; only the failure path mentions resolving a login. The CLI cannot
be asked which account it used.

**The discriminator, and the design that follows.** Absence of the "Failed to fetch PAT
user login" line is what proves an injected token was actually used — the one reliable
signal, and the reason a pre-flight check must assert on it rather than on exit status.
Route by injecting `COPILOT_GITHUB_TOKEN` per session and validate before launch, refusing
when the resolved account is not the intended one. Atrium already owns both halves:
per-session `GH_CONFIG_DIR` injection, and `resolveGitHubToken`, whose `--user` pin exists
precisely because a bare lookup can return another account's entry. That converts the CLI's
fail-open into a fail-closed refusal.

What must not be claimed is parity. For claude, routing is structural; for copilot it is
advisory, and any UI labelling a copilot session with an account name asserts something it
has not verified. That is the defect class `atrium doctor` exists to avoid — a displayed
value whose source cannot answer. Either verify at launch and record the verdict, or show
no label.

An alternative worth measuring before choosing: denying the CLI a credential store forces
the documented plaintext fallback into the config directory, making credentials
`COPILOT_HOME`-scoped and routing structural again. It costs a plaintext token under the
data dir and an interactive login per account. NOT MEASURED.

## Next step

Stage 0 is complete. Every prerequisite is satisfied: the organization's CLI policy is
enabled, its AI-credits budget is raised off zero, the intended account holds a seat, and
the injected token is proven to be the credential in use.

Stage 1 can be written from the driven captures for all three surfaces. The busy marker's
ladder was re-driven over all eight rungs rather than encoding a table nothing could cite;
the invalid one was moved aside in the same pass, not overwritten.

Four things it must carry that were not obvious before driving:

1. A wall-stripping scan beside `flattenChrome`, anchored on `bottomBoxBlock` rather than
   the whole pane, since both the gate and the approval matcher need it and neither works
   without it below 40 columns.
2. `NoAutoTap` on the approval matcher, because its pre-selected option widens the
   allowed-path list rather than approving one action.
3. Literals taken from LOW in each dialog. At 20 columns the gate's box outgrows the pane
   and its title scrolls off, so a title-keyed matcher misses a gate that is on screen.
4. A pinned width floor for the busy marker, as a value a test reads rather than a
   sentence prose repeats — and pinned to the re-driven ladder, not to the table this
   spec used to carry. That floor is 26, and the two rungs below it are recorded as
   misses, because the multi-column footer splits `Working` mid-word there.

Remaining before Stage 1 is finished, not before it starts: the paste-chip capture, the
`resume` drive and its `RESUME_TABLE` row, and a `ResumeProbe` needle chosen against the
help listing. All three are listed under NOT MEASURED above.
