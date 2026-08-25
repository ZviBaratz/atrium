package app

// Message handlers extracted from home.Update (app_update.go). Update stays a
// thin type-switch dispatcher: the substantial message cases delegate to the
// handleXxx methods here, mirroring how handleKeyPress delegates its per-state
// and per-action work to app_keys.go. Trivial cases remain inline in the switch.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "charm.land/bubbletea/v2"
)

func (m *home) handleDriftFound(msg driftFoundMsg) (tea.Model, tea.Cmd) {
	// Try to show the hint on the bar's reserved row. Two cases leave cmd nil and fall
	// through to the persistent-badge path below, where we record no ack so the hint
	// re-arms on a later startup instead of being silently consumed (atrium doctor
	// stays the durable surface meanwhile):
	//   - hint_bar off: the drift hint stays badge-only (#108/#438). Don't even attempt
	//     the toast — the clean-mode row is always reserved now and would otherwise
	//     carry this unsolicited flash onto the frame the user quieted.
	//   - a modal owns the screen: showMenuNotice itself returns nil (menuVisible false).
	var cmd tea.Cmd
	if m.appConfig.GetHintBar() {
		cmd = m.showMenuNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()), ui.NoticeInfo)
	}
	if cmd == nil {
		// Toast dropped (hint bar off, or a modal owns the screen). Surface the
		// drift via the persistent panel badge instead — the durable fallback
		// for users who'd otherwise never see it. Don't ack: leave it re-armed.
		if m.list != nil {
			m.list.SetDriftBadge(driftBadgeText())
		}
		return m, nil
	}
	// Shown: record the ack at each agent's current installed version so the
	// hint shows once per version. Batched into a single persist.
	acks := make(map[string]string, len(msg.agents))
	for _, r := range msg.agents {
		acks[string(r.Key)] = r.Installed
	}
	if err := m.appState.SetAckedDrift(acks); err != nil {
		log.WarningLog.Printf("failed to record drift acks: %v", err)
	}
	return m, cmd
}

// splashDefaultFPS is the empty-state splash's animation rate. The splash
// runs on its own tick, decoupled from the 100ms preview poll — at the poll
// rate (frames pushed at ~5Hz) the drift read as visibly choppy. 60 is also
// Bubble Tea's renderer cap, so a higher rate would only burn CPU. The loop
// only exists while the idle splash is actually on screen (handleSplashTick
// dies the moment it isn't), so the rate costs nothing once a session exists
// or an overlay is up.
const splashDefaultFPS = 60

// splashTickInterval resolves the splash frame interval once per process,
// honoring the dev-only ATRIUM_SPLASH_FPS override (clamped to 5–60) — a
// live A/B knob for slower terminals (e.g. over SSH) without a rebuild.
var splashTickInterval = sync.OnceValue(func() time.Duration {
	fps := splashDefaultFPS
	if s := os.Getenv("ATRIUM_SPLASH_FPS"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			fps = min(max(v, 5), 60)
		}
	}
	return time.Second / time.Duration(fps)
})

// splashTickMsg drives the dedicated splash animation loop (see armSplashTick).
type splashTickMsg struct{}

// splashAnimating reports whether a splash is on screen and allowed to move:
// the idle empty state (no sessions, default state — outside it an overlay is
// up, and motion churning behind a modal the user is reading is distracting),
// or the full-window screensaver, which is the splash regardless of how many
// sessions exist.
//
// config.SplashOff bars the idle arm only, and that is what makes the opt-out
// worth having: the loop never arms, so an idle Atrium repaints nothing rather
// than pushing 60 frames a second at a screen the panes now render as a static
// wordmark (#316). The screensaver above it is an explicit keypress and stays
// exempt — the same scope ui's pane gates keep.
func (m *home) splashAnimating() bool {
	if m.state == stateScreensaver {
		return true
	}
	return m.state == stateDefault && m.appConfig.SplashEnabled() &&
		m.list != nil && m.list.NumInstances() == 0
}

// armSplashTick starts the splash animation loop, unless one is already live
// or the splash isn't on screen. Called from the 100ms preview tick — which
// is what revives the animation (within a tick) after an overlay closes or
// the last session is killed.
func (m *home) armSplashTick() tea.Cmd {
	if m.splashTicking || !m.splashAnimating() {
		return nil
	}
	m.splashTicking = true
	return m.splashTickCmd()
}

func (m *home) splashTickCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(splashTickInterval())
		return splashTickMsg{}
	}
}

// splashTickAdvance is the clock step per tick in nominal 60fps frame units:
// exactly 1 at the default rate, proportionally more at a lower override —
// the animation covers the same distance per second however often it paints.
var splashTickAdvance = sync.OnceValue(func() float64 {
	return float64(splashTickInterval()) / float64(time.Second/splashDefaultFPS)
})

// spinnerAnimating reports whether any row is currently drawing a spinner frame.
//
// Running and Loading are the only two statuses whose glyph is the animated
// spinner (ui.stateGlyph); every other status draws a still theme glyph, and the
// menu's busy row is plain text. So when no session holds either, the 10Hz tick is
// advancing a frame counter nothing reads.
func (m *home) spinnerAnimating() bool {
	if m.list == nil {
		return false
	}
	for _, inst := range m.list.GetInstances() {
		switch inst.GetStatus() {
		case session.Running, session.Loading:
			return true
		}
	}
	return false
}

