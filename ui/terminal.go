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
	sessions      map[string]*terminalSession // terminalKey (the shell's own tmux name) → session
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
	// call created — the branch above it adopts a live shell left by a crashed
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
	//
	// Keyed by terminalKey (not pointer, as PreviewPane does) to match the sessions
	// map. The key holds still for as long as a shell is cached under it, because it
	// is owned rather than derived (see terminalKey); a reap releases it and the next
	// claim mints a fresh one. Drift is survivable either way and was never the
	// hazard here: the guard below treats a moved key exactly like a changed
	// selection and drops the snapshot, where the sessions map instead loses a live
	// shell (#708).
	scrollKey string
	viewport  viewport.Model
}

// terminalKey is the cache key for an instance's terminal shell, and it is the shell's own
// tmux session name rather than anything derived from it. Unlike Title it is unique across
// repo groups (same-titled sessions are legal in different groups), and unlike the agent's
// tmux name it does not move: the instance mints it once and owns it from then on
// (session/termname.go), which is what keeps every lookup, capture and reap pointed at the
// same shell across a deep rename (#708).
//
// The mint is the fallback only before a shell has ever been claimed, and that case is safe
// for the reason the claimed one is not: nothing a create depends on is filed under it. The
// sessions map has no entry, no snapshot names it, and a create claims its own key before it
// snapshots any generation. CloseForInstance does bump a generation under a minted key for
// an instance with no shell, which is inert — the create that would consult it claims first,
// and reads the count under the key it claimed.
func terminalKey(i *session.Instance) string {
	if name := i.TerminalSessionName(); name != "" {
		return name
	}
	return i.MintTerminalSessionName()
}

