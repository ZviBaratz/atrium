package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// CurrentBranchName resolves the checked-out branch of a repo; "HEAD" for a detached
// HEAD (git's own convention for --abbrev-ref); empty for a non-repo. The picker renders
// the result as the "HEAD (<branch>)" base option in the new-session form.
func TestCurrentBranchName(t *testing.T) {
	repo := newTestRepo(t)
	mustRunGit(t, repo, "switch", "-c", "feat")
	if got := CurrentBranchName(context.Background(), repo); got != "feat" {
		t.Fatalf("CurrentBranchName() = %q, want %q", got, "feat")
	}

	mustRunGit(t, repo, "switch", "--detach")
	if got := CurrentBranchName(context.Background(), repo); got != "HEAD" {
		t.Fatalf("CurrentBranchName() detached = %q, want %q", got, "HEAD")
	}

	if got := CurrentBranchName(context.Background(), t.TempDir()); got != "" {
		t.Fatalf("CurrentBranchName() non-repo = %q, want empty", got)
	}
}

// A freshly-initialized repo has an unborn HEAD (the branch ref exists only as a symref,
// no commit yet) — the branch name must still resolve so the picker's default base option
// can label it instead of falling back to the generic "current branch" text.
func TestCurrentBranchNameUnbornHead(t *testing.T) {
	repo := t.TempDir()
	mustRunGit(t, "", "init", "-b", "newborn", repo)
	if got := CurrentBranchName(context.Background(), repo); got != "newborn" {
		t.Fatalf("CurrentBranchName() unborn HEAD = %q, want %q", got, "newborn")
	}
}

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple lowercase string",
			input:    "feature",
			expected: "feature",
		},
		{
			name:     "string with spaces",
			input:    "new feature branch",
			expected: "new-feature-branch",
		},
		{
			name:     "mixed case string",
			input:    "FeAtUrE BrAnCh",
			expected: "feature-branch",
		},
		{
			name:     "string with special characters",
			input:    "feature!@#$%^&*()",
			expected: "feature",
		},
		{
			name:     "string with allowed special characters",
			input:    "feature/sub_branch.v1",
			expected: "feature/sub_branch.v1",
		},
		{
			name:     "string with multiple dashes",
			input:    "feature---branch",
			expected: "feature-branch",
		},
		{
			name:     "string with leading and trailing dashes",
			input:    "-feature-branch-",
			expected: "feature-branch",
		},
		{
			name:     "string with leading and trailing slashes",
			input:    "/feature/branch/",
			expected: "feature/branch",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "complex mixed case with special chars",
			input:    "USER/Feature Branch!@#$%^&*()/v1.0",
			expected: "user/feature-branch/v1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeBranchName(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeBranchName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetRemoteURL(t *testing.T) {
	repo := newTestRepo(t)

	// No remote yet -> empty.
	if got := GetRemoteURL(context.Background(), repo); got != "" {
		t.Fatalf("GetRemoteURL() with no remote = %q, want empty", got)
	}

	mustRunGit(t, repo, "remote", "add", "origin", "git@github.com:quantivly/atrium.git")
	if got := GetRemoteURL(context.Background(), repo); got != "git@github.com:quantivly/atrium.git" {
		t.Fatalf("GetRemoteURL() = %q, want the origin URL", got)
	}

	// Non-repo path -> empty (best-effort, like IsGitRepo).
	if got := GetRemoteURL(context.Background(), t.TempDir()); got != "" {
		t.Fatalf("GetRemoteURL() non-repo = %q, want empty", got)
	}
}

