package ui

import (
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
)

// prBadgeColor picks the list-row PR badge color by the most urgent signal, so a
// glance at the row tells you whether the PR needs action. It deliberately does
// not use Palette.Attention (reserved for the waiting/behind state) — a PR
// awaiting review is a neutral, not an attention, state.
func prBadgeColor(th *theme.Theme, pr *git.PRStatus) lipgloss.Color {
	switch {
	case pr.State == "MERGED":
		return th.Palette.Purple
	case pr.CI == git.CIFailing || pr.Review == git.ReviewChangesRequested:
		return th.Palette.Danger
	case pr.CI == git.CIPending:
		return th.Palette.Working
	// "Ready" green is reserved for a PR that can actually merge: approved with CI
	// green/absent. A draft is excluded — it reads as ready-to-merge otherwise,
	// when it explicitly is not. (A failing/pending draft still flags above.)
	case pr.Review == git.ReviewApproved && (pr.CI == git.CIPassing || pr.CI == git.CINone) && !pr.IsDraft:
		return th.Palette.Success
	default:
		return th.Palette.FgDim
	}
}

// prStateWord renders the PR's lifecycle state for the diff-tab header, folding
// the draft flag into the open state.
func prStateWord(pr *git.PRStatus) string {
	switch pr.State {
	case "MERGED":
		return "merged"
	case "CLOSED":
		return "closed"
	default:
		if pr.IsDraft {
			return "draft"
		}
		return "open"
	}
}

// reviewWord renders a review decision for the diff-tab header, or "" when there
// is nothing to say.
func reviewWord(r git.ReviewStatus) string {
	switch r {
	case git.ReviewApproved:
		return "approved"
	case git.ReviewChangesRequested:
		return "changes requested"
	case git.ReviewRequired:
		return "review required"
	default:
		return ""
	}
}
