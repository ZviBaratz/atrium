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

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// terminalPaneStyle / terminalFooterStyle read the active theme at render time.
func terminalPaneStyle() lipgloss.Style   { return theme.Current().FgStyle() }
func terminalFooterStyle() lipgloss.Style { return theme.Current().DimStyle() }

// terminalSession holds a cached tmux session for a specific instance.
type terminalSession struct {
	tmuxSession *tmux.Session
	cwd         string
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
	content       string
	fallback      bool
	fallbackText  string
	// splash is true only for the idle empty screen (nil instance): String() then
	// renders the animated nebula behind the wordmark. Implies fallback.
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
		viewport: viewport.New(0, 0),
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
	t.viewport.Width = width
	t.viewport.Height = height
	if s, ok := t.sessions[t.currentKey]; ok && s.tmuxSession != nil {
		if err := s.tmuxSession.SetDetachedSize(width, height); err != nil {
			log.InfoLog.Printf("terminal pane: failed to set detached size: %v", err)
		}
	}
}

// SetSplashFrame stores the current splash animation frame, pushed from the
// app's 100ms tick. It only affects the idle-splash render in String().
func (t *TerminalPane) SetSplashFrame(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.splashFrame = n
}

// setFallbackState sets the terminal pane to display a fallback message.
// Caller must hold t.mu.
func (t *TerminalPane) setFallbackState(message string) {
	t.fallback = true
	t.splash = false
	t.fallbackText = lipgloss.JoinVertical(lipgloss.Center, FallbackBanner(), "", message)
	t.content = ""
}

// setSplashState is setFallbackState for the idle empty screen (nil instance),
// additionally flagging the splash so String() renders the animated nebula behind
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

	// Ensure we have a terminal session for this instance
	if err := t.ensureSessionLocked(instance); err != nil {
		return err
	}

	s, ok := t.liveCurrentSession()
	if !ok {
		t.setFallbackState("Terminal session not available.")
		return nil
	}

	content, err := s.tmuxSession.CapturePaneContent()
	if err != nil {
		return fmt.Errorf("terminal pane: failed to capture content: %w", err)
	}

	t.fallback = false
	t.splash = false
	// Decompose font-dependent emoji clusters so our laid-out width matches the
	// terminal's rendered width and the pane can't wrap (see theme.SanitizeWidth).
	t.content = theme.SanitizeWidth(content)
	return nil
}

// ensureSessionLocked creates or reuses a cached terminal tmux session for the
// given instance. Caller must hold t.mu.
func (t *TerminalPane) ensureSessionLocked(instance *session.Instance) error {
	if instance == nil || !instance.Started() || instance.Paused() {
		return nil
	}

	// Host the shell in the same cwd as the agent: the worktree for a git session, or
	// Path for a direct (non-git) session. GetWorktreePath() would be "" for a direct
	// session and wrongly skip terminal creation, so use WorkingDir().
	cwd := instance.WorkingDir()
	if cwd == "" {
		return nil
	}
	key := terminalKey(instance)
	if key == "" {
		// No persisted tmux name (an instance fabricated without Start, e.g. in
		// tests): there is no stable key to cache a shell under.
		return nil
	}

	t.currentKey = key

	// Check if we already have a cached session for this instance
	if s, ok := t.sessions[key]; ok {
		if s.tmuxSession != nil && s.tmuxSession.DoesSessionExist() {
			return nil
		}
		// Session died, remove stale entry and recreate below
		delete(t.sessions, key)
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
				return fmt.Errorf("terminal pane: failed to start session: %w", err)
			}
		}
	} else {
		if err := ts.Start(cwd); err != nil {
			return fmt.Errorf("terminal pane: failed to start session: %w", err)
		}
	}

	t.sessions[key] = &terminalSession{
		tmuxSession: ts,
		cwd:         cwd,
	}

	// Set the size
	if t.width > 0 && t.height > 0 {
		if err := ts.SetDetachedSize(t.width, t.height); err != nil {
			log.InfoLog.Printf("terminal pane: failed to set size: %v", err)
		}
	}

	return nil
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
	t.fallbackText = ""
}

// CloseForInstance kills the cached terminal session for a specific instance.
func (t *TerminalPane) CloseForInstance(inst *session.Instance) {
	if inst == nil {
		return
	}
	key := terminalKey(inst)
	t.mu.Lock()
	defer t.mu.Unlock()
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
		t.fallbackText = ""
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

	if t.splash && splashFits(width, height) {
		return splashScene(width, height, t.splashFrame, t.splashMessage)
	}

	fallback := t.fallback
	fallbackText := t.fallbackText
	content := t.content

	if fallback {
		// Center the fallback in the pane's exact box, the same way the preview
		// and diff panes center their placeholders. The hand-rolled padding this
		// replaces subtracted the tab/frame chrome a second time (height-3-4) even
		// though TabbedWindow.SetSize had already removed it, so the banner sat
		// high rather than at true center. Clamp both axes like the preview pane:
		// lipgloss.Place does not clip oversize content, so a fallback line wider
		// than a narrow pane would inflate the whole frame and throw every
		// centered overlay off.
		return lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(
			centerInBox(width, height, terminalPaneStyle().Render(fallbackText)))
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
	t.viewport.LineUp(1)
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
	t.viewport.LineDown(1)
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