// armSpinnerTick starts the spinner loop when a row needs it, and is a no-op while
// one is already running or nothing is spinning.
//
// Same shape as armSplashTick, for the same reason: with nothing to animate, the
// loop was still delivering 10 messages a second, and Bubble Tea rebuilds the
// entire frame after every message (Program.eventLoop's render call) — measured
// at 6-9ms and 1.2-2.3MB per rebuild, so this was ~31% of an idle Atrium's render
// cost animating nothing (#546).
//
// Called from the 100ms preview tick and from the tail of applyMetadataResults.
// The tick is the one that makes this correct: it fires unconditionally, so it
// re-arms for *any* path that sets Running or Loading — and there are more than
// the poll (app_session.go's new session, app_update.go's Loading→Running, the
// optimistic flips in approveSelected and the suggestion handler), which is why
// the general self-heal is the guarantee rather than an enumeration of writers.
// applyMetadataResults is the fast path only: the poll is where a status flips
// most often, and arming there makes it immediate instead of up to 100ms late.
func (m *home) armSpinnerTick() tea.Cmd {
	if m.spinnerTicking || !m.spinnerAnimating() {
		return nil
	}
	m.spinnerTicking = true
	return m.spinner.Tick
}

// handleSplashTick advances the splash clock one tick's worth of nominal
// frames and re-arms itself — or dies (clearing splashTicking) as soon as
// the splash leaves the screen, so a parked session view or a modal never
// repaints at animation rate.
func (m *home) handleSplashTick() (tea.Model, tea.Cmd) {
	if !m.splashAnimating() {
		m.splashTicking = false
		return m, nil
	}
	m.splashClock += splashTickAdvance()
	m.splashFrame = int(m.splashClock)
	m.tabbedWindow.SetSplashFrame(m.splashFrame)
	return m, m.splashTickCmd()
}

func (m *home) handlePreviewTick(msg previewTickMsg) (tea.Model, tea.Cmd) {
	// The pane owns hint-overlay validity (a selection change or pause
	// drops it there); if it dropped, follow it back to default so keys
	// stop being captured for a vanished overlay.
	if m.state == stateHints && !m.tabbedWindow.InPreviewHintMode() {
		m.exitHintMode()
	}
	m.markSeenAfterDwell(time.Now())
	cmd := m.instanceChanged()
	return m, tea.Batch(
		cmd,
		// Revive the splash animation loop when the idle splash is (back) on
		// screen; no-op while one is already running.
		m.armSplashTick(),
		// Likewise for the row spinner, which dies whenever nothing is Running or
		// Loading. This is the self-heal: a status that starts spinning between
		// metadata sweeps lights up within one tick.
		m.armSpinnerTick(),
		// Likewise for the pane-capture chain, which dies whenever there is nothing
		// to capture — a paused or unstarted selection, the diff tab, the
		// screensaver, or a preview pane that has stopped moving. Arming with no
		// delay so the first frame back arrives within a tick rather than two; a
		// no-op while a capture is already in flight or the target is still empty.
		m.armFrameCapture(0),
		// An update notice that arrived while an overlay owned the screen
		// is buffered; deliver it as soon as the hint bar is back.
		m.flushPendingUpdateNotice(),
		// Likewise for agent detection notices.
		m.flushPendingAgentNotice(),
		// Likewise for "what's new" notes buffered behind another overlay.
		m.flushPendingReleaseNotes(),
		// Likewise for a crash-at-launch modal buffered behind another overlay.
		m.flushPendingLaunchCrash(),
		// Likewise for the custom_commands entries validation refused at startup.
		m.flushCustomCommandProblems(),
		// Likewise for the repo_scripts entries it refused (#389).
		m.flushRepoScriptProblems(),
		// Likewise for the keybindings overrides it refused (#376).
		m.flushKeybindingProblems(),
		// Likewise for the user theme files it refused (#813).
		m.flushThemeProblems(),
		// Likewise for a per-repo setup script that failed. Unlike the others this
		// reads the fleet rather than a buffer — see flushSetupFailures.
		m.flushSetupFailures(),
		// Likewise for the startup recoveries the host session budget deferred.
		m.flushDeferredRecovery(),
		// Likewise for what a spent `atrium new` request left stranded (#731, #732).
		m.flushCreateDisclosures(),
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
	)
}

func (m *home) handleSmartDispatchDone(msg smartDispatchDoneMsg) (tea.Model, tea.Cmd) {
	// Drop a result the user has moved past: the exact form it was launched for is no
	// longer the active overlay (cancelled, submitted, or a different form opened).
	if msg.form == nil || m.textInputOverlay != msg.form {
		return m, nil
	}
	m.textInputOverlay.SetProjectHint("")
	if msg.err != nil {
		return m, nil // routing failed; leave the form as the user left it
	}
	// Upgrade the title independently of routing: a confident match wants only a
	// better title, and even an unrouted result may carry a usable one. Replace the
	// deterministic placeholder only while the user hasn't typed their own. Do this
	// before any re-point so the retarget's async branch check below validates the
	// final title (not the placeholder) against the routed repo.
	if msg.title != "" && m.textInputOverlay.GetTitle() == m.smartDispatchSeededTitle {
		m.textInputOverlay.SetTitleValue(msg.title)
		m.refreshTitleError()
	}
	var cmds []tea.Cmd
	// Re-point the project only when the router found one and the user hasn't moved
	// the picker themselves (still on the contextual default the form opened with).
	// A confident match already sits on its project, so this is a no-op there.
	if msg.project != "" {
		if path := m.candidatePathForBasename(msg.project); path != "" &&
			m.textInputOverlay.GetSelectedPath() == m.newSessionPath && path != m.newSessionPath {
			m.textInputOverlay.SelectPath(path)
			cmds = append(cmds, m.retargetNewSession(path))
		}
	}
	return m, tea.Batch(cmds...)
}

