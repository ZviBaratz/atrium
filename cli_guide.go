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
// Static text on purpose: it takes no lock, reads no state and logs nothing, so there is no
// moment at which running it is unsafe and no way for it to answer with something stale but
// plausible. Where a fact already has an owner — when a queued create actually lands — the page
// names that owner rather than restating it, because the second copy is the one that rots.
//
// Scoping what an agent should touch is the other half of its job, and the reason the brief
// points here rather than at `atrium --help`: that lists reset and reap beside ls and peek,
// with nothing to say which of them belong to the person at the keyboard.
const guidePage = "You are an AI agent running inside an Atrium session. Atrium runs several agents at\n" +
	"once, each in its own tmux session, in its own git worktree, on its own branch. This\n" +
	"is what you can do from in here.\n" +
	"\n" +
	"YOUR WORKTREE\n" +
	"\n" +
	"  Atrium created this worktree and Atrium reclaims it. Never run `git worktree\n" +
	"  remove` or `git worktree prune` against it, and never touch a sibling worktree\n" +
	"  beside it — those are other live agents' desks. Killing the session removes the\n" +
	"  worktree and deletes the branch; pausing removes the worktree and keeps the branch.\n" +
	"\n" +
	"  A session created *direct* is the exception: it has no worktree of its own, it\n" +
	"  works in the real checkout on whatever branch that checkout is on, and Atrium\n" +
	"  reclaims none of it. `atrium ls --json` reports that as the `direct` field.\n" +
	"\n" +
	"WHAT YOU CAN RUN\n" +
	"\n" +
	"  atrium ls [--json]             every session: title, branch, status, diff, queue\n" +
	"  atrium peek <title>            read another session's screen, without attaching\n" +
	"  atrium send <title> <prompt>   queue a prompt for another session\n" +
	"  atrium new <title> [prompt]    create a session: a worktree, a branch and an agent\n" +
	"\n" +
	"  Each carries its own --help with the rules. Read `atrium new --help` before your\n" +
	"  first handoff: it owns the answer to when a queued create actually lands, and this\n" +
	"  page deliberately does not repeat it.\n" +
	"\n" +
	"HANDING OFF\n" +
	"\n" +
	"  When your work is finished and the next step is a fresh session on a new branch,\n" +
	"  create it yourself instead of asking for one:\n" +
	"\n" +
	"      atrium new \"fix the parser\" \"start from the failing test in parse_test.go\"\n" +
	"\n" +
	"  `new` does not create the session itself — it spools a request that the running\n" +
	"  Atrium executes — so it prints what it queued, not what it built. When it can tell\n" +
	"  that nothing is currently draining that request it says so on stderr, so read what\n" +
	"  it prints. Pass --wait if you need the branch name before you go on; otherwise\n" +
	"  `atrium ls` is how you watch for the session to appear.\n" +
	"\n" +
	"  A title is a branch: the branch and tmux names derive from it, and a title whose\n" +
	"  names are already taken is refused rather than quietly suffixed.\n" +
	"\n" +
	"NOT YOURS TO RUN\n" +
	"\n" +
	"  `atrium reset` wipes every stored session, worktree and queued request. It refuses\n" +
	"  while a TUI is running, which is a guard rather than a guarantee.\n" +
	"\n" +
	"  `atrium reap --kill` stops tmux servers, and a tmux server here is running agents.\n" +
	"\n" +
	"  Both belong to the person at the keyboard. Ask."

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
// peek, send, new, reap, reset): this one resolves nothing and touches no data dir, so its
// only failure is a stdout it cannot write to — which Cobra already reports — and opening the
// shared log would be the only side effect it has.
func runGuide(w io.Writer) error {
	_, err := fmt.Fprintln(w, guidePage)
	return err
}
