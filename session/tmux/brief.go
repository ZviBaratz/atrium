package tmux

import (
	"encoding/json"
	"fmt"
)

// SessionStart context brief (#485): the one thing Atrium says TO the agent.
//
// Everything else on the --settings channel points inward — the status hooks in hooks.go read
// the agent's working/ready latch, effort, permission mode and in-flight sub-agent set. This
// points outward. A session is dropped into <dataDir>/worktrees/<slug>_<hex> and Claude Code
// tells it only cwd, "is a git repository" and the branch; nothing says the checkout is
// disposable, that the origin is elsewhere, or that the neighbouring directories are other
// live agents' desks.
//
// The concrete damage that forced this: Superpowers' finishing-a-development-branch decides
// worktree ownership by PATH and claims anything under a `worktrees/` component as its own to
// clean up. Atrium's worktree path matches on every session, so the skill runs
// `git worktree remove` on a live worktree and orphans an instance still listed in state.json.
// Renaming the data dir is not available — it is live state holding absolute paths (see
// CLAUDE.md), so ownership has to be asserted in words instead.
//
// The facts are BAKED into the hook command line, the same way hooks.go already bakes the
// absolute state-file path, rather than read from state.json when the hook fires:
//
//   - config.LoadState() mutates the data dir, so it is unsafe to call from a subprocess
//     running beside a live TUI (doctor hand-rolls a raw read for exactly this reason), and a
//     hook would additionally need a sanitizedName→instance join that does not exist.
//   - Baking is not freezing. The Session holds a PROVIDER for the facts, not a value
//     (SetSessionBriefFunc), and start() reads it at each launch — so a deep rename, the one
//     thing that changes them mid-session, is picked up by the next launch that rewrites the
//     file. Until that relaunch a rename does sever the hook STATE channel, which is a separate
//     pre-existing wart: Session.Rename moves nothing under <configDir>/hooks/, so the live
//     claude keeps writing the state path frozen into the settings.json it was launched with
//     (the OLD name) while Poll reads under the new one — status falls back to the scrape
//     classifier, and the old directory is left behind. Verified by probe, not inferred.
//   - Only the FACTS are baked. The copy is rendered by the binary at fire time, so it stays a
//     single pure function here and an upgraded atrium improves the wording for an already-
//     running session on its next /clear, without relaunching it.

// HookEventSessionStart is the `atrium hook --event` verb for the SessionStart brief. Unlike
// every other verb it is deliberately NOT part of the state-record vocabulary in hookstate.go:
// it mutates no record, has no case in applyHookEvent, reads no stdin (HookEventReadsStdin), and
// needs no --state-file. Its whole job is to print the brief on stdout for Claude to read back.
const HookEventSessionStart = "session-start"

// sessionStartMatcher is the SessionStart hook's matcher. Claude evaluates it against the
// session's SOURCE, so every alternative is load-bearing — `resume` most of all, since Atrium
// resurrects a paused session with `claude --continue` (session/agent/registry.go, the claude
// adapter's Resume), which fires source "resume" and would otherwise drop the brief on every
// pause→resume round trip.
//
// Verified live against claude 2.1.220, because a hook that never matches fails SILENTLY —
// nothing is printed and nothing is logged anywhere:
//
//	"startup|resume|clear|compact" + a fresh session   → fires, context reaches the model
//	"startup|resume|clear|compact" + `claude --continue` → fires, context reaches the model
//	"resume|clear|compact"         + a fresh session   → does NOT fire
//
// That last line is the one that matters: it proves the matcher is really evaluated rather than
// ignored, so dropping an alternative silently loses that source. (Claude's own bundled hooks
// reference lists SessionStart's matcher as "-"; the probe says otherwise.)
const sessionStartMatcher = "startup|resume|clear|compact"

// sessionBriefMaxLen caps the rendered brief. additionalContext is not paid once: Claude
// re-delivers it on every session start, every /clear and every compaction, and it competes
// with CLAUDE.md and auto-memory for early-context attention. The cap is enforced by
// TestSessionBriefLengthCap against a realistic worst case, NOT by truncating at runtime —
// truncation would silently amputate the worktree-ownership sentence, which is the entire
// reason the payload exists. The copy renders to 626 chars (~155 tokens) at that worst case
// today, so the budget leaves roughly one short clause of slack — deliberately tight.
const sessionBriefMaxLen = 660

