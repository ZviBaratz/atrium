package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// previewFallbackLog rate-limits the diagnostic emitted whenever the preview falls
// back to the "setting up" splash (or a capture error), so a genuinely stuck session
// is recorded without flooding the log on every 100ms tick. It is the evidence trail
// for the still-coming-up vs. stale-readiness question (see UpdateContent).
var previewFallbackLog = log.NewEvery(5 * time.Second)

// logPreviewFallback records the instance's observable readiness signals when the
// preview cannot show live content, so the lying signal (stale Loading vs. !Started
// vs. !TmuxAlive vs. a persistent capture error) can be identified from the log.
func logPreviewFallback(instance *session.Instance, reason string, err error) {
	if instance == nil || !previewFallbackLog.ShouldLog() {
		return
	}
	log.InfoLog.Printf(
		"preview fallback (%s): title=%q status=%d started=%t tmuxAlive=%t err=%v",
		reason, instance.Title, instance.GetStatus(), instance.Started(), instance.TmuxAlive(), err,
	)
}

// previewPaneStyle reads the active theme at render time.
func previewPaneStyle() lipgloss.Style { return theme.Current().FgStyle() }

// scrollExitFooter is the dim hint shown at the bottom of the scroll viewport,
// labeled by where the frozen content came from. "snapshot"/"transcript" is
// the important word: entering scroll mode freezes the content, so new agent
// output is invisible until the user leaves — and a transcript is the agent's
// conversation log, not the pane, so it should say so.
func scrollExitFooter(source session.ScrollbackSource) string {
	label := "snapshot"
	if source == session.ScrollbackTranscript {
		label = "transcript"
	}
	return theme.Current().DimStyle().Render("— " + label + " · ESC to resume live view")
}

// PreviewPane renders the selected instance's captured tmux pane content, with
// an optional scroll mode backed by a viewport.
type PreviewPane struct {
	width  int
	height int

	previewState previewState
	isScrolling  bool
	// scrollInstance is the instance the scroll-mode snapshot was captured from.
	// The snapshot is only meaningful for that instance: UpdateContent drops it the
	// moment it is asked to render any other instance, so a frozen capture can never
	// pin across selection changes (the "preview stuck for all sessions" bug).
	scrollInstance *session.Instance
	viewport       viewport.Model
}

type previewState struct {
	// fallback is true if the preview pane is displaying fallback text
	fallback bool
	// text is the text displayed in the preview pane
	text string
}

// NewPreviewPane returns an empty PreviewPane.
func NewPreviewPane() *PreviewPane {
	return &PreviewPane{
		viewport: viewport.New(0, 0),
	}
}

// SetSize sets the pane's render dimensions and resizes the scroll viewport to
// match.
func (p *PreviewPane) SetSize(width, maxHeight int) {
	p.width = width
	p.height = maxHeight
	p.viewport.Width = width
	p.viewport.Height = maxHeight
}

// setFallbackState sets the preview state with fallback text and a message
func (p *PreviewPane) setFallbackState(message string) {
	p.previewState = previewState{
		fallback: true,
		text:     lipgloss.JoinVertical(lipgloss.Center, FallbackBanner(), "", message),
	}
}

