package ui

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui/theme"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// terminalPaneStyle / terminalFooterStyle read the active theme at render time.
func terminalPaneStyle() lipgloss.Style   { return theme.Current().FgStyle() }
func terminalFooterStyle() lipgloss.Style { return theme.Current().DimStyle() }

// terminalCaptureLog throttles the background capture's failure trail, which would
// otherwise repeat at the frame cadence.
var terminalCaptureLog = log.NewEvery(5 * time.Second)

// terminalSession holds a cached tmux session for a specific instance, plus the
// last frame captured from it. The frame lives here rather than on the Instance
// (where the agent pane's does) because the shell is this pane's own resource: the
// map is already keyed and already pruned by Close/CloseForInstance, so the frame
// needs no lifecycle of its own.
type terminalSession struct {
	tmuxSession *tmux.Session
	cwd         string
	content     string
	frameAt     time.Time
}

// TerminalPane manages shell tmux sessions in the working directory of selected instances.
// Sessions are cached per instance so switching between instances preserves terminal state.
type TerminalPane struct {
	// ctx is the app lifecycle context the pane's shell tmux sessions derive
	// their subprocess contexts from. Set once at construction; nil means
	// Background (tests).
	ctx           context.Context
	mu            sync.Mutex
	width, height int
	sessions      map[string]*terminalSession // terminalKey (instance tmux name) → session
	currentKey    string                      // terminalKey of the currently displayed instance
	// reapGen counts CloseForInstance requests per terminal key, whether or not a
	// shell was there to reap. EnsureSession snapshots the key's count before its
	// tmux round trip and re-reads it at install time, which is how a shell created
	// during a pause or kill is closed instead of installed (#701): the
	// done-handlers' reap can only close what is already in sessions, so an install
	// landing after it would otherwise leave a shell in a deleted worktree that
	// nothing but `atrium reset` sweeps.
	//
	// Per key rather than one counter for the pane, because the abort path closes
	// the session it was about to install and that session is not always one the
	// call created — the branch above it adopts a live <key>_term left by a crashed
	// run. Aborting on some other instance's reap would not cost a retry there, it
	// would kill the user's shell and everything running in it.
	reapGen map[string]uint64
	// closeGen counts whole-pane teardowns. Close reaps every key at once, including
	// keys no entry names yet, which is the one reap a per-key count cannot express.
	closeGen uint64
	// beforeInstall, when set, runs after EnsureSession's tmux round trip and
	// before its install re-check. It is a test seam — the same idiom as app's
	// cleanupTerminalForInstance and tmuxAvailable — and exists because the window
	// it opens is exactly the one #701 loses: without it, landing a reap inside
	// that window is a timing bet rather than an assertion. Nil in production, and
	// set before the pane is shared with a goroutine — it is read without the lock.
	beforeInstall func()
	content       string
	fallback      bool
	// fallbackMessage is the raw fallback text while fallback is true, laid out
	// by String() against the live pane width (see fallbackBlock).
	fallbackMessage string
	// splash is true only for the idle empty screen (nil instance): String() then
	// renders the animated field behind the wordmark. Implies fallback.
	splash        bool
	splashMessage string
	splashFrame   int

	isScrolling bool
	// scrollKey is the terminalKey the scroll-mode snapshot was captured from.
	// The snapshot is only meaningful while that same live instance is displayed:
	// UpdateContent drops it for any other state (different instance, none, paused,
	// not started), so a frozen capture can never pin across selection changes —
	// the terminal-pane twin of the stuck-preview bug. This matters doubly here
	// because String() renders the scroll viewport before the fallbacks.
	// Keyed by terminalKey (not pointer, as PreviewPane does) to match the
	// sessions map; the key is stable for a started instance's lifetime, so it
	// cannot drift while the snapshot is up.
	scrollKey string
	viewport  viewport.Model
}