// sessionBriefTemplate is the copy. Every clause states something read out of this codebase,
// not assumed:
//
//   - "already the session branch" — newSessionWorktree mints <BranchPrefix><title> once at
//     creation and checks the worktree out on it; baseBranch only picks the start point.
//   - "Atrium owns this worktree" — the sentence this whole change exists for; see the package
//     comment above and TestSessionBriefAssertsWorktreeOwnership, which pins it as a literal.
//   - "Killing … removes the worktree and deletes the branch" — Worktree.Cleanup runs
//     `git worktree remove -f`, then `git worktree prune`, then `git branch -D`.
//   - "pausing removes the worktree but keeps the branch" — session/pause.go commits dirty work
//     as an "[atrium] update from … (paused)" marker, removes the worktree, keeps the branch,
//     and Resume soft-resets the marker away.
//
// A brief that confidently states the wrong lifecycle is worse than no brief, so an edit here
// means re-reading those call sites.
const sessionBriefTemplate = "You are running in Atrium session %q. " +
	"The working directory is an Atrium-managed git worktree of %s, checked out on %s — " +
	"already the session branch, so do not create another. " +
	"Atrium owns this worktree: never run `git worktree remove` or `git worktree prune` against it. " +
	"Killing the session removes the worktree and deletes the branch; " +
	"pausing removes the worktree but keeps the branch. " +
	"Other worktrees under %s belong to other Atrium sessions — never touch them."

// SessionBrief is the set of per-session facts the brief is rendered from, baked into the hook
// command line at launch. Supplied by the provider bound with SetSessionBriefFunc, which start()
// calls once per launch — the values are a snapshot of that moment, not of session creation.
type SessionBrief struct {
	// Name is the Atrium session's title, as the user sees it in the list.
	Name string
	// Origin is the git repo the worktree was created from — the checkout the user is
	// actually looking at, which is NOT the agent's cwd.
	Origin string
	// Branch is the session branch the worktree is checked out on.
	Branch string
	// WorktreesRoot is <dataDir>/worktrees, the tree every other session's worktree also
	// lives under (config.WorktreesDir).
	WorktreesRoot string
}

// ok reports whether every fact is present. A brief with a hole in it is never rendered: a
// direct (non-git) session has no worktree and no branch, so the copy's load-bearing claims
// would all be false for it, and it gets no SessionStart hook at all.
func (b SessionBrief) ok() bool {
	return b.Name != "" && b.Origin != "" && b.Branch != "" && b.WorktreesRoot != ""
}

// RenderSessionBrief renders the brief, or "" when any fact is missing.
func RenderSessionBrief(b SessionBrief) string {
	if !b.ok() {
		return ""
	}
	return fmt.Sprintf(sessionBriefTemplate, b.Name, b.Origin, b.Branch, b.WorktreesRoot)
}

// sessionStartPayload is Claude's hookSpecificOutput for a SessionStart hook. HookEventName is
// Claude's event name, deliberately distinct from HookEventSessionStart (atrium's own --event
// verb) — two vocabularies that happen to describe the same edge.
type sessionStartPayload struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type sessionStartOutput struct {
	HookSpecificOutput sessionStartPayload `json:"hookSpecificOutput"`
}

// SessionStartHookOutput marshals the JSON a SessionStart hook prints on stdout for Claude to
// read back, or (nil, nil) when there is no renderable brief. Built from typed structs rather
// than assembled by hand because the field names are the whole contract: Claude discards a
// malformed payload without an error anywhere, so a typo would ship as "the brief just never
// appears".
func SessionStartHookOutput(b SessionBrief) ([]byte, error) {
	brief := RenderSessionBrief(b)
	if brief == "" {
		return nil, nil
	}
	return json.Marshal(sessionStartOutput{HookSpecificOutput: sessionStartPayload{
		HookEventName:     "SessionStart",
		AdditionalContext: brief,
	}})
}

// hookSessionStartCommand builds the shell command the SessionStart hook runs: the atrium
// binary's hidden `hook` subcommand with the four facts baked in. Every value is single-quoted
// for the same reason the settings path is (tmux hands the launch command to `sh -c`, and a
// title can carry shell metacharacters); the event verb is a fixed literal. No --state-file:
// this event touches no state record.
func hookSessionStartCommand(binPath string, b SessionBrief) string {
	return shellSingleQuote(binPath) + " " + HookSubcommand +
		" --event " + HookEventSessionStart +
		" --session " + shellSingleQuote(b.Name) +
		" --origin " + shellSingleQuote(b.Origin) +
		" --branch " + shellSingleQuote(b.Branch) +
		" --worktrees-root " + shellSingleQuote(b.WorktreesRoot)
}
