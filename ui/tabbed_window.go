package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/hints"
	"github.com/ZviBaratz/atrium/internal/memo"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

func tabZoneID(i int) string { return fmt.Sprintf("tab-%d", i) }

// tabbedWindowZoneID marks the entire right tabbed pane (tab strip + window
// body), wrapping the per-tab zones nested inside it. Wheel events landing here
// scroll the active tab's content.
const tabbedWindowZoneID = "tabbed-window"

func tabBorderWithBottom(th *theme.Theme, left, middle, right string) lipgloss.Border {
	// Start from the theme's box style so a fallback theme's square corners
	// apply to the tab strip too, not just the panels.
	border := th.Borders.Style
	border.BottomLeft = left
	border.Bottom = middle
	border.BottomRight = right
	return border
}

// Tab/window styles take the theme rather than reading theme.Current(), so the
// one that composed a frame is the one the memo keyed on — see tabbedKey. Border
// color carries focus: the right pane's chrome is faint by default (the list
// panel, which owns the selection, keeps the accent border) and lights up accent
// only while a pane is in scroll mode — the one state where keyboard input is
// captured by this pane. focused is that scroll-mode flag; frame metrics are
// identical either way, so size computations may pass false.
func inactiveTabStyle(th *theme.Theme) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(tabBorderWithBottom(th, "┴", "─", "┴"), true).
		BorderForeground(th.Palette.FgFaint).
		Foreground(th.Palette.FgDim).
		AlignHorizontal(lipgloss.Center)
}

func activeTabStyle(th *theme.Theme, focused bool) lipgloss.Style {
	border := th.Palette.FgFaint
	label := th.Palette.AccentMuted
	if focused {
		border = th.Palette.Accent
		label = th.Palette.Accent
	}
	return lipgloss.NewStyle().
		Border(tabBorderWithBottom(th, "┘", " ", "└"), true).
		BorderForeground(border).
		Foreground(label).
		Bold(true).
		AlignHorizontal(lipgloss.Center)
}

func windowStyle(th *theme.Theme, focused bool) lipgloss.Style {
	color := th.Palette.FgFaint
	if focused {
		color = th.Palette.Accent
	}
	return lipgloss.NewStyle().
		BorderForeground(color).
		Border(th.Borders.Style, false, true, true, true)
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

	// memo skips compose for a window whose inputs have not moved. This pane is the
	// single most expensive thing Atrium builds: 40% of a cold 14-session frame
	// build, and only one of those forty points is the active pane's own String().
	// The other thirty-nine are the wrapping — Place, the bordered windowStyle, the
	// height clamp, two joins — each layer re-measuring every line of what it wraps
	// (#565). Keying on the wrapped bytes is what lets the cheap point run every
	// frame while the expensive thirty-nine are skipped. See tabbedKey.
	//
	// (It was 58% before this pane stopped being composed twice per frame; see the
	// note beside the call in app.viewContent.)
	memo memo.Cache[tabbedKey]
}