// terminalKey is the cache key for an instance's terminal shell: its persisted
// tmux session name. Unlike Title it is unique across repo groups (same-titled
// sessions are legal in different groups) and stable once the instance has
// started — and the pane only creates shells for started instances.
func terminalKey(i *session.Instance) string { return i.TmuxSessionName() }

// NewTerminalPane returns an empty TerminalPane with no shell sessions yet.
// ctx is the app lifecycle context its shell tmux sessions derive from.
func NewTerminalPane(ctx context.Context) *TerminalPane {
	return &TerminalPane{
		ctx:      ctx,
		sessions: make(map[string]*terminalSession),
		viewport: viewport.New(),
	}
}

// baseContext returns the lifecycle context shell sessions derive from,
// defaulting to Background for panes constructed without one.
func (t *TerminalPane) baseContext() context.Context {
	if t.ctx != nil {
		return t.ctx
	}
	return context.Background()
}

// SetSize sets the pane's render dimensions and resizes the currently
// displayed shell's detached tmux session to match.
func (t *TerminalPane) SetSize(width, height int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.width = width
	t.height = height
	t.viewport.SetWidth(width)
	t.viewport.SetHeight(height)
	if s, ok := t.sessions[t.currentKey]; ok && s.tmuxSession != nil {
		if err := s.tmuxSession.SetDetachedSize(width, height); err != nil {
			log.InfoLog.Printf("terminal pane: failed to set detached size: %v", err)
		}
	}
}

// SetSplashFrame stores the current splash animation frame, pushed from the
// app's 60fps splash tick. It only affects the idle-splash render in String().
func (t *TerminalPane) SetSplashFrame(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.splashFrame = n
}

// setFallbackState sets the terminal pane to display a fallback message. The
// message is stored raw, not composed: String() lays it out against the live pane
// width (see fallbackBlock). Caller must hold t.mu.
func (t *TerminalPane) setFallbackState(message string) {
	t.fallback = true
	t.splash = false
	t.fallbackMessage = message
	t.content = ""
}

// setSplashState is setFallbackState for the idle empty screen (nil instance),
// additionally flagging the splash so String() renders the animated field behind
// the wordmark. Every other empty state keeps the plain fallback. Caller holds t.mu.
func (t *TerminalPane) setSplashState(message string) {
	t.setFallbackState(message)
	t.splash = true
	t.splashMessage = message
}

// UpdateContent captures the tmux pane output for the terminal session.
func (t *TerminalPane) UpdateContent(instance *session.Instance) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// The scroll snapshot belongs to one live instance; rendering anything else
	// exits scroll mode so the pane reflects the new selection (or the right
	// fallback) instead of pinning the old capture.
	if t.isScrolling &&
		(instance == nil || terminalKey(instance) != t.scrollKey || instance.Paused() || !instance.Started()) {
		t.exitScrollModeLocked()
	}

	if instance == nil {
		t.setSplashState("Select an instance to open a terminal")
		return nil
	}
	if instance.Paused() {
		t.setFallbackState("Session is paused. Resume to use terminal.")
		return nil
	}
	if !instance.Started() {
		t.setFallbackState("Instance is not started yet.")
		return nil
	}

	// Skip content updates while in scroll mode
	if t.isScrolling {
		return nil
	}

	// Point the pane at this instance's shell and render whatever was last captured
	// for it. No I/O happens here: UpdateContent runs inside Update, and this pane
	// used to create a tmux session and capture it inline on every 100ms tick (#380).
	// The app's capture chain fills the cache; ApplyFrame installs it.
	key := terminalKey(instance)
	if key == "" {
		// No persisted tmux name (an instance fabricated without Start, e.g. in
		// tests): there is no stable key to cache a shell under.
		t.setFallbackState("Terminal session not available.")
		return nil
	}
	t.currentKey = key

	s, ok := t.sessions[key]
	if !ok || s.frameAt.IsZero() {
		// The shell is still being created, or its first capture has not landed.
		t.setFallbackState("Opening terminal…")
		return nil
	}

	t.fallback = false
	t.splash = false
	t.content = s.content
	return nil
}

