package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/require"
)

// TestPRBadgeColor pins the badge color to the most urgent signal. The cases are
// theme-agnostic: each asserts against the palette token, not a literal hue.
func TestPRBadgeColor(t *testing.T) {
	th := theme.Current()
	p := th.Palette

	tests := []struct {
		name string
		pr   git.PRStatus
		want interface{}
	}{
		{"merged outranks everything", git.PRStatus{State: "MERGED", CI: git.CIFailing}, p.Purple},
		{"failing ci is danger", git.PRStatus{State: "OPEN", CI: git.CIFailing}, p.Danger},
		{"changes requested is danger", git.PRStatus{State: "OPEN", Review: git.ReviewChangesRequested}, p.Danger},
		{"pending ci is working", git.PRStatus{State: "OPEN", CI: git.CIPending}, p.Working},
		{"approved + passing is success", git.PRStatus{State: "OPEN", CI: git.CIPassing, Review: git.ReviewApproved}, p.Success},
		{"approved + no checks is success", git.PRStatus{State: "OPEN", CI: git.CINone, Review: git.ReviewApproved}, p.Success},
		{"awaiting review is neutral", git.PRStatus{State: "OPEN", CI: git.CIPassing, Review: git.ReviewRequired}, p.FgDim},
		// A draft never reads as ready-to-merge green, even when approved + green.
		{"approved + passing draft is neutral, not green", git.PRStatus{State: "OPEN", CI: git.CIPassing, Review: git.ReviewApproved, IsDraft: true}, p.FgDim},
		// ...but a draft still surfaces a failing check.
		{"failing draft still flags danger", git.PRStatus{State: "OPEN", CI: git.CIFailing, IsDraft: true}, p.Danger},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := tt.pr
			require.Equal(t, tt.want, prBadgeColor(th, &pr))
		})
	}
}

// TestPRStateWord folds the draft flag into the open state and otherwise mirrors
// the PR lifecycle.
func TestPRStateWord(t *testing.T) {
	require.Equal(t, "merged", prStateWord(&git.PRStatus{State: "MERGED"}))
	require.Equal(t, "closed", prStateWord(&git.PRStatus{State: "CLOSED"}))
	require.Equal(t, "open", prStateWord(&git.PRStatus{State: "OPEN"}))
	require.Equal(t, "draft", prStateWord(&git.PRStatus{State: "OPEN", IsDraft: true}))
	// A merged PR that happens to carry the draft flag is still merged.
	require.Equal(t, "merged", prStateWord(&git.PRStatus{State: "MERGED", IsDraft: true}))
}

// TestReviewWord renders each decision and stays silent when there is nothing to
// say (the empty string is omitted by the caller).
func TestReviewWord(t *testing.T) {
	require.Equal(t, "approved", reviewWord(git.ReviewApproved))
	require.Equal(t, "changes requested", reviewWord(git.ReviewChangesRequested))
	require.Equal(t, "review required", reviewWord(git.ReviewRequired))
	require.Equal(t, "", reviewWord(git.ReviewNone))
}