// UpdateContent updates the preview pane content with the tmux pane content.
//
// The splash decision is driven by what we can actually observe in the pane, not by
// the mutable Status flag: a live pane (non-empty capture) always wins, so a stale
// Loading / started value can never pin the "Setting up workspace..." splash. #28's
// status-gated splash still relied on Started()/TmuxAlive() being current; when one of
// those went stale the splash could freeze until restart. Capturing first removes that
// dependency — the moment the pane yields content, the splash is gone on the next tick.
func (p *PreviewPane) UpdateContent(instance *session.Instance) error {
	// The scroll snapshot belongs to one live instance; rendering any other (or
	// none), or the owner once paused, exits scroll mode so the live view (or the
	// right fallback) resumes immediately. Without the identity check the snapshot
	// pinned across selection changes until restart; without the pause check, scroll
	// mode survived a pause/resume and the early-return below kept the stale
	// "Session is paused" fallback on screen after resuming.
	if p.isScrolling && (instance != p.scrollInstance || instance.Paused()) {
		p.exitScrollMode()
	}
	switch {
	case instance == nil:
		p.setFallbackState("No agents running yet. Spin up a new session with 'n' to get started!")
		return nil
	case instance.Paused():
		// A direct (non-git) session has no branch to check out — show a plain resume hint.
		if instance.IsDirect() {
			p.setFallbackState("Session is paused. Press 'r' to resume.")
			return nil
		}
		p.setFallbackState(lipgloss.JoinVertical(lipgloss.Center,
			"Session is paused. Press 'r' to resume.",
			"",
			theme.Current().AttentionStyle().
				Render(fmt.Sprintf(
					"The instance can be checked out at '%s' (copied to your clipboard)",
					instance.Branch,
				)),
			theme.Current().AttentionStyle().
				Render("Switch your main repo off this branch before resuming."),
		))
		return nil
	}

	// Scroll mode: capture full scrollback into the viewport once.
	if p.isScrolling {
		if p.viewport.Height > 0 && len(p.viewport.View()) == 0 {
			if err := p.fillScrollViewport(instance); err != nil {
				logPreviewFallback(instance, "scroll capture error", err)
				return err
			}
		}
		return nil
	}

	// Normal mode.
	content, err := instance.Preview()
	if err != nil {
		// Never freeze a stale fallback (e.g. the setup splash) on a transient capture
		// error: leave previewState untouched so the last good content stays, and surface
		// the error rather than masking it.
		logPreviewFallback(instance, "capture error", err)
		return err
	}
	// Untrusted agent output: decompose font-dependent emoji clusters so the line
	// width we lay out matches what the terminal renders (see theme.SanitizeWidth).
	content = theme.SanitizeWidth(content)

	// A live pane always wins, regardless of the Status flag — this is the guarantee
	// that the splash can never pin once the session is actually producing output.
	if len(content) > 0 {
		p.previewState = previewState{fallback: false, text: content}
		return nil
	}

	// No content to show. Pick a fallback that reflects the session's real state.
	switch {
	case instance.GetStatus() == session.Loading || !instance.Started() || !instance.TmuxAlive():
		// Still coming up (or its pane isn't readable yet): show the setup splash.
		p.setFallbackState("Setting up workspace...")
		logPreviewFallback(instance, "empty pane, not ready", nil)
	default:
		// Started, live, but the pane is momentarily blank — render it blank rather than
		// reverting to the splash.
		p.previewState = previewState{fallback: false, text: content}
	}
	return nil
}

// Returns the preview pane content as a string.
func (p *PreviewPane) String() string {
	if p.width == 0 || p.height == 0 {
		return strings.Repeat("\n", p.height)
	}

	if p.previewState.fallback {
		// Calculate available height for fallback text
		availableHeight := p.height - 3 - 4 // 2 for borders, 1 for margin, 1 for padding

		// Count the number of lines in the fallback text
		fallbackLines := len(strings.Split(p.previewState.text, "\n"))

		// Calculate padding needed above and below to center the content
		totalPadding := availableHeight - fallbackLines
		topPadding := 0
		bottomPadding := 0
		if totalPadding > 0 {
			topPadding = totalPadding / 2
			bottomPadding = totalPadding - topPadding // accounts for odd numbers
		}

		// Build the centered content
		var lines []string
		if topPadding > 0 {
			lines = append(lines, strings.Repeat("\n", topPadding))
		}
		lines = append(lines, p.previewState.text)
		if bottomPadding > 0 {
			lines = append(lines, strings.Repeat("\n", bottomPadding))
		}

		// Center both vertically and horizontally
		return previewPaneStyle().
			Width(p.width).
			Align(lipgloss.Center).
			Render(strings.Join(lines, ""))
	}

	// If in copy mode, use the viewport to display scrollable content
	if p.isScrolling {
		return p.viewport.View()
	}

	// Normal mode display
	// Calculate available height accounting for border and margin
	availableHeight := p.height - 1 //  1 for ellipsis

	lines := strings.Split(p.previewState.text, "\n")

	// Truncate if we have more lines than available height
	if availableHeight > 0 {
		if len(lines) > availableHeight {
			lines = lines[:availableHeight]
			lines = append(lines, "...")
		} else {
			// Pad with empty lines to fill available height
			padding := availableHeight - len(lines)
			lines = append(lines, make([]string, padding)...)
		}
	}

	content := strings.Join(lines, "\n")
	// Clamp the rendered block to the pane box. Using .Width() here would soft-wrap
	// any captured line wider than the pane — common mid-resize, when capture-pane
	// still reflects the pane's previous (wider) size — and those extra wrapped rows
	// push the block past p.height. Since View composes the right pane against the
	// list with JoinHorizontal, an over-tall preview makes the whole frame exceed the
	// terminal height and scroll upward (then snap back once capture settles). The
	// line-count truncation above does not account for wrapping, so cap both axes:
	// MaxWidth truncates each line instead of wrapping, MaxHeight bounds the rows.
	return previewPaneStyle().MaxWidth(p.width).MaxHeight(p.height).Render(content)
}