// LiveContent returns the shell text the pane is currently rendering, and whether
// it is real content rather than a fallback or a frozen scroll snapshot. It is the
// terminal twin of PreviewPane.LiveContent, and exists for the same reason: the
// copy action needs the captured text, not String()'s trimmed and styled render.
func (t *TerminalPane) LiveContent() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fallback || t.isScrolling {
		return "", false
	}
	return t.content, t.content != ""
}

// CaptureTarget returns the shell session to capture for instance, its cache key,
// and whether one exists yet. Main-thread, lock-only, and deliberately free of the
// has-session probe liveCurrentSession used to run *while holding t.mu* — the same
// lock String() takes, so an unresponsive server could block View itself.
//
// ok=false means "create it first" (see EnsureSession), not "give up".
func (t *TerminalPane) CaptureTarget(instance *session.Instance) (sess *tmux.Session, key string, ok bool) {
	if instance == nil || !instance.Started() || instance.Paused() {
		return nil, "", false
	}
	key = terminalKey(instance)
	if key == "" {
		return nil, "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s, cached := t.sessions[key]
	if !cached || s.tmuxSession == nil {
		return nil, key, false
	}
	return s.tmuxSession, key, true
}

// ApplyFrame installs a background capture against its cache key. Main thread only
// — it writes the state String() reads. A failed capture leaves the last content in
// place, exactly as the preview pane does.
func (t *TerminalPane) ApplyFrame(key, content string, err error, at time.Time) {
	if key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[key]
	if !ok {
		return
	}
	if err != nil {
		if terminalCaptureLog.ShouldLog() {
			log.WarningLog.Printf("terminal pane: capture failed for %s: %v", key, err)
		}
		return
	}
	s.content = content
	s.frameAt = at
}

// EnsureSession creates (or restores, or reaps-legacy-and-recreates) the shell
// session for instance and returns its cache key.
//
// It is the old ensureSessionLocked with the I/O moved OUT of t.mu: the lock is
// taken only to read the cached entry, to read the pane size, and to install the
// finished session. That matters twice over — this runs on the app's capture
// goroutine now, and t.mu is the lock String() takes, so holding it across a tmux
// round trip could block rendering even before the update-thread question.
//
// Safe against concurrent entry because the capture chain keeps exactly one
// capture in flight at a time.
//
// Moving the I/O out of the lock is what makes the install a second decision
// rather than a formality: a pause or a kill can complete during that round trip,
// and the entry-time instance.Paused() check below cannot see it — pause() flips
// the status as its LAST statement (session/pause.go) and Kill() never flips one
// at all. So the install re-reads reapGen and the status, and closes the shell it
// just created rather than caching one in a deleted worktree (#701). The other
// half of that fix is app's shellStartRefused, which stops this being reached
// during a teardown in the first place.
func (t *TerminalPane) EnsureSession(instance *session.Instance) (string, error) {
	if instance == nil || !instance.Started() || instance.Paused() {
		return "", nil
	}

	// Host the shell in the same cwd as the agent: the worktree for a git session, or
	// Path for a direct (non-git) session. GetWorktreePath() would be "" for a direct
	// session and wrongly skip terminal creation, so use WorkingDir().
	cwd := instance.WorkingDir()
	if cwd == "" {
		return "", nil
	}
	key := terminalKey(instance)
	if key == "" {
		// No persisted tmux name (an instance fabricated without Start, e.g. in
		// tests): there is no stable key to cache a shell under.
		return "", nil
	}

	// Check if we already have a cached session for this instance. Both generations
	// are snapshotted in the SAME critical section, and deliberately before the
	// stale-entry delete below: that delete is this call's own bookkeeping, not a
	// reap, so bumping there would make every recreate refuse its own install.
	t.mu.Lock()
	cached, ok := t.sessions[key]
	gen, closeGen := t.reapGen[key], t.closeGen
	t.mu.Unlock()
	if ok && cached.tmuxSession != nil {
		if cached.tmuxSession.DoesSessionExist() {
			return key, nil
		}
		// Session died; drop the stale entry and recreate below.
		t.mu.Lock()
		delete(t.sessions, key)
		t.mu.Unlock()
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	// The shell session rides the instance's own (unique, repo-qualified) tmux
	// name with a "_term" suffix — already prefix-matched by CleanupSessions, and
	// the suffix is reserved by the new-session/rename guards so no agent session
	// can mint it. The window name is cosmetic.
	termName := key + "_term"

	// Shells were keyed term_<title> before tmux names became persisted state;
	// that name is unreachable under the new key, so a shell left from a
	// pre-upgrade run would idle on the socket forever. Reap it here on the
	// create path (one has-session probe, cache misses only). For an instance
	// literally titled "term" the two names coincide — the "legacy" session IS
	// the one being ensured, so leave it for the restore logic below.
	if legacy := tmux.NewSession(t.baseContext(), "term_"+instance.Title, shell); legacy.Name() != termName && legacy.DoesSessionExist() {
		if err := legacy.Close(); err != nil {
			log.InfoLog.Printf("terminal pane: failed to reap legacy session %s: %v", legacy.Name(), err)
		}
	}

	ts := tmux.NewSessionWithName(t.baseContext(), termName, "term: "+instance.Title, shell)

	// Check if session already exists (e.g. from a previous run)
	if ts.DoesSessionExist() {
		if err := ts.Restore(); err != nil {
			// Session exists but can't restore, kill it and start fresh
			_ = ts.Close()
			ts = tmux.NewSessionWithName(t.baseContext(), termName, "term: "+instance.Title, shell)
			if err := ts.Start(cwd); err != nil {
				return "", fmt.Errorf("terminal pane: failed to start session: %w", err)
			}
		}
	} else {
		if err := ts.Start(cwd); err != nil {
			return "", fmt.Errorf("terminal pane: failed to start session: %w", err)
		}
	}

	if t.beforeInstall != nil {
		t.beforeInstall()
	}

	// Re-read the status OUTSIDE t.mu: Paused() takes the instance's own lock, and
	// t.mu is the lock String() holds for a whole render.
	//
	// It is not redundant with reapGen, and the reason is no longer the one #701
	// wrote here. That comment said handlePauseDone returns early on a failed pause
	// and never reaches its reap; since #707 it reaps that path too — but only when
	// the pause actually freed the worktree. The branch that keeps it (a failed WIP
	// commit, session/pause.go) reaps nothing by design, so it bumps no generation
	// while still ending Paused; the entry check above cannot see it either, because
	// the flip lands mid-round-trip. This re-read is what stops that call caching a
	// shell against a session already parked. reapGen covers the other direction: a
	// kill sets no status at all, so its reap is the only signal there is.
	pausedNow := instance.Paused()

	// ts is not always a session this call started — the branch above adopts a live
	// <key>_term — so closing it has to be right for an adopted one too. All three
	// arms are about THIS shell: its own key was reaped, the pane that would own it
	// is gone, or its instance is Paused with the worktree it sits in removed. A
	// per-key generation is what keeps a fourth, wrong arm out of this set.
	t.mu.Lock()
	if t.reapGen[key] != gen || t.closeGen != closeGen || pausedNow {
		t.mu.Unlock()
		if err := ts.Close(); err != nil {
			log.InfoLog.Printf("terminal pane: failed to close a shell reaped mid-create for %s: %v", key, err)
		}
		return "", nil
	}
	t.sessions[key] = &terminalSession{tmuxSession: ts, cwd: cwd}
	width, height := t.width, t.height
	t.mu.Unlock()

	// Set the size
	if width > 0 && height > 0 {
		if err := ts.SetDetachedSize(width, height); err != nil {
			log.InfoLog.Printf("terminal pane: failed to set size: %v", err)
		}
	}

	return key, nil
}

// Attach attaches to the terminal tmux session (full-screen).
func (t *TerminalPane) Attach() (chan struct{}, error) {
	t.mu.Lock()
	s, ok := t.sessions[t.currentKey]
	if !ok || s.tmuxSession == nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("no terminal session to attach to")
	}
	if !s.tmuxSession.DoesSessionExist() {
		t.mu.Unlock()
		return nil, fmt.Errorf("terminal session does not exist")
	}
	ts := s.tmuxSession
	t.mu.Unlock()
	// Terminal-tab shell: do not intercept Ctrl+X — it's a normal editing key here.
	return ts.Attach(false)
}

// Close kills all cached terminal tmux sessions and cleans up.
func (t *TerminalPane) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// An EnsureSession still in its round trip must not install into the emptied map.
	// Its key may never have been cached, so this is the epoch rather than a per-key
	// bump; dropping the per-key counts with it keeps that map bounded by the
	// instances live at any one time, and any create that snapshotted one is already
	// refused by the epoch.
	t.closeGen++
	t.reapGen = nil
	for title, s := range t.sessions {
		if s.tmuxSession != nil {
			if err := s.tmuxSession.Close(); err != nil {
				log.InfoLog.Printf("terminal pane: failed to close session for %s: %v", title, err)
			}
		}
	}
	t.sessions = make(map[string]*terminalSession)
	t.currentKey = ""
	t.content = ""
	t.fallback = false
	t.splash = false
	t.fallbackMessage = ""
}