// dividerGrab is how many columns on each side of the list/preview seam count as
// grabbing the divider, so the user doesn't have to land the exact border column.
const dividerGrab = 1

// mouseAction is the gesture kind handleMouse routes on.
//
// Bubble Tea v2 splits mouse input into one message type per kind
// (MouseClickMsg / MouseReleaseMsg / MouseMotionMsg / MouseWheelMsg) where v1
// carried a flat Action field. handleMouse is a single ~180-line state machine
// over press/motion/release — a divider drag, a double-click window, wheel
// routing by hover — so it is flattened back rather than split four ways: the
// gesture logic is what has the tests, and re-shaping it into four entry points
// would have moved risk into the one part of this migration that has no
// compile-time check.
type mouseAction int

const (
	mousePress mouseAction = iota
	mouseRelease
	mouseMotion
)

// mouseGesture is the (action, button, position) shape handleMouse was written
// against, reconstructed from whichever v2 message arrived.
type mouseGesture struct {
	action mouseAction
	button tea.MouseButton
	x, y   int
}

// newMouseGesture flattens a v2 mouse message.
//
// Wheel deltas map to mousePress deliberately, preserving v1's encoding (a wheel
// tick was a press whose Button was MouseWheelUp/Down). handleMouse's early
// return drops everything that is not a press before the wheel-routing block, so
// classifying the wheel as anything else would silently disable scrolling — and
// the screensaver's "a nudged mouse must not dismiss it" guard reads the same
// press/button pair.
func newMouseGesture(msg tea.MouseMsg) mouseGesture {
	mouse := msg.Mouse()
	g := mouseGesture{action: mousePress, button: mouse.Button, x: mouse.X, y: mouse.Y}
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		g.action = mouseRelease
	case tea.MouseMotionMsg:
		g.action = mouseMotion
	}
	return g
}

