package ui

import (
	"fmt"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// Diff styles read the active theme at render time. additions are success
// (green), deletions danger (red), hunk headers cyan, meta/status muted, and
// "behind base" the lone attention color (amber) since it implies a rebase.
func additionStyle() lipgloss.Style   { return theme.Current().SuccessStyle() }
func deletionStyle() lipgloss.Style   { return theme.Current().DangerStyle() }
func hunkStyle() lipgloss.Style       { return theme.Current().CyanStyle() }
func metaStyle() lipgloss.Style       { return theme.Current().DimStyle() }
func diffBehindStyle() lipgloss.Style { return theme.Current().AttentionStyle() }

type DiffPane struct {
	viewport viewport.Model
	diff     string
	stats    string
	width    int
	height   int
}

func NewDiffPane() *DiffPane {
	return &DiffPane{
		viewport: viewport.New(0, 0),
	}
}

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

func (d *DiffPane) SetDiff(instance *session.Instance) {
	centeredFallbackMessage := lipgloss.Place(
		d.width,
		d.height,
		lipgloss.Center,
		lipgloss.Center,
		"No changes",
	)

	if instance == nil || !instance.Started() {
		d.viewport.SetContent(centeredFallbackMessage)
		return
	}

	stats := instance.GetDiffStats()
	if stats == nil {
		// Show loading message if worktree is not ready
		centeredMessage := lipgloss.Place(
			d.width,
			d.height,
			lipgloss.Center,
			lipgloss.Center,
			"Setting up worktree...",
		)
		d.viewport.SetContent(centeredMessage)
		return
	}

	if stats.Error != nil {
		// Show error message
		centeredMessage := lipgloss.Place(
			d.width,
			d.height,
			lipgloss.Center,
			lipgloss.Center,
			fmt.Sprintf("Error: %v", stats.Error),
		)
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
		d.diff = colorizeDiff(theme.SanitizeWidth(stats.Content))
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
		segs = append(segs, metaStyle().Render(fmt.Sprintf("⇡%s", pluralize(stats.Commits, "commit"))))
	}
	if stats.Behind > 0 {
		segs = append(segs, diffBehindStyle().Render(fmt.Sprintf("⇣%d behind", stats.Behind)))
	}
	if stats.Dirty {
		segs = append(segs, metaStyle().Render("uncommitted"))
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

func colorizeDiff(diff string) string {
	var coloredOutput strings.Builder

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		if len(line) > 0 {
			if strings.HasPrefix(line, "@@") {
				// Color hunk headers cyan
				coloredOutput.WriteString(hunkStyle().Render(line) + "\n")
			} else if line[0] == '+' && (len(line) == 1 || line[1] != '+') {
				// Color added lines green, excluding metadata like '+++'
				coloredOutput.WriteString(additionStyle().Render(line) + "\n")
			} else if line[0] == '-' && (len(line) == 1 || line[1] != '-') {
				// Color removed lines red, excluding metadata like '---'
				coloredOutput.WriteString(deletionStyle().Render(line) + "\n")
			} else {
				// Print metadata and unchanged lines without color
				coloredOutput.WriteString(line + "\n")
			}
		} else {
			// Preserve empty lines
			coloredOutput.WriteString("\n")
		}
	}

	return coloredOutput.String()
}