// CloseForInstance kills the cached terminal session for a specific instance.
//
// It can only close what is already cached, which is why it also bumps this key's
// reap generation — unconditionally, and the empty-cache case is the one that
// matters. The pause and kill done-handlers call this the moment their teardown
// lands; a shell whose EnsureSession is still mid-round-trip is not in the map yet,
// so without the bump the reap would silently no-op and the install would then cache
// a shell in a worktree that no longer exists (#701).
//
// Calling it for an instance with no cached shell is therefore cheap and harmless —
// a generation bump and a map miss — which is what lets app reap defensively on every
// teardown that freed a worktree rather than only on the ones that succeeded (#707).
// Nothing else sweeps: Close has no production caller, so a shell missed here outlives
// the process and the next run's EnsureSession adopts it.
func (t *TerminalPane) CloseForInstance(inst *session.Instance) {
	if inst == nil {
		return
	}
	key := terminalKey(inst)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reapGen == nil {
		t.reapGen = make(map[string]uint64)
	}
	t.reapGen[key]++
	if s, ok := t.sessions[key]; ok {
		if s.tmuxSession != nil {
			if err := s.tmuxSession.Close(); err != nil {
				log.InfoLog.Printf("terminal pane: failed to close session for %s: %v", key, err)
			}
		}
		delete(t.sessions, key)
	}
	if t.currentKey == key {
		t.currentKey = ""
		t.content = ""
		t.fallback = false
		t.splash = false
		t.fallbackMessage = ""
	}
}