// ScrollUp scrolls up in the viewport
func (p *PreviewPane) ScrollUp(instance *session.Instance) error {
	if instance == nil || instance.Paused() {
		return nil
	}

	if !p.isScrolling {
		// Entering scroll mode - freeze the best available scrollback (the
		// agent's transcript when supported, else the full tmux history).
		if err := p.fillScrollViewport(instance); err != nil {
			return err
		}

		// Position the viewport at the bottom initially
		p.viewport.GotoBottom()

		p.enterScrollMode(instance)
		return nil
	}

	// Already in scroll mode, just scroll the viewport
	p.viewport.LineUp(1)
	return nil
}

// ScrollDown scrolls down within an existing snapshot. From the live view it is
// a no-op: the live view already shows the bottom, and a snapshot entered at the
// bottom is indistinguishable from it while silently freezing updates — entry is
// ScrollUp's job. (It would also make the bottom-exit below an enter/exit toggle
// under a held wheel.)
func (p *PreviewPane) ScrollDown(instance *session.Instance) error {
	if instance == nil || instance.Paused() || !p.isScrolling {
		return nil
	}

	// A wheel-down at the very bottom leaves scroll mode and resumes the live
	// view (tmux copy-mode style). Entering calls GotoBottom(), so a wheel-down
	// right after an accidental entry self-heals.
	if p.viewport.AtBottom() {
		return p.ResetToNormalMode(instance)
	}
	p.viewport.LineDown(1)
	return nil
}

// transcriptPaneDivider is the dim rule separating rendered transcript history
// from the frozen capture of the current screen below it.
func transcriptPaneDivider(width int) string {
	const label = "── current screen "
	rule := label
	if pad := width - lipgloss.Width(label); pad > 0 {
		rule += strings.Repeat("─", pad)
	}
	return theme.Current().DimStyle().Render(rule)
}

// fillScrollViewport loads the instance's scrollback into the viewport with
// the source-labeled exit footer. Both scroll-mode fill paths (ScrollUp entry
// and UpdateContent's lazy refill) go through here so they can never disagree
// on source, sanitization, or footer.
//
// A transcript snapshot is anchored on a frozen capture of the current screen:
// the transcript's rendered tail lags the pane (the in-progress turn is not in
// the JSONL yet, and Lean rendering skips thinking/tool output), so entering
// at the bare transcript bottom visibly "jumped" to older content. With the
// screen capture at the bottom, entry is seamless — the snapshot's tail is
// exactly what the live view showed — and history continues above the divider.
// The last completed message can appear twice (pane-rendered below the
// divider, transcript-rendered above); that redundancy is the price of the
// seamless anchor.
func (p *PreviewPane) fillScrollViewport(instance *session.Instance) error {
	content, source, err := instance.ScrollbackContent(p.width)
	if err != nil {
		return err
	}
	if source == session.ScrollbackTranscript {
		if pane, perr := instance.Preview(); perr == nil && strings.TrimSpace(pane) != "" {
			content = lipgloss.JoinVertical(lipgloss.Left,
				content,
				transcriptPaneDivider(p.width),
				strings.TrimRight(pane, "\n"),
			)
		}
	}
	// Untrusted agent output: decompose font-dependent emoji clusters so the
	// laid-out width matches what the terminal renders (see theme.SanitizeWidth).
	content = theme.SanitizeWidth(content)
	p.viewport.SetContent(lipgloss.JoinVertical(lipgloss.Left, content, scrollExitFooter(source)))
	return nil
}

// enterScrollMode flags the pane as showing a frozen snapshot of instance, and
// exitScrollMode returns it to the live per-tick view. The pair keeps isScrolling
// and the snapshot's owning instance in lockstep — scroll mode must never outlive
// the instance it captured.
func (p *PreviewPane) enterScrollMode(instance *session.Instance) {
	p.isScrolling = true
	p.scrollInstance = instance
}

func (p *PreviewPane) exitScrollMode() {
	p.isScrolling = false
	p.scrollInstance = nil
	p.viewport.SetContent("")
	p.viewport.GotoTop()
}

// ResetToNormalMode exits scroll mode and returns to normal mode. Leaving scroll
// mode is unconditional — refusing for a nil or paused instance used to latch the
// snapshot with no exit besides restarting the app. Only the immediate live
// re-capture needs a usable instance; otherwise the next UpdateContent tick picks
// the right fallback.
func (p *PreviewPane) ResetToNormalMode(instance *session.Instance) error {
	if !p.isScrolling {
		return nil
	}
	p.exitScrollMode()

	if instance == nil || instance.Paused() {
		return nil
	}

	// Immediately update content instead of waiting for next UpdateContent call.
	// Replace the whole state (not just text): a leftover fallback=true would render
	// the live capture through the centered-fallback layout for a tick. Sanitize for
	// the same reason UpdateContent does — captured width must match rendered width.
	content, err := instance.Preview()
	if err != nil {
		return err
	}
	p.previewState = previewState{fallback: false, text: theme.SanitizeWidth(content)}
	return nil
}
