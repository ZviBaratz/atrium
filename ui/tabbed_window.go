package ui

import (
	"fmt"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

func tabZoneID(i int) string { return fmt.Sprintf("tab-%d", i) }

// tabbedWindowZoneID marks the entire right tabbed pane (tab strip + window
// body), wrapping the per-tab zones nested inside it. Wheel events landing here
// scroll the active tab's content.
const tabbedWindowZoneID = "tabbed-window"

func tabBorderWithBottom(left, middle, right string) lipgloss.Border {
	// Start from the theme's box style so a fallback theme's square corners
	// apply to the tab strip too, not just the panels.
	border := theme.Current().Borders.Style
	border.BottomLeft = left
	border.Bottom = middle
	border.BottomRight = right
	return border
}

// Tab/window styles read the active theme at render time. Border color carries
// focus: the right pane's chrome is faint by default (the list panel, which owns
// the selection, keeps the accent border) and lights up accent only while a pane
// is in scroll mode — the one state where keyboard input is captured by this
// pane. focused is that scroll-mode flag; frame metrics are identical either
// way, so size computations may pass false.
func inactiveTabStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(tabBorderWithBottom("┴", "─", "┴"), true).
		BorderForeground(theme.Current().Palette.FgFaint).
		Foreground(theme.Current().Palette.FgDim).
		AlignHorizontal(lipgloss.Center)
}

func activeTabStyle(focused bool) lipgloss.Style {
	border := theme.Current().Palette.FgFaint
	label := theme.Current().Palette.AccentMuted
	if focused {
		border = theme.Current().Palette.Accent
		label = theme.Current().Palette.Accent
	}
	return lipgloss.NewStyle().
		Border(tabBorderWithBottom("┘", " ", "└"), true).
		BorderForeground(border).
		Foreground(label).
		Bold(true).
		AlignHorizontal(lipgloss.Center)
}

func windowStyle(focused bool) lipgloss.Style {
	color := theme.Current().Palette.FgFaint
	if focused {
		color = theme.Current().Palette.Accent
	}
	return lipgloss.NewStyle().
		BorderForeground(color).
		Border(theme.Current().Borders.Style, false, true, true, true)
}

// Indices of the right pane's tabs, in display order.
const (
	PreviewTab int = iota
	DiffTab
	TerminalTab
)

// Tab pairs a tab's display name with the function that renders its content.
type Tab struct {
	Name   string
	Render func(width int, height int) string
}

// TabbedWindow has tabs at the top of a pane which can be selected. The tabs
// take up one rune of height.
type TabbedWindow struct {
	tabs []string

	activeTab int
	height    int
	width     int

	preview  *PreviewPane
	diff     *DiffPane
	terminal *TerminalPane
	instance *session.Instance
}

// NewTabbedWindow assembles the right pane from its three tab panes.
func NewTabbedWindow(preview *PreviewPane, diff *DiffPane, terminal *TerminalPane) *TabbedWindow {
	return &TabbedWindow{
		tabs: []string{
			"Preview",
			"Diff",
			"Terminal",
		},
		preview:  preview,
		diff:     diff,
		terminal: terminal,
	}
}

// SetInstance records which instance the window is showing; scroll events are
// forwarded to it.
func (w *TabbedWindow) SetInstance(instance *session.Instance) {
	w.instance = instance
}

// SetSize resizes the window and propagates the resulting content area to all
// three tab panes.
func (w *TabbedWindow) SetSize(width, height int) {
	// w.width is the inner (pre-border) width; the window border adds its
	// horizontal frame back, so the pane's total rendered width equals the given
	// width and the right pane fills its column exactly.
	w.width = width - windowStyle(false).GetHorizontalFrameSize()
	w.height = height

	// Calculate the content height by subtracting:
	// 1. Tab height (tab border top+bottom + 1 label row)
	// 2. Window style vertical frame size (bottom border)
	// The tab strip's top border is the pane's visual top edge, so it aligns
	// with the list panel's top border at row 0 — no leading blank rows.
	tabHeight := activeTabStyle(false).GetVerticalFrameSize() + 1
	contentHeight := height - tabHeight - windowStyle(false).GetVerticalFrameSize()
	contentWidth := w.width - windowStyle(false).GetHorizontalFrameSize()

	w.preview.SetSize(contentWidth, contentHeight)
	w.diff.SetSize(contentWidth, contentHeight)
	w.terminal.SetSize(contentWidth, contentHeight)
}

