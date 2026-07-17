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
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *home) handleDriftFound(msg driftFoundMsg) (tea.Model, tea.Cmd) {
	// Try to show the hint first. showMenuNotice returns nil when the hint
	// bar can't render right now (e.g. hint_bar off, or a modal owns the
	// screen); in that case record no ack so the hint re-arms on a later
	// startup instead of being silently consumed. atrium doctor remains the
	// durable surface meanwhile.
	cmd := m.showMenuNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()), ui.NoticeInfo)
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
func (m *home) splashAnimating() bool {
	if m.state == stateScreensaver {
		return true
	}
	return m.state == stateDefault && m.list != nil && m.list.NumInstances() == 0
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
		// An update notice that arrived while an overlay owned the screen
		// is buffered; deliver it as soon as the hint bar is back.
		m.flushPendingUpdateNotice(),
		// Likewise for "what's new" notes buffered behind another overlay.
		m.flushPendingReleaseNotes(),
		// Likewise for a crash-at-launch modal buffered behind another overlay.
		m.flushPendingLaunchCrash(),
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

func (m *home) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// The screensaver dismisses on a click, mirroring the any-key exit; wheel
	// and motion events are ignored so a nudged mouse doesn't tear it down.
	if m.state == stateScreensaver {
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown,
				tea.MouseButtonWheelLeft, tea.MouseButtonWheelRight:
				// Wheel deltas arrive as presses; not a deliberate wake.
			default:
				m.state = stateDefault
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
		if m.state != stateDefault || msg.Action == tea.MouseActionPress {
			m.draggingDivider = false
		} else {
			switch msg.Action {
			case tea.MouseActionMotion:
				if m.windowWidth <= 0 {
					return m, nil
				}
				m.listRatio = config.ClampListRatio(float64(msg.X) / float64(m.windowWidth))
				// Reflow the panes so the divider tracks the cursor live. The pane
				// content (tmux/diff capture) is intentionally left to the periodic
				// preview tick rather than re-fetched here: a full instanceChanged()
				// would re-capture on every motion event of the drag. recomputeLayout
				// re-clamps the already-captured text to the new width in the meantime.
				m.recomputeLayout()
				return m, nil
			case tea.MouseActionRelease:
				m.draggingDivider = false
				if err := m.appState.SetListRatio(m.listRatio); err != nil {
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
	// the grab is bounded to the pane rows so a press on the hint/error strip below
	// them doesn't start a drag. This runs before the press-only early return and
	// the row/tab click logic, so a seam press starts a drag instead of selecting
	// the row behind it.
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress &&
		m.state == stateDefault && m.windowWidth > 0 && msg.Y < m.paneContentHeight() {
		listWidth := int(float32(m.windowWidth) * float32(m.listRatio))
		if msg.X >= listWidth-dividerGrab && msg.X <= listWidth+dividerGrab {
			m.draggingDivider = true
			return m, nil
		}
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	// Modal text overlays (help / info) own the screen: the wheel scrolls
	// an overflowing cheatsheet wherever it hovers, and a left-click
	// outside the box dismisses — mirroring the keyboard semantics
	// (scroll keys scroll, anything else closes). Clicks inside the box
	// are inert so a stray selection click doesn't tear the dialog down.
	if (m.state == stateHelp || m.state == stateInfo) && m.textOverlay != nil {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.textOverlay.ScrollBy(-1)
		case tea.MouseButtonWheelDown:
			m.textOverlay.ScrollBy(1)
		case tea.MouseButtonLeft:
			if !m.textOverlayContains(msg.X, msg.Y) {
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
	if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
		if m.state != stateDefault {
			return m, nil
		}
		// Over the list: move the selection, regardless of the selected
		// instance's state (paused / nil), exactly like the keyboard paths.
		if m.list.InPanelBounds(msg) {
			if msg.Button == tea.MouseButtonWheelUp {
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
			if msg.Button == tea.MouseButtonWheelUp {
				m.tabbedWindow.ScrollUp(wheelScrollLines)
			} else {
				m.tabbedWindow.ScrollDown(wheelScrollLines)
			}
			return m, nil
		}
		return m, nil
	}
	// A left-click on a hint-bar entry mirrors pressing its key. The bar carries
	// click zones in the default view and the three modal bars (filter / hint /
	// visual); KeyAtZone resolves nothing in any other state — an overlay owns
	// the screen, or the bar shows progress, not keys — so this is inert there,
	// like the row/tab path ignoring rows behind an overlay. The resolved key is
	// re-injected through handleKeyPress so it runs the exact same dispatch
	// (state routing + guards) as the keypress it advertises: nothing here
	// becomes mouse-only.
	if msg.Button == tea.MouseButtonLeft && m.hintBarClickState() {
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
	if msg.Button == tea.MouseButtonLeft && m.state == stateDefault {
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
			return m, m.instanceChanged()
		}
	}
	return m, nil
}

// hintBarClickState reports whether the hint bar is the live surface a click can
// act on: the default view and the three modal bars (filter / hint / visual). In
// every other state an overlay owns the screen (or the bar shows non-key
// progress), so a click on the bar's last-scanned zones must be ignored — the
// same reason the row/tab path gates itself to stateDefault.
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
// message must stringify back to k: the special keys the bars can show (enter,
// esc, space, ctrl+x, the shift arrows) map to their KeyType, and every other
// bar key is a single rune whose KeyRunes message stringifies to that rune. A
// key it can't represent reports false, so an unrecognized entry is a no-op
// rather than a wrong action.
func synthKeyMsg(k string) (tea.KeyMsg, bool) {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}, true
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}, true
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}, true
	case "ctrl+x":
		return tea.KeyMsg{Type: tea.KeyCtrlX}, true
	case "shift+up":
		return tea.KeyMsg{Type: tea.KeyShiftUp}, true
	case "shift+down":
		return tea.KeyMsg{Type: tea.KeyShiftDown}, true
	default:
		if r := []rune(k); len(r) == 1 {
			return tea.KeyMsg{Type: tea.KeyRunes, Runes: r}, true
		}
		return tea.KeyMsg{}, false
	}
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
	// start). tea.Exec's RestoreTerminal has already repainted the frame; refine
	// the layout and selection-derived panes from here.
	m.state = stateDefault
	// Re-assert the OS chrome the attach handed to tmux (onAttached reset it); the
	// next metadata tick refines it, this repaints the known state immediately.
	m.applyOSChrome(false)
	if msg.err != nil {
		// A failed sibling-cycle re-attach still carries keeper losses from the
		// previous attach (attachExecCarry seeds them before Run can fail); surface
		// them alongside the attach error, honoring the promise below that only the
		// kill and AttachExitError paths stay log-only.
		if len(msg.keeperErrs) > 0 {
			return m, m.handleError(errors.Join(msg.err, errors.New(strings.Join(msg.keeperErrs, "\n"))))
		}
		return m, m.handleError(msg.err)
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
		// hop siblings while this session is half-broken. Keep tea.WindowSize() so
		// the modal and layout redraw at the correct dimensions after the
		// full-screen attach, matching the other detach returns below. (The
		// terminal tab, killTarget nil, has no such teardown to report.)
		if derr := msg.killTarget.AttachExitError(); derr != nil {
			m.showInfo(fmt.Sprintf(
				"Session detach hit an error and may need re-attaching "+
					"(pause then resume to recover):\n%v", derr))
			return m, tea.WindowSize()
		}
	}
	// Honor an in-session kill (Ctrl+X) requested before detach. killTarget is the
	// attached instance (nil for the terminal tab, which has no kill key); keep
	// tea.WindowSize() so the confirmation overlay redraws at the correct
	// dimensions after the full-screen attach (confirmKill only mutates state).
	if msg.killTarget != nil && msg.killTarget.AttachKillRequested() {
		return m, tea.Batch(tea.WindowSize(), m.confirmKill(msg.killTarget))
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
		m.showInfo("Raw mode couldn't be set for this attach, so Ctrl+Q detach (and " +
			"other in-session keys) didn't work — the attach may have looked stuck. " +
			"Detach instead with tmux's own keys: press the prefix (Ctrl-B by default), " +
			"then d — then Enter, since cooked mode buffers input until a newline, so the " +
			"prefix may not register on its own. If this keeps happening, check that the " +
			"terminal/SSH/Docker session provides a real TTY; `stty -ixon` can also stop " +
			"Ctrl+Q being swallowed.")
	}
	// Polling stalled for the whole list while attached (the keeper services only
	// prompt-delivery and auto-yes work), so every row is stale on return. Sweep
	// every active session immediately instead of waiting up to a full ~2s sweep
	// cycle: the selected row is polled face-value (PollNow) so a stale "running" on
	// a now-idle agent doesn't linger — and re-runs through the hysteresis from
	// there — while background rows keep the hysteresis Poll so a mid-turn agent
	// isn't falsely flagged done. Pin the poll tracker to the current selection first so
	// instanceChanged's own (hysteresis) poll doesn't also fire for the same instance.
	selected := m.list.GetSelectedInstance()
	m.lastStatusPollSelection = selected
	cmds := []tea.Cmd{tea.WindowSize(), m.instanceChanged(),
		sweepMetadataNowCmd(m.ctx, m.snapshotActiveInstances(), selected, m.attachGen)}
	// Prompts the keeper definitively failed to deliver mid-attach: surface the loss
	// like promptSendErrorMsg would, rather than leaving sessions silently
	// Ready-but-idle. The sibling-cycle branch carries its errs forward to the next
	// keeper, so they land here at the chain's end; only the kill and
	// AttachExitError paths remain log-only (each opens its own modal that a second
	// notice would fight).
	if len(msg.keeperErrs) > 0 {
		cmds = append(cmds, m.handleError(errors.New(strings.Join(msg.keeperErrs, "\n"))))
	}
	return m, tea.Batch(cmds...)
}

func (m *home) handleInstanceStarted(msg instanceStartedMsg) (tea.Model, tea.Cmd) {
	// Select the instance that just started (or failed)
	m.list.SelectInstance(msg.instance)

	if msg.err != nil {
		// Tear down the session that failed to start. Any teardown error is already
		// logged inside KillInstance; the meaningful failure here is msg.err, which
		// is surfaced below, so discard Kill's return rather than fight that modal.
		_ = m.list.Kill()
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
		if m.quitRequested {
			if cmd, done := m.resumeQuitAfterStart(); done {
				return m, cmd
			}
		}
		return m, m.handleError(err)
	}

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
	m.recordRecentPath(msg.instance.Path)
	// First successful session start retires the one-time welcome. This is the single
	// chokepoint every start (inline `n` and the `N` form) funnels through, so the
	// welcome re-shows on every launch until the user has actually created a session —
	// a dismissal alone no longer burns it (see showHelpScreen). Best-effort persist.
	m.markWelcomeSeen()
	if m.autoYes {
		msg.instance.AutoYes = true
	}

	// A prompt from the N form is delivered later by the metadata tick loop,
	// once the agent is past its startup/trust screen and ready for input
	// (see deliverReadyPrompts). Sending here races the agent's boot and lands
	// keystrokes in the trust dialog instead of the input box.
	// Don't clobber a "busy" progress row if an action is in flight — asyncActionDoneMsg
	// restores StateDefault when it completes.
	if !m.actionInFlight {
		m.menu.SetState(ui.StateDefault)
	}

	if m.shouldAutoOpen(msg.instance, msg.hadPrompt) {
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

	return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
}
