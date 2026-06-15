package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
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

	// hintContent, when non-empty, is hint mode's decorated rendering of
	// hintInstance's frozen capture; String() shows it instead of the live
	// text and UpdateContent freezes, mirroring the scroll snapshot's
	// ownership rules (one owning instance, dropped the moment any other
	// instance — or the owner once paused — is rendered).
	hintContent  string
	hintInstance *session.Instance
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
	// The hint overlay belongs to one live instance, exactly like the scroll
	// snapshot above: rendering any other instance, or the owner once paused,
	// drops it. While it is valid the pane is frozen, so the per-tick capture
	// cannot repaint over the hints.
	if p.InHintMode() {
		if instance != p.hintInstance || instance.Paused() {
			p.ClearHintOverlay()
		} else {
			return nil
		}
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
		// Center the fallback in the pane's exact box, the same way the diff
		// pane centers its placeholders. (The hand-rolled padding loop this
		// replaces guessed at chrome offsets that no longer exist and sat the
		// text slightly high.) Clamp both axes like the normal branch below:
		// lipgloss.Place does not clip oversize content, so a fallback line
		// wider than the pane (the empty-state message on a narrow terminal)
		// would widen the whole frame past the terminal — invisibly, since the
		// renderer truncates output lines, but every centered overlay then
		// computes its position against the inflated width and lands off-center.
		return lipgloss.NewStyle().MaxWidth(p.width).MaxHeight(p.height).Render(
			lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center,
				previewPaneStyle().Render(p.previewState.text)))
	}

	// Hint mode: show the frozen decorated frame, clamped exactly like the
	// live view so the layout cannot shift on entry.
	if p.hintContent != "" {
		return previewPaneStyle().MaxWidth(p.width).MaxHeight(p.height).Render(p.hintContent)
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

// ScrollUp enters scroll mode (freezing the snapshot at its bottom) or, when
// already scrolling, moves the viewport up by lines. The entry step ignores
// lines — it always lands at the bottom — so the count only governs in-scroll
// granularity (a wheel notch moves several lines, a key one).
func (p *PreviewPane) ScrollUp(instance *session.Instance, lines int) error {
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
	p.viewport.LineUp(lines)
	return nil
}

// ScrollDown scrolls down within an existing snapshot. From the live view it is
// a no-op: the live view already shows the bottom, and a snapshot entered at the
// bottom is indistinguishable from it while silently freezing updates — entry is
// ScrollUp's job. (It would also make the bottom-exit below an enter/exit toggle
// under a held wheel.)
func (p *PreviewPane) ScrollDown(instance *session.Instance, lines int) error {
	if instance == nil || instance.Paused() || !p.isScrolling {
		return nil
	}

	// A wheel-down at the very bottom leaves scroll mode and resumes the live
	// view (tmux copy-mode style). Entering calls GotoBottom(), so a wheel-down
	// right after an accidental entry self-heals.
	if p.viewport.AtBottom() {
		return p.ResetToNormalMode(instance)
	}
	p.viewport.LineDown(lines)
	return nil
}

// fillScrollViewport loads the instance's scrollback into the viewport with
// the source-labeled exit footer. Both scroll-mode fill paths (ScrollUp entry
// and UpdateContent's lazy refill) go through here so they can never disagree
// on source, sanitization, or footer.
//
// The rendered transcript already holds the whole conversation, so it is the
// scrollback on its own. We splice the live screen capture onto its tail *only*
// when TrimOverlap finds a confident, pane-top-anchored overlap — then the seam
// is seamless and deduplicated, anchoring the bottom on exactly what the live
// view showed. Without that overlap the two are misaligned (most often the last
// turn is still streaming, so the capture sits mid-message while the JSONL holds
// the finished turn): stacking them under a divider would render the shared
// region twice, so we show the transcript alone. Nothing is lost — the capture's
// content is already in the transcript — and the bottom simply rests on the last
// completed message instead of the in-flight frame.
func (p *PreviewPane) fillScrollViewport(instance *session.Instance) error {
	content, source, err := instance.ScrollbackContent(p.width)
	if err != nil {
		return err
	}
	if source == session.ScrollbackTranscript {
		if pane, perr := instance.Preview(); perr == nil && strings.TrimSpace(pane) != "" {
			paneTrim := strings.TrimRight(pane, "\n")
			if trimmed, ok := transcript.TrimOverlap(content, paneTrim); ok {
				// When the whole transcript was already on screen, the pane is the
				// entire scrollback — joining with an empty trimmed half would only
				// prepend a stray blank line.
				if trimmed == "" {
					content = paneTrim
				} else {
					content = lipgloss.JoinVertical(lipgloss.Left, trimmed, paneTrim)
				}
			}
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

// LiveContent returns the text the pane is currently rendering live, and
// whether hint mode may act on it: no fallback splash, not scrolling, not
// already in hint mode, and non-empty.
func (p *PreviewPane) LiveContent() (string, bool) {
	if p.previewState.fallback || p.isScrolling || p.hintContent != "" {
		return "", false
	}
	return p.previewState.text, p.previewState.text != ""
}

// SetHintOverlay enters (or refreshes) hint mode: content is the decorated
// frame shown frozen in place of instance's live capture.
func (p *PreviewPane) SetHintOverlay(instance *session.Instance, content string) {
	p.hintInstance = instance
	p.hintContent = content
}

// ClearHintOverlay leaves hint mode; the next UpdateContent tick resumes the
// live view.
func (p *PreviewPane) ClearHintOverlay() {
	p.hintInstance = nil
	p.hintContent = ""
}

// InHintMode reports whether a hint overlay is currently displayed.
func (p *PreviewPane) InHintMode() bool { return p.hintContent != "" }

// ScrollContent returns the text currently visible in the scroll viewport for
// hint mode. Returns "", false when not in scroll mode or when a hint overlay
// is already active (re-entering would be a no-op).
func (p *PreviewPane) ScrollContent() (string, bool) {
	if !p.isScrolling || p.hintContent != "" {
		return "", false
	}
	v := p.viewport.View()
	return v, v != ""
}
