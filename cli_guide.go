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
// ensureHookSettings injects the brief only for an adapter declaring HookSupport, so a codex,
// gemini or aider session is never sent one and this page is the ONLY place those rules can
// reach it. Anything load-bearing in the brief — worktree ownership, the branch already being
// checked out — therefore has to appear here too, in full, not by reference.
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
	"  The working directory is already checked out on the session branch. Work on\n" +
	"  it; do not create another branch.\n" +
	"\n" +
	"  Atrium created this worktree and Atrium reclaims it. Never run\n" +
	"  `git worktree remove` or `git worktree prune` against it, and never touch a\n" +
	"  sibling worktree beside it — those are other live agents' desks.\n" +
	"\n" +
	"  Killing the session removes the worktree and deletes the branch. Pausing\n" +
	"  removes the worktree and keeps the branch, first committing whatever is\n" +
	"  uncommitted as an \"[atrium] … (paused)\" marker; resuming rewinds that marker,\n" +
	"  so leave it alone rather than cleaning it up.\n" +
	"\n" +
	"  A session created *direct* is the exception: its directory is not a git\n" +
	"  repository at all, so it has no worktree, no branch and no diff, and Atrium\n" +
	"  reclaims none of it. `atrium ls --json` reports that as the `direct` field.\n" +
	"\n" +
	"WHAT YOU CAN RUN\n" +
	"\n" +
	"  atrium ls [--json]               every session: title, status, branch, diff\n" +
	"  atrium peek <session>            read another session's pane without attaching\n" +
	"  atrium send <session> [message]  queue a prompt for another session\n" +
	"  atrium new <title> [prompt]      create a session: worktree, branch, agent\n" +
	"\n" +
	"  Each carries its own --help with the rules. Read `atrium new --help` before\n" +
	"  your first handoff: it owns the answer to when a queued create actually\n" +
	"  lands, and this page deliberately does not repeat it.\n" +
	"\n" +
	"  `send` reads its message from stdin when you leave the argument off or pass\n" +
	"  `-`, which is how a multi-line prompt is handed over.\n" +
	"\n" +
	"HANDING OFF\n" +
	"\n" +
	"  When your work is finished and the next step is a fresh session on a new\n" +
	"  branch, create it yourself instead of asking for one:\n" +
	"\n" +
	"      atrium new \"fix the parser\" \"start from the failing test in parse_test.go\"\n" +
	"\n" +
	"  `new` does not create the session itself — it spools a request that the\n" +
	"  running Atrium executes — so it prints what it queued, not what it built.\n" +
	"  Read its stderr: when it can tell that nothing is draining the request, it\n" +
	"  says so there.\n" +
	"\n" +
	"  Pass `--wait 30s` — a duration, not a bare flag — to block until the session\n" +
	"  exists and be told its branch. Under `--wait` the \"nothing is draining this\"\n" +
	"  warning is deliberately suppressed, because the wait itself reports that\n" +
	"  outcome for real if it times out.\n" +
	"\n" +
	"  Without `--wait` you cannot tell a refused create from a slow one, and\n" +
	"  `atrium ls` will not tell you either: it lists sessions that exist, while a\n" +
	"  request the drain refuses leaves no session and no row — only a receipt that\n" +
	"  `--wait` reads and `ls` does not.\n" +
	"\n" +
	"  A title is a branch: the branch and tmux names derive from it. Use 32\n" +
	"  characters or fewer, and expect a title whose branch or tmux name is already\n" +
	"  taken to be refused rather than quietly suffixed.\n" +
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
	"  These belong to the person at the keyboard — ask. Treat anything else in\n" +
	"  `atrium --help` and not listed above as theirs too."

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