func (m *home) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	g := newMouseGesture(msg)
	// The screensaver dismisses on a click, mirroring the any-key exit; wheel
	// and motion events are ignored so a nudged mouse doesn't tear it down.
	if m.state == stateScreensaver {
		if g.action == mousePress {
			switch g.button {
			case tea.MouseWheelUp, tea.MouseWheelDown,
				tea.MouseWheelLeft, tea.MouseWheelRight:
				// Wheel deltas arrive as presses; not a deliberate wake.
			default:
				m.dismissScreensaver()
			}
		}
		return m, nil
	}

	// A live drag of the list/preview seam owns mouse events until release. A drag
	// is a single press→motion→release gesture that only makes sense in the default
	// state, so abandon a stale one instead of trapping later events: if an overlay
	// took the screen mid-drag, or a fresh press arrives (the matching release was
	// dropped — e.g. the button came up off-screen, which the terminal never
	// reports), clear the flag and fall through to normal handling of this event.
	// Without this a lost release would swallow every click and let the next
	// press-drag anywhere snap the divider to the cursor.
	if m.draggingDivider {
		if m.state != stateDefault || g.action == mousePress {
			m.draggingDivider = false
		} else {
			switch g.action {
			case mouseMotion:
				if m.windowWidth <= 0 {
					return m, nil
				}
				m.listRatio = config.ClampListRatio(float64(g.x) / float64(m.windowWidth))
				// Reflow the panes so the divider tracks the cursor live. The pane
				// content (tmux/diff capture) is intentionally left to the periodic
				// preview tick rather than re-fetched here: a full instanceChanged()
				// would re-capture on every motion event of the drag. recomputeLayout
				// re-clamps the already-captured text to the new width in the meantime.
				m.recomputeLayout()
				return m, nil
			case mouseRelease:
				m.draggingDivider = false
				// A drag is a manual split, so it is a custom override of the active
				// preset (like < / >): persist preset+override+ratio together so it
				// complements the preset cycle rather than resetting on relaunch.
				m.layoutCustom = true
				if err := m.appState.SetLayout(m.currentPreset().name, true, m.listRatio); err != nil {
					return m, m.handleError(err)
				}
				// One content refresh at the end of the gesture, now that the width
				// has settled, so the preview/diff aren't left a tick stale.
				return m, m.instanceChanged()
			default:
				return m, nil
			}
		}
	}
	// Begin a divider drag when the left button presses on (or adjacent to) the
	// seam between the panes. Default state only; the seam column is listWidth and
	// the grab is bounded to the pane rows so a press on the safety banner above or
	// the hint/error strip below them doesn't start a drag. This runs before the
	// press-only early return and the row/tab click logic, so a seam press starts a
	// drag instead of selecting the row behind it.
	bannerH := m.topBannerHeight()
	if g.button == tea.MouseLeft && g.action == mousePress &&
		m.state == stateDefault && m.windowWidth > 0 &&
		g.y >= bannerH && g.y < bannerH+m.paneContentHeight() && !m.listHidden() {
		listWidth := m.computeRegions(m.windowWidth).list
		if g.x >= listWidth-dividerGrab && g.x <= listWidth+dividerGrab {
			m.draggingDivider = true
			return m, nil
		}
	}
	if g.action != mousePress {
		return m, nil
	}
	// Modal text overlays (help / info) own the screen: the wheel scrolls
	// an overflowing cheatsheet wherever it hovers, and a left-click
	// outside the box dismisses — mirroring the keyboard semantics
	// (scroll keys scroll, anything else closes). Clicks inside the box
	// are inert so a stray selection click doesn't tear the dialog down.
	if (m.state == stateHelp || m.state == stateInfo) && m.textOverlay != nil {
		switch g.button {
		case tea.MouseWheelUp:
			m.textOverlay.ScrollBy(-1)
		case tea.MouseWheelDown:
			m.textOverlay.ScrollBy(1)
		case tea.MouseLeft:
			if !m.textOverlayContains(g.x, g.y) {
				return m.closeTextOverlay()
			}
		}
		return m, nil
	}
	// Mouse wheel is routed by what it hovers, only in the default state
	// (overlays own the screen otherwise, mirroring the left-click gate
	// below). Over the session list it moves the selection like ↑/↓; over
	// the right tabbed pane it scrolls the active tab; anywhere else (menu /
	// hint bar / error rows) it is ignored. Zones are resolved against the
	// frame scanned in View(); before the first scan both InBounds checks
	// return false, so the wheel does nothing.
	if g.button == tea.MouseWheelDown || g.button == tea.MouseWheelUp {
		if m.state != stateDefault {
			return m, nil
		}
		// Over the list: move the selection, regardless of the selected
		// instance's state (paused / nil), exactly like the keyboard paths.
		if m.list.InPanelBounds(msg) {
			if g.button == tea.MouseWheelUp {
				m.list.Up()
			} else {
				m.list.Down()
			}
			return m, m.instanceChanged()
		}
		// Over the right tabbed pane: scroll the active tab. A nil or
		// paused selection has nothing to scroll.
		if m.tabbedWindow.InBounds(msg) {
			selected := m.list.GetSelectedInstance()
			if selected == nil || selected.Paused() {
				return m, nil
			}
			if g.button == tea.MouseWheelUp {
				m.tabbedWindow.ScrollUp(wheelScrollLines)
			} else {
				m.tabbedWindow.ScrollDown(wheelScrollLines)
			}
			return m, nil
		}
		return m, nil
	}
	// A left-click on a hint-bar entry mirrors pressing its key, in the states
	// hintBarClickState admits. The diff-comment mode bar registers the same
	// zone-marked entries as the three bars the gate accepts, and its clicks
	// are dead (#852). The resolved key is re-injected through handleKeyPress
	// so it runs the exact same dispatch (state routing + guards) as the
	// keypress it advertises: nothing here becomes mouse-only.
	if g.button == tea.MouseLeft && m.hintBarClickState() {
		if k, ok := m.menu.KeyAtZone(msg); ok {
			if kmsg, ok := synthKeyMsg(k); ok {
				return m.handleKeyPress(kmsg)
			}
			return m, nil // a marked entry with no synthesizable key: no-op
		}
	}
	// Left-click selects a session row, switches the active tab, or (on a quick
	// second click of the same row) attaches. Only in the default state — when
	// an overlay is up the rows behind it still have recorded bounds, so a click
	// there must be ignored. Click regions are resolved against the frame
	// scanned in View().
	if g.button == tea.MouseLeft && m.state == stateDefault {
		if inst := m.list.InstanceAtZone(msg); inst != nil {
			m.list.SelectInstance(inst)
			// A second click on the same row within doubleClickWindow attaches,
			// mirroring Enter, via the tea.Exec attach path (attachExec). The first
			// click already selected the row, so it is the current selection.
			now := time.Now()
			if m.lastClickInstance == inst && now.Sub(m.lastClickAt) <= doubleClickWindow {
				m.lastClickInstance = nil
				if inst.Paused() || inst.GetStatus() == session.Loading || !inst.TmuxAlive() {
					return m, m.instanceChanged()
				}
				if m.tabbedWindow.IsInTerminalTab() {
					return m, m.attachExec(m.tabbedWindow.AttachTerminal, nil)
				}
				// Attach inst directly (not m.list.Attach, which re-reads the
				// selected index when the deferred command runs and could target a
				// row the selection moved to in between); killTarget carries it for
				// the ctrl-x in-session kill flow. Matches the sibling/auto-open
				// attach paths, which also bind the instance up front.
				return m, m.attachExec(inst.Attach, inst)
			}
			m.lastClickInstance = inst
			m.lastClickAt = now
			return m, m.instanceChanged()
		}
		// A click on a repo-group header toggles its fold, mirroring ←/→.
		// Persist the new collapsed set exactly like the keyboard paths do.
		if key, ok := m.list.HeaderAtZone(msg); ok {
			// Mirroring ←/→ includes their filter guard: a live filter overrides the
			// fold in the render, so this click would rewrite the persisted set with
			// the header standing still (#339). Refuse and name the filter instead.
			if m.list.Filtering() {
				return m, m.handleInfoNotice(filterFoldNotice)
			}
			if m.list.ClickHeader(key) {
				if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
					return m, m.handleError(err)
				}
				return m, m.instanceChanged()
			}
			return m, nil
		}
		if idx, ok := m.tabbedWindow.TabAtZone(msg); ok {
			m.tabbedWindow.SetActiveTab(idx)
			return m, m.tabChanged()
		}
	}
	return m, nil
}

