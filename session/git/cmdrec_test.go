package git

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmdlog"
)

// The session/git chokepoints bypass cmd.Executor but still record into the
// command log (#372). SearchBranches runs `git branch -a` directly; invoking it
// must leave a record whose argv shows the command.
func TestGit_BypassSiteRecords(t *testing.T) {
	cmdlog.Reset()
	repo := newTestRepo(t)

	if _, err := SearchBranches(context.Background(), repo, ""); err != nil {
		t.Fatalf("SearchBranches: %v", err)
	}

	var found bool
	for _, r := range cmdlog.Snapshot() {
		if strings.Contains(r.Argv, "branch -a") {
			found = true
			if r.Err {
				t.Errorf("a successful branch listing was recorded as an error: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("SearchBranches did not record into the command log: %+v", cmdlog.Snapshot())
	}
}

// A *Worktree git command attributes its record to the session (its Title), so the
// per-session filter (AC5) has something to show.
func TestGit_WorktreeCommandsAttributeToSession(t *testing.T) {
	cmdlog.Reset()
	repo := newTestRepo(t)

	wt, _, err := NewWorktree(context.Background(), repo, "mysess")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := wt.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if got := cmdlog.ForSession("mysess"); len(got) == 0 {
		t.Fatalf("no commands attributed to session %q; snapshot=%+v", "mysess", cmdlog.Snapshot())
	}
}
