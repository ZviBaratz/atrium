package git

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestParsePRStatus(t *testing.T) {
	tests := []struct {
		name        string
		json        string
		wantHasPR   bool
		wantNumber  int
		wantCI      CIStatus
		wantReview  ReviewStatus
		wantPass    int
		wantFail    int
		wantPending int
		wantErr     bool
	}{
		{
			name:      "no pr (number 0)",
			json:      `{"number":0,"statusCheckRollup":[]}`,
			wantHasPR: false,
		},
		{
			name:       "open pr, all checks success",
			json:       `{"number":12,"state":"OPEN","reviewDecision":"APPROVED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"COMPLETED","conclusion":"SUCCESS"}]}`,
			wantHasPR:  true,
			wantNumber: 12,
			wantCI:     CIPassing,
			wantReview: ReviewApproved,
			wantPass:   2,
		},
		{
			name:       "one failing check dominates",
			json:       `{"number":3,"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"COMPLETED","conclusion":"FAILURE"}]}`,
			wantHasPR:  true,
			wantNumber: 3,
			wantCI:     CIFailing,
			wantPass:   1,
			wantFail:   1,
		},
		{
			name:        "in-progress check is pending",
			json:        `{"number":4,"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"IN_PROGRESS","conclusion":""}]}`,
			wantHasPR:   true,
			wantNumber:  4,
			wantCI:      CIPending,
			wantPass:    1,
			wantPending: 1,
		},
		{
			name:       "empty rollup is CINone",
			json:       `{"number":5,"statusCheckRollup":[]}`,
			wantHasPR:  true,
			wantNumber: 5,
			wantCI:     CINone,
		},
		{
			name:       "changes requested",
			json:       `{"number":6,"reviewDecision":"CHANGES_REQUESTED","statusCheckRollup":[]}`,
			wantHasPR:  true,
			wantNumber: 6,
			wantReview: ReviewChangesRequested,
		},
		{
			name:       "review required",
			json:       `{"number":7,"reviewDecision":"REVIEW_REQUIRED","statusCheckRollup":[]}`,
			wantHasPR:  true,
			wantNumber: 7,
			wantReview: ReviewRequired,
		},
		{
			name:        "legacy status context state field",
			json:        `{"number":8,"statusCheckRollup":[{"state":"SUCCESS"},{"state":"PENDING"},{"state":"FAILURE"}]}`,
			wantHasPR:   true,
			wantNumber:  8,
			wantCI:      CIFailing, // failure dominates pending
			wantPass:    1,
			wantFail:    1,
			wantPending: 1,
		},
		{
			name:    "malformed json errors",
			json:    `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePRStatus([]byte(tt.json))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePRStatus(%q) error = nil, want error", tt.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePRStatus(%q) unexpected error: %v", tt.json, err)
			}
			if got.HasPR != tt.wantHasPR {
				t.Errorf("HasPR = %v, want %v", got.HasPR, tt.wantHasPR)
			}
			if got.Number != tt.wantNumber {
				t.Errorf("Number = %d, want %d", got.Number, tt.wantNumber)
			}
			if got.CI != tt.wantCI {
				t.Errorf("CI = %d, want %d", got.CI, tt.wantCI)
			}
			if got.Review != tt.wantReview {
				t.Errorf("Review = %d, want %d", got.Review, tt.wantReview)
			}
			if got.ChecksPass != tt.wantPass || got.ChecksFail != tt.wantFail || got.ChecksPending != tt.wantPending {
				t.Errorf("checks pass/fail/pending = %d/%d/%d, want %d/%d/%d",
					got.ChecksPass, got.ChecksFail, got.ChecksPending, tt.wantPass, tt.wantFail, tt.wantPending)
			}
		})
	}
}

// TestPRStatus_CacheHit verifies a fresh cache entry is served without invoking gh.
func TestPRStatus_CacheHit(t *testing.T) {
	wt := &Worktree{}
	wt.prCacheMu.Lock()
	wt.prCache = PRStatus{HasPR: true, Number: 7, fetchedAt: time.Now()}
	wt.prCacheMu.Unlock()

	called := false
	restore := stubGHPRView(func(context.Context, string, string) ([]byte, error) {
		called = true
		return nil, nil
	})
	defer restore()

	got := wt.PRStatus(context.Background(), false)
	if got.Number != 7 {
		t.Errorf("cache hit: Number = %d, want 7", got.Number)
	}
	if called {
		t.Error("fresh cache must not invoke gh")
	}
}

// TestPRStatus_SelectedTTLStricter verifies the selected TTL treats a 10s-old entry
// as stale while the background TTL still serves it.
func TestPRStatus_SelectedTTLStricter(t *testing.T) {
	seed := func() *Worktree {
		wt := &Worktree{} // bare: the recompute path's local ref gate fails => empty
		wt.prCacheMu.Lock()
		wt.prCache = PRStatus{HasPR: true, Number: 5, fetchedAt: time.Now().Add(-10 * time.Second)}
		wt.prCacheMu.Unlock()
		return wt
	}
	restore := stubGHPRView(func(context.Context, string, string) ([]byte, error) {
		t.Fatal("gh must not run: background serves cache, selected fails the local gate first")
		return nil, nil
	})
	defer restore()

	// Background TTL (25s): 10s-old entry is fresh -> cache hit.
	if got := seed().PRStatus(context.Background(), false); got.Number != 5 {
		t.Errorf("background TTL should serve 10s-old cache, got Number=%d", got.Number)
	}
	// Selected TTL (8s): 10s-old entry is stale -> recompute (which yields empty here).
	if got := seed().PRStatus(context.Background(), true); got.HasPR {
		t.Errorf("selected TTL should treat 10s-old entry as stale and recompute, got %+v", got)
	}
}

// TestPRStatus_NoRemoteBranchSkipsGH verifies the local pre-gate: a branch with no
// origin/<branch> ref never spends a gh call.
func TestPRStatus_NoRemoteBranchSkipsGH(t *testing.T) {
	repo := newTestRepo(t)
	wt := &Worktree{worktreePath: repo, branchName: "feat"} // no refs/remotes/origin/feat

	called := false
	restore := stubGHPRView(func(context.Context, string, string) ([]byte, error) {
		called = true
		return nil, nil
	})
	defer restore()

	got := wt.PRStatus(context.Background(), false)
	if got.HasPR {
		t.Errorf("unpushed branch must yield empty PRStatus, got %+v", got)
	}
	if called {
		t.Error("gh must not run for a branch with no remote-tracking ref")
	}
}

// TestPRStatus_NoPRCachesEmpty verifies a benign "no pull requests" gh error is
// cached as empty so it isn't retried within the TTL.
func TestPRStatus_NoPRCachesEmpty(t *testing.T) {
	wt := pushedWorktree(t)

	calls := 0
	restore := stubGHPRView(func(context.Context, string, string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf(`gh pr view: no pull requests found for branch "feat": exit status 1`)
	})
	defer restore()

	if got := wt.PRStatus(context.Background(), false); got.HasPR {
		t.Errorf("no-PR error should yield empty, got %+v", got)
	}
	_ = wt.PRStatus(context.Background(), false)
	if calls != 1 {
		t.Errorf("benign no-PR error must be cached, gh calls = %d, want 1", calls)
	}
}

// TestPRStatus_AuthErrorCachesEmpty verifies a not-authenticated gh failure is
// treated as benign and cached, so a pushed session doesn't re-spawn gh every
// tick while auth is missing (a deterministic, non-transient condition).
func TestPRStatus_AuthErrorCachesEmpty(t *testing.T) {
	wt := pushedWorktree(t)

	calls := 0
	restore := stubGHPRView(func(context.Context, string, string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("gh pr view: To get started with GitHub CLI, please run:  gh auth login: exit status 4")
	})
	defer restore()

	if got := wt.PRStatus(context.Background(), false); got.HasPR {
		t.Errorf("auth error should yield empty, got %+v", got)
	}
	_ = wt.PRStatus(context.Background(), false)
	if calls != 1 {
		t.Errorf("auth error must be cached, gh calls = %d, want 1", calls)
	}
}

// TestPRStatus_RealErrorNotCached verifies a genuine error (e.g. timeout) is not
// cached, so the next eligible tick retries.
func TestPRStatus_RealErrorNotCached(t *testing.T) {
	wt := pushedWorktree(t)

	calls := 0
	restore := stubGHPRView(func(context.Context, string, string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("gh pr view: : signal: killed")
	})
	defer restore()

	_ = wt.PRStatus(context.Background(), false)
	_ = wt.PRStatus(context.Background(), false)
	if calls != 2 {
		t.Errorf("genuine error must not be cached, gh calls = %d, want 2", calls)
	}
}

// TestPRStatus_Success verifies the full happy path: pre-gate passes, gh returns
// canned JSON, the parsed result populates and caches.
func TestPRStatus_Success(t *testing.T) {
	wt := pushedWorktree(t)

	calls := 0
	restore := stubGHPRView(func(_ context.Context, _ string, branch string) ([]byte, error) {
		calls++
		if branch != "feat" {
			t.Errorf("gh called with branch %q, want feat", branch)
		}
		return []byte(`{"number":42,"url":"https://example/42","state":"OPEN","isDraft":false,"reviewDecision":"APPROVED","mergeable":"MERGEABLE","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
	})
	defer restore()

	got := wt.PRStatus(context.Background(), false)
	if !got.HasPR || got.Number != 42 {
		t.Errorf("HasPR/Number = %v/%d, want true/42", got.HasPR, got.Number)
	}
	if got.CI != CIPassing {
		t.Errorf("CI = %d, want CIPassing", got.CI)
	}
	if got.Review != ReviewApproved {
		t.Errorf("Review = %d, want ReviewApproved", got.Review)
	}
	// Second call within TTL is served from cache.
	_ = wt.PRStatus(context.Background(), false)
	if calls != 1 {
		t.Errorf("successful result must be cached, gh calls = %d, want 1", calls)
	}
}

// stubGHPRView swaps the gh seam for a fake and returns a restore func.
func stubGHPRView(fn func(context.Context, string, string) ([]byte, error)) func() {
	orig := runGHPRView
	runGHPRView = fn
	return func() { runGHPRView = orig }
}

// pushedWorktree returns a Worktree over a fresh repo whose "feat" branch has a
// remote-tracking ref (created locally, no network), so the local pre-gate passes
// and PRStatus proceeds to the gh seam.
func pushedWorktree(t *testing.T) *Worktree {
	t.Helper()
	repo := newTestRepo(t)
	sha := revParse(t, repo, "HEAD")
	mustRunGit(t, repo, "update-ref", "refs/remotes/origin/feat", sha)
	return &Worktree{worktreePath: repo, branchName: "feat"}
}
