# Copilot CLI Adapter (Stage 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Register a `copilot` adapter in `session/agent` whose trust gate, approval prompt and busy marker are each pinned to verbatim panes driven off GitHub Copilot CLI 1.0.80 at a width ladder.

**Architecture:** Copilot's two dialogs are closed round boxes whose bottom border is the last non-empty line at every driven width, with `│`-walled interior rows — gemini's shape. So both matchers anchor on the existing `bottomBoxBlock` liveness anchor and reconstruct their wrapped literals through one new primitive (`flattenBottomBox`) that strips the walls before flattening. No `GateWindow`, no whole-pane scan. The busy marker keys on `Working` alone in the below-composer footer region (`MarkerWindow` 0, claude's arrangement).

**Tech Stack:** Go 1.x, `session/agent` (pure data + string matching, no IO), `scripts/drive-agent.sh` for live capture, `testify/require`, `just` for the verification gate.

**Spec:** `docs/superpowers/specs/2026-08-26-copilot-cli-integration-design.md` (amended at `b58d619`)

## Global Constraints

- **Driven version:** GitHub Copilot CLI **1.0.80** (npm `@github/copilot`), Linux. `VerifiedVersion: "1.0.80"`, `DriftGranularity: GranularityMinor` (matching every other pinned adapter).
- **`HookSupport` stays `false`.** Copilot's hook *output* schema is a flat `{"additionalContext": …}`; claude's nested `hookSpecificOutput.additionalContext` fires and delivers nothing. The field routes through claude's emitter, so setting it ships a brief that is registered, documented and dead — #773's exact failure mode. Stage 2 replaces the bool with a capability that can say which schema.
- **`NoAutoTap: true` on the approval matcher is mandatory, not defensive.** Its pre-selected option is `2. Yes, and add these directories to the allowed list`. Enter widens the session's allowed-path list rather than approving one action, so an autoyes tap extends the agent's filesystem reach past its worktree. Copilot must NOT be added to `TestAutoTapRequiresAnAnchoredMatcher`'s allowlist.
- **`InputBoxPrompts` stays nil.** The composer glyph is `❯` (U+276F), already in `defaultPrompts`.
- **`MarkerWindow` stays 0.** The status row replaces the hint row *below* the composer, so the footer anchor finds it.
- **`GateWindow` stays 0.** Both matchers use `Match` and do their own anchoring. Widening a window is codex's remedy and cannot work here — the failure is a destroyed literal, not a literal pushed out of view.
- **`Resume` stays nil**, therefore no `ResumeProbe` and no `RESUME_TABLE` row. The resume drive is later work (spec's NOT MEASURED). Verify rather than trust: `TestDriveAgentResumeTableMatchesTheRegistry` in `drive_agent_drift_test.go` skips an adapter whose `Resume` is nil, so a nil one needs no script row — but a non-nil one added later without a row fails that test, which is the point of leaving it nil.
- **`HeadlessNamer` stays false.** `copilot -p --allow-all-tools` is driven but the naming branch in `session/naming.go` is out of scope.
- **Accounts are out of scope.** `COPILOT_HOME` does not scope credentials, so no `copilot_accounts` config, no row badge, no `agy`-style bwrap isolation.
- **Verification gate:** `just ci` (= build vet fmt-check lint test cover). Lint via `just lint`, never a bare `golangci-lint run` — the recipe keys `GOLANGCI_LINT_CACHE` to this worktree, and the global cache a bare run uses reports stale findings from a sibling worktree.
- **`golangci-lint` is installed on this machine but NOT on `PATH`** (it lives in `$(go env GOPATH)/bin`). Without it the sweep runs build, vet and fmt-check green and *then* dies at `lint` with `not found` (exit 127), which reads like a broken recipe rather than a missing tool. Every `just ci` and `just lint` in this plan must be run as `PATH="$(go env GOPATH)/bin:$PATH" just ci`, or with that directory exported once in the executing shell. Lint is the part of the gate nothing else substitutes for: build, vet and fmt-check all pass happily while `golangci-lint` fails, and `unused` is the usual culprit — a const declared for an assertion you meant to write compiles fine.
- **Scoped test runs call `go` directly.** `just test` and `just test-race` are `go test ./...` with no argument splice, so `just test ./session/agent/` silently runs the whole suite. `just lint` *does* take packages (`just lint ./ui/...`).
- **Mutation-prove every new assertion, and commit before every batch.** A proof loop that runs `git checkout --` eats uncommitted work.
- **Prose rule:** cite a symbol, never a position. `TestNoProseCitesAPosition` reads the git index and enforces it for Go comments.

---

## File Structure

| File | Responsibility |
|---|---|
| `session/agent/chrome.go` (modify) | `stripBoxWalls` extracted from `stripBoxInterior`; new `flattenBottomBox` beside `flattenChrome` |
| `session/agent/chrome_walls_test.go` (create) | The primitive's contract, on composed box art: walls stripped, wrap rejoined, `ok=false` without an anchor |
| `session/agent/agent.go` (modify) | `KeyCopilot` const |
| `session/agent/registry.go` (modify) | The `copilot` adapter, its two `Match` predicates, its four literal consts, `registry` list |
| `session/agent/copilot_pane_test.go` (create) | All 24 driven panes, three `[]paneCapture` ladders, per-adapter positive and negative assertions |
| `session/agent/pane_width_test.go` (modify) | `paneCoverage` ×3 keys, `wantRungs` ×3 keys |
| `session/agent/drift_fields_test.go` (modify) | Version pin row, adapter count 5 → 6 |
| `config/agents.go` (modify) | `knownAgentBins` |
| `ui/theme/agent.go` (modify) | `plainAgentGlyphs` `⎈`, `asciiAgentGlyphs` `P` |
| `README.md` (modify) | Intro agent list, launch example, first-run probe list |
| `config/readme_agents_test.go` (create) | Holds the README's probe list to `knownAgentBins` — the one drift site in this change nothing currently guards |

`copilot_pane_test.go` is one file rather than three (one per surface) because the repo's convention is one pane file per *agent* (`codex_pane_test.go`, `gemini_pane_test.go`) and because the working panes serve three keys at once — positive for `copilot/busy`, negative for the gate and the prompt.

---

## Task 1: Re-drive the working ladder

The captures in `~/.local/share/atrium-captures/copilot-1.0.80/` are the **invalid** ladder: the turn ended after the width-60 rung, so `working-w40` through `working-w20` captured an idle pane. `Working` is present in exactly two of the eight. Every fixture in Tasks 3–5 depends on fixing that, so this runs first.

**Files:**
- Create: `~/.local/share/atrium-captures/copilot-1.0.80/captures/working-w{120,60,40,34,28,26,24,20}.txt` (plus `cat-A/` and `prod/` forms), replacing the invalid ones
- Create: `~/.local/share/atrium-captures/copilot-1.0.80/captures-invalid-working-2026-08-26/` (the invalid ladder, moved aside)
- Create: `~/.local/share/atrium-captures/copilot-1.0.80/fixtures-working.go.txt`

**Interfaces:**
- Produces: eight verbatim `working-*` panes in which `Working` is present at every rung, plus the emitted Go fixtures Task 5 pastes from.

**Do NOT re-drive `trustgate` or `approval`.** Those sixteen panes are valid and byte-verified; re-driving them costs a `fresh` restart plus an approval turn for no gain.

- [ ] **Step 1: Move the invalid ladder aside rather than overwriting it**

The spec now cites these captures as evidence *that the ladder was invalid*. Deleting them makes that claim uncheckable.

```bash
CAP=~/.local/share/atrium-captures/copilot-1.0.80
mkdir -p "$CAP/captures-invalid-working-2026-08-26"/{cat-A,prod}
for w in 120 60 40 34 28 26 24 20; do
  mv "$CAP/captures/working-w$w.txt"              "$CAP/captures-invalid-working-2026-08-26/"
  mv "$CAP/captures/cat-A/working-w$w.cat-A.txt"  "$CAP/captures-invalid-working-2026-08-26/cat-A/"
  mv "$CAP/captures/prod/working-w$w.esc.txt"     "$CAP/captures-invalid-working-2026-08-26/prod/"
done
cat > "$CAP/captures-invalid-working-2026-08-26/WHY.txt" <<'EOF'
The first working-state ladder. INVALID: the turn ended after the width-60 rung, so
w40 and below captured an idle pane. `Working` is present only in w120 and w60; those
two read "Session: 1.52 AIC used" and the other six all read "2.7" with the hint row
where the status row belongs. Kept because the design spec cites this directory as the
evidence that the ladder was invalid. Do not emit fixtures from it.
EOF
ls "$CAP/captures-invalid-working-2026-08-26"
```

Expected: eight `.txt` files plus `WHY.txt`, and `cat-A`/`prod` each holding eight.

- [ ] **Step 2: Resolve the work token**

This session's `gh` account routes to `personal`, and atrium is not a quantivly remote, so the account must be named explicitly.

```bash
export COPILOT_TOKEN="$(GH_CONFIG_DIR=$HOME/.config/gh gh auth token --user zvi-quantivly)"
[ -n "$COPILOT_TOKEN" ] && echo "token resolved (${#COPILOT_TOKEN} chars)"
```

Expected: a non-empty token. If it is empty, stop — the drive would silently bill the personal account.

- [ ] **Step 3: Start a capture session with an isolated COPILOT_HOME**

```bash
export CAPHOME=/tmp/atr-cap-copilot-home
rm -rf "$CAPHOME" && mkdir -p "$CAPHOME"
export ATR_CAP_NAME=copilot-busy
export ATR_CAP_ENV=$'COPILOT_HOME='"$CAPHOME"$'\nCOPILOT_GITHUB_TOKEN='"$COPILOT_TOKEN"
just drive-agent up copilot 120 40
```

Expected: the run starts and prints an attach command. `COPILOT_HOME` is safe to isolate — a fresh empty directory populates without error and cannot sign the session out (the credential lives in the OS keyring).

- [ ] **Step 4: Dismiss the trust gate**

```bash
just drive-agent wait 'Do you trust the files in this folder'
just drive-agent keys 2 Enter
just drive-agent wait '❯'
```

Expected: the gate appears, option 2 is selected, the composer returns. (`2` remembers the folder, so a later `fresh` in the same path will not re-gate — which is fine here because this task never calls `fresh`.)

- [ ] **Step 5: Prove the injected token was the credential actually used**

Injection fails open: a bad token logs a 401 and the session succeeds on the keyring entry. Absence of that line is the only positive signal.

```bash
grep -c 'Failed to fetch PAT user login' "$CAPHOME"/logs/*.log || echo "0 (clean)"
```

Expected: zero matches. Any match means the drive is billing the wrong account — stop and re-resolve the token.

- [ ] **Step 6: Start a turn long enough to outlive the whole sweep**

The invalid ladder's prompt ("count to 300") finished after two rungs. Eight rungs at `SETTLE=1.5` plus capture overhead is roughly 20–30 seconds, so the turn needs minutes of headroom, and it must be pure generation — a tool call would raise the approval dialog mid-ladder.

```bash
just drive-agent send 'Without running any tools or commands, write out the numbers from 1 to 4000, one per line, nothing else.'
just drive-agent wait 'Working'
```

Expected: `Working` matches within the default timeout.

- [ ] **Step 7: Sweep all eight rungs**

All eight, not just the six that were invalid: a ladder spliced from two runs cannot show a monotonically growing byte counter, which is the validity proof.

```bash
just drive-agent ladder working 120 60 40 34 28 26 24 20
```

Expected: eight lines of `w<N>  <count> non-empty lines`.

- [ ] **Step 8: Verify the ladder is valid before emitting anything**

This is the step whose absence produced the first ladder. Two independent checks: the marker is present at every rung, and the byte counter grew — a stalled counter means the turn ended even if the row is still painted.

Read the run directory out of `status` rather than reconstructing the pointer path: the root
is `${ATR_CAP_ROOT:-/tmp/atrium-capture}` and `status` is the documented way to ask.

```bash
CAP=~/.local/share/atrium-captures/copilot-1.0.80
just drive-agent status
RUN="$(just drive-agent status | awk '/^run /{print $2}')"
[ -d "$RUN/captures" ] || { echo "could not read the run dir from status"; exit 1; }
for w in 120 60 40 34 28 26 24 20; do
  f="$RUN/captures/working-w$w.txt"
  printf 'w%-4s Working=%s  bytes=%s  AIC=%s\n' "$w" \
    "$(grep -c 'Working' "$f")" \
    "$(grep -oE '[0-9]+ B' "$f" | head -1)" \
    "$(grep -oE 'Session: [0-9.]+ AIC' "$f" | head -1)"
done
```

Expected: `Working=1` on **all eight** rungs. If any rung reads 0, the turn ended mid-sweep — `just drive-agent send` a fresh long prompt and re-run Step 7 for the failed rungs. Do not proceed with a partial ladder.

Note what a valid ladder may legitimately show: at the narrowest rungs `Working` may be split mid-word (`Worki`/`ng`) by the multi-column footer. That is a **miss**, not an invalid capture — it is recorded as negative evidence in Task 5, and it is distinguished from an invalid rung by the byte counter still growing and the spinner glyph still present.

- [ ] **Step 9: Emit the fixtures and copy the artifacts into the canonical directory**

Each step here is its own shell, so `$CAP` and `$RUN` are re-derived rather than inherited.

```bash
CAP=~/.local/share/atrium-captures/copilot-1.0.80
RUN="$(just drive-agent status | awk '/^run /{print $2}')"
just drive-agent emit copilot > "$CAP/fixtures-working.go.txt"
cp "$RUN"/captures/working-w*.txt        "$CAP/captures/"
cp "$RUN"/captures/cat-A/working-w*.txt  "$CAP/captures/cat-A/"
cp "$RUN"/captures/prod/working-w*.txt   "$CAP/captures/prod/"
grep -c '^const copilotWorking' "$CAP/fixtures-working.go.txt"
```

Expected: `8`. Note `emit` prints fixtures for **every** capture in the run, so if this run
directory only ever held the `working` ladder the file is exactly the eight panes; if it
holds others, take only the `copilotWorking*` consts from it.

- [ ] **Step 10: Tear the capture session down**

```bash
just drive-agent down
just drive-agent status || true
```

Expected: the capture server is gone and the live fleet count is unchanged from before the run. If a server was stranded, `just drive-agent reap-all`.

- [ ] **Step 11: Record the drive in the spec**

Replace the NOT MEASURED bullet for the busy marker with what the ladder actually measured. Exact edit depends on Step 8's readings, so write what was observed — the rung table, and whether `Working` survives to 20 — using the same shape as the gate paragraphs above it.

- [ ] **Step 12: Commit**

```bash
git add docs/superpowers/specs/2026-08-26-copilot-cli-integration-design.md
git commit -m "docs(specs): record the re-driven copilot busy ladder"
```

No code changed in this task; the deliverable is the captures (which live outside the repo) plus the spec record of them.

---

## Task 2: The wall-stripping primitive

**Files:**
- Modify: `session/agent/chrome.go` — add `stripBoxWalls` and `flattenBottomBox`, rewrite `stripBoxInterior`'s first two lines to call the former
- Create: `session/agent/chrome_walls_test.go`

**Interfaces:**
- Consumes: `bottomBoxBlock(content string) ([]string, bool)` and `whiteSpaceRegex`, both already in `chrome.go`.
- Produces:
  - `stripBoxWalls(line string) string`
  - `flattenBottomBox(content string) (flat string, ok bool)` — Tasks 3 and 4 call this and nothing else.

- [ ] **Step 1: Write the failing test**

Composed box art, not captures: this is a primitive's contract, and the real-pane proof arrives through `paneCoverage` in Tasks 3–5. Mirrors `window_test.go`'s style.

Create `session/agent/chrome_walls_test.go`:

```go
package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStripBoxWallsTakesBothWallsAndNoGlyph pins the split from stripBoxInterior: the walls
// come off, the composer glyph does NOT. That asymmetry is the whole reason the helper
// exists — a dialog's prose opens with "❯ 1. Yes" on its selected row, and eating the glyph
// there would be harmless, but eating it in stripBoxInterior's caller is what reads back a
// user's typed text, so only one of the two callers may want it.
func TestStripBoxWallsTakesBothWallsAndNoGlyph(t *testing.T) {
	require.Equal(t, "Confirm folder trust", stripBoxWalls("│ Confirm folder trust      │"))
	require.Equal(t, "❯ 1. Yes", stripBoxWalls("│ ❯ 1. Yes                  │"),
		"the glyph must survive: flattenBottomBox reads dialog prose, not a composer")
	require.Equal(t, "plain prose", stripBoxWalls("  plain prose  "),
		"a line with no walls is just trimmed")
	require.Equal(t, "╭──────╮", stripBoxWalls("│ ╭──────╮ │"),
		"a NESTED box's own border survives as a separator; only the outer walls come off")
}

// TestStripBoxInteriorStillStripsTheGlyph is the regression half of the extraction: the
// existing caller's behaviour must be byte-identical, so the split is a refactor rather than
// a change. Reads the composer glyph off defaultPrompts rather than a literal so a change to
// that set cannot leave this asserting against a glyph nothing draws.
func TestStripBoxInteriorStillStripsTheGlyph(t *testing.T) {
	require.Equal(t, "refactor the parser",
		stripBoxInterior("│ ❯ refactor the parser     │", defaultPrompts))
	require.Equal(t, "1. Yes", stripBoxInterior("│ ❯ 1. Yes │", defaultPrompts))
}

// TestFlattenBottomBoxRejoinsAWrappedSentence is the property the two adapters below need
// and that flattenChrome structurally cannot deliver: a sentence hard-wrapped inside a box
// has the border runes and their padding BETWEEN its fragments, so collapsing newlines to
// spaces leaves "…files in this │ │ folder?…". Stripping the walls first is what rejoins it.
func TestFlattenBottomBoxRejoinsAWrappedSentence(t *testing.T) {
	pane := "  transcript above\n" +
		"╭──────────────────╮\n" +
		"│ Do you trust the │\n" +
		"│  files in this   │\n" +
		"│ folder?          │\n" +
		"╰──────────────────╯"
	flat, ok := flattenBottomBox(pane)
	require.True(t, ok, "the bottom border ends the pane and walled rows sit above it")
	require.Contains(t, flat, "Do you trust the files in this folder?")

	// The contrast, asserted rather than described: the same pane through the flat window
	// the prompt matchers use does NOT reconstruct the sentence.
	require.NotContains(t, flattenChrome(pane, WindowPrompt),
		"Do you trust the files in this folder?",
		"if this ever passes, flattenBottomBox has stopped being the thing that earns its keep")
}

// TestFlattenBottomBoxRefusesAPaneWithNoAnchoredBox is the half that makes the whole-pane
// alternative unnecessary. A composer pane, a bare transcript and box art with a composer
// drawn below it all report false, so "no dialog" is an anchored answer rather than a scan
// of scrollback — which is what confines the false-positive surface to bottomBoxBlock's own
// disclosed one (quoted box art that ends the pane).
func TestFlattenBottomBoxRefusesAPaneWithNoAnchoredBox(t *testing.T) {
	for name, pane := range map[string]string{
		"bare transcript": "some prose\nand more prose",
		"composer only":   "transcript\n──────────\n❯\n──────────\n hints here",
		"box then composer": "╭────────╮\n│ 1. Yes │\n╰────────╯\n" +
			"──────────\n❯\n──────────\n hints",
	} {
		_, ok := flattenBottomBox(pane)
		require.Falsef(t, ok, "%s must not read as an anchored dialog", name)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./session/agent/ 2>&1 | tail -20
```

Expected: FAIL — `undefined: stripBoxWalls`, `undefined: flattenBottomBox`.

- [ ] **Step 3: Add the two functions and rewire stripBoxInterior**

In `session/agent/chrome.go`, insert `stripBoxWalls` immediately above `stripBoxInterior` and rewrite that function's first two lines:

```go
// stripBoxWalls removes a box interior line's left and right side walls and the whitespace
// around them. It is the wall half of stripBoxInterior, split out because two callers now
// need it and only one of them wants the composer glyph taken off as well: stripBoxInterior
// reads back what a user typed into a composer, so the glyph must go (the signature it is
// compared against does not carry one); flattenBottomBox reads a DIALOG's prose, where the
// same glyph is the selection pointer on the highlighted row and carries meaning.
//
// One function knows what a wall looks like, so an agent that draws a different one is a
// single edit rather than a hunt. Only the LIGHT wall is accepted, matching isBoxWallLine —
// see its doc for why the heavy form was dead code the compiler could not see.
func stripBoxWalls(line string) string {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, "│")) // left border
	s = strings.TrimSpace(strings.TrimSuffix(s, "│")) // right border
	return s
}

// stripBoxInterior removes an input-box interior line's side borders, its leading prompt
// glyph (one of prompts), and surrounding whitespace, leaving just the typed text. Used
// to read back what the user (or a queued-prompt send) has entered into the composer.
// The glyph must come off: the readback is compared against the prompt's signature
// (session/prompt.go boxHoldsPrompt), which does not carry it. At most one glyph is
// removed — a composer line opens with exactly one, and stripping a second would eat
// real text on an agent whose glyph is a legal first character of user input.
func stripBoxInterior(line string, prompts promptSet) string {
	s := stripBoxWalls(line)
	for _, g := range prompts {
		if rest, ok := strings.CutPrefix(s, g); ok {
			return strings.TrimSpace(rest)
		}
	}
	return strings.TrimSpace(s)
}
```

Then add `flattenBottomBox` immediately after `flattenChrome`:

```go
// flattenBottomBox returns the pane's bottom-most anchored box as one whitespace-normalized
// line, with the interior rows' side walls removed first, and reports whether such a box was
// on screen at all.
//
// It exists because a dialog drawn INSIDE box borders wraps its body rather than truncating
// it, and the border runes and their padding then sit between a sentence's fragments:
// copilot 1.0.80's folder-trust headline reconstructs through flattenChrome as
// "…files in this │ │ folder?…", which no amount of newline collapsing can rejoin. Stripping
// the walls before flattening recovers it, and that was measured at every driven rung rather
// than reasoned about — see the copilot ladders in copilot_pane_test.go, whose narrowest is
// the one flattenChrome cannot reach.
//
// IT DELIBERATELY SYNTHESISES ACROSS ROWS, which inverts bottomBoxBlock's own contract.
// That function returns lines unjoined precisely so a caller matching a literal gets no
// cross-line synthesis; here the synthesis IS the feature, and the difference that makes it
// safe is the region. The trap it inverts (#713, gemini's trust gate) was flattening across
// a bottom-N WINDOW, where the transcript scrolls through and any two neighbouring lines can
// manufacture a phrase neither renders. Here the region is one anchored box's interior, so
// the only text that can combine is text the dialog itself drew. A caller that needs the
// unjoined form still has bottomBoxBlock.
//
// What it does NOT strip is a NESTED box's own borders. Copilot draws the path under review
// inside a second box, and leaving those runes in place keeps them as separators, so a
// literal cannot be manufactured across one. The outer walls are the only thing removed.
//
// ok=false means no box whose bottom border all but ends the pane — a composer frame, a bare
// transcript, or a dismissed dialog with the composer redrawn below it. That is what keeps
// the false-positive surface down to the one bottomBoxBlock already discloses (quoted box art
// that ends the pane) rather than the whole pane's worth a stripped full-pane scan would take.
//
// Input must already be cleaned for detection (ANSI stripped), like every other predicate here.
func flattenBottomBox(content string) (string, bool) {
	block, ok := bottomBoxBlock(content)
	if !ok {
		return "", false
	}
	parts := make([]string, 0, len(block))
	for _, line := range block {
		parts = append(parts, stripBoxWalls(line))
	}
	flat := whiteSpaceRegex.ReplaceAllString(strings.Join(parts, " "), " ")
	return strings.TrimSpace(flat), true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./session/agent/ 2>&1 | tail -10
```

Expected: PASS, including every pre-existing test in the package — the `stripBoxInterior` rewrite must be behaviour-identical.

- [ ] **Step 5: Mutation-prove each new assertion**

Per-assertion, not per-test: a test file that passes with the mutation in place is asserting nothing. Commit first (the loop below reverts the tree).

```bash
git add -A && git commit -m "wip: chrome wall-stripping primitive"
```

Then, one at a time, apply each mutation, confirm the named test FAILS, and revert with `git checkout -- session/agent/chrome.go`:

| mutation in `chrome.go` | must redden |
|---|---|
| drop the `TrimSuffix` line from `stripBoxWalls` | `TestStripBoxWallsTakesBothWallsAndNoGlyph` |
| drop the `TrimPrefix` line from `stripBoxWalls` | `TestStripBoxWallsTakesBothWallsAndNoGlyph` |
| make `stripBoxWalls` also strip `"❯"` | `TestStripBoxWallsTakesBothWallsAndNoGlyph` |
| have `flattenBottomBox` join with `"\n"` instead of `" "` and skip the regex | `TestFlattenBottomBoxRejoinsAWrappedSentence` |
| have `flattenBottomBox` call `liveChromeLines(content, WindowPrompt)` instead of `bottomBoxBlock` | `TestFlattenBottomBoxRefusesAPaneWithNoAnchoredBox` |
| have `flattenBottomBox` return `ok=true` when `bottomBoxBlock` is false | `TestFlattenBottomBoxRefusesAPaneWithNoAnchoredBox` |

If any mutation leaves the suite green, the assertion is vacuous — restructure it rather than adding another.

- [ ] **Step 6: Run the gate and commit**

```bash
PATH="$(go env GOPATH)/bin:$PATH" just ci
```

Expected: all six stages pass. Then:

```bash
git add session/agent/chrome.go session/agent/chrome_walls_test.go
git commit -m "feat(agent): a wall-stripping scan for box-drawn dialogs

A dialog drawn inside box borders wraps its body rather than truncating it,
so the border runes and their padding land between a sentence's fragments and
flattenChrome can never rejoin them. flattenBottomBox strips the walls off
bottomBoxBlock's interior rows before flattening, which recovers the sentence.

Anchored on bottomBoxBlock rather than the whole pane: a stripped full-pane
scan would match the same sentence quoted in a transcript, and the anchor makes
\"no dialog\" a positive answer instead. stripBoxWalls is the wall half of
stripBoxInterior, split out because only one of the two callers wants the
composer glyph taken off as well."
```

---

## Task 3: The adapter and its trust gate

**Files:**
- Modify: `session/agent/agent.go` — `KeyCopilot`
- Modify: `session/agent/registry.go` — the adapter, `copilotTrustGateVisible`, two literal consts, `registry`
- Create: `session/agent/copilot_pane_test.go` — the eight `trustgate` panes, `copilotTrustGateLadder`, per-adapter assertions
- Modify: `session/agent/pane_width_test.go` — `paneCoverage["copilot/gate/trust"]`, `wantRungs["copilot/gate/trust"]`
- Modify: `session/agent/drift_fields_test.go` — pin row, count 5 → 6
- Modify: `config/agents.go` — `knownAgentBins`
- Modify: `ui/theme/agent.go` — both glyph tables

**Interfaces:**
- Consumes: `flattenBottomBox(content string) (string, bool)` from Task 2.
- Produces: `KeyCopilot Key`, `copilot *Adapter`, `copilotTrustHeadline` / `copilotTrustOption` consts, `copilotTrustgateLadder []paneCapture`.

The adapter declares **only `Gates`** in this task. `requiredCoverageKeys` walks `BusyMarkers` and `Prompts` too, so declaring all three surfaces at once would demand three coverage keys before their fixtures exist. Tasks 4 and 5 add one surface each, and the suite stays green throughout.

- [ ] **Step 1: Write the failing test**

Create `session/agent/copilot_pane_test.go`. Paste the eight `copilotTrustgateW*Pane` consts verbatim from `~/.local/share/atrium-captures/copilot-1.0.80/fixtures.go.txt` (lines beginning `const copilotTrustgateW`), then replace the emitted RENAME-ME/FILE-ME boilerplate with one file header and the ladder below.

```go
package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Driven panes from GitHub Copilot CLI 1.0.80 (npm @github/copilot), Linux, captured
// 2026-08-26 by scripts/drive-agent.sh in an isolated COPILOT_HOME with the work token
// injected via ATR_CAP_ENV. Widths 120/60/40/34/28/26/24/20; the pane height is 40 at every
// rung, which matters for the gate (see the height note on copilotTrustgateLadder).
//
// Both of this adapter's dialogs are CLOSED round boxes whose bottom border is the last
// non-empty line at every rung, with "│"-walled interior rows — gemini's shape, not codex's
// borderless overlay. That is what lets both matchers anchor on bottomBoxBlock and read their
// literals through flattenBottomBox instead of a stripped whole-pane scan.
//
// WHAT A gemini-SHAPED MATCHER WOULD GET WRONG HERE, because the resemblance is close enough
// to invite copying: geminiTrustGateVisible vetoes a block containing an isInputBoxLine, on
// the reasoning that a composer is not a dialog. Copilot's selector IS the composer glyph
// "❯", and it sits on the dialog's highlighted option row, so that veto would return false on
// every rung of both ladders below. TestCopilotDialogsAreAlsoComposersToTheBoxPredicate holds
// the collision as a fact rather than leaving it as a trap.

// … the eight const copilotTrustgateW*Pane blocks go here, pasted verbatim …

// copilotTrustgateLadder is the folder-trust gate at every driven width. The notes carry
// what is notable at each rung; the widths are the datum pane_width_test.go computes the
// floor from, so this is where "the floor is 20" stops being a sentence.
//
// THE 20 RUNG IS A HEIGHT FINDING, not just a width one. At 20 columns the dialog's box grows
// taller than the 40-row pane, so its TOP border and the title row scroll off — the title
// "Confirm folder trust" is present at 24 and simply absent at 20, not truncated. Every
// literal this matcher keys on therefore sits LOW in the dialog; a title-keyed matcher would
// miss a gate that is plainly on screen. TestCopilotTrustGateTitleIsGoneAtWidth20 pins it.
var copilotTrustgateLadder = []paneCapture{
	{name: "copilotTrustgateW20Pane", width: 20, note: "box outgrows the pane; title scrolled off", pane: copilotTrustgateW20Pane},
	{name: "copilotTrustgateW24Pane", width: 24, note: "headline in two lines", pane: copilotTrustgateW24Pane},
	{name: "copilotTrustgateW26Pane", width: 26, note: "", pane: copilotTrustgateW26Pane},
	{name: "copilotTrustgateW28Pane", width: 28, note: "raw flatten already fails here", pane: copilotTrustgateW28Pane},
	{name: "copilotTrustgateW34Pane", width: 34, note: "", pane: copilotTrustgateW34Pane},
	{name: "copilotTrustgateW40Pane", width: 40, note: "the widest rung raw flatten fails at", pane: copilotTrustgateW40Pane},
	{name: "copilotTrustgateW60Pane", width: 60, note: "headline on one line", pane: copilotTrustgateW60Pane},
	{name: "copilotTrustgateW120Pane", width: 120, note: "headline on one line", pane: copilotTrustgateW120Pane},
}

// TestCopilotTrustGateFiresAtEveryDrivenWidth is the positive half. paneCoverage asserts the
// same thing generically; this exists so a failure names the rung in this file's own terms,
// and because the negative halves below need the ladder anyway.
func TestCopilotTrustGateFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotTrustgateLadder {
		t.Run(c.label(), func(t *testing.T) {
			g, up := copilot.GateUp(c.pane)
			require.True(t, up, "the folder-trust gate must be detected")
			require.Equal(t, "trust", g.Name)
		})
	}
}

// TestCopilotTrustGateNeedsTheWallStrippingScan is the measurement that justifies Task 2's
// primitive existing at all, as an assertion rather than a claim in a comment: the SAME
// literal read through the flat window every declarative matcher uses is absent from 40 down.
// If this ever goes green at 40, flattenChrome has started reconstructing across box borders
// and flattenBottomBox is no longer load-bearing.
func TestCopilotTrustGateNeedsTheWallStrippingScan(t *testing.T) {
	for _, c := range copilotTrustgateLadder {
		flat := flattenChrome(c.pane, WindowPrompt)
		if c.width >= 60 {
			require.Containsf(t, flat, copilotTrustHeadline,
				"%s: the headline is on one line here, so the flat window still reaches it", c.label())
			continue
		}
		require.NotContainsf(t, flat, copilotTrustHeadline,
			"%s: the headline wraps inside the borders here, so the flat window must NOT "+
				"reconstruct it — that is what flattenBottomBox is for", c.label())
	}
}

// TestCopilotTrustGateTitleIsGoneAtWidth20 is the height cliff the width ladder hides, and the
// reason this matcher keys on the headline and an option label rather than on the title. It
// asserts the title's absence at the narrowest rung and its presence one rung up, so a future
// build that stops overflowing reddens this instead of silently widening the matcher's options.
func TestCopilotTrustGateTitleIsGoneAtWidth20(t *testing.T) {
	const title = "Confirm folder trust"

	flat20, ok := flattenBottomBox(copilotTrustgateW20Pane)
	require.True(t, ok)
	require.NotContains(t, flat20, title,
		"at 20 columns the box outgrows the pane and the title row scrolls off the top")

	flat24, ok := flattenBottomBox(copilotTrustgateW24Pane)
	require.True(t, ok)
	require.Contains(t, flat24, title,
		"one rung up the box still fits, so the cliff is between these two and not below both")
}

// TestCopilotDialogsAreAlsoComposersToTheBoxPredicate holds the collision the adapter's
// InputBoxPrompts comment describes: the gate's selector is the composer's own "❯", so
// InputBoxVisible answers TRUE on a screen that consumes keystrokes. That is why GateUp and
// DetectPrompt are the guards keeping a queued first prompt off this dialog — exactly as for
// claude and agy — and it is why a gemini-style composer veto inside the matcher is wrong here.
func TestCopilotDialogsAreAlsoComposersToTheBoxPredicate(t *testing.T) {
	for _, c := range copilotTrustgateLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.True(t, copilot.InputBoxVisible(c.pane),
				"the dialog reads as a composer, which is the hazard GateUp exists to cover")
			_, up := copilot.GateUp(c.pane)
			require.True(t, up, "and GateUp is what actually covers it")
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./session/agent/ 2>&1 | tail -20
```

Expected: FAIL — `undefined: copilot`, `undefined: copilotTrustHeadline`.

- [ ] **Step 3: Add the key, the adapter and the predicate**

In `session/agent/agent.go`, add to the key block:

```go
	KeyCopilot Key = "copilot"
```

In `session/agent/registry.go`, add after the `agy` adapter and before `generic`:

```go
// GitHub Copilot CLI (github/copilot-cli, npm @github/copilot). DRIVEN at 1.0.80 on Linux,
// 2026-08-26, in an isolated COPILOT_HOME against a scratch git repo with the organization's
// token injected via ATR_CAP_ENV — three surfaces, each with a verbatim width ladder in
// copilot_pane_test.go: the folder-trust Gate, the approval PromptMatcher, and the busy
// marker. The design record is docs/superpowers/specs/2026-08-26-copilot-cli-integration-design.md.
//
// WHAT SHAPE THIS ADAPTER IS. Its two dialogs are closed round boxes whose bottom border is
// the last non-empty line at every driven rung — gemini's shape, so both matchers anchor on
// bottomBoxBlock. Its composer and busy row are claude's arrangement: a borderless composer
// between two horizontal rules, with the status row replacing the hint row BELOW it, so
// MarkerWindow stays 0 and the footer anchor finds the marker. Two vendors' shapes in one
// CLI, which is why neither codex's GateWindow nor gemini's composer veto transfers.
//
// WHY NOT GateWindow, since codex's trust gate looks like the same problem. Codex draws its
// overlay with no border at all, so its headline is intact and merely pushed out of a
// line-count budget, and a wider window reaches it. Copilot's headline is DESTROYED — the
// border runes and their padding sit between its fragments — so no window reaches it and
// flattenBottomBox is the remedy instead. TestCopilotTrustGateNeedsTheWallStrippingScan
// measures the difference at every rung.
//
// WHY NOT a composer veto inside the matchers, since geminiTrustGateVisible has one. Copilot's
// selector IS the composer glyph "❯" and it sits on the dialog's highlighted row, so the veto
// would return false on every rung of both ladders.
// TestCopilotDialogsAreAlsoComposersToTheBoxPredicate holds that collision.
//
// HookSupport is deliberately FALSE even though copilot has hooks and they fire. The
// invocation schema is claude-compatible, keyed by camelCase event names; the OUTPUT schema is
// not. Claude's nested hookSpecificOutput.additionalContext fires and delivers nothing, while
// a flat {"additionalContext": …} works — both driven. The field routes through claude's
// emitter, so setting it here ships a brief that is registered, documented and dead, which is
// the #773 failure mode verbatim. #773 replaces the bool with a capability that can say which
// schema; this adapter waits for it.
//
// Resume is deliberately NIL. `--continue` and `-r, --resume` are both in --help (VENDOR at
// 1.0.80), but ResumeProbe's needle must pin the listing rather than the bare word and that
// has not been chosen, and the behaviour in a directory with nothing to resume has not been
// driven. A nil Resume relaunches blank, which is the adapter's safe mode; a needle guessed
// off a help line is the failure mode ResumeProbe exists to prevent.
var copilot = &Adapter{
	Key:         KeyCopilot,
	DisplayName: "Copilot CLI",
	aliases:     []string{"copilot"},

	VerifiedVersion:  "1.0.80",
	DriftGranularity: GranularityMinor,

	// InputBoxPrompts deliberately nil: the composer glyph is "❯" (U+276F), byte-verified
	// with cat -vet against the driven panes, and defaultPrompts already accepts it. It
	// collides with both dialogs' selector, which is the hazard that field's doc describes
	// for claude and agy — GateUp and DetectPrompt are what exclude those screens.

	Gates: []Gate{
		// The folder-trust screen. A conjunction through Match, not Contains: the headline
		// alone is a plausible sentence for a session to print while discussing this file,
		// and Contains would read a flat bottom-N window that cannot reconstruct it below 60
		// columns anyway. Both literals sit LOW in the dialog because at 20 columns the box
		// outgrows the pane and its title scrolls off the top —
		// TestCopilotTrustGateTitleIsGoneAtWidth20.
		{Name: "trust", Match: copilotTrustGateVisible},
	},
}

// copilotTrustHeadline and copilotTrustOption are the folder-trust gate's two literals, as
// consts so the guards measure against the symbol the matcher reads rather than restating a
// string. Both survive every driven rung; the title does not, which is why neither is it.
//
// THEY ARE NOT GUARDED ALIKE, and saying so is the point. Shortening either one keeps the
// ladder green — a conjunction only narrows as its terms lengthen — so what the guards hold
// is that both are REACHABLE at every rung, not that either is the shortest sufficient form.
// Lengthening one past what the narrowest rung renders is what reddens the ladder.
const (
	copilotTrustHeadline = "Do you trust the files in this folder?"
	copilotTrustOption   = "Yes, and remember this folder for future sessions"
)

// copilotTrustGateVisible reports copilot's folder-trust screen: both literals inside the
// bottom-most anchored box, read through flattenBottomBox so a headline hard-wrapped across
// the box's own borders still reconstructs.
//
// Two clauses doing different jobs, the way geminiTrustGateVisible's do. The box says the
// dialog is LIVE — a dismissed one is replaced by the composer, which is not an anchored box,
// so this goes false; and it is what keeps the wall-stripping scan off the whole pane. The
// literal pair says WHICH dialog, and both terms are needed: the headline is ordinary English
// and the option label is the specific half.
//
// What the box clause narrows and does not close is bottomBoxBlock's own disclosed exposure —
// quoted box art that ends the pane. A transcript quoting this dialog and stopping exactly at
// its bottom border does fire this. That direction fails CLOSED (a queued prompt is held,
// never mis-delivered), which GateUp's own doc records as the acceptable one.
func copilotTrustGateVisible(content string) bool {
	flat, ok := flattenBottomBox(content)
	if !ok {
		return false
	}
	return strings.Contains(flat, copilotTrustHeadline) && strings.Contains(flat, copilotTrustOption)
}
```

Then extend the registry list:

```go
var registry = []*Adapter{claude, codex, gemini, aider, agy, copilot}
```

- [ ] **Step 4: Wire the coverage and pin tables**

In `session/agent/pane_width_test.go`, add to `paneCoverage`:

```go
	// copilot's gate is one Match over a ladder driven at every rung from 120 to 20. The 20
	// rung is included on purpose even though it is where the dialog's title disappears: the
	// matcher keys on the headline and an option label, both of which survive, so this is a
	// rung the predicate FIRES on rather than a miss. Contrast gemini, whose 20 rung is a real
	// cliff and lives outside its ladder.
	"copilot/gate/trust": copilotTrustgateLadder,
```

and to `wantRungs`:

```go
	// Eight rungs, one per driven width, and the floor really is 20 rather than "20 is simply
	// where driving stopped": the narrowest rung was driven and the gate fires there. What
	// fails at 20 is the TITLE, which no matcher here reads.
	"copilot/gate/trust": {20, 24, 26, 28, 34, 40, 60, 120},
```

In `session/agent/drift_fields_test.go`, add to the `want` map:

```go
		// Driven at 1.0.80 across three surfaces, each with its own verbatim width ladder:
		// the folder-trust gate, the approval prompt and the busy marker. registry.go's
		// copilot header enumerates them; this row only pins the version they were driven at.
		KeyCopilot: {"1.0.80", GranularityMinor, nil},
```

and change the count:

```go
	if len(got) != 6 {
		t.Fatalf("Adapters() returned %d adapters, want 6", len(got))
	}
```

- [ ] **Step 5: Add the identity glyph**

In `ui/theme/agent.go`, add to `plainAgentGlyphs`:

```go
		"copilot": "⎈", // U+2388 HELM SYMBOL — a pilot's wheel; see below on the neighbour it was checked against
```

and to `asciiAgentGlyphs`:

```go
	g["copilot"] = "P"
```

Extend that file's two rule comments. Under the plain table's header note on what a new glyph is checked against, add:

```go
// copilot's "⎈" (U+2388 HELM SYMBOL) is a pilot's wheel. The neighbour it was checked
// against is Branch's "⎇" (U+2387) — one codepoint below it, so a font rendering one
// renders the other, and they share a FRAME (the agent glyph is pinned to the far right
// of the row's first line, the branch chip sits on its second). They do not share a
// shape: ⎇ is an alternate-key fork, ⎈ is a spoked wheel. The circle family was ruled
// out first — Ready "●", ReadySeen "○" and AcctAvailable "●" have it — and the diamond
// family second, for the reason codex's entry already records about "◆".
```

and in the ascii table's letter-derivation list, between `agy` and `generic`:

```go
//	copilot P  c is claude's and o is ReadySeen, so co(p)ilot
```

- [ ] **Step 6: Add copilot to the detected binaries**

In `config/agents.go`:

```go
var knownAgentBins = []string{"claude", "codex", "gemini", "aider", "agy", "copilot"}
```

No change to `detectAgentCommand` is needed: `copilot` resolves on the bare `PATH` (DRIVEN — through a mise shim on the drive host), so the plain `exec.LookPath` branch already covers it. Only claude needs the shell-profile-aware probe.

- [ ] **Step 7: Run the tests to verify they pass**

```bash
go test ./session/agent/ ./config/ ./ui/... ./app/ ./internal/doctor/ 2>&1 | tail -20
```

Expected: PASS. In particular `TestEveryDeclaredMatcherIsCoveredOrExempt` (the new gate key is covered), `TestEveryAgentAdapterHasAnIdentityGlyph`, `TestASCIIAgentGlyphsDoNotCollide`, `TestLegendCoversRowVocabulary` and `TestLegendLinesFit` must all be green. If `TestLegendLinesFit` fails, the agents legend group has outgrown the 80-column line budget — the group wraps with an indent, so the fix is in `app/help.go`'s legend layout, not in the glyph.

- [ ] **Step 8: Mutation-prove each new assertion**

Commit first:

```bash
git add -A && git commit -m "wip: copilot adapter and trust gate"
```

Then one at a time, revert with `git checkout -- <file>` after each:

| mutation | must redden |
|---|---|
| `copilotTrustGateVisible` drops the `ok` check and matches on `flattenChrome(content, WindowPrompt)` | `TestCopilotTrustGateFiresAtEveryDrivenWidth` at 40 and below |
| `copilotTrustGateVisible` requires only `copilotTrustHeadline` | nothing — **expected**; a conjunction's terms are not individually guarded, and the const's own comment says so. Confirm the ladder stays green and move on. |
| `copilotTrustHeadline` lengthened to `"Do you trust the files in this folder? Really?"` | `TestCopilotTrustGateFiresAtEveryDrivenWidth` at every rung |
| `copilotTrustOption` changed to `"Confirm folder trust"` | `TestCopilotTrustGateFiresAtEveryDrivenWidth` at the 20 rung only — which is the height cliff, proven by where it fails |
| `copilotTrustGateVisible` adds gemini's composer veto (`isInputBoxLine` in the block ⇒ false) | `TestCopilotTrustGateFiresAtEveryDrivenWidth` at **every** rung |
| `wantRungs["copilot/gate/trust"]` drops the `20` | the width invariant in `pane_width_test.go` |
| remove `"copilot"` from `plainAgentGlyphs` | `TestEveryAgentAdapterHasAnIdentityGlyph` |
| set `plainAgentGlyphs["copilot"] = "✦"` | `TestEveryAgentAdapterHasAnIdentityGlyph` (collides with gemini) |
| set `asciiAgentGlyphs["copilot"] = "x"` | `TestASCIIAgentGlyphsDoNotCollide` |

- [ ] **Step 9: Run the gate and commit**

```bash
PATH="$(go env GOPATH)/bin:$PATH" just ci
```

```bash
git add -A
git commit -m "feat(agent): a copilot adapter and its folder-trust gate

Driven at GitHub Copilot CLI 1.0.80 on a width ladder from 120 to 20, in an
isolated COPILOT_HOME with the organization's token injected. The gate is a
conjunction of two literals inside the bottom-most anchored box, read through
flattenBottomBox: raw matching of the headline fails from 40 down, because the
dialog wraps its body inside box borders rather than truncating it.

Both literals sit low in the dialog on purpose. At 20 columns the box outgrows
the pane and the title row scrolls off the top, so a title-keyed matcher would
miss a gate that is plainly on screen.

HookSupport stays false: copilot's hook output schema is a flat additionalContext
object, not claude's nested one, and the field routes through claude's emitter.
Resume stays nil pending a driven needle."
```

---

## Task 4: The approval prompt

**Files:**
- Modify: `session/agent/registry.go` — `Prompts`, `copilotApprovalVisible`, two literal consts
- Modify: `session/agent/copilot_pane_test.go` — eight `approval` panes, `copilotApprovalLadder`, assertions
- Modify: `session/agent/pane_width_test.go` — `paneCoverage["copilot/prompt/approval"]`, `wantRungs["copilot/prompt/approval"]`

**Interfaces:**
- Consumes: `flattenBottomBox` (Task 2), `copilot *Adapter` (Task 3).
- Produces: `copilotApprovalHeadline` / `copilotApprovalOption` consts, `copilotApprovalLadder []paneCapture`.

- [ ] **Step 1: Write the failing test**

Paste the eight `copilotApprovalW*Pane` consts verbatim from `fixtures.go.txt`, then append to `copilot_pane_test.go`:

```go
// copilotApprovalLadder is the out-of-worktree path approval at every driven width. Unlike
// the trust gate's box, this one FITS the 40-row pane at every rung — its top border is on
// screen at all eight — so its title survives too. That is box height rather than a property
// of titles, and the matcher keys on the headline and the option label regardless.
var copilotApprovalLadder = []paneCapture{
	{name: "copilotApprovalW20Pane", width: 20, note: "option label in four lines", pane: copilotApprovalW20Pane},
	{name: "copilotApprovalW24Pane", width: 24, note: "", pane: copilotApprovalW24Pane},
	{name: "copilotApprovalW26Pane", width: 26, note: "", pane: copilotApprovalW26Pane},
	{name: "copilotApprovalW28Pane", width: 28, note: "the widest rung raw flatten fails at", pane: copilotApprovalW28Pane},
	{name: "copilotApprovalW34Pane", width: 34, note: "", pane: copilotApprovalW34Pane},
	{name: "copilotApprovalW40Pane", width: 40, note: "selector renders \"❯2.\" with no space", pane: copilotApprovalW40Pane},
	{name: "copilotApprovalW60Pane", width: 60, note: "", pane: copilotApprovalW60Pane},
	{name: "copilotApprovalW120Pane", width: 120, note: "everything on one line", pane: copilotApprovalW120Pane},
}

// TestCopilotApprovalFiresAtEveryDrivenWidth is the positive half, and it also asserts the
// matcher's NoAutoTap, because that flag is the load-bearing part of this entry: the dialog's
// pre-selected option WIDENS the allowed-path list rather than approving one action.
func TestCopilotApprovalFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotApprovalLadder {
		t.Run(c.label(), func(t *testing.T) {
			m, ok := copilot.DetectPrompt(c.pane)
			require.True(t, ok, "the approval dialog must be detected")
			require.Equal(t, "approval", m.Name)
			require.True(t, m.NoAutoTap,
				"Enter here selects \"Yes, and add these directories to the allowed list\", "+
					"which extends the agent's filesystem reach past its worktree for the session")
		})
	}
}

// TestCopilotApprovalNeedsTheWallStrippingScan is the approval half of the same measurement
// the gate carries: raw flattening reaches this headline down to 34 and no further.
func TestCopilotApprovalNeedsTheWallStrippingScan(t *testing.T) {
	for _, c := range copilotApprovalLadder {
		flat := flattenChrome(c.pane, WindowPrompt)
		if c.width >= 34 {
			require.Containsf(t, flat, copilotApprovalHeadline,
				"%s: the headline is on one line here", c.label())
			continue
		}
		require.NotContainsf(t, flat, copilotApprovalHeadline,
			"%s: the headline wraps inside the borders here, so the flat window must NOT "+
				"reconstruct it", c.label())
	}
}

// TestCopilotApprovalOptionExcludesTheSelector is why copilotApprovalOption starts at "Yes,"
// rather than at "❯ 2.". The gap between selector and number is not stable and NOT MONOTONIC
// in width: "❯ 2." at 120, 60, 34 and 28, "❯2." at 40, 26, 24 and 20. A matcher including the
// prefix would have passed a 120-column check, failed at 40, and passed again at 34 — the
// shape of drift a single wide capture cannot see.
func TestCopilotApprovalOptionExcludesTheSelector(t *testing.T) {
	spaced := map[int]bool{120: true, 60: true, 34: true, 28: true}
	for _, c := range copilotApprovalLadder {
		t.Run(c.label(), func(t *testing.T) {
			flat, ok := flattenBottomBox(c.pane)
			require.True(t, ok)
			require.Contains(t, flat, copilotApprovalOption,
				"the label without the selector reaches every rung")

			want := "❯2. " + copilotApprovalOption
			if spaced[c.width] {
				want = "❯ 2. " + copilotApprovalOption
			}
			require.Containsf(t, flat, want,
				"this rung renders the selector %q; the OTHER spelling is what a "+
					"prefix-bearing literal would have missed", want[:len(want)-len(copilotApprovalOption)])
		})
	}
}

// TestCopilotApprovalAndTrustGateDoNotCrossMatch is the discriminator, and it is needed
// because the two dialogs share their decline row ("3. No (Esc)") and their whole navigation
// footer ("↑/↓ to navigate · enter to select · esc to cancel"). Neither shared string can tell
// them apart, so each matcher's literals must be the ones only its own dialog renders. A
// crossing failure would surface as a trust gate reported on a live approval, which holds the
// queued prompt forever, or as an approval reported on a startup gate, which is worse.
func TestCopilotApprovalAndTrustGateDoNotCrossMatch(t *testing.T) {
	for _, c := range copilotApprovalLadder {
		t.Run("approval pane is not a gate: "+c.label(), func(t *testing.T) {
			_, up := copilot.GateUp(c.pane)
			require.False(t, up)
		})
	}
	for _, c := range copilotTrustgateLadder {
		t.Run("gate pane is not an approval: "+c.label(), func(t *testing.T) {
			_, ok := copilot.DetectPrompt(c.pane)
			require.False(t, ok)
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./session/agent/ 2>&1 | tail -20
```

Expected: FAIL — `undefined: copilotApprovalHeadline`.

- [ ] **Step 3: Add the prompt matcher**

In `session/agent/registry.go`, add `Prompts` to the `copilot` adapter, above `Gates`:

```go
	Prompts: []PromptMatcher{
		// The out-of-worktree path approval. NoAutoTap for a strictly worse reason than the
		// one it carries on codex, where Enter approves a single command: this dialog's
		// pre-selected option is the SECOND one, "Yes, and add these directories to the
		// allowed list", so Enter widens the session's allowed-path list rather than
		// approving one action. An autoyes tap would silently extend a copilot agent's
		// filesystem reach past its worktree for the rest of the session — a sandbox
		// widening performed by a convenience feature.
		{Name: "approval", NoAutoTap: true, Match: copilotApprovalVisible},
	},
```

and after `copilotTrustGateVisible`:

```go
// copilotApprovalHeadline and copilotApprovalOption are the approval dialog's two literals.
// The option label deliberately starts at "Yes," and not at the selector: the space between
// "❯" and "2." is not stable and not monotonic in width, so including it would pass a wide
// check and fail at a narrower one while passing again narrower still.
// TestCopilotApprovalOptionExcludesTheSelector carries the eight readings.
//
// Neither the decline row "3. No (Esc)" nor the "↑/↓ to navigate · enter to select · esc to
// cancel" footer appears here, though both survive every width: the folder-trust dialog
// renders them identically, so neither can discriminate.
const (
	copilotApprovalHeadline = "Do you want to allow this?"
	copilotApprovalOption   = "Yes, and add these directories to the allowed list"
)

// copilotApprovalVisible reports copilot's out-of-worktree path approval: both literals inside
// the bottom-most anchored box, read through flattenBottomBox. Same two clauses as
// copilotTrustGateVisible — the box says the dialog is live, the pair says which dialog — and
// the pair matters more here than there, because this adapter's two dialogs share their
// decline row and their entire navigation footer.
//
// It carries NoAutoTap on the matcher rather than relying on this predicate's precision, and
// that is not belt-and-braces: bottomBoxBlock's disclosed exposure is quoted box art that ends
// the pane, and a session reading this very file could produce it. What NoAutoTap costs is a
// needs-input on a working session; what it buys is that nothing Atrium does can widen the
// agent's allowed-path list.
func copilotApprovalVisible(content string) bool {
	flat, ok := flattenBottomBox(content)
	if !ok {
		return false
	}
	return strings.Contains(flat, copilotApprovalHeadline) &&
		strings.Contains(flat, copilotApprovalOption)
}
```

- [ ] **Step 4: Wire the coverage and pin tables**

In `paneCoverage`:

```go
	// Eight rungs, all positive. This dialog's box fits the 40-row pane at every driven width,
	// so unlike the gate's ladder nothing here is a height edge.
	"copilot/prompt/approval": copilotApprovalLadder,
```

In `wantRungs`:

```go
	"copilot/prompt/approval": {20, 24, 26, 28, 34, 40, 60, 120},
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./session/agent/ 2>&1 | tail -10
```

Expected: PASS, and `TestAutoTapRequiresAnAnchoredMatcher` in particular — its allowlist assertion must remain exactly `[]string{"claude/permission", "aider/confirm"}`. If copilot appears there, `NoAutoTap` was dropped.

- [ ] **Step 6: Mutation-prove each new assertion**

Commit first (`git add -A && git commit -m "wip: copilot approval matcher"`), then:

| mutation | must redden |
|---|---|
| drop `NoAutoTap` from the approval matcher | `TestCopilotApprovalFiresAtEveryDrivenWidth` **and** `TestAutoTapRequiresAnAnchoredMatcher` |
| `copilotApprovalOption = "❯ 2. Yes, and add these directories to the allowed list"` | `TestCopilotApprovalFiresAtEveryDrivenWidth` at 40, 26, 24 and 20 — and NOT at 34, which is the non-monotonicity |
| `copilotApprovalVisible` matches on `flattenChrome(content, WindowPrompt)` | `TestCopilotApprovalFiresAtEveryDrivenWidth` at 28 and below |
| `copilotApprovalHeadline = "3. No (Esc)"` | `TestCopilotApprovalAndTrustGateDoNotCrossMatch` (the gate panes start matching) |
| `copilotTrustOption = copilotApprovalOption` | `TestCopilotTrustGateFiresAtEveryDrivenWidth` |
| flip the `spaced` map in `TestCopilotApprovalOptionExcludesTheSelector` to `{40, 26, 24, 20}` | that test — proving it reads the panes rather than restating the const |

- [ ] **Step 7: Run the gate and commit**

```bash
PATH="$(go env GOPATH)/bin:$PATH" just ci
git add -A
git commit -m "feat(agent): copilot's path-approval prompt, never auto-tapped

Driven at 1.0.80 from 120 to 20 columns. NoAutoTap is mandatory rather than
defensive: the dialog's pre-selected option is \"Yes, and add these directories
to the allowed list\", so Enter widens the session's allowed-path list rather
than approving one action. That is worse than codex's case, where Enter approves
a single command.

The option label excludes the selector. The space between \"❯\" and \"2.\" is
not monotonic in width — present at 120, 60, 34 and 28, absent at 40, 26, 24 and
20 — so a prefix-bearing literal would have passed a wide check, failed at 40 and
passed again at 34.

Both of this adapter's dialogs share their decline row and their whole navigation
footer, so each matcher keys only on strings its own dialog renders."
```

---

## Task 5: The busy marker, and the idle panes as negative evidence

**Files:**
- Modify: `session/agent/registry.go` — `BusyMarkers`
- Modify: `session/agent/copilot_pane_test.go` — eight `working` panes, `copilotBusyLadder`, assertions
- Modify: `session/agent/pane_width_test.go` — `paneCoverage["copilot/busy"]`, `wantRungs["copilot/busy"]`

**Interfaces:**
- Consumes: `copilot *Adapter` (Task 3), the re-driven captures (Task 1).
- Produces: `copilotBusyLadder []paneCapture`, and — only if Task 1's Step 8 found truncated rungs — `copilotBusyTruncatedRungs []paneCapture`.

**This task's ladder is written from Task 1's readings, not from the spec.** The spec's old width table has no capture behind it and must not be transcribed. Two branches below; take the one Task 1 measured.

- [ ] **Step 1: Read the re-driven ladder and decide which branch applies**

```bash
CAP=~/.local/share/atrium-captures/copilot-1.0.80/captures
for w in 120 60 40 34 28 26 24 20; do
  printf 'w%-4s footer: %s\n' "$w" "$(tail -3 $CAP/working-w$w.txt | tr '\n' '/')"
done
```

Record, per rung: whether the contiguous string `Working` is present, and what the footer looks like. **Branch A** — `Working` present at all eight: one ladder, eight positive rungs, `LiveSpinner` stays nil. **Branch B** — `Working` absent at some narrow rung because the multi-column footer split it mid-word: that rung leaves the ladder and joins a truncated-rungs list as negative evidence, exactly as `geminiBusyTruncatedRungs` does.

- [ ] **Step 2: Write the failing test (Branch A)**

Paste the eight re-driven `copilotWorkingW*Pane` consts from `~/.local/share/atrium-captures/copilot-1.0.80/fixtures-working.go.txt`, then append:

```go
// copilotBusyLadder is a live turn at every driven width. The marker sits in the status row
// that REPLACES the hint row below the composer, so MarkerWindow stays 0 and footerRegion's
// below-the-box anchor finds it — claude's arrangement, not codex's or gemini's, both of
// which render their status row ABOVE the composer and need a window instead.
//
// WHY THIS LADDER WAS DRIVEN TWICE. The first sweep ended its turn after the width-60 rung, so
// six of its eight rungs captured an IDLE pane while looking like a measurement — an identical
// credit figure across all six and the hint row where the status row belongs. A ladder is only
// valid for a transient state if the state outlives the sweep. The invalid captures are kept
// beside the run directory under captures-invalid-working-2026-08-26 rather than deleted,
// because the design spec cites them as the evidence that they were invalid.
var copilotBusyLadder = []paneCapture{
	{name: "copilotWorkingW20Pane", width: 20, note: "", pane: copilotWorkingW20Pane},
	{name: "copilotWorkingW24Pane", width: 24, note: "", pane: copilotWorkingW24Pane},
	{name: "copilotWorkingW26Pane", width: 26, note: "", pane: copilotWorkingW26Pane},
	{name: "copilotWorkingW28Pane", width: 28, note: "footer becomes multi-column", pane: copilotWorkingW28Pane},
	{name: "copilotWorkingW34Pane", width: 34, note: "", pane: copilotWorkingW34Pane},
	{name: "copilotWorkingW40Pane", width: 40, note: "", pane: copilotWorkingW40Pane},
	{name: "copilotWorkingW60Pane", width: 60, note: "", pane: copilotWorkingW60Pane},
	{name: "copilotWorkingW120Pane", width: 120, note: "status row on one line", pane: copilotWorkingW120Pane},
}

// TestCopilotBusyMarkerFiresAtEveryDrivenWidth is the positive half.
func TestCopilotBusyMarkerFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.True(t, copilot.HasBusyMarker(c.pane))
		})
	}
}

// TestCopilotBusyMarkerCannotKeyOnTheInterruptHint is why BusyMarkers holds "Working" alone.
// The status row reads "<spinner> Working · <N> B esc interrupt", so the byte counter sits
// BETWEEN the two words and "Working esc interrupt" is never contiguous at any width — a fact
// a wide capture alone would suggest is fine. Below the width at which the footer goes
// multi-column, its cells wrap independently and "esc interrupt" stops being contiguous too.
// Both halves are asserted against the driven panes rather than described.
func TestCopilotBusyMarkerCannotKeyOnTheInterruptHint(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			region := footerRegion(c.pane)
			require.NotContains(t, region, "Working esc interrupt",
				"the byte counter sits between the words at every width")
			if c.width >= 34 {
				require.Contains(t, region, "esc interrupt",
					"the hint is contiguous while the footer is single-column")
				return
			}
			require.NotContains(t, region, "esc interrupt",
				"the multi-column footer wraps its cells independently, so a matcher keyed "+
					"on this hint would miss every narrow pane")
		})
	}
}

// TestCopilotIdlePanesAreNeitherGateNorPrompt is the negative direction paneCoverage cannot
// express (that table is positive-only). It uses the busy ladder's panes because a working
// pane is the shape most likely to false-match: it is the one that actually renders a composer
// and a footer, where both dialog matchers must stay silent.
//
// The third assertion is the one with teeth. A copilot dialog reads as a composer to
// InputBoxVisible, so that predicate cannot tell the two apart; what makes the collision
// harmless is that GateUp and DetectPrompt disagree on these panes and agree on the dialogs.
func TestCopilotIdlePanesAreNeitherGateNorPrompt(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			_, up := copilot.GateUp(c.pane)
			require.False(t, up, "a live turn is not a startup gate")
			_, ok := copilot.DetectPrompt(c.pane)
			require.False(t, ok, "a live turn is not a blocking prompt")
			require.True(t, copilot.InputBoxVisible(c.pane),
				"and the composer IS readable here, which is what makes prompt delivery work")
		})
	}
}
```

**If Branch B applies instead**, keep every test above but move the truncated rung(s) out of `copilotBusyLadder` into:

```go
// copilotBusyTruncatedRungs are the rungs where the multi-column footer splits "Working"
// mid-word, so no substring survives and no window value could reach one. They are negative
// evidence, not a windowing failure: the row is ON SCREEN and the phrase is not there.
// This is the rung LiveSpinner exists for — the animating spinner is the only signal left —
// and it is deliberately not a standalone latch, so a session here reports idle until the
// spinner support lands.
var copilotBusyTruncatedRungs = []paneCapture{ /* the measured rung(s) */ }

func TestCopilotBusyMarkerIsTruncatedAtTheNarrowestRungs(t *testing.T) {
	for _, c := range copilotBusyTruncatedRungs {
		t.Run(c.label(), func(t *testing.T) {
			require.False(t, copilot.HasBusyMarker(c.pane),
				"the marker is split mid-word here; recording the miss is the point")
			require.NotContains(t, footerRegion(c.pane), "Working")
		})
	}
}
```

and drop those widths from `wantRungs["copilot/busy"]`.

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./session/agent/ -run Copilot 2>&1 | tail -20
```

Expected: FAIL — `HasBusyMarker` returns false because `BusyMarkers` is still empty.

- [ ] **Step 4: Add the busy marker**

In `session/agent/registry.go`, add to the `copilot` adapter above `Prompts`:

```go
	// "Working" alone, and the two words it is deliberately not paired with are the finding.
	// The status row reads "<spinner> Working · <N> B esc interrupt": the byte counter sits
	// BETWEEN "Working" and "esc interrupt", so that pair is never contiguous at any width,
	// and below the width at which the footer goes multi-column its cells wrap independently
	// so "esc interrupt" stops being contiguous on its own too.
	// TestCopilotBusyMarkerCannotKeyOnTheInterruptHint asserts both halves per rung.
	//
	// The floor is a datum, not a sentence: copilotBusyLadder's widths feed wantRungs, which
	// is where "the marker survives to N columns" is computed from real panes and real
	// predicates rather than restated.
	BusyMarkers: []string{"Working"},
	// MarkerWindow deliberately 0. The status row REPLACES the hint row below the composer,
	// so footerRegion's below-the-box anchor finds it. This is claude's arrangement; codex
	// and gemini paint their status row above the composer, which is why they need a window,
	// and copying one of theirs here would search past the row entirely.
```

`LiveSpinner` stays nil under Branch A: with the marker reachable at every driven rung there is nothing for a spinner detector to rescue, and a spinner predicate with no rung that needs it is a surface nobody drove. Under Branch B, leave it nil too and record the gap — `LiveSpinner` needs its own driven evidence (the spinner's frame set across the animation), which this ladder's single frame per rung cannot supply.

- [ ] **Step 5: Wire the coverage and pin tables**

In `paneCoverage`:

```go
	// The re-driven ladder. Its predecessor captured an idle pane at every rung below 60 —
	// see copilotBusyLadder's own header for how that happened and where those captures went.
	"copilot/busy": copilotBusyLadder,
```

In `wantRungs`, the widths Task 1 measured — under Branch A:

```go
	"copilot/busy": {20, 24, 26, 28, 34, 40, 60, 120},
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go test ./session/agent/ 2>&1 | tail -10
```

Expected: PASS. `TestEveryDeclaredMatcherIsCoveredOrExempt` now sees all three copilot keys covered, and `paneCoverageExempt` must contain **no** copilot entry.

- [ ] **Step 7: Mutation-prove each new assertion**

Commit first, then:

| mutation | must redden |
|---|---|
| `BusyMarkers: []string{"Working esc interrupt"}` | `TestCopilotBusyMarkerFiresAtEveryDrivenWidth` at every rung |
| `BusyMarkers: []string{"esc interrupt"}` | `TestCopilotBusyMarkerFiresAtEveryDrivenWidth` at the narrow rungs |
| `MarkerWindow: 8` (codex's) | nothing at the wide rungs — a window that includes the footer still finds it. **If it reddens nothing at all**, add an assertion that `footerRegion` is what locates the marker, since otherwise the field is unguarded. |
| flip `c.width >= 34` to `c.width >= 28` in `TestCopilotBusyMarkerCannotKeyOnTheInterruptHint` | that test — proving the threshold is read off panes |
| `copilotTrustHeadline = "Working"` | `TestCopilotIdlePanesAreNeitherGateNorPrompt` |
| drop a rung from `wantRungs["copilot/busy"]` | the width invariant |

- [ ] **Step 8: Run the gate and commit**

```bash
PATH="$(go env GOPATH)/bin:$PATH" just ci
git add -A
git commit -m "feat(agent): copilot's busy marker, on a re-driven ladder

BusyMarkers keys on \"Working\" alone. The status row reads \"<spinner> Working
· <N> B esc interrupt\", so the byte counter sits between the two words and that
pair is never contiguous at any width; below the width at which the footer goes
multi-column, its cells wrap independently and \"esc interrupt\" stops being
contiguous on its own too. Both halves are asserted per rung.

MarkerWindow stays 0: the status row replaces the hint row BELOW the composer,
so the footer anchor finds it. That is claude's arrangement, not codex's.

The ladder was driven twice. The first sweep's turn ended after the width-60 rung,
so six of its eight rungs captured an idle pane while looking like a measurement.
The invalid captures are kept rather than deleted, because the design spec cites
them as the evidence that they were invalid."
```

---

## Task 6: Documentation, and a guard for the one unguarded site

`README.md` names the agent set in three places and nothing holds any of them to `knownAgentBins`. Every other site this change touches has a drift test; this one is the exception, and closing it is the same defect class the rest of the table exists to catch.

**Files:**
- Modify: `README.md`
- Create: `config/readme_agents_test.go`

**Interfaces:**
- Consumes: `knownAgentBins` (Task 3).

- [ ] **Step 1: Write the failing test**

Create `config/readme_agents_test.go`. Modelled on the three existing README drift guards (`TestReadmeDocumentsEveryCommand`, `TestReadmeDocumentsEveryConfigField`, `TestReadmeDocumentsEveryBinding`), and living in `config` because that is where `knownAgentBins` is.

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadmeNamesEveryProbedAgent is the fourth README drift guard, and it exists because
// adding an agent used to touch three README sentences that no test could see. Every other
// site a new adapter touches has one — paneCoverage derives its keys from the registry, the
// glyph tables are held by TestEveryAgentAdapterHasAnIdentityGlyph, the version pin by
// TestAdaptersExposesSeededVersions — so the README was the one place a sixth agent could
// ship undocumented with a green suite.
//
// It asserts against the FIRST-RUN PROBE SENTENCE specifically, not against the README as a
// whole. A bare document-wide search would pass on any agent whose name happens to appear
// anywhere in the file — "codex" occurs in several unrelated examples — which is the
// coincidental pass TestReadmeDocumentsEveryPaletteToken's own header warns about, and it
// reads one subsection for the same reason. The probe sentence is the one place the README
// claims to enumerate the set, so it is the one place that can be wrong about it.
func TestReadmeNamesEveryProbedAgent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "README.md"))
	require.NoError(t, err)

	const marker = "Atrium probes for installed agent CLIs ("
	i := strings.Index(string(raw), marker)
	require.GreaterOrEqual(t, i, 0,
		"the README no longer has the first-run probe sentence this guard reads; if it moved, "+
			"move the marker, and if it went, this guard needs a new anchor rather than deleting")
	rest := string(raw)[i+len(marker):]
	j := strings.Index(rest, ")")
	require.GreaterOrEqual(t, j, 0, "the probe sentence's agent list is unterminated")
	listed := rest[:j]

	for _, bin := range knownAgentBins {
		require.Containsf(t, listed, "`"+bin+"`",
			"knownAgentBins probes for %q, and the README's first-run sentence does not name "+
				"it — a user installing that CLI is told Atrium will not find it", bin)
	}

	// The other direction: a name the README lists and the code no longer probes for is just
	// as wrong, and it is the half a one-way check misses.
	for _, name := range strings.Split(listed, ",") {
		name = strings.Trim(strings.TrimSpace(name), "`")
		if name == "" {
			continue
		}
		require.Containsf(t, knownAgentBins, name,
			"the README's first-run sentence names %q, which knownAgentBins does not probe for", name)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./config/ -run TestReadmeNamesEveryProbedAgent -v 2>&1 | tail -15
```

Expected: FAIL — `knownAgentBins probes for "copilot", and the README's first-run sentence does not name it`.

- [ ] **Step 3: Update the README**

Three edits. The intro sentence, adding Copilot to the named agents:

```
Atrium is a terminal command center for orchestrating multiple AI coding agents — [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [GitHub Copilot CLI](https://github.com/github/copilot-cli), [Antigravity](https://antigravity.google/docs/cli/reference), [Gemini](https://github.com/google-gemini/gemini-cli), and other local agents including [Aider](https://github.com/Aider-AI/aider) — each in its own isolated git worktree, so you can drive several tasks at once from a single panel.
```

The "Using Atrium with other AI assistants" block, adding a launch example and an auth line. The auth line is the one worth getting right, because a copilot 403 presents as an auth problem and usually is not one:

```
- For [Codex](https://github.com/openai/codex): Set your API key with `export OPENAI_API_KEY=<your_key>`
- For [GitHub Copilot CLI](https://github.com/github/copilot-cli): run `copilot login` once — the credential goes to the OS keyring and is shared across sessions, so Atrium needs no per-session setup. If a session reports `unauthorized: not authorized to use this Copilot feature`, check in this order, cheapest and most decisive first: the organization's AI-credits budget (a zero budget with blocking denies the *included* allowance too, immediately), then its Copilot **CLI** policy (separate from the IDE policy, and unset by default), then the seat, then the token.
- Launch with specific assistants:
   - Codex: `atrium -p "codex"`
   - Aider: `atrium -p "aider ..."`
   - Gemini: `atrium -p "gemini"`
   - Antigravity: `atrium -p "agy"`
   - GitHub Copilot CLI: `atrium -p "copilot"`
```

And the first-run probe sentence:

```
On first run, Atrium probes for installed agent CLIs (`claude`, `codex`, `gemini`, `aider`, `agy`, `copilot`) and seeds a profile for each one it finds. After installing a new agent, press `D` in the panel's Profiles category, or run:
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./config/ -run TestReadmeNamesEveryProbedAgent -v 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 5: Mutation-prove the guard**

Commit first, then:

| mutation | must redden |
|---|---|
| remove `` `copilot` `` from the README's probe sentence | `TestReadmeNamesEveryProbedAgent` |
| remove `"copilot"` from `knownAgentBins` | `TestReadmeNamesEveryProbedAgent` (the reverse direction) **and** `TestReadmeNamesEveryProbedAgent`'s forward loop must not be the only thing that catches it |
| add `` `crush` `` to the README's probe sentence | `TestReadmeNamesEveryProbedAgent` |
| change the marker string in the test to something absent | `TestReadmeNamesEveryProbedAgent` fails with the "no longer has the probe sentence" message rather than passing vacuously |

That last one is the important one: a guard that reads a marker out of a large file passes by default if the marker moves, which is exactly the vacuous-guard shape to check for.

- [ ] **Step 6: Update the spec's status line**

The spec's Status field says Stage 1 design settled. Change it to record Stage 1 as landed, and move the busy-marker entry out of NOT MEASURED (Task 1 Step 11 already replaced its body; this is the list entry).

- [ ] **Step 7: Run the full gate and commit**

```bash
PATH="$(go env GOPATH)/bin:$PATH" just ci
go test -race ./session/agent/ ./config/
```

Expected: both green. `just vuln` needs network and is CI-only; `just ci` does not cover the race detector or the macOS job.

```bash
git add -A
git commit -m "docs: document the copilot agent, and guard the README's agent list

Adds Copilot CLI to the README's intro, launch examples and first-run probe
sentence, with an auth note ordering the four things a 403 can mean — budget,
CLI policy, seat, token — cheapest and most decisive first, because the error is
phrased as authorization and is most often a zero AI-credits budget.

Adds the fourth README drift guard. Every other site a new adapter touches has
one; the README's agent list was the exception, so a sixth agent could ship
undocumented with a green suite. It reads the probe sentence specifically rather
than the whole file, because a document-wide search for an agent name passes by
coincidence, and it fails loudly if its anchor moves rather than passing
vacuously."
```

---

## Self-Review

**Spec coverage.** Walking the spec's Stage-1-relevant sections against the tasks:

| spec requirement | task |
|---|---|
| `"copilot"` in `knownAgentBins`, plain `LookPath` branch | 3 (Step 6) |
| Composer glyph `❯`, `InputBoxPrompts` nil | 3 (adapter comment + `TestCopilotDialogsAreAlsoComposersToTheBoxPredicate`) |
| Busy marker keys on `Working`, `MarkerWindow` 0, floor as a datum | 5 |
| Folder-trust gate | 3 |
| Wall-stripping scan beside `flattenChrome`, anchored on `bottomBoxBlock` | 2 |
| Approval prompt, `NoAutoTap` mandatory | 4 |
| Literals taken from low in each dialog (the height cliff) | 3 (`TestCopilotTrustGateTitleIsGoneAtWidth20`) |
| `HookSupport` false | 3 (Global Constraints + adapter comment) |
| Re-drive the invalid busy ladder | 1 |
| Identity glyph, README, drift pin | 3 and 6 |

Deliberately **not** in this plan, each with the spec section that owns it: `HookSupport` → capability (#773, Stage 2); transcript readers over `events.jsonl` (Stage 3); adapter-declared launch knobs (#816, Stage 4); the credits meter and `session.usage_checkpoint`; `-i` first-prompt delivery; account routing; the paste chip; the `resume` drive and `RESUME_TABLE` row; `ResumeProbe`'s needle; `HeadlessNamer`. The first four are later stages; the last five are the spec's NOT MEASURED list, which Stage 1 does not gate on.

**Placeholder scan.** No `TBD`, no "add error handling", no "similar to Task N". Task 1 Step 11 and Task 5 Steps 1–2 are the only steps whose exact content depends on a measurement, and both spell out what to record and give both branches rather than deferring the decision.

**Type consistency.** `flattenBottomBox(content string) (string, bool)` is defined in Task 2 and called with that signature in Tasks 3 and 4. `stripBoxWalls(line string) string` likewise. `copilotTrustHeadline`, `copilotTrustOption`, `copilotApprovalHeadline`, `copilotApprovalOption` are declared in the tasks that introduce them and referenced under those exact names in the test files. Ladder identifiers are `copilotTrustgateLadder` / `copilotApprovalLadder` / `copilotBusyLadder` throughout — note `Trustgate` (one word, lowercase g) matches what `drive-agent emit` generates from the `trustgate` label, so the fixture consts and the ladder name agree.

One residual inconsistency worth flagging rather than hiding: the coverage key is `copilot/gate/trust` while the fixtures and ladder say `trustgate`, because the key comes from `Gate.Name` and the fixture names come from the capture label. Renaming either would break the other's convention, so they stay different on purpose.
