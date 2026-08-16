package git

import (
	"context"
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
