package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// previewFallbackLog rate-limits the diagnostic emitted whenever the preview falls
// back to the "setting up" splash (or a capture error), so a genuinely stuck session
// is recorded without flooding the log on every 100ms tick. It is the evidence trail
// for the still-coming-up vs. stale-readiness question (see UpdateContent).
var previewFallbackLog = log.NewEvery(5 * time.Second)

// logPreviewFallback records the instance's observable readiness signals when the
// preview cannot show live content, so the lying signal (stale Loading vs. !Started
// vs. a pane that has never captured) can be identified from the log.
//
// It reports frame liveness from the cached capture rather than calling
// TmuxAlive(): this runs on the update thread, and that probe was a has-session
// subprocess fired from a *logging* helper on every fallback tick (#380).
func logPreviewFallback(instance *session.Instance, reason string, err error) {
	if instance == nil || !previewFallbackLog.ShouldLog() {
		return
	}
	_, frameAt, captured := instance.PaneFrame()
	log.InfoLog.Printf(
		"preview fallback (%s): title=%q status=%d started=%t captured=%t frameAt=%s err=%v",
		reason, instance.Title, instance.GetStatus(), instance.Started(), captured,
		frameAt.Format(time.RFC3339), err,
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

// previewStaleAfter is how overdue a frame must be before the pane says so.
//
// It is ~12 missed frames at the 100ms capture cadence — two orders of magnitude
// above the measured capture cost (p50 7ms, max 11ms against a live 11-session
// server), so ordinary load never blinks it at the user, and ~8.8s ahead of the
// tmux operation timeout, so a wedged server is announced long before the
// subprocess gives up. Marking a frozen pane is the whole point: stale-but-marked
// beats fresh-but-frozen, but *silently* stale is worse than both — a wedged
// server would be indistinguishable from an idle agent.
const previewStaleAfter = 1200 * time.Millisecond

// staleMarker is the dim overdue notice, styled like scrollExitFooter so every
// "what you see is not live" message on this pane reads the same way.
//
// It stays silent in the modes that already say so themselves (scroll and hint
// mode carry their own footer/overlay), in a fallback (there is no frame to be
// stale), and while nothing has been stamped at all — the last of which is what
// makes the marker invisible to every pane a test builds by hand.
func (p *PreviewPane) staleMarker(now time.Time) string {
	if p.isScrolling || p.hintContent != "" || p.previewState.fallback || p.frameAt.IsZero() {
		return ""
	}
	age := now.Sub(p.freshFrom())
	if age < previewStaleAfter {
		return ""
	}
	return theme.Current().DimStyle().Render(fmt.Sprintf("— stale %ds", int(age.Seconds())))
}

// NoteTargetChange restamps freshness because the pane was pointed somewhere new
// (a different session, or back from a tab that captures nothing). Without it,
// returning to the preview tab would flash the age of the frame it left behind
// before the first new capture lands — reporting a real number about the wrong
// question.
func (p *PreviewPane) NoteTargetChange(now time.Time) { p.targetChangedAt = now }

// stampRight lays marker flush against the right edge of a width-wide row,
// truncating base to make room. It overwrites a row that already exists (the one
// holding the truncation ellipsis or its padding) rather than appending one, so
// the marker cannot change the block's height — the pane must never inflate the
// frame, or View's JoinHorizontal pushes the whole layout past the terminal.
func stampRight(width int, base, marker string) string {
	if width <= 0 || marker == "" {
		return base
	}
	markerWidth := xansi.StringWidth(marker)
	if markerWidth >= width {
		return marker
	}
	// Truncate ANSI-aware: pane rows carry the agent's own escape sequences
	// (capture-pane -e), and a byte-wise cut would slice one in half.
	room := width - markerWidth - 1 // one column of breathing space
	base = xansi.Truncate(base, room, "")
	pad := width - markerWidth - xansi.StringWidth(base)
	return base + strings.Repeat(" ", max(0, pad)) + marker
}

// PreviewPane renders the selected instance's captured tmux pane content, with
// an optional scroll mode backed by a viewport.
type PreviewPane struct {
	width  int
	height int

	// splashFrame is the current animation frame for the idle splash
	// field, pushed from the app's 60fps splash tick via SetSplashFrame.
	splashFrame  int
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

	// frameAt is the capture stamp of the frame on screen, and targetChangedAt the
	// last time the pane was pointed somewhere new (another session, or back from a
	// tab that captures nothing). Freshness is the LATER of the two — see freshFrom:
	// a target with no capture yet is new, not stale, and keeping them as separate
	// fields is what stops the per-tick frame stamp from immediately overwriting the
	// target change (they are written by different callers at different rates).
	// A zero frameAt means no frame has ever been applied, which is what keeps the
	// marker off every pane a test builds by hand.
	frameAt         time.Time
	targetChangedAt time.Time
}

// freshFrom is the moment the pane last had a good reason to consider itself
// current. staleMarker measures the overdue age from here.
func (p *PreviewPane) freshFrom() time.Time {
	if p.targetChangedAt.After(p.frameAt) {
		return p.targetChangedAt
	}
	return p.frameAt
}

type previewState struct {
	// fallback is true if the preview pane is displaying fallback text
	fallback bool
	// splash is true only for the idle empty screen (no agents): String() then
	// renders the animated splash field behind the wordmark. Implies fallback.
	splash bool
	// splashMessage is the onboarding line composited below the wordmark in the
	// splash. Kept separate from text so it can be overlaid at its own width
	// (the field then hugs the narrower wordmark, not the wider message).
	splashMessage string
	// lines holds the fallback message as separate, uncomposed lines while
	// fallback is true. String() lays them out, because String() is the only
	// place the pane's width is known and current: a block composed at set time
	// is already stale by the next resize, and composing without a width at all
	// is what made the paused view unreadable on a narrow pane (#355).
	lines []string
	// text is the text displayed in the preview pane
	text string
}

// NewPreviewPane returns an empty PreviewPane.
func NewPreviewPane() *PreviewPane {
	return &PreviewPane{
		viewport: viewport.New(),
	}
}

// SetSize sets the pane's render dimensions and resizes the scroll viewport to
// match.
func (p *PreviewPane) SetSize(width, maxHeight int) {
	p.width = width
	p.height = maxHeight
	p.viewport.SetWidth(width)
	p.viewport.SetHeight(maxHeight)
}

// SetSplashFrame stores the current splash animation frame, pushed from the
// app's 60fps splash tick. It only affects the idle-splash render in String().
func (p *PreviewPane) SetSplashFrame(n int) { p.splashFrame = n }

// setFallbackState sets the preview state to show the given message lines over
// the wordmark. The lines are stored, not composed: String() lays them out
// against the live pane width (see fallbackBlock).
func (p *PreviewPane) setFallbackState(lines ...string) {
	p.previewState = previewState{fallback: true, lines: lines}
}

// pausedResumeHint is the paused pane's one-line "how do I get this back"
// sentence. Both paused branches render it, and it names the resume key through
// the registry: a literal would be a sentence telling the user to press a key
// that, once they have rebound resume, moves the selection instead.
func pausedResumeHint() string {
	return fmt.Sprintf("Session is paused. Press '%s' to resume.", keys.LabelOf(keys.KeyResume))
}

// setSplashState is setFallbackState for the idle empty screen (no agents),
// additionally flagging the splash so String() renders the animated splash
// field behind the wordmark. Every other empty state keeps the plain fallback.
func (p *PreviewPane) setSplashState(message string) {
	p.setFallbackState(message)
	p.previewState.splash = true
	p.previewState.splashMessage = message
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
		p.setSplashState(fmt.Sprintf(
			"No agents running yet. Spin up a new session with '%s' to get started!",
			keys.LabelOf(keys.KeyNew)))
		return nil
	case instance.Paused():
		// Before every paused hint below, because a resume runs the setup script while
		// the instance is STILL Paused: Resume re-materializes the worktree, runs the
		// script, and only then flips to Running (session/pause.go). Without this the
		// pane says "press 'r' to resume" for the two minutes an `npm ci` is running,
		// having just been pressed — the one moment the phase is the only thing worth
		// saying.
		if phase := instance.SetupPhase(); phase != "" {
			p.setFallbackState(phase)
			return nil
		}
		// A direct (non-git) session has no branch to check out — show a plain resume hint.
		if instance.IsDirect() {
			p.setFallbackState(pausedResumeHint())
			return nil
		}
		// Nothing copies on pause (#173 dropped that unsolicited write), so the branch
		// is offered with the key that copies it on request. The lines are passed
		// separately rather than pre-joined so fallbackBlock can wrap each one against
		// the pane: the branch is interpolated and unbounded, and this block's fixed
		// text already outruns the wordmark on its own ("Switch your main repo off
		// this branch before resuming." is 54 cols against a 48-col banner).
		p.setFallbackState(
			pausedResumeHint(),
			"",
			theme.Current().AttentionStyle().
				Render(fmt.Sprintf(
					"The instance can be checked out at '%s'",
					instance.Branch,
				)),
			theme.Current().AttentionStyle().
				Render(fmt.Sprintf("(press '%s' to copy)", keys.LabelOf(keys.KeyCopyBranch))),
			theme.Current().AttentionStyle().
				Render("Switch your main repo off this branch before resuming."),
		)
		return nil
	}

	// Scroll mode: capture full scrollback into the viewport once.
	if p.isScrolling {
		if p.viewport.Height() > 0 && len(p.viewport.View()) == 0 {
			if err := p.fillScrollViewport(instance); err != nil {
				logPreviewFallback(instance, "scroll capture error", err)
				return err
			}
		}
		return nil
	}

	// Normal mode. The frame comes from the cache the app's background capture
	// fills (session/paneframe.go), never from a capture on this thread: this
	// function runs inside Update, so a tmux round trip here delayed every repaint
	// and every keypress, and a wedged server froze the app outright (#380).
	content, frameAt, captured := instance.PaneFrame()
	p.frameAt = frameAt

	// A live pane always wins, regardless of the Status flag — this is the guarantee
	// that the splash can never pin once the session is actually producing output.
	if len(content) > 0 {
		p.previewState = previewState{fallback: false, text: content}
		return nil
	}

	// No content to show. Pick a fallback that reflects the session's real state.
	switch {
	case !captured || instance.GetStatus() == session.Loading || !instance.Started():
		// Still coming up, or its pane has never yielded a readable frame: show the
		// setup splash. "No capture has ever succeeded" is the same statement the old
		// TmuxAlive() probe made here, without spending a subprocess to make it.
		// Named when the per-repo setup script is what the wait is actually spent on
		// (#389). "Setting up workspace..." is true of every pre-agent session and so
		// says nothing about the one that will sit here for two minutes installing
		// dependencies; the phase is the only thing that distinguishes them.
		if phase := instance.SetupPhase(); phase != "" {
			p.setFallbackState(phase)
		} else {
			p.setFallbackState("Setting up workspace...")
		}
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

	// splashEnabled is checked here rather than inside splashScene because the
	// screensaver renders through that same function and is out of the setting's
	// scope (#316). Turning it off falls through to the fallback arm below — the
	// plain centered wordmark this pane already shows below the size floor.
	if p.previewState.splash && splashEnabled() && splashFits(p.width, p.height) {
		return splashScene(p.width, p.height, p.splashFrame, p.previewState.splashMessage)
	}

	if p.previewState.fallback {
		// Composed here, against the live width, rather than back where the state was
		// set (#355). Center it in the pane's exact box, the same way the diff pane
		// centers its placeholders. (The hand-rolled padding loop this replaces guessed
		// at chrome offsets that no longer exist and sat the text slightly high.)
		// centerInBox still clamps: fallbackBlock already fits the block to the pane, so
		// the clamp is now a backstop rather than the thing doing the fitting, and it
		// keeps the #251 guarantee that this pane cannot inflate the frame.
		return centerInBox(p.width, p.height,
			previewPaneStyle().Render(fallbackBlock(p.width, p.height, p.previewState.lines...)))
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

	// An overdue frame says so on the row it already has — the ellipsis row when the
	// capture was truncated, its padding otherwise. Overwriting keeps the row count
	// identical whether or not the marker is showing (see stampRight).
	if marker := p.staleMarker(time.Now()); marker != "" && len(lines) > 0 {
		lines[len(lines)-1] = stampRight(p.width, lines[len(lines)-1], marker)
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
	p.viewport.ScrollUp(lines)
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
	p.viewport.ScrollDown(lines)
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

// IsScrolling reports whether the preview pane is in scroll mode. It mirrors
// TerminalPane.IsScrolling so the tabbed window can query both panes the same way
// instead of reaching into this pane's private field.
func (p *PreviewPane) IsScrolling() bool { return p.isScrolling }

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