// hintBarClickState reports whether the hint bar is the live surface a click
// can act on: the default view and three of the four mode bars (filter / hint /
// visual). The fourth mode bar is the exception: diff-comment renders the same
// zone-marked entries and is refused anyway, so its clicks are dead — #852
// tracks admitting it.
func (m *home) hintBarClickState() bool {
	switch m.state {
	case stateDefault, stateFilter, stateHints, stateVisual:
		return true
	default:
		return false
	}
}

// synthKeyMsg builds the tea.KeyMsg a hint-bar click re-injects to fire the
// clicked entry's key. The dispatch path keys off msg.String(), so the returned
// message must stringify back to k — which is precisely keys.ParseKey's
// contract, so the whole job is that call.
//
// It used to be a hand-written table of the chords the bars happened to show
// (enter, esc, space, ctrl+x, the shift arrows). That covered every bar key by
// luck rather than by construction, and nothing asserted it: a key the table did
// not spell reported false, and the click silently did nothing. Deriving it from
// the vocabulary instead makes the coverage total, and
// TestEveryBarKeyIsSynthesizable now asserts it.
//
// A key it can't represent still reports false, so an unrecognized entry is a
// no-op rather than a wrong action.
func synthKeyMsg(k string) (tea.KeyPressMsg, bool) {
	// v2 reports the space bar as "space" where v1 said " ", and a stale bar
	// entry must still resolve rather than silently no-op.
	if k == " " {
		k = "space"
	}
	msg, err := keys.ParseKey(k)
	if err != nil {
		return tea.KeyPressMsg{}, false
	}
	return msg, true
}

func (m *home) handleTargetValidityResult(msg targetValidityResultMsg) (tea.Model, tea.Cmd) {
	// Apply only if the result is for the still-current target, so a stale check
	// (the user has navigated on) can't clobber the indicator.
	if m.textInputOverlay != nil && msg.path == m.newSessionPath {
		m.textInputOverlay.SetTargetValidity(msg.valid, msg.direct, msg.headBranch)
		// Re-point the account picker at the new project's auto-routed account so the
		// displayed selection tracks the target. No-op once the user has overridden it.
		m.textInputOverlay.PreselectAccount(msg.accountName)
		// Re-scope the duplicate-title check to the confirmed target's group and
		// re-run it: the same title may be free in one repo and taken in another.
		if msg.groupKey != "" {
			m.newSessionGroup = msg.groupKey
			m.refreshTitleError()
		}
		// A confirmed git target gets one background fetch per form-session, so its
		// branch list reflects current remote refs. The verdict (not the path change)
		// is the trigger: filesystem browsing through non-repos never fetches.
		if msg.valid && !msg.direct && !m.fetchedPaths[msg.path] {
			if m.fetchedPaths == nil {
				m.fetchedPaths = make(map[string]bool)
			}
			m.fetchedPaths[msg.path] = true
			return m, m.runBranchFetch(msg.path)
		}
	}
	return m, nil
}

