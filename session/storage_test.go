package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
)

// TestDeleteInstanceDoesNotReconstructSiblings is the regression test for the
// zombie-session bug: a stored instance whose repo/worktree no longer exist on
// disk (e.g. after the user renamed their project directory) must not block
// deleting another session, and must not be silently corrupted in the process.
//
// DeleteInstance must operate on the serialized []InstanceData directly. The old
// implementation went through LoadInstances -> FromInstanceData, which reattaches
// to / restarts tmux and rewrites a dead session's Status (Running -> Paused) and
// UpdatedAt. This test pins that untouched siblings are preserved byte-for-byte.
func TestDeleteInstanceDoesNotReconstructSiblings(t *testing.T) {
	keeperUpdated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	keeper := InstanceData{
		Title:     "keeper",
		Path:      "/nonexistent/repo",
		Branch:    "feature",
		Status:    Running, // 0 — would flip to Paused if reconstructed
		Program:   "claude",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: keeperUpdated,
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/repo",
			WorktreePath: "/nonexistent/worktree",
			SessionName:  "keeper",
			BranchName:   "feature",
		},
	}
	target := InstanceData{
		Title:   "target",
		Path:    "/nonexistent/repo2",
		Status:  Running,
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/repo2",
			WorktreePath: "/nonexistent/worktree2",
			SessionName:  "target",
			BranchName:   "feature2",
		},
	}

	seeded, err := json.Marshal([]InstanceData{keeper, target})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}

	state := config.DefaultState()
	state.InstancesData = seeded
	storage, err := NewStorage(state)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	if err := storage.DeleteInstance("target"); err != nil {
		t.Fatalf("DeleteInstance returned error: %v", err)
	}

	var got []InstanceData
	if err := json.Unmarshal(state.GetInstances(), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 remaining instance, got %d", len(got))
	}
	g := got[0]
	if g.Title != "keeper" {
		t.Fatalf("wrong instance kept: %q", g.Title)
	}
	if g.Status != Running {
		t.Errorf("keeper status corrupted: want Running(%d), got %d", Running, g.Status)
	}
	if !g.UpdatedAt.Equal(keeperUpdated) {
		t.Errorf("keeper UpdatedAt rewritten: want %s, got %s", keeperUpdated, g.UpdatedAt)
	}
	if g.Worktree.RepoPath != keeper.Worktree.RepoPath {
		t.Errorf("keeper repo_path changed: %q", g.Worktree.RepoPath)
	}
}

// TestDeleteInstanceNotFound documents that deleting a missing title is an error.
func TestDeleteInstanceNotFound(t *testing.T) {
	state := config.DefaultState() // InstancesData == "[]"
	storage, err := NewStorage(state)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	if err := storage.DeleteInstance("ghost"); err == nil {
		t.Fatal("expected error deleting non-existent instance, got nil")
	}
}