// SetSplashFrame advances the empty-state splash animation clock on the panes
// that render it. Driven by the 100ms preview tick (not the content path) so the
// field keeps drifting regardless of which tab is focused.
func (w *TabbedWindow) SetSplashFrame(n int) {
	w.preview.SetSplashFrame(n)
	w.terminal.SetSplashFrame(n)
}

// GetPreviewSize returns the preview pane's content dimensions, used to size
// each instance's detached tmux session to match.
func (w *TabbedWindow) GetPreviewSize() (width, height int) {
	return w.preview.width, w.preview.height
}

// Toggle cycles to the next tab, wrapping from the last back to the first.
func (w *TabbedWindow) Toggle() {
	w.activeTab = (w.activeTab + 1) % len(w.tabs)
}

// SetActiveTab switches directly to tab i (e.g. from a mouse click). Like Toggle
// it only moves the index; the caller refreshes the active pane via
// instanceChanged(). Out-of-range indices are ignored.
func (w *TabbedWindow) SetActiveTab(i int) {
	if i < 0 || i >= len(w.tabs) {
		return
	}
	w.activeTab = i
}

// InBounds reports whether the mouse event lands within the tabbed window's
// rendered box. Used to route wheel events to the active tab's scroll. False
// before the first zone scan (zero ZoneInfo), so early frames route nowhere.
func (w *TabbedWindow) InBounds(msg tea.MouseMsg) bool {
	return zone.Get(tabbedWindowZoneID).InBounds(msg)
}

// TabAtZone returns the index of the tab containing the given mouse event, and
// whether any tab was hit.
func (w *TabbedWindow) TabAtZone(msg tea.MouseMsg) (int, bool) {
	for i := range w.tabs {
		if zone.Get(tabZoneID(i)).InBounds(msg) {
			return i, true
		}
	}
	return 0, false
}

// ToggleReverse cycles to the previous tab, wrapping from the first tab to the
// last. It is the complement of Toggle. The + len(w.tabs) term keeps the
// operand non-negative, since Go's % can return a negative result.
func (w *TabbedWindow) ToggleReverse() {
	w.activeTab = (w.activeTab - 1 + len(w.tabs)) % len(w.tabs)
}

// UpdatePreview updates the content of the preview pane. instance may be nil.
func (w *TabbedWindow) UpdatePreview(instance *session.Instance) error {
	if w.activeTab != PreviewTab {
		return nil
	}
	return w.preview.UpdateContent(instance)
}

// UpdateDiff refreshes the diff pane from the instance's worktree. Only
// updates when the diff tab is active.
func (w *TabbedWindow) UpdateDiff(instance *session.Instance) {
	if w.activeTab != DiffTab {
		return
	}
	w.diff.SetDiff(instance)
}

// UpdateTerminal updates the terminal pane content. Only updates when terminal tab is active.
func (w *TabbedWindow) UpdateTerminal(instance *session.Instance) error {
	if w.activeTab != TerminalTab {
		return nil
	}
	return w.terminal.UpdateContent(instance)
}

// ResetPreviewToNormalMode resets the preview pane to normal mode
func (w *TabbedWindow) ResetPreviewToNormalMode(instance *session.Instance) error {
	return w.preview.ResetToNormalMode(instance)
}

// PreviewLiveContent exposes the preview pane's live text for hint mode.
func (w *TabbedWindow) PreviewLiveContent() (string, bool) {
	return w.preview.LiveContent()
}

// SetPreviewHintOverlay shows a frozen hint-decorated frame over instance's
// live preview; ClearPreviewHintOverlay resumes the live view.
func (w *TabbedWindow) SetPreviewHintOverlay(instance *session.Instance, content string) {
	w.preview.SetHintOverlay(instance, content)
}

// ClearPreviewHintOverlay exits hint mode on the preview pane.
func (w *TabbedWindow) ClearPreviewHintOverlay() { w.preview.ClearHintOverlay() }

// InPreviewHintMode reports whether the preview pane shows a hint overlay.
func (w *TabbedWindow) InPreviewHintMode() bool { return w.preview.InHintMode() }