func (m *home) handleAttachFinished(msg attachFinishedMsg) (tea.Model, tea.Cmd) {
	// A tea.Exec terminal attach returned (the user detached, or it failed to
	// start). tea.Exec's RestoreTerminal only does a soft (diff-cache) repaint,
	// which can leave the reclaimed frame blank/stale after tmux hands the terminal
	// back; every path that returns to the list therefore forces a hard repaint via
	// repaintAfterAttach (see its doc). Refine the layout and selection-derived
	// panes from here.
	m.state = stateDefault
	// The renderer restored the title and progress bar it cleared for the attach, so
	// this is not a re-assert — it refreshes the counts, which may have moved while
	// the loop was suspended, a tick earlier than the poll would.
	m.refreshOSChrome(false)
	if msg.err != nil {
		// A failed sibling-cycle re-attach still carries keeper losses from the
		// previous attach (attachExecCarry seeds them before Run can fail); surface
		// them alongside the attach error, honoring the promise below that only the
		// kill and AttachExitError paths stay log-only.
		if len(msg.keeperErrs) > 0 {
			return m, m.repaintAfterAttach(m.handleError(errors.Join(msg.err, errors.New(strings.Join(msg.keeperErrs, "\n")))))
		}
		return m, m.repaintAfterAttach(m.handleError(msg.err))
	}
	// The attach keeper cleared prompt(s) while the loop was suspended — delivered
	// ones, or abandoned ones whose hard-failure budget ran out — but it cannot
	// persist (persistence is main-loop-owned). Mirror promptDeliveredMsg's persist
	// here — before the kill/cycle early returns below, so no detach path leaves a
	// cleared prompt resurrectable from state.json.
	if msg.keeperDelivered || len(msg.keeperErrs) > 0 {
		if err := m.persistInstances(); err != nil {
			log.ErrorLog.Printf("failed to persist after keeper prompt delivery: %v", err)
		}
	}
	// The user was watching this session until a moment ago, so if the agent
	// finished while attached, the poll below settles a stale Running to Ready —
	// a synthetic transition that must not flag unread. An agent still working
	// at detach is observed as Running first, which clears the suppression, so a
	// later genuine completion flags normally. Armed before BOTH detach paths:
	// the sibling-cycle early return below and the normal fresh poll.
	if msg.killTarget != nil {
		msg.killTarget.ArmReadySuppression()
		// A detach that hit a pty close/restore error can't ride msg.err (that
		// comes from Run(), nil on a normal detach). Surface it via the persistent
		// modal — it's actionable — and short-circuit the kill/cycle so we don't
		// hop siblings while this session is half-broken. Force a full repaint +
		// relayout (repaintAfterAttach) so the modal and layout redraw cleanly at the
		// correct dimensions after the full-screen attach, matching the other detach
		// returns below. (The terminal tab, killTarget nil, has no such teardown to
		// report.)
		if derr := msg.killTarget.AttachExitError(); derr != nil {
			m.showInfo(fmt.Sprintf(
				"Session detach hit an error and may need re-attaching "+
					"(pause then resume to recover):\n%v", derr))
			return m, m.repaintAfterAttach()
		}
	}
	// Honor an in-session kill (Ctrl+X) requested before detach. killTarget is the
	// attached instance (nil for the terminal tab, which has no kill key); force a
	// full repaint + relayout (repaintAfterAttach) so the confirmation overlay
	// redraws cleanly at the correct dimensions after the full-screen attach
	// (confirmKill only mutates state).
	if msg.killTarget != nil && msg.killTarget.AttachKillRequested() {
		return m, m.repaintAfterAttach(m.confirmKill(msg.killTarget))
	}
	// A sibling-cycle key (Ctrl+PgUp/PgDn) detaches with a direction; re-attach the
	// neighbouring session in the repo group, keeping cycling inside Atrium's model.
	// killTarget is the session just detached (nil for the terminal tab, which has
	// no cycle keys).
	if msg.killTarget != nil {
		if next := m.cycleTarget(msg.killTarget); next != nil {
			m.list.SelectInstance(next)
			m.pushOneContext(next)
			// Carry keeper losses into the next attach's keeper so the chain's final
			// plain detach surfaces them (this branch returns before the surfacing).
			return m, m.attachExecCarry(next.Attach, next, msg.keeperErrs)
		}
	}
	if msg.rawModeFailed {
		// Raw mode couldn't be set, so the attach ran cooked: IXON swallowed Ctrl+Q
		// (and keystrokes were line-buffered), so detach didn't work and the attach
		// may have looked stuck. Explain it via the persistent modal, give the working
		// escape (tmux's own prefix), and suggest the IXON/TTY check. The session
		// itself is fine, so still run the normal post-detach refresh below. (Safe to
		// land here: the kill/cycle branches above need single-byte control reads that
		// cooked mode can't deliver, so they're unreachable when rawModeFailed.)
		detach := keys.LabelOf(keys.KeyAttachToggle)
		m.showInfo("Raw mode couldn't be set for this attach, so " + detach + " detach (and " +
			"other in-session keys) didn't work — the attach may have looked stuck. " +
			"Detach instead with tmux's own keys: press the prefix (Ctrl-B by default), " +
			"then d — then Enter, since cooked mode buffers input until a newline, so the " +
			"prefix may not register on its own. If this keeps happening, check that the " +
			// Ctrl+Q is a literal here on purpose, and is the one key in this modal that
			// has to stay one: `stty -ixon` disables XOFF, which is Ctrl+Q specifically.
			// Reading the detach binding would make the advice follow a rebind onto a
			// chord flow control never touches — telling a user whose detach is ctrl+g to
			// run a command that cannot affect it, and deleting the one true fact the
			// sentence carried.
			"terminal/SSH/Docker session provides a real TTY; if detach is still Ctrl+Q, " +
			"`stty -ixon` can also stop it being swallowed.")
	}
	// Prompts the keeper definitively failed to deliver mid-attach: surface the loss
	// like promptSendErrorMsg would, rather than leaving sessions silently
	// Ready-but-idle. The sibling-cycle branch carries its errs forward to the next
	// keeper, so they land here at the chain's end; only the kill and
	// AttachExitError paths remain log-only (each opens its own modal that a second
	// notice would fight).
	var cmds []tea.Cmd
	if len(msg.keeperErrs) > 0 {
		cmds = append(cmds, m.handleError(errors.New(strings.Join(msg.keeperErrs, "\n"))))
	}
	return m, m.resumeAfterSuspendedLoop(cmds...)
}

// resumeAfterSuspendedLoop is the tail every return from a tea.Exec suspension shares,
// whichever one suspended the loop — a tmux attach or a custom command in terminal mode.
//
// Polling stalled for the whole list while the loop was suspended (the keeper services
// only prompt-delivery and auto-yes work), so every row is stale on return. Sweep
// every active session immediately instead of waiting up to a full ~2s sweep
// cycle: the selected row is polled face-value (PollNow) so a stale "running" on
// a now-idle agent doesn't linger — and re-runs through the hysteresis from
// there — while background rows keep the hysteresis Poll so a mid-turn agent
// isn't falsely flagged done. Pin the poll tracker to the current selection first so
// instanceChanged's own (hysteresis) poll doesn't also fire for the same instance.
//
// Then hard-repaint: tea.Exec's RestoreTerminal only does a soft (diff-cache) repaint,
// which leaves the reclaimed frame stale or blank after a full-screen child hands the
// terminal back (see repaintAfterAttach).
//
// Callers compose their own error surfacing and pass it in, rather than this deciding:
// handleError writes ONE notice, so two calls in the same batch would have the second
// silently overwrite the first, and which error wins differs per caller.
func (m *home) resumeAfterSuspendedLoop(extra ...tea.Cmd) tea.Cmd {
	selected := m.list.GetSelectedInstance()
	m.lastStatusPollSelection = selected
	cmds := []tea.Cmd{m.instanceChanged(),
		sweepMetadataNowCmd(m.ctx, m.snapshotActiveInstances(), selected, m.attachGen, m.usagePolicy(), m.diffContentFloor())}
	return m.repaintAfterAttach(append(cmds, extra...)...)
}