// ProbeGitRepo splits IsGitRepo's single bool into a verdict and whether there was one.
// The distinction is load-bearing for `atrium new`: `direct` (no worktree, no branch —
// the agent runs in the target itself) is derived from this answer, so a probe that
// could not run must not read as "not a repo".
func TestProbeGitRepo(t *testing.T) {
	repo := newTestRepo(t)

	if isRepo, known := ProbeGitRepo(context.Background(), repo); !isRepo || !known {
		t.Fatalf("ProbeGitRepo(repo) = (%v, %v), want (true, true)", isRepo, known)
	}

	// git ran and said no. A verdict, not a silence — the caller may act on it.
	if isRepo, known := ProbeGitRepo(context.Background(), t.TempDir()); isRepo || !known {
		t.Fatalf("ProbeGitRepo(plain dir) = (%v, %v), want (false, true)", isRepo, known)
	}

	// git could not answer. A cancelled context is the member of that class a hermetic
	// test can produce on demand; git off PATH and a fork failure under memory pressure
	// reach the same arm, since neither yields an *exec.ExitError with a real status.
	// The repo is the same one that answered true above, so the only variable is the
	// probe's ability to run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if isRepo, known := ProbeGitRepo(ctx, repo); isRepo || known {
		t.Fatalf("ProbeGitRepo(cancelled) = (%v, %v), want (false, false)", isRepo, known)
	}

	// And the fold IsGitRepo keeps: it still reports a plain false for that case, which
	// is why it is the wrong predicate for a headless create and stays right for a form.
	if IsGitRepo(ctx, repo) {
		t.Fatal("IsGitRepo(cancelled) = true, want false — it folds 'could not answer' into 'no'")
	}
}

// TestProbeGitRepoWillNotCallAnUnreadableRepoAPlainDirectory is the case the exit status
// cannot decide, and the one with teeth.
//
// git prints "fatal: not a git repository (or any of the parent directories): .git" and exits
// 128 for a directory with no repository AND for a real repository whose .git it cannot read.
// The same sentence, byte for byte, and the same status — verified by hand at a shell, which is
// why no amount of stderr parsing was going to separate them. Read as a verdict, the second
// case licenses everything the first does: app.executeCreateRequest's isolation guard passes
// because git DID answer, and `atrium new` builds a direct session — no worktree, no branch —
// with the agent editing the user's own checkout. A chmod that outlives one drain tick is
// enough, and nothing about the outcome is recoverable or visible.
//
// So the negative needs corroboration from something git is not the source of, and a .git that
// is present while git says there is none is it. os.Lstat reads the entry out of the parent
// directory, so mode 000 still answers present.
//
// The absent half is asserted by TestProbeGitRepo's plain-dir case above, and it is not
// incidental: it is what keeps a repo somebody deleted answerable in one tick
// (app.recheckAdoption) rather than held for the whole spool horizon.
func TestProbeGitRepoWillNotCallAnUnreadableRepoAPlainDirectory(t *testing.T) {
	repo := newTestRepo(t)
	gitDir := filepath.Join(repo, ".git")
	if err := os.Chmod(gitDir, 0o000); err != nil {
		t.Fatalf("chmod .git: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })
	// Root, or a filesystem with no permission enforcement, reads it anyway.
	if _, err := LookupLocalBranchTip(context.Background(), repo, "main"); err == nil {
		t.Skip("this user can read the repository through mode 000")
	}

	isRepo, known := ProbeGitRepo(context.Background(), repo)
	if isRepo || known {
		t.Fatalf("ProbeGitRepo(unreadable .git) = (%v, %v), want (false, false): git says the "+
			"same thing here as for a plain directory, and a plain directory is what licenses "+
			"an unisolated session", isRepo, known)
	}

	// The control, on the same repo one chmod later: the answer is a verdict again, so the
	// assertion above cannot be passing because this repo was never answerable.
	if err := os.Chmod(gitDir, 0o755); err != nil {
		t.Fatalf("chmod .git back: %v", err)
	}
	if isRepo, known := ProbeGitRepo(context.Background(), repo); !isRepo || !known {
		t.Fatalf("ProbeGitRepo(readable again) = (%v, %v), want (true, true)", isRepo, known)
	}
}
