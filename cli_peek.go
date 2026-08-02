package main

import (
	"context"
	"fmt"
	"io"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/spf13/cobra"
)

var (
	peekLinesFlag int
	peekColorFlag bool
	peekPathFlag  string

	peekCmd = &cobra.Command{
		Use:   "peek <session>",
		Short: "Print what a session's pane is showing, without attaching",
		Long: "Captures a session's agent pane and prints it. Read-only: it never attaches to\n" +
			"the session, sends it keys, or alters it in any way.\n\n" +
			"Needs a live tmux server, so a paused session cannot be peeked — `atrium ls`\n" +
			"shows which are running.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			// One-shot CLI command; a plain Background context is enough (the
			// per-operation timeouts still bound every subprocess).
			return runPeek(cmd.Context(), cmd.OutOrStdout(), cmd2.MakeExecutor(),
				args[0], peekPathFlag, peekLinesFlag, peekColorFlag)
		},
	}
)

// runPeek resolves the selector against stored state and writes the target
// session's pane to w.
func runPeek(ctx context.Context, w io.Writer, exec cmd2.Executor, selector, path string, lines int, color bool) error {
	instances, err := loadStoredInstances()
	if err != nil {
		return err
	}
	target, err := resolveSession(instances, selector, path)
	if err != nil {
		return err
	}

	// Checked before touching tmux: pausing kills the tmux session (the branch
	// and worktree are what survive), so peeking a paused session would fail deep
	// in tmux with a much less useful message than naming the actual state.
	if target.Status == session.Paused {
		return fmt.Errorf("session %q is paused and has no live pane — resume it in %s first", target.Title, binName)
	}

	content, err := tmux.CapturePaneForSession(ctx, exec, tmuxSessionName(target), tmux.CaptureOpts{
		Lines: lines,
		Color: color,
	})
	if err != nil {
		return fmt.Errorf("%w (run `%s ls` for its last known status)", err, binName)
	}

	_, err = io.WriteString(w, content)
	return err
}

// tmuxSessionName returns the tmux session name to address an instance by.
//
// TmuxName is persisted rather than derived, and is absent from state written
// before the field existed. Those instances still have a live tmux session under
// the older title-derived name, so fall back to that derivation rather than
// giving tmux an empty target.
func tmuxSessionName(d session.InstanceData) string {
	if d.TmuxName != "" {
		return d.TmuxName
	}
	return tmux.Prefix() + tmux.SanitizeNameSegment(d.Title)
}