// ScrollUp scrolls the active tab's pane up. lines governs the preview pane's
// in-scroll granularity (a wheel notch moves several, a key one); the diff and
// terminal panes keep their own single-step scroll.
func (w *TabbedWindow) ScrollUp(lines int) {
	switch w.activeTab {
	case PreviewTab:
		err := w.preview.ScrollUp(w.instance, lines)
		if err != nil {
			log.InfoLog.Printf("tabbed window failed to scroll up: %v", err)
		}
	case DiffTab:
		w.diff.ScrollUp()
	case TerminalTab:
		if err := w.terminal.ScrollUp(); err != nil {
			log.InfoLog.Printf("tabbed window failed to scroll terminal up: %v", err)
		}
	}
}

// ScrollDown scrolls the active tab's pane down; see ScrollUp on lines.
func (w *TabbedWindow) ScrollDown(lines int) {
	switch w.activeTab {
	case PreviewTab:
		err := w.preview.ScrollDown(w.instance, lines)
		if err != nil {
			log.InfoLog.Printf("tabbed window failed to scroll down: %v", err)
		}
	case DiffTab:
		w.diff.ScrollDown()
	case TerminalTab:
		if err := w.terminal.ScrollDown(); err != nil {
			log.InfoLog.Printf("tabbed window failed to scroll terminal down: %v", err)
		}
	}
}

// IsInPreviewTab returns true if the preview tab is currently active
func (w *TabbedWindow) IsInPreviewTab() bool {
	return w.activeTab == PreviewTab
}

// IsInDiffTab returns true if the diff tab is currently active
func (w *TabbedWindow) IsInDiffTab() bool {
	return w.activeTab == DiffTab
}

// IsInTerminalTab returns true if the terminal tab is currently active
func (w *TabbedWindow) IsInTerminalTab() bool {
	return w.activeTab == TerminalTab
}

// --- Diff-tab comment mode (#383): thin proxies to the diff pane's line cursor ---

// EnterDiffComment freezes the diff pane and drops the comment cursor; false when
// the diff has no code line to anchor a comment to.
func (w *TabbedWindow) EnterDiffComment() bool { return w.diff.EnterComment() }

// ExitDiffComment leaves comment mode and lets live diff refreshes resume.
func (w *TabbedWindow) ExitDiffComment() { w.diff.ExitComment() }

// DiffCursorDown steps the comment cursor to the next code line.
func (w *TabbedWindow) DiffCursorDown() { w.diff.CursorDown() }

// DiffCursorUp steps the comment cursor to the previous code line.
func (w *TabbedWindow) DiffCursorUp() { w.diff.CursorUp() }

// DiffExtendDown grows the comment selection to the next contiguous code line below.
func (w *TabbedWindow) DiffExtendDown() { w.diff.ExtendDown() }

// DiffExtendUp grows the comment selection to the next contiguous code line above.
func (w *TabbedWindow) DiffExtendUp() { w.diff.ExtendUp() }

// IsDiffCommenting reports whether the diff pane is in comment mode.
func (w *TabbedWindow) IsDiffCommenting() bool { return w.diff.IsCommenting() }

// DiffCommentLocation returns the "file:line" the cursor sits on, for the composer title.
func (w *TabbedWindow) DiffCommentLocation() (string, bool) { return w.diff.CommentLocation() }

// DiffCommentMessage builds the queued-prompt text for the cursor's line and note.
func (w *TabbedWindow) DiffCommentMessage(note string) (string, bool) {
	return w.diff.CommentMessage(note)
}

// GetActiveTab returns the currently active tab index
func (w *TabbedWindow) GetActiveTab() int {
	return w.activeTab
}

// AttachTerminal attaches to the terminal tmux session
func (w *TabbedWindow) AttachTerminal() (chan struct{}, error) {
	return w.terminal.Attach()
}

// CleanupTerminal closes the terminal session
func (w *TabbedWindow) CleanupTerminal() {
	w.terminal.Close()
}

// CleanupTerminalForInstance closes the cached terminal session for the given instance.
func (w *TabbedWindow) CleanupTerminalForInstance(inst *session.Instance) {
	w.terminal.CloseForInstance(inst)
}