// tabbedKey is everything compose reads. The load-bearing entry is content: the
// active pane's already-rendered text, so the memo is keyed on the actual bytes
// rather than on the pane state that produced them — which is what keeps a stale
// splash frame, scroll snapshot or "— stale 3s" marker from ever being served
// (#561's argument for zone.Scan, applied one layer out).
//
// The rest are the scalars compose reads directly. theme is the *Theme pointer
// rather than a name because theme.compose allocates a fresh one on every Set /
// SetGlyphSet, so the pointer IS the theme generation — a new palette or glyph
// rung cannot slip past the key. It is also the theme compose actually renders
// with: the style helpers take it as a parameter rather than reading
// theme.Current() themselves, so the frame in the cache was built by the theme
// the cache is filed under, and not merely invalidated by it.
//
// w.tabs is deliberately absent: it is set once in NewTabbedWindow and has no
// setter, so keying on it would claim a guard that guards nothing.
type tabbedKey struct {
	content   string
	width     int
	height    int
	activeTab int
	focused   bool
	theme     *theme.Theme
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
	th := theme.Current()
	w.width = width - windowStyle(th, false).GetHorizontalFrameSize()
	w.height = height

	// Calculate the content height by subtracting:
	// 1. Tab height (tab border top+bottom + 1 label row)
	// 2. Window style vertical frame size (bottom border)
	// The tab strip's top border is the pane's visual top edge, so it aligns
	// with the list panel's top border at row 0 — no leading blank rows.
	tabHeight := activeTabStyle(th, false).GetVerticalFrameSize() + 1
	contentHeight := height - tabHeight - windowStyle(th, false).GetVerticalFrameSize()
	contentWidth := w.width - windowStyle(th, false).GetHorizontalFrameSize()

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

// CopyableContent returns the active tab's content as plain text, and a label
// naming what it is, for the copy action (#380). "Plain" is the whole point: the
// alt-screen makes a mouse selection take borders and gutters with it, and the
// rendered diff is colorized, tab-expanded and truncated to the pane, so neither
// is worth pasting anywhere.
//
//   - Diff tab: the raw `git diff` output the pane rendered FROM — or, in comment
//     mode, just the rows the cursor has selected.
//   - Preview / Terminal: the captured pane with its ANSI stripped.
//
// ok is false when there is nothing to copy (an empty diff, a pane that has never
// captured, a fallback state), so the caller can say so rather than copying "".
func (w *TabbedWindow) CopyableContent(instance *session.Instance) (text, what string, ok bool) {
	switch w.activeTab {
	case DiffTab:
		if sel := w.diff.SelectedText(); sel != "" {
			return sel, "selected diff lines", true
		}
		if instance == nil {
			return "", "", false
		}
		stats := instance.GetDiffStats()
		if stats == nil || strings.TrimSpace(stats.Content) == "" {
			return "", "", false
		}
		return stats.Content, "diff", true
	case TerminalTab:
		content, live := w.terminal.LiveContent()
		if !live {
			return "", "", false
		}
		return hints.StripANSI(content), "terminal", true
	default:
		content, live := w.preview.LiveContent()
		if !live {
			return "", "", false
		}
		return hints.StripANSI(content), "pane", true
	}
}

// TerminalCaptureTarget exposes the terminal pane's shell session for the app's
// background capture chain. ok=false means the shell has yet to be created —
// EnsureTerminalSession does that, off the update thread.
func (w *TabbedWindow) TerminalCaptureTarget(instance *session.Instance) (*tmux.Session, string, bool) {
	return w.terminal.CaptureTarget(instance)
}

// EnsureTerminalSession creates the instance's shell session. Runs on the capture
// goroutine: it starts a tmux session, which is exactly the work that must not
// happen inside Update.
//
// title rides along rather than being read off the instance down there, because Title is
// unguarded and AdoptRename writes it on the update thread (#718). See EnsureSession for
// what the resulting staleness costs at each use.
func (w *TabbedWindow) EnsureTerminalSession(instance *session.Instance, title string) (string, error) {
	return w.terminal.EnsureSession(instance, title)
}

// HasTerminalSession reports whether a shell session has been created for
// instance. Exposed so a test can observe *when* creation happens — the pane's own
// sessions are built with the real executor, so a fake one cannot see them.
func (w *TabbedWindow) HasTerminalSession(instance *session.Instance) bool {
	_, _, ok := w.terminal.CaptureTarget(instance)
	return ok
}

// CloseTerminal tears down every shell session the terminal pane opened.
func (w *TabbedWindow) CloseTerminal() { w.terminal.Close() }

// ApplyTerminalFrame installs a background shell capture (main thread).
func (w *TabbedWindow) ApplyTerminalFrame(key, content string, err error, at time.Time) {
	w.terminal.ApplyFrame(key, content, err, at)
}

// NoteFrameTargetChange tells the preview pane its frame source just changed, so
// the staleness marker measures from now rather than from a frame captured for a
// different session (or before a stint on a tab that captures nothing).
func (w *TabbedWindow) NoteFrameTargetChange(now time.Time) { w.preview.NoteTargetChange(now) }

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

// SetDiffContent seeds the diff pane's comment rows from raw unified-diff content,
// bypassing the live-instance path so comment mode (#383) is reachable in tests
// without a real git worktree. Exported only because those tests live in package
// app; it is not a display path — it sets no rendered content, so the pane still
// draws whatever SetDiff last put in the viewport.
func (w *TabbedWindow) SetDiffContent(content string) {
	w.diff.rows = parseDiffRows(content)
}

// GetActiveTab returns the currently active tab index
func (w *TabbedWindow) GetActiveTab() int {
	return w.activeTab
}

// AttachTerminal attaches to the terminal tmux session
func (w *TabbedWindow) AttachTerminal() (chan struct{}, error) {
	return w.terminal.Attach()
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
	// Render the active pane first, so the memo can key on its output bytes instead
	// of on the pane state behind them. That render is the one part of this method
	// no hit can skip, and its cost depends on which tab is showing: ~1% of the
	// method on the preview tab (where the 40%-of-a-frame-build figure was measured),
	// materially more on the other two, since DiffPane re-colours the whole diff and
	// TerminalPane takes a lock and rebuilds from its session map. The memo is worth
	// most on the tab Atrium sits on by default, and less on the other two.
	k := tabbedKey{
		content:   w.activePaneContent(),
		width:     w.width,
		height:    w.height,
		activeTab: w.activeTab,
		// Scroll mode is the one state where this pane captures keyboard input, so
		// it is what lights the pane's chrome up as focused.
		focused: w.paneScrolling(),
		theme:   theme.Current(),
	}
	return w.memo.Get(k, func() string { return w.compose(k) })
}

// activePaneContent renders whichever tab is showing. An index outside the three
// tabs yields "", exactly as the switch this was lifted from did; Toggle wraps and
// SetActiveTab range-checks, so that arm is unreachable rather than a fallback.
func (w *TabbedWindow) activePaneContent() string {
	switch w.activeTab {
	case PreviewTab:
		return w.preview.String()
	case DiffTab:
		return w.diff.String()
	case TerminalTab:
		return w.terminal.String()
	}
	return ""
}

// ComposeRuns reports how many times the window has actually been composed, and
// ResetMemo drops the cached frame. Exported so a test can prove a repeat render
// was served from the memo (rather than asserting equality, which passes just as
// well when nothing was cached) and so a benchmark can stay cold.
func (w *TabbedWindow) ComposeRuns() int { return w.memo.Runs() }

// ResetMemo drops the memoized frame and the compose count. See ComposeRuns.
func (w *TabbedWindow) ResetMemo() { w.memo.Reset() }

// compose builds the window from k and nothing else — bar w.tabs, which is fixed
// at construction. That is the property the memo rests on, and it is meant to be
// checkable by reading this body.
//
// Two things make an input escape the key, and the second is the one that is easy
// to miss: a `w.` selector that is not w.tabs, and a call that reads mutable
// PACKAGE state. theme.Current() is the live example — it is not a `w.` anything,
// and an earlier draft of this comment named only the first rule, so it would have
// audited clean over four style helpers all reading the global theme. They take
// *theme.Theme now for exactly that reason. Any new global read here needs the
// same treatment or a line in tabbedKey.
func (w *TabbedWindow) compose(k tabbedKey) string {
	var renderedTabs []string

	focused := k.focused

	totalTabWidth := k.width + windowStyle(k.theme, false).GetHorizontalFrameSize()
	tabWidth := totalTabWidth / len(w.tabs)
	lastTabWidth := totalTabWidth - tabWidth*(len(w.tabs)-1)
	tabHeight := activeTabStyle(k.theme, false).GetVerticalFrameSize() + 1 // get padding border margin size + 1 for character height

	for i, t := range w.tabs {
		width := tabWidth
		if i == len(w.tabs)-1 {
			width = lastTabWidth
		}

		var style lipgloss.Style
		isFirst, isLast, isActive := i == 0, i == len(w.tabs)-1, i == k.activeTab
		if isActive {
			style = activeTabStyle(k.theme, focused)
		} else {
			style = inactiveTabStyle(k.theme)
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
		// v2's Width is the total including the frame, so the frame size is no
		// longer subtracted here — see the note in theme.Panel.
		style = style.Width(width)
		renderedTabs = append(renderedTabs, zone.Mark(tabZoneID(i), style.Render(t)))
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, renderedTabs...)
	window := windowStyle(k.theme, focused).Render(
		lipgloss.Place(
			k.width, k.height-windowStyle(k.theme, false).GetVerticalFrameSize()-tabHeight,
			lipgloss.Left, lipgloss.Top, k.content))

	// Defensive height cap: lipgloss.Place aligns content but does not truncate, so
	// an over-tall tab body (e.g. wrapped capture/diff lines) would make this column
	// taller than its budget. View joins it against the list with JoinHorizontal, so
	// any excess overflows the terminal and scrolls the whole frame. Bound it to
	// w.height so the right column always matches the list column.
	// The panel zone wraps outside MaxHeight so truncation cannot eat the end marker.
	return zone.Mark(tabbedWindowZoneID, lipgloss.NewStyle().MaxHeight(k.height).Render(
		lipgloss.JoinVertical(lipgloss.Left, row, window)))
}