func (t *TerminalPane) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	width := t.width
	height := t.height

	if width == 0 || height == 0 {
		return strings.Repeat("\n", height)
	}

	if t.isScrolling {
		return t.viewport.View()
	}

	// Gated at the call site, not inside splashScene, so the screensaver that
	// shares that function keeps animating (#316); off falls through to the
	// plain fallback below, which setSplashState has already armed.
	if t.splash && splashEnabled() && splashFits(width, height) {
		return splashScene(width, height, t.splashFrame, t.splashMessage)
	}

	fallback := t.fallback
	fallbackMessage := t.fallbackMessage
	content := t.content

	if fallback {
		// Composed here, against the live width, exactly like the preview pane (#355):
		// these messages outrun a narrow pane too ("Session is paused. Resume to use
		// terminal." is 42 cols against a reachable 28). Center it in the pane's exact
		// box, the same way the preview and diff panes center their placeholders. The
		// hand-rolled padding this replaces subtracted the tab/frame chrome a second
		// time (height-3-4) even though TabbedWindow.SetSize had already removed it, so
		// the banner sat high rather than at true center. centerInBox clamps both axes
		// on its own, so the outer clamp that used to wrap this call was redundant.
		return centerInBox(width, height,
			terminalPaneStyle().Render(fallbackBlock(width, height, fallbackMessage)))
	}

	// Normal mode: show captured content
	lines := strings.Split(content, "\n")

	if height > 0 {
		if len(lines) > height {
			lines = lines[len(lines)-height:]
		} else {
			padding := height - len(lines)
			lines = append(lines, make([]string, padding)...)
		}
	}

	contentStr := strings.Join(lines, "\n")
	return terminalPaneStyle().Width(width).Render(contentStr)
}

