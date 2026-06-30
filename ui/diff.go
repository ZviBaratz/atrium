package ui

import (
	"fmt"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Diff styles read the active theme at render time. additions are success
// (green), deletions danger (red), hunk headers cyan, meta/status muted, and
// "behind base" the lone attention color (amber) since it implies a rebase.
func additionStyle() lipgloss.Style   { return theme.Current().SuccessStyle() }
func deletionStyle() lipgloss.Style   { return theme.Current().DangerStyle() }
func hunkStyle() lipgloss.Style       { return theme.Current().CyanStyle() }
func metaStyle() lipgloss.Style       { return theme.Current().DimStyle() }
func diffBehindStyle() lipgloss.Style { return theme.Current().AttentionStyle() }

// DiffPane renders the selected instance's diff against its base, with summary
// stats above the scrollable patch.
type DiffPane struct {
	viewport viewport.Model
	diff     string
	stats    string
	width    int
	height   int
}

// NewDiffPane returns an empty DiffPane.
func NewDiffPane() *DiffPane {
	return &DiffPane{
		viewport: viewport.New(0, 0),
	}
}

// SetSize sets the pane's render dimensions and re-flows existing content into
// the resized viewport.
func (d *DiffPane) SetSize(width, height int) {
	d.width = width
	d.height = height
	d.viewport.Width = width
	d.viewport.Height = height
	// Update viewport content if diff exists
	if d.diff != "" || d.stats != "" {
		d.viewport.SetContent(lipgloss.JoinVertical(lipgloss.Left, d.stats, d.diff))
	}
}

// SetDiff recomputes and renders the instance's diff, falling back to a
// centered placeholder when there are no changes (or no instance).
func (d *DiffPane) SetDiff(instance *session.Instance) {
	centeredFallbackMessage := centerInBox(d.width, d.height, metaStyle().Render("No changes"))

	if instance == nil || !instance.Started() {
		d.viewport.SetContent(centeredFallbackMessage)
		return
	}

	// A direct (non-git) session has no worktree to diff. Say so explicitly rather than
	// falling into the "Setting up workspace..." path below, which never resolves.
	if instance.IsDirect() {
		d.stats = ""
		d.diff = ""
		d.viewport.SetContent(centerInBox(d.width, d.height,
			metaStyle().Render(fmt.Sprintf("Direct session — git tracking disabled.\nAgent runs in %s", instance.Path))))
		return
	}

	stats := instance.GetDiffStats()
	if stats == nil {
		// Show loading message if worktree is not ready
		// matches the preview pane's splash
		centeredMessage := centerInBox(d.width, d.height, metaStyle().Render("Setting up workspace..."))
		d.viewport.SetContent(centeredMessage)
		return
	}

	if stats.Error != nil {
		// Show error message — danger-styled, so a broken diff doesn't render as
		// unstyled default text while every sibling placeholder is dim.
		centeredMessage := centerInBox(d.width, d.height,
			theme.Current().DangerStyle().Render(fmt.Sprintf("Error: %v", stats.Error)))
		d.viewport.SetContent(centeredMessage)
		return
	}

	if stats.IsEmpty() {
		d.stats = ""
		d.diff = ""
		d.viewport.SetContent(centeredFallbackMessage)
	} else {
		additions := additionStyle().Render(fmt.Sprintf("%d additions(+)", stats.Added))
		deletions := deletionStyle().Render(fmt.Sprintf("%d deletions(-)", stats.Removed))
		lineStats := lipgloss.JoinHorizontal(lipgloss.Center, additions, " ", deletions)
		if header := gitContextHeader(instance, stats); header != "" {
			d.stats = lipgloss.JoinVertical(lipgloss.Left, header, lineStats)
		} else {
			d.stats = lineStats
		}
		// Decompose font-dependent emoji clusters in the diff so the width we lay out
		// matches what the terminal renders and the pane can't wrap (see theme.SanitizeWidth).
		d.diff = colorizeDiff(theme.SanitizeWidth(stats.Content), d.width)
		d.viewport.SetContent(lipgloss.JoinVertical(lipgloss.Left, d.stats, d.diff))
	}
}

// gitContextHeader builds the one-line git-context summary shown above the
// additions/deletions line. Segments that are zero/empty are omitted, so a clean
// session with no commits shows nothing extra. Returns "" when there is nothing to add.
func gitContextHeader(instance *session.Instance, stats *git.DiffStats) string {
	var baseRef string
	if wt, err := instance.GetGitWorktree(); err == nil && wt != nil {
		baseRef = wt.GetBaseRef()
	}

	var segs []string
	if branch := instance.Branch; branch != "" {
		if baseRef != "" {
			segs = append(segs, metaStyle().Render(fmt.Sprintf("%s ← %s", baseRef, branch)))
		} else {
			segs = append(segs, metaStyle().Render(branch))
		}
	}
	if stats.FilesChanged > 0 {
		segs = append(segs, metaStyle().Render(fmt.Sprintf("%s changed", pluralize(stats.FilesChanged, "file"))))
	}
	if stats.Commits > 0 {
		segs = append(segs, metaStyle().Render(fmt.Sprintf("%s%s", theme.Current().Glyphs.Ahead, pluralize(stats.Commits, "commit"))))
	}
	if stats.Behind > 0 {
		segs = append(segs, diffBehindStyle().Render(fmt.Sprintf("%s%d behind", theme.Current().Glyphs.Behind, stats.Behind)))
	}
	if stats.Dirty {
		segs = append(segs, metaStyle().Render("uncommitted"))
	}

	// Pull-request detail: number + lifecycle state, check tallies, review
	// decision. Omitted entirely when there is no PR, so a session whose branch
	// isn't pushed shows nothing extra (silent degradation, like the diff stats).
	if pr := instance.GetPRStatus(); pr != nil && pr.HasPR {
		segs = append(segs, metaStyle().Render(fmt.Sprintf("PR #%d %s", pr.Number, prStateWord(pr))))
		if pr.ChecksPass+pr.ChecksFail+pr.ChecksPending > 0 {
			checks := fmt.Sprintf("checks %d✓ %d✗ %d•", pr.ChecksPass, pr.ChecksFail, pr.ChecksPending)
			if pr.CI == git.CIFailing {
				segs = append(segs, deletionStyle().Render(checks))
			} else {
				segs = append(segs, metaStyle().Render(checks))
			}
		}
		if word := reviewWord(pr.Review); word != "" {
			segs = append(segs, metaStyle().Render(word))
		}
		if pr.Mergeable == "CONFLICTING" {
			segs = append(segs, diffBehindStyle().Render("conflicting"))
		}
	}

	if len(segs) == 0 {
		return ""
	}
	return strings.Join(segs, metaStyle().Render("  ·  "))
}

// pluralize formats a count with a singular/plural noun (e.g. "1 file", "3 files").
func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func (d *DiffPane) String() string {
	return d.viewport.View()
}

// ScrollUp scrolls the viewport up
func (d *DiffPane) ScrollUp() {
	d.viewport.LineUp(1)
}

// ScrollDown scrolls the viewport down
func (d *DiffPane) ScrollDown() {
	d.viewport.LineDown(1)
}

// diffMetaPrefixes mark the per-file metadata lines git emits after a
// "diff --git" header; they render dimmed so the content lines stand out.
var diffMetaPrefixes = []string{
	"index ", "--- ", "+++ ", "old mode", "new mode", "new file mode",
	"deleted file mode", "rename from", "rename to", "similarity index",
	"copy from", "copy to", "Binary files", "---", "+++",
}

func isDiffMeta(line string) bool {
	for _, p := range diffMetaPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// diffFilePath extracts the post-change path from a "diff --git a/x b/y"
// header. Returns "" when the line doesn't carry one.
func diffFilePath(line string) string {
	if i := strings.Index(line, " b/"); i >= 0 {
		return line[i+3:]
	}
	return ""
}

// colorizeDiff renders a unified diff for the pane: each "diff --git" header
// becomes a file boundary (faint rule + bold path) so a multi-file diff reads
// as sections; remaining metadata is dimmed; hunks are cyan and +/- lines
// colored. Tabs expand to spaces and every line truncates to width — the
// viewport must never soft-wrap, or scroll position and apparent boundaries
// jump (the same discipline the preview pane applies). A non-positive width
// (startup, tests) skips truncation.
func colorizeDiff(diff string, width int) string {
	var out strings.Builder

	// fit expands tabs and truncates the *plain* line before styling, so the
	// width math never has to see escape sequences.
	fit := func(line string) string {
		line = strings.ReplaceAll(line, "\t", "    ")
		if width > 0 && runewidth.StringWidth(line) > width {
			line = runewidth.Truncate(line, width-1, "…")
		}
		return line
	}

	ruleLen := 24
	if width > 0 {
		ruleLen = width
	}
	rule := theme.Current().FaintStyle().Render(strings.Repeat("─", ruleLen))

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case line == "":
			out.WriteString("\n")
		case strings.HasPrefix(line, "diff --git"):
			out.WriteString(rule + "\n")
			if path := diffFilePath(line); path != "" {
				out.WriteString(theme.Current().FgStyle().Bold(true).Render(fit(path)) + "\n")
			} else {
				out.WriteString(metaStyle().Render(fit(line)) + "\n")
			}
		case strings.HasPrefix(line, "@@"):
			out.WriteString(hunkStyle().Render(fit(line)) + "\n")
		case line[0] == '+' && (len(line) == 1 || line[1] != '+'):
			out.WriteString(additionStyle().Render(fit(line)) + "\n")
		case line[0] == '-' && (len(line) == 1 || line[1] != '-'):
			out.WriteString(deletionStyle().Render(fit(line)) + "\n")
		case isDiffMeta(line):
			out.WriteString(metaStyle().Render(fit(line)) + "\n")
		default:
			// Context lines pass through uncolored.
			out.WriteString(fit(line) + "\n")
		}
	}

	return out.String()
}