// IsPreviewInScrollMode returns true if the preview pane is in scroll mode
func (w *TabbedWindow) IsPreviewInScrollMode() bool {
	return w.preview.IsScrolling()
}

// PreviewScrollContent exposes the preview pane's visible viewport text for
// hint mode while the pane is in scroll mode.
func (w *TabbedWindow) PreviewScrollContent() (string, bool) {
	return w.preview.ScrollContent()
}

// SetPreviewScrollContent puts the preview pane into scroll mode with content
// loaded directly into the viewport. Used by tests to simulate a scrolled
// state without a live tmux session.
func (w *TabbedWindow) SetPreviewScrollContent(inst *session.Instance, content string) {
	w.preview.viewport.SetContent(content)
	w.preview.enterScrollMode(inst)
	w.preview.viewport.GotoBottom()
	w.instance = inst
}

// IsTerminalInScrollMode returns true if the terminal pane is in scroll mode
func (w *TabbedWindow) IsTerminalInScrollMode() bool {
	return w.terminal.IsScrolling()
}

// paneScrolling reports whether any tab pane is in a key-capturing mode
// (scroll or hint) — the state that renders the window's chrome as focused.
// The diff tab scrolls live without a mode, so it never claims focus.
func (w *TabbedWindow) paneScrolling() bool {
	return w.preview.IsScrolling() || w.preview.InHintMode() || w.terminal.IsScrolling()
}

// ResetTerminalToNormalMode exits scroll mode on the terminal pane
func (w *TabbedWindow) ResetTerminalToNormalMode() {
	w.terminal.ResetToNormalMode()
}

func (w *TabbedWindow) String() string {
	if w.width == 0 || w.height == 0 {
		return ""
	}

	var renderedTabs []string

	// Scroll mode is the one state where this pane captures keyboard input, so
	// it is what lights the pane's chrome up as focused.
	focused := w.paneScrolling()

	totalTabWidth := w.width + windowStyle(false).GetHorizontalFrameSize()
	tabWidth := totalTabWidth / len(w.tabs)
	lastTabWidth := totalTabWidth - tabWidth*(len(w.tabs)-1)
	tabHeight := activeTabStyle(false).GetVerticalFrameSize() + 1 // get padding border margin size + 1 for character height

	for i, t := range w.tabs {
		width := tabWidth
		if i == len(w.tabs)-1 {
			width = lastTabWidth
		}

		var style lipgloss.Style
		isFirst, isLast, isActive := i == 0, i == len(w.tabs)-1, i == w.activeTab
		if isActive {
			style = activeTabStyle(focused)
		} else {
			style = inactiveTabStyle()
		}
		border, _, _, _, _ := style.GetBorder()
		if isFirst && isActive {
			border.BottomLeft = "│"
		} else if isFirst {
			border.BottomLeft = "├"
		} else if isLast && isActive {
			border.BottomRight = "│"
		} else if isLast {
			border.BottomRight = "┤"
		}
		style = style.Border(border)
		style = style.Width(width - style.GetHorizontalFrameSize())
		renderedTabs = append(renderedTabs, zone.Mark(tabZoneID(i), style.Render(t)))
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, renderedTabs...)
	var content string
	switch w.activeTab {
	case PreviewTab:
		content = w.preview.String()
	case DiffTab:
		content = w.diff.String()
	case TerminalTab:
		content = w.terminal.String()
	}
	window := windowStyle(focused).Render(
		lipgloss.Place(
			w.width, w.height-windowStyle(false).GetVerticalFrameSize()-tabHeight,
			lipgloss.Left, lipgloss.Top, content))

	// Defensive height cap: lipgloss.Place aligns content but does not truncate, so
	// an over-tall tab body (e.g. wrapped capture/diff lines) would make this column
	// taller than its budget. View joins it against the list with JoinHorizontal, so
	// any excess overflows the terminal and scrolls the whole frame. Bound it to
	// w.height so the right column always matches the list column.
	// The panel zone wraps outside MaxHeight so truncation cannot eat the end marker.
	return zone.Mark(tabbedWindowZoneID, lipgloss.NewStyle().MaxHeight(w.height).Render(
		lipgloss.JoinVertical(lipgloss.Left, row, window)))
}