// liveCurrentSession returns the session for the current key when it exists and its
// tmux session is alive, else (nil, false). It is the shared existence guard for the
// capture paths (UpdateContent, enterScrollMode). Caller must hold t.mu. (Attach's
// own existence check is deliberately separate — see there.)
func (t *TerminalPane) liveCurrentSession() (*terminalSession, bool) {
	s, ok := t.sessions[t.currentKey]
	if !ok || s.tmuxSession == nil || !s.tmuxSession.DoesSessionExist() {
		return nil, false
	}
	return s, true
}

// enterScrollMode captures the full terminal history and enters scroll mode.
// Caller must hold t.mu.
func (t *TerminalPane) enterScrollMode() error {
	s, ok := t.liveCurrentSession()
	if !ok {
		return nil
	}

	content, err := s.tmuxSession.CapturePaneContentWithOptions("-", "-")
	if err != nil {
		return fmt.Errorf("terminal pane: failed to capture full history: %w", err)
	}
	content = theme.SanitizeWidth(content)

	footer := terminalFooterStyle().Render("— snapshot · ESC to resume live view")
	contentWithFooter := lipgloss.JoinVertical(lipgloss.Left, content, footer)
	t.viewport.SetContent(contentWithFooter)
	t.viewport.GotoBottom()
	t.isScrolling = true
	t.scrollKey = t.currentKey
	return nil
}

// exitScrollModeLocked returns the pane to the live per-tick view, keeping
// isScrolling and the snapshot's owning title in lockstep. Caller must hold t.mu.
func (t *TerminalPane) exitScrollModeLocked() {
	t.isScrolling = false
	t.scrollKey = ""
	t.viewport.SetContent("")
	t.viewport.GotoTop()
}

// ScrollUp enters scroll mode (if not already) and scrolls up.
func (t *TerminalPane) ScrollUp() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.isScrolling {
		return t.enterScrollMode()
	}
	t.viewport.ScrollUp(1)
	return nil
}

// ScrollDown scrolls down within an existing snapshot; from the live view it is
// a no-op (entry is ScrollUp's job — see PreviewPane.ScrollDown). A wheel-down
// while the snapshot is already at its bottom leaves scroll mode instead (tmux
// copy-mode style); the next UpdateContent tick repaints the live shell.
func (t *TerminalPane) ScrollDown() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.isScrolling {
		return nil
	}
	// The mutex is already held here, so exit directly — ResetToNormalMode would
	// re-lock t.mu and deadlock.
	if t.viewport.AtBottom() {
		t.exitScrollModeLocked()
		return nil
	}
	t.viewport.ScrollDown(1)
	return nil
}

// ResetToNormalMode exits scroll mode and restores normal content display.
func (t *TerminalPane) ResetToNormalMode() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.isScrolling {
		return
	}
	t.exitScrollModeLocked()
}

// IsScrolling returns whether the terminal pane is in scroll mode.
func (t *TerminalPane) IsScrolling() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.isScrolling
}