func (m *home) handleInstanceStarted(msg instanceStartedMsg) (tea.Model, tea.Cmd) {
	// Normally re-select the just-started instance: it corrects a possibly-stale
	// selection index, and an auto-open attach drops into it. The exception is #439: on
	// a successful start with no auto-open, if the user navigated to another session
	// during the slow async Start(), preserve their cursor instead of snapping it
	// back to the new session.
	//
	// A failure does NOT select it. The teardown below names its target, so nothing
	// here has to aim the cursor at a row that is about to be destroyed.
	if m.shouldAutoOpen(msg.instance, msg.hadPrompt, msg.origin) ||
		m.list.GetSelectedInstance() == msg.instance {
		m.list.SelectInstance(msg.instance)
	}

	if msg.err != nil {
		// Close out an `atrium new` request before the teardown below: the outcome
		// this message carries is the only thing that tells a waiting `--wait` whether
		// the session it asked for exists (#703). A session that came from the form or
		// smart-dispatch is a map miss. Rejecting first also means forgetInstance's
		// own settle (a removal with no start outcome) finds nothing left to do.
		m.settleCreateRequest(msg.instance, msg.err)
		// Tear down the session that failed to start, by name. KillInstance exists for
		// exactly this — a target that need not be the selected row — where list.Kill
		// destroys whatever the cursor is on: SelectInstance ends in
		// clampSelectionToNavigable, which for a row hidden inside a folded group or
		// filtered out by an active query snaps to the group anchor or the nearest
		// visible row, so aiming the cursor first could have killed a live session the
		// user was working in. Any teardown error is already logged inside KillInstance;
		// the meaningful failure here is msg.err, which is surfaced below, so discard
		// the return rather than fight that modal.
		_ = m.list.KillInstance(msg.instance)
		m.forgetInstance(msg.instance) // the failed session is gone from the list; drop its bookkeeping
		// A quit deferred while this session was Loading (issue #268): the failed
		// session is torn down and gone from the list, so resume the quit if it's now
		// safe. Surface the start error last either way — if the quit re-defers (a
		// sibling is still starting) the toast still matters; if it exits, it's moot.
		if m.quitRequested {
			if cmd, done := m.resumeQuitAfterStart(); done {
				return m, tea.Batch(cmd, m.handleError(msg.err), m.instanceChanged())
			}
		}
		return m, tea.Batch(m.handleError(msg.err), m.instanceChanged())
	}

	// Own the Loading -> Running transition here, on the main thread. Start()
	// deliberately no longer sets Running from its background goroutine (that
	// raced the UI/poll readers and could leave the session stuck on the
	// "Setting up workspace..." splash); this message arrives after Start()
	// completed, so the write is race-free. ApplyPaneState refines it to
	// Ready/NeedsInput on later ticks.
	msg.instance.SetStatus(session.Running)

	// Save after successful start — before honoring a deferred quit, so this
	// completion is durably recorded even while a sibling is still starting (a
	// crash in that window would otherwise orphan it, the very #268 symptom). On
	// failure a deferred+safe quit still gets its escape-hatch modal (via
	// resumeQuitAfterStart → handleQuit) rather than a dead-end error toast.
	if err := m.persistInstances(); err != nil {
		// The session is live but unrecorded, so an `atrium new` caller must hear the
		// failure rather than read the unlink as success and go looking in state.json
		// for a branch that is not there. Its own wording, because the session did
		// start — what failed is the record of it.
		m.discloseUnrecordedSession(msg.instance,
			fmt.Sprintf("the session was created but atrium could not record it: %v", err))
		if m.quitRequested {
			if cmd, done := m.resumeQuitAfterStart(); done {
				return m, cmd
			}
		}
		return m, m.handleError(err)
	}

	// Only now: the row is durable, so the request file going away means the session
	// exists *and* --wait can read its branch back. See settleCreateRequest.
	m.settleCreateRequest(msg.instance, nil)

	// A quit deferred while this session was Loading (issue #268) takes precedence
	// over the rest of the post-start handling (welcome, auto-open): now that this
	// start is persisted, complete the quit if it's safe. resumeQuitAfterStart waits
	// for any sibling still Loading and won't exit from under an open overlay.
	if m.quitRequested {
		if cmd, done := m.resumeQuitAfterStart(); done {
			return m, cmd
		}
		// The deferred quit was dropped (the user navigated into an overlay); fall
		// through and finish this start normally.
	}
	// The recent-path list and the one-time welcome are both about what the person at
	// the keyboard has done, so a background create writes neither. The recent paths
	// feed the create form's picker — an MRU a human arranged, which a CI job's repo has
	// no business jumping to the head of.
	//
	// The welcome's seen-bit is the sharper case, because this drain runs *under* the
	// welcome modal on purpose (see drainCreateRequests: a fresh install sits in
	// stateWelcome until someone answers it, and refusing to create there is the deadlock
	// #703 exists to remove). Answering it is what retires it — overlay confirm and skip
	// alike, app_welcome.go — and this chokepoint is the backstop for a user who reaches
	// a first session without ever meeting the overlay. A request drained out of the
	// spool while the modal is still on screen is neither, so without this gate
	// `atrium new` would burn an unanswered welcome the user is still looking at.
	//
	// All three producers funnel through here — the create form, smart auto-dispatch and
	// the `atrium new` drain — so being the single chokepoint is what makes an explicit
	// gate necessary rather than redundant.
	if msg.origin != spawnBackground {
		m.recordRecentPath(msg.instance.Path)
		m.markWelcomeSeen() // best-effort persist; markWelcomeSeen names the other callers
	}
	if m.autoYes {
		msg.instance.AutoYes = true
	}

	// A prompt from the N form is delivered later by the metadata tick loop,
	// once the agent is past its startup/trust screen and ready for input
	// (see deliverReadyPrompts). Sending here races the agent's boot and lands
	// keystrokes in the trust dialog instead of the input box.
	// Leave a live progress row alone: its owner clears it when the operation
	// finishes (the ranking in ui.Menu decides which line shows meanwhile).
	//
	// And leave the bar's STATE alone for a background create. This is a bare SetState,
	// so unlike Menu.SetInstance — which rewrites only StateDefault/StateEmpty, precisely
	// so the periodic instanceChanged cannot do this — it would permanently drop a mode
	// the user is mid-gesture in: StateVisual, StateFilter, StateHints and StateDiffComment
	// each own the bar while their mode is active, and with hint_bar off StateDefault
	// renders as an empty row. A create nobody asked for must not end one; instanceChanged,
	// batched by startNewSession, moves it off StateEmpty through the protected path.
	//
	// The state, not the row: the drain's own notice still rides that row for a few
	// seconds via Menu.SetNotice, which is independent of Menu.State. What this prevents
	// is the mode being ended, not the bar being borrowed.
	if msg.origin != spawnBackground && m.menu.State() != ui.StateBusy {
		m.menu.SetState(ui.StateDefault)
	}

	if m.shouldAutoOpen(msg.instance, msg.hadPrompt, msg.origin) {
		// Drop straight into the new session, mirroring the KeyEnter attach path.
		// Attach msg.instance directly rather than via m.list.Attach(): a background
		// instanceStartedMsg from another freshly-created session could have moved
		// the list selection by now. The attach runs through tea.Exec, which hands
		// the terminal to tmux and repaints on detach; post-detach handling — an
		// in-session Ctrl+X kill request, keyed on msg.instance since the selection
		// may have drifted, or a sibling-cycle request — lands in the
		// attachFinishedMsg handler.
		return m, m.attachExec(msg.instance.Attach, msg.instance)
	}

	// A background create asks for no global resize — the WindowSizeMsg it would send
	// exits hint mode (see the tea.WindowSizeMsg arm in Update) and reflows a frame
	// nothing about the terminal changed. But one half of that resize IS load-bearing
	// here: updateHandleWindowSizeEvent ends in SetSessionPreviewSize, the only thing
	// that gives a session's detached tmux pane the preview's geometry as a matter of
	// course, and it skips any instance that is not yet Started — which this one already
	// is on arrival, Instance.Start having set the flag on the goroutine before this
	// message was ever constructed. Left unsized the pane keeps its new-session -d
	// default: measured at 80 columns against a 116-column preview, so the preview
	// renders it wrapped at the wrong width and every width-sensitive classifier in
	// session/agent reads a capture taken at a width the pane never had. So size just the
	// row this message is about.
	//
	// Not the only site that has to do that by hand any more: finishBlankRelaunches sizes
	// the pane a repaired session comes back on, which arrives with no resize behind it
	// either. Both go through sizeStartedPane.
	if msg.origin == spawnBackground {
		w, h := m.tabbedWindow.GetPreviewSize()
		if err := sizeStartedPane(msg.instance, w, h); err != nil {
			log.ErrorLog.Printf("could not size the pane for a background create: %v", err)
		}
		return m, m.instanceChanged()
	}
	return m, tea.Batch(tea.RequestWindowSize, m.instanceChanged())
}

// sizeStartedPane is Instance.SetPreviewSize, as a seam — config.detectAgentCommand's
// precedent, and betweenSpoolSamples' in this same change.
//
// A var rather than a direct call because the effect is a pty ioctl whose visibility
// belongs to tmux, not to Atrium: SetPreviewSize resizes the pty and tmux reacts to the
// SIGWINCH on its own schedule and by its own client-size reconciliation rules, which
// differ by version. A test that reads the width back through `display-message` was
// green on one machine and red on CI for both reasons — it raced the propagation, and
// where it did not race it measured tmux's policy rather than this branch. What has to
// be pinned here is only that the call is made, with the preview's geometry, on exactly
// the origin that no longer asks for a resize.
var sizeStartedPane = func(inst *session.Instance, width, height int) error {
	return inst.SetPreviewSize(width, height)
}
