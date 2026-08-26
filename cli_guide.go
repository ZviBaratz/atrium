package main

import (
	"fmt"
	"io"

	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/spf13/cobra"
)

// The agent-facing page (#759). The SessionStart brief (session/tmux/brief.go) is re-delivered
// on every session start, every /clear and every compaction, so it can afford one clause and no
// more; this is where that clause points. Everything the brief has no room for lives here,
// pulled once, by an agent that went looking for it.
//
// It restates the brief's worktree rules rather than assuming them, and the reason is reach:
// ensureHookSettings has several gates (README's Scripting section enumerates them), and a
// session failing any of them — every codex, gemini and aider session, and every direct
// session whatever the agent — is never sent a brief at all. For those this page is the ONLY
// carrier, so anything load-bearing in the brief appears here too, in full, not by reference.
// That is also why the branch instruction here is conditioned on not being a direct session,
// where it would be false: the brief is never rendered for one, so it can state it flatly.
//
// Static text on purpose: it takes no lock, reads no state and logs nothing, so there is no
// moment at which running it is unsafe and no way for it to answer with something stale but
// plausible. Where a fact already has an owner — when a queued create actually lands — the page
// names that owner rather than restating it, because the second copy is the one that rots.
//
// Scoping what an agent should touch is the other half of its job, and the reason the brief
// points here rather than at `atrium --help`: that lists reset and reap beside ls and peek,
// with nothing to say which of them belong to the person at the keyboard.
const guidePage = "You are an AI agent running inside an Atrium session. Atrium runs several agents\n" +
	"at once, each in its own tmux session, in its own git worktree, on its own\n" +
	"branch. This is what you can do from in here.\n" +
	"\n" +
	"YOUR WORKTREE\n" +
	"\n" +
	"  Unless this is a direct session (below), the working directory is a worktree\n" +
	"  Atrium created, already checked out on the session branch. Work on it; do not\n" +
	"  create another branch.\n" +
	"\n" +
	"  Atrium created this worktree and Atrium reclaims it. Never run\n" +
	"  `git worktree remove` or `git worktree prune` against it, and never touch a\n" +
	"  sibling worktree beside it — those are other live agents' desks.\n" +
	"\n" +
	"  Killing the session removes the worktree and deletes the branch. Pausing\n" +
	"  removes the worktree and keeps the branch, committing whatever is uncommitted\n" +
	"  first as a marker Atrium unwinds when the session resumes. Leave that commit\n" +
	"  alone rather than cleaning it up.\n" +
	"\n" +
	"  A direct session is the exception: its directory is not a git repository at\n" +
	"  all, so it has no worktree, no branch and no diff, and Atrium reclaims none of\n" +
	"  it. `atrium ls --json` reports that as the `direct` field.\n" +
	"\n" +
	"WHAT YOU CAN RUN\n" +
	"\n" +
	"  atrium guide                     this page\n" +
	"  atrium ls [--json]               every session, and what each is doing\n" +
	"  atrium peek <session>            read another session's pane without attaching\n" +
	"  atrium send <session> <message>  queue a prompt for another session\n" +
	"  atrium new <title> [prompt]      create a session: worktree, branch, agent\n" +
	"\n" +
	"  Always pass `send` its message as an argument. Left off, it reads the message\n" +
	"  from stdin instead, which on a terminal waits forever.\n" +
	"\n" +
	"  Each command carries its own --help, and that is where the rules live. Read\n" +
	"  `atrium new --help` before your first handoff: it owns the answer to when a\n" +
	"  queued create actually lands, which this page deliberately does not repeat.\n" +
	"  Read what `new` and `send` print on stderr — some of it is addressed to the\n" +
	"  person at the keyboard rather than to you.\n" +
	"\n" +
	"HANDING OFF\n" +
	"\n" +
	"  When your work is finished and the next step is a fresh session on a new\n" +
	"  branch, create it yourself instead of asking for one:\n" +
	"\n" +
	"      atrium new \"fix the parser\" \"start from the failing test in parse_test.go\"\n" +
	"\n" +
	"  `new` does not build the session — it queues a request that the running Atrium\n" +
	"  executes — so it reports what it queued, not what it built.\n" +
	"\n" +
	"  The new session's branch starts from the origin repo's HEAD, not from yours,\n" +
	"  so it will not contain work you have not merged. When the next agent needs to\n" +
	"  start from where you stopped, pass `--branch` naming your own branch.\n" +
	"\n" +
	"  When the follow-up should run on a given model, pin it at creation instead\n" +
	"  of asking for it in the prompt: `--model`, `--effort` and `--permission-mode`\n" +
	"  set claude's flags on the new session. They need a claude somewhere in what\n" +
	"  is being created: a lone non-claude session is refused, and so is a fan-out\n" +
	"  with no claude member — but a MIXED fan-out is accepted, and the pins ride\n" +
	"  only its claude members while the rest are left untouched, with no warning.\n" +
	"  `--account` pins which configured Claude account it runs on,\n" +
	"  by name; do not write `CLAUDE_CONFIG_DIR` into the program to do that —\n" +
	"  whether on `--program` or in your config.json — which runs the session on one\n" +
	"  account and records another.\n" +
	"\n" +
	"  A claude session is normally handed a skill for this: `/atrium:spawn` walks\n" +
	"  the same choices — what to pin, whether to start from your own branch, and\n" +
	"  what the first prompt has to say — and asks before creating anything. Every\n" +
	"  gate around that is fail-open, so if it does not resolve, `atrium doctor`'s\n" +
	"  Agent skills section names the gate that declined, per configured program.\n" +
	"  Every other agent makes the choices by hand.\n" +
	"\n" +
	"  A title is a branch: the branch and tmux names derive from it, and a title\n" +
	"  whose names are already taken is refused rather than quietly suffixed — with\n" +
	"  one exception, `--variants`, which `atrium new --help` owns. An over-long\n" +
	"  title is refused too, and the error names the limit.\n" +
	"\n" +
	"NOT YOURS TO RUN\n" +
	"\n" +
	"  `atrium reset` wipes every stored session, worktree and queued request. It\n" +
	"  refuses while a TUI is running, which is a guard rather than a guarantee.\n" +
	"\n" +
	"  `atrium reap --kill` stops tmux servers, and a tmux server here is running\n" +
	"  agents. Bare `atrium reap` only reports, and is safe.\n" +
	"\n" +
	"  `atrium update` replaces the running binary with a newer release.\n" +
	"\n" +
	"  Those three belong to the person at the keyboard — ask before running any of\n" +
	"  them. Everything listed under WHAT YOU CAN RUN is yours, this page included."

var guideCmd = &cobra.Command{
	Use:   tmux.GuideSubcommand,
	Short: "Print what an agent running inside a session can do",
	// Long is the page itself so that `atrium guide` and `atrium guide --help` cannot
	// disagree: one const, printed by both paths.
	Long: guidePage,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGuide(cmd.OutOrStdout())
	},
}

// runGuide prints the page. No log.Initialize, unlike the headless commands beside it (ls,
// peek, send, new, reap, reset): this one resolves nothing and touches no data dir, so opening
// the shared log would be the only side effect it has. Its one failure is a stdout it cannot
// write to, which it returns rather than prints — rootCmd sets SilenceErrors, so main() is what
// puts the message on stderr.
func runGuide(w io.Writer) error {
	_, err := fmt.Fprintln(w, guidePage)
	return err
}