// terminalShellProgram is the program a terminal-tab shell runs: the user's own login
// shell, or /bin/sh where $SHELL is unset. One home, because the reap path builds a
// Session for the same tmux name the create path does and tmux resolves a session by
// name — but a Session carries its program, and two spellings of "the shell" is exactly
// the kind of split that only shows up as a shell nobody can explain.
func terminalShellProgram() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

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
	// Claim the shell's name here, before the tmux round trip below rather than at install
	// time, and this is the only site that may claim. Claiming pins the key for the whole
	// call: an AdoptRename landing mid-round-trip moves the agent's tmux name but not this,
	// so the entry is filed under the key CloseForInstance and every later lookup compute.
	// Claiming at install instead would leave the generation snapshot below reading one key
	// and the reap bumping another, which is #701's window re-opened by a rename (#708).
	//
	// Under t.mu, and in the same critical section as the snapshot, because that is what
	// makes a create and a concurrent reap agree about WHICH key they are talking about.
	// CloseForInstance bumps and releases under the same lock, so the two possible
	// interleavings are the two that are safe: claim first and this call reads the
	// pre-bump count, then fails its install re-check; reap first and this call claims a
	// fresh name whose count it snapshots from zero. Claiming outside the lock allows a
	// third — snapshot the already-bumped count, pass the re-check, and install under a
	// name the instance no longer owns.
	//
	// Both generations are snapshotted here too, and deliberately before the stale-entry
	// delete below: that delete is this call's own bookkeeping, not a reap, so bumping
	// there would make every recreate refuse its own install.
	t.mu.Lock()
	key, minted := instance.ClaimTerminalSessionName()
	cached, ok := t.sessions[key]
	gen, closeGen := t.reapGen[key], t.closeGen
	t.mu.Unlock()
	if key == "" {
		// No persisted tmux name (an instance fabricated without Start, e.g. in
		// tests): there is nothing to mint a shell name from.
		return "", nil
	}

	// releaseIfMinted hands a name this call minted back when the call ends with no shell
	// on it. Without it a create that fails, or one whose install is refused, leaves the
	// instance owning — and persisting — a name nothing hosts, and the collision guards go
	// on reserving that title against new sessions on behalf of a shell that never existed.
	// A name the instance already owned is deliberately left alone: a shell may still be
	// sitting on it, and the owned name is the only thing that names it.
	releaseIfMinted := func() {
		if minted {
			instance.ReleaseTerminalSessionName()
		}
	}
	if ok && cached.tmuxSession != nil {
		if cached.tmuxSession.DoesSessionExist() {
			return key, nil
		}
		// Session died; drop the stale entry and recreate below.
		t.mu.Lock()
		delete(t.sessions, key)
		t.mu.Unlock()
	}

	shell := terminalShellProgram()

	// Shells were keyed term_<title> before tmux names became persisted state;
	// that name is unreachable under the new key, so a shell left from a
	// pre-upgrade run would idle on the socket forever. Reap it here on the
	// create path (one has-session probe, cache misses only). For an instance
	// literally titled "term" the two names coincide — the "legacy" session IS
	// the one being ensured, so leave it for the restore logic below.
	if legacy := tmux.NewSession(t.baseContext(), "term_"+instance.Title, shell); legacy.Name() != key && legacy.DoesSessionExist() {
		if err := legacy.Close(); err != nil {
			log.InfoLog.Printf("terminal pane: failed to reap legacy session %s: %v", legacy.Name(), err)
		}
	}

	// key IS the shell's tmux session name — minted from the instance's own (unique,
	// repo-qualified) tmux name plus session.TermSessionSuffix, which CleanupSessions
	// prefix-matches and the new-session/rename guards reserve so no agent session can
	// claim it. One name, one home: the cache key and the session name are the same fact,
	// so neither can drift from the other. The window name is cosmetic.
	ts := tmux.NewSessionWithName(t.baseContext(), key, "term: "+instance.Title, shell)

	// Adopt a shell already sitting on this name — the previous run's, since a shell is
	// meant to outlive Atrium — and recreate it when it cannot be restored.
	adopted := false
	if ts.DoesSessionExist() {
		if err := ts.Restore(); err == nil {
			adopted = true
		} else {
			// Session exists but can't restore, kill it and start fresh
			_ = ts.Close()
			ts = tmux.NewSessionWithName(t.baseContext(), key, "term: "+instance.Title, shell)
		}
	}
	// One exit for a create that could not start, rather than one per branch. Both used to
	// carry their own copy of this, and a mutation of either was invisible to the other's
	// test — a name left owned on the harder-to-reach branch is the same permanent
	// reservation against a title as one left on the easy one.
	if !adopted {
		if err := ts.Start(cwd); err != nil {
			releaseIfMinted()
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

	// ts is not always a session this call started — the branch above adopts a live session
	// already sitting on key — so closing it has to be right for an adopted one too. All
	// three arms are about THIS shell: its own key was reaped, the pane that would own it
	// is gone, or its instance is Paused with the worktree it sits in removed. A per-key
	// generation is what keeps a fourth, wrong arm out of this set.
	t.mu.Lock()
	if t.reapGen[key] != gen || t.closeGen != closeGen || pausedNow {
		// Under the lock, for the reason the claim above is: the release and the reap that
		// refused this install must not interleave with another call's claim.
		releaseIfMinted()
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
//
// Unlike CloseForInstance it cannot release the owned names it invalidates — it holds keys,
// not instances. That costs nothing: a released name only changes which name the NEXT claim
// mints, and a claim finding its owned session gone recreates it under that name (the
// DoesSessionExist branch in EnsureSession). It has no production caller either way.
func (t *TerminalPane) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// An EnsureSession still in its round trip must not install into the emptied map.
	// Its key may never have been cached, so this is the epoch rather than a per-key
	// bump; the per-key counts are dropped with it because any create that snapshotted
	// one is already refused by the epoch.
	//
	// It is the only thing that drops them, and it has no production caller, so nothing
	// in a running TUI ever shrinks reapGen. Since #708 the keys churn — a reap releases
	// the owned name and the next claim mints another — so the map grows by one entry per
	// shell an instance has over the session's life rather than holding at one per live
	// instance. Two strings and a counter each; worth knowing, not worth a sweep, and a
	// sweep would have to prove no in-flight create had snapshotted the entry it dropped.
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
// Calling it for an instance with no cached shell is therefore cheap and harmless — a
// generation bump and a map miss — which is what lets app reap defensively on every
// teardown that freed a worktree rather than only on the ones that succeeded (#707).
// Nothing else sweeps: Close has no production caller, so a shell missed here outlives the
// process and the next run's EnsureSession adopts it.
//
// That last sentence is why an uncached OWNED name is not a miss but a probe. A shell is
// meant to outlive Atrium, so a restored instance can own one that this run's pane has
// never opened and therefore has no entry for; the owned name is then the only record of
// it anywhere. Reaping by name is the same shape as session.releaseRunTmux, and for the
// same reason — dropping the name instead would strand the shell permanently, which is the
// bug (#708), not the fix.
//
// The release follows the reap and not the other way round: the name is given up only once
// nothing of ours is left on it, so a Close that failed keeps its pointer. A held name
// costs a stale title on the next shell and a reservation against that title; a released
// one costs the shell.
func (t *TerminalPane) CloseForInstance(inst *session.Instance) {
	if inst == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Owned and minted are different facts here. owned=="" means this instance has never
	// had a shell, so there is nothing on the socket to look for; the bump still lands on
	// the name a create WOULD claim, because an in-flight create claims that exact name
	// and reads its count.
	owned := inst.TerminalSessionName()
	key := owned
	if key == "" {
		key = inst.MintTerminalSessionName()
	}
	if t.reapGen == nil {
		t.reapGen = make(map[string]uint64)
	}
	t.reapGen[key]++
	reaped := true
	if s, ok := t.sessions[key]; ok {
		if s.tmuxSession != nil {
			if err := s.tmuxSession.Close(); err != nil {
				log.InfoLog.Printf("terminal pane: failed to close session for %s: %v", key, err)
				reaped = false
			}
		}
		delete(t.sessions, key)
	} else if owned != "" {
		probe := tmux.NewSessionWithName(t.baseContext(), owned, "term: "+inst.Title, terminalShellProgram())
		if probe.DoesSessionExist() {
			if err := probe.Close(); err != nil {
				log.InfoLog.Printf("terminal pane: failed to close uncached session %s: %v", owned, err)
				reaped = false
			}
		}
	}
	if reaped {
		inst.ReleaseTerminalSessionName()
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
