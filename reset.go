package main

import (
	"context"
	"fmt"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/daemon"
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
		log.Initialize(false)
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

	if err := tmux.CleanupSessions(ctx, cmdExec); err != nil {
		return fmt.Errorf("failed to cleanup tmux sessions: %w", err)
	}
	fmt.Println("Tmux sessions have been cleaned up")

	if err := git.CleanupWorktrees(ctx, repoPaths); err != nil {
		return fmt.Errorf("failed to cleanup worktrees: %w", err)
	}
	fmt.Println("Worktrees have been cleaned up")

	return nil
}
