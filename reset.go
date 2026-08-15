package main

import (
	"context"
	"fmt"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/daemon"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/internal/undo"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/spf13/cobra"
)

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset all stored instances",
	RunE: func(cmd *cobra.Command, args []string) error {
		// One-shot CLI command; a plain Background context is enough (the
		// per-operation timeouts still bound every subprocess).
		ctx := context.Background()
		log.Initialize(logDir(), false)
		defer log.Close()
		return runReset(ctx, cmd2.MakeExecutor())
	},
}

// runReset wipes all Atrium-managed state: stored instances, tmux sessions, and
// worktrees. Ordering carries the correctness (issue #265):
//
//   - A live TUI makes reset refuse outright — deleting sessions and worktrees
//     under it would have the TUI's in-memory state re-persist every deleted
//     instance on its next save. The lock is then held for the whole reset so
//     no TUI (and therefore no exit-time autoyes daemon) can start mid-wipe.
//   - The autoyes daemon is stopped BEFORE state is read: its shutdown handler
//     persists the instance snapshot it loaded at startup (see RunDaemon), and
//     StopDaemon blocks until the daemon is gone — so stopping it first
//     guarantees that dying save lands before the deletion below rather than
//     resurrecting the deleted instances after it.
func runReset(ctx context.Context, cmdExec cmd2.Executor) error {
	release, err := acquireTUILockOrWarn("resetting", "close it before resetting")
	if err != nil {
		return err
	}
	defer release()

	// Abort on failure: with the daemon possibly still alive, proceeding would
	// let its dying save rewrite state.json after the deletion below.
	if err := daemon.StopDaemon(); err != nil {
		// Log (to the file) before returning, matching the root command's
		// handling, so the failure is captured and not just surfaced to stderr.
		log.ErrorLog.Printf("failed to stop daemon: %v", err)
		return fmt.Errorf("failed to stop daemon — reset aborted, nothing was deleted (check daemon.pid in the data dir): %w", err)
	}
	fmt.Println("daemon has been stopped")

	// Loaded only after the daemon is gone, so this read includes its final save.
	state := config.LoadState()
	storage, err := session.NewStorage(state)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Capture the repo paths before deleting instances so CleanupWorktrees
	// can run its git commands in the correct repositories regardless of the
	// current working directory.
	repoPaths, err := storage.RepoPaths()
	if err != nil {
		return err
	}

	if err := storage.DeleteAllInstances(); err != nil {
		return fmt.Errorf("failed to reset storage: %w", err)
	}
	fmt.Println("Storage has been reset successfully")

	// Immediately after the wipe, and above everything that can still return early: a
	// queued `atrium new` request is the one piece of state that can *create* after the
	// reset. Everything here is ordered so nothing survives to re-persist a deleted
	// session, and a create request defeats that from outside the lock by design (`new`
	// never acquires it before it spools; it only tests it afterwards, to warn when no
	// TUI is running) — with no session left to collide with, every gate it meets now
	// passes, so the next launch silently builds a worktree, a branch and an agent from
	// a request made before the reset.
	//
	// Left until after the tmux and worktree cleanups, that protection would be
	// conditional on both succeeding: an uninstalled tmux or a repo that has since moved
	// aborts the reset with state.json already empty, and the request outlives it. So it
	// runs here — past the point of no return, where discarding a queued request is
	// unambiguously right, and before any step that can still fail.
	//
	// Queued prompts go with them: each one addresses a session this reset has just
	// deleted, so the only thing left to do with it is refuse it, and doing that here
	// means the caller hears "reset" instead of "no such session".
	//
	// Every record is discarded through a rejection receipt, never a bare unlink — a
	// producer blocked in `--wait` reads the file's disappearance as success. The
	// receipts are what a reset deliberately leaves behind; they expire on their own.
	//
	// One window this cannot close: `new` takes no lock, so a request spooled after the
	// ReadDir below survives. That is a race with a concurrent `atrium new`, not a
	// failure mode of the reset itself.
	//
	// Best-effort: a reset whose real work is done must not fail on a spool it could not
	// read. The count is printed either way — a partial clear that dropped nine of ten
	// records has still dropped nine — and it counts records in both spools, prompts and
	// create requests alike.
	cleared, err := outbox.Clear()
	if err != nil {
		log.WarningLog.Printf("reset: could not fully clear the outbox: %v", err)
	}
	fmt.Printf("Outbox has been cleared; %d spooled record%s discarded\n", cleared, plural(cleared))

	if err := tmux.CleanupSessions(ctx, cmdExec); err != nil {
		return fmt.Errorf("failed to cleanup tmux sessions: %w", err)
	}
	fmt.Println("Tmux sessions have been cleaned up")

	if err := git.CleanupWorktrees(ctx, repoPaths); err != nil {
		return fmt.Errorf("failed to cleanup worktrees: %w", err)
	}
	fmt.Println("Worktrees have been cleaned up")

	// The undo journal and the retention refs it names must go too, and nothing
	// above can reach them: CleanupWorktrees enumerates branches from
	// `git worktree list`, which cannot see a ref outside refs/heads. Left behind,
	// those refs would keep every killed session's objects alive in the user's
	// repositories forever, gc-immune, with no record left to expire them — a
	// permanent leak caused by the command whose entire job is cleanup.
	//
	// Best-effort: a repository that has since moved must not fail a reset whose
	// real work is already done.
	dropped := 0
	if err := undo.Clear(func(e undo.Entry) {
		if e.Ref == "" || e.RepoPath == "" {
			return
		}
		if derr := git.DeleteRef(ctx, e.RepoPath, e.Ref); derr != nil {
			log.WarningLog.Printf("reset: could not drop retention ref %s in %s: %v", e.Ref, e.RepoPath, derr)
			return
		}
		dropped++
	}); err != nil {
		log.WarningLog.Printf("reset: could not clear the undo journal: %v", err)
	}
	fmt.Printf("Undo journal has been cleared; %d retained branch ref%s released\n", dropped, plural(dropped))

	return nil
}

// plural is the "s" suffix for a count, shared by package main's commands (reset here,
// reap's connected-client lines). app has its own, which this cannot borrow.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
