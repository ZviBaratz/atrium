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
