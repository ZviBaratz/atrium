package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// The user's own verbs (#375): `!` opens a menu of the custom_commands declared in
// config.json, and each entry's own key runs its shell template against the selected
// session.
//
// The division of labour is the same one the palette uses. customcmd owns what a
// valid entry is and how a template renders; overlay.CustomCommandsOverlay owns the
// box; this file owns the three things only the app knows — which rows can run right
// now, what running one means, and where the resulting process is recorded.

// customCommandRow is what an open menu's row index means: the command that index
// runs, and the reason it could not when the menu was built.
//
// Kept beside the overlay's own rows rather than derived from m.customCommands by
// index, for the reason paletteRows exists: it is rebuilt on every open and dropped
// on close, so a stale index can never resolve to a command.
type customCommandRow struct {
	cmd   customcmd.Command
	inert string
}

// customCommandSpec is everything a run needs, resolved to strings.
//
// The type is the thread-safety argument, not a comment about it. Instance's Title,
// Branch, displayName and Path are unguarded fields read on the update thread, so a
// tea.Cmd that closed over an *Instance and read them in a goroutine would race
// every rename and every restore. Rendering the template, building the environment
// and resolving the argv all happen on the update thread; only this struct crosses
// over. TestCustomCommandSpecCarriesOnlyStrings is what keeps that true.
type customCommandSpec struct {
	key     string
	desc    string
	script  string
	dir     string
	session string
	env     []string
	// argv is what the command log records — see customcmd.Command.LogArgv, and the
	// comment at the swap in execCustomCommand.
	argv []string
}

// runCustomCommandMsg starts a run that a confirmation dialog approved.
//
// It exists because neither way handleConfirmState can run an action fits. A named
// busyLabel routes through beginAsyncAction, whose actionInFlight gate freezes every
// mutating key for the duration — the opposite of what a background command wants.
// instantAction runs the closure inline on the update thread, which for a three-minute
// build would freeze the whole TUI. So the confirmed action is an instantAction that
// only returns this, and Update starts the real work from here.
type runCustomCommandMsg struct{ spec customCommandSpec }

// customCommandDoneMsg reports a finished run. Every exit path in the goroutine
// returns one; see startCustomCommand for why that is load-bearing.
type customCommandDoneMsg struct {
	key  string
	desc string
	err  error
}

// runCustomCommand is the exec leg of a custom command.
//
// A package var so tests can substitute a recorder and assert that a refused row
// spawns no process — the internal/actions.CopyToClipboard seam. A gate that only
// stops the notice, not the subprocess, would pass every assertion about what the
// screen says while still running the command.
var runCustomCommand = execCustomCommand

// openCustomCommands builds the menu over the validated commands, gating each row
// against the current selection.
func (m *home) openCustomCommands() (tea.Model, tea.Cmd) {
	inst := m.list.GetSelectedInstance()
	ctx := customCommandCtx(inst)

	m.customCommandRows = make([]customCommandRow, 0, len(m.customCommands))
	rows := make([]overlay.CustomCommandRow, 0, len(m.customCommands))
	for _, c := range m.customCommands {
		inert := customCommandInertReason(c, inst, ctx)
		m.customCommandRows = append(m.customCommandRows, customCommandRow{cmd: c, inert: inert})
		rows = append(rows, overlay.CustomCommandRow{
			Key:         c.Key,
			Description: c.Description,
			Repo:        c.Context == customcmd.ContextRepo,
			Inert:       inert,
		})
	}

	m.customCommandsOverlay = overlay.NewCustomCommandsOverlay(rows)
	m.state = stateCustomCommands
	// Sized here rather than by returning tea.RequestWindowSize: reached from the
	// command palette, both states hide the hint bar, so Update's before/after
	// comparison sees no change and fires no recompute — and an overlay left at its
	// constructor size can be wider than the terminal, which makes PlaceOverlay
	// return the box alone and break the frame's exact-width invariant.
	m.recomputeLayout()
	return m, nil
}

// handleCustomCommandsState routes a key to the menu and runs what it chose.
func (m *home) handleCustomCommandsState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	chosen, shouldClose := m.customCommandsOverlay.HandleKeyPress(msg)
	if !shouldClose {
		// An inert row answered inside the box. Deliberately not routed through
		// flashNotice: from a bar-hiding overlay that falls through to the error
		// box and recomputes the height budget under a live overlay.
		return m, nil
	}

	var chosenCmd customcmd.Command
	var found bool
	if chosen >= 0 && chosen < len(m.customCommandRows) {
		chosenCmd, found = m.customCommandRows[chosen].cmd, true
	}

	// The menu goes before anything that runs or says something. A confirmation sets
	// stateConfirm, and dismissing afterwards would reset it to stateDefault and
	// orphan the dialog; a notice from here would reach flashNotice while the bar is
	// still hidden.
	m.dismissCustomCommands()
	if !found {
		return m, nil
	}
	return m.launchCustomCommand(chosenCmd)
}

// dismissCustomCommands tears down the menu and returns to the list. Same shape as
// dismissCommandPalette, including the menu state: closing without it leaves the
// frame a row taller than the terminal forever (#518).
func (m *home) dismissCustomCommands() {
	m.customCommandsOverlay = nil
	m.customCommandRows = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
}

// launchCustomCommand resolves a command against the live selection and either runs
// it, asks first, or refuses with a reason.
func (m *home) launchCustomCommand(c customcmd.Command) (tea.Model, tea.Cmd) {
	spec, reason := m.customCommandSpec(c)
	if reason != "" {
		return m, m.handleInfoNotice(reason)
	}
	if c.Confirm {
		cmd := m.confirmAction(
			fmt.Sprintf("Run %s in %s?", quoteDesc(c.Description), filepath.Base(spec.dir)),
			// instantAction, and it must stay one: the closure returns a message and
			// nothing else, so the work it names starts from Update.
			instantAction,
			func() tea.Msg { return runCustomCommandMsg{spec: spec} },
		)
		m.confirmationOverlay.SetConfirmLabel("run")
		return m, cmd
	}
	return m, m.startCustomCommand(spec)
}

// startCustomCommand puts a resolved command on the progress row and runs it off the
// UI thread.
//
// beginBackgroundAction, never beginAsyncAction: a user's own verb must not gate
// every other key for as long as it runs. The cost of that choice is that nothing
// else serializes the runs, which is what runningCustomCommand is for —
// ui.BusyBackground is a single shared slot, so a second concurrent run would have
// the row name one command while a shorter one finishes and clears it.
func (m *home) startCustomCommand(spec customCommandSpec) tea.Cmd {
	if m.runningCustomCommand != "" {
		return m.handleInfoNotice(fmt.Sprintf(
			"%s is still running — one custom command at a time", quoteDesc(m.runningCustomCommand)))
	}
	m.runningCustomCommand = spec.key
	// Captured here, not read from m inside the goroutine.
	ctx := m.ctx
	return m.beginBackgroundAction(fmt.Sprintf("running %s…", quoteDesc(spec.desc)), func() tea.Msg {
		if msg := runCustomCommand(ctx, spec); msg != nil {
			return msg
		}
		// Every path has to yield a done message. beginBackgroundAction's wrapper
		// passes the inner result through Update, and the runtime drops a nil
		// message — so a silent exit would leave the single-flight latch set for the
		// rest of the process, with the progress row already cleared. That is
		// indistinguishable from a bug from the outside, which is why the guarantee
		// lives here rather than in each return inside the seam.
		return customCommandDoneMsg{key: spec.key, desc: spec.desc}
	})
}

// handleCustomCommandDone releases the single-flight latch and reports a failure.
//
// A success says nothing: the command log has the record, and a toast per run would
// be noise for the "open lazygit" case this feature exists for. A failure must speak
// — the output went to a buffer the user never sees.
//
// The message is bounded so it stays a TOAST. handleError routes anything a one-line
// row cannot show to the persistent info modal, and the description is user-authored
// with no ceiling — so the unbounded spelling took over the whole screen on every
// failure of a command the user deliberately ran in the BACKGROUND. Found by running
// it, not by reading it. What is dropped is the exit code, which the log record
// carries; what is kept is which command failed and where to look.
func (m *home) handleCustomCommandDone(msg customCommandDoneMsg) (tea.Model, tea.Cmd) {
	m.runningCustomCommand = ""
	if msg.err == nil {
		return m, nil
	}
	// The full error, with the exit code, still reaches the log even though the row
	// cannot carry it.
	log.ErrorLog.Printf("custom command %q (%s) failed: %v", msg.key, msg.desc, msg.err)
	return m, m.handleError(errors.New(customCommandFailureNotice(msg.desc)))
}

// customCommandFailureNotice is the failure toast, provably no wider than
// customCommandNoticeWidth cells. TestCustomCommandFailureNoticeFitsARow asserts that
// against a pathological description rather than trusting the arithmetic.
// Truncated by DISPLAY WIDTH, not by rune count: a description in CJK is two cells per
// rune, so a rune-count bound lets a 30-rune description render 60 cells wide and land
// back in the modal it was written to avoid.
func customCommandFailureNotice(desc string) string {
	// Built from the opening quote and the suffix rather than through quoteDesc, so the
	// closing quote is counted once, by the constant that bounds it.
	return "'" + runewidth.Truncate(desc, customCommandNoticeDescWidth, "…") +
		customCommandFailureSuffix
}

// customCommandCtx is the template and environment context for the selected session.
//
// Session.Worktree is empty unless the session's worktree directory actually exists,
// and that is the single most load-bearing line here. WorkingDir() falls back to
// Instance.Path whenever the worktree pointer is nil — which it is before Start —
// and Path is the user's ORIGIN CHECKOUT. A repo-context row is gated on having a
// selection and nothing more, so `git -C {{.Session.Worktree}} clean -xfd` fired at
// an unstarted session would run in the real repository. The same predicate covers
// the paused case, where the pointer survives but the directory does not.
//
// Repo.Name comes from GroupKey() rather than RepoName(), which returns an error for
// a not-yet-started instance. GroupKey's cold path shells out to git, but the list
// render warms its cache every frame.
func customCommandCtx(inst *session.Instance) customcmd.Ctx {
	if inst == nil {
		return customcmd.Ctx{}
	}
	ctx := customcmd.Ctx{
		Session: customcmd.SessionCtx{
			Title:  inst.Title,
			Name:   inst.DisplayName(),
			Branch: inst.Branch,
		},
		Repo: customcmd.RepoCtx{
			Path: inst.GetRepoPath(),
			Name: inst.GroupKey(),
		},
	}
	if inst.Started() && !inst.Paused() {
		ctx.Session.Worktree = inst.WorkingDir()
	}
	// GetRepoPath is empty for a direct session and for an unstarted git one; Path is
	// the repository root in both cases, which is what repo context asks for.
	if ctx.Repo.Path == "" {
		ctx.Repo.Path = inst.Path
	}
	return ctx
}

// customCommandDir is the directory a command runs in.
func customCommandDir(c customcmd.Command, ctx customcmd.Ctx) string {
	if c.Context == customcmd.ContextRepo {
		return ctx.Repo.Path
	}
	return ctx.Session.Worktree
}

// customCommandInertReason reports why a command cannot run against inst, or "".
//
// It follows the palette gate table's rules — the handler stays authoritative, and a
// row is dimmed only when the refusal is CERTAIN — with one addition the palette has
// no equivalent for: a command whose template names a context field this selection
// leaves empty is refused, because rendering it would silently hand a shell a flag
// with no value.
//
// The session-context test is Started() && !Paused(), NOT the palette's
// GetStatus() != Loading. Those are different predicates and the difference is the
// whole hazard: a Ready instance that has not been started is constructible (see
// newGateInstance), and for one of those WorkingDir() is the user's origin checkout.
func customCommandInertReason(c customcmd.Command, inst *session.Instance, ctx customcmd.Ctx) string {
	if inst == nil {
		return noSelectionReason
	}
	if c.Context == customcmd.ContextSession {
		if !inst.Started() {
			return stillStartingReason
		}
		if inst.Paused() {
			return pausedWorktreeReason
		}
	}
	if customCommandDir(c, ctx) == "" {
		return noDirectoryReason
	}
	if missing := c.MissingFields(ctx); len(missing) > 0 {
		return customCommandMissingReason(missing[0])
	}
	return ""
}

// customCommandMissingReason is the chip-sized refusal for a context field the
// selection leaves empty. TestCustomCommandMissingReasonsCoverEveryField pins the
// table against what MissingFields can actually report.
func customCommandMissingReason(path string) string {
	if reason, ok := customCommandMissingReasons[path]; ok {
		return reason
	}
	return "no " + path
}

// customCommandMissingReasons names each context field in the vocabulary the palette
// gates already use, so the reasons read as one voice and fit the row.
var customCommandMissingReasons = map[string]string{
	"Session.Title":    "no title yet",
	"Session.Name":     "no name yet",
	"Session.Branch":   noBranchReason,
	"Session.Worktree": noWorktreeReason,
	"Repo.Path":        "no repo path",
	"Repo.Name":        "no repo name",
}

// customCommandSpec resolves a command against the live selection, on the update
// thread, or reports why it will not run.
//
// The gate runs again here rather than trusting the reason recorded when the menu was
// built: the selection can move under an open menu, and the palette's first rule is
// that the handler stays authoritative.
func (m *home) customCommandSpec(c customcmd.Command) (customCommandSpec, string) {
	inst := m.list.GetSelectedInstance()
	ctx := customCommandCtx(inst)
	if reason := customCommandInertReason(c, inst, ctx); reason != "" {
		return customCommandSpec{}, reason
	}

	script, err := c.Render(ctx)
	if err != nil {
		// Validation rendered this template against a fully-populated probe, so an
		// error here is about this selection, not the template.
		return customCommandSpec{}, fmt.Sprintf("%s could not be rendered: %v", quoteDesc(c.Description), err)
	}

	dir := customCommandDir(c, ctx)
	// Belt and braces over the gate above. The gate proves the session should have a
	// directory; this proves the directory is there, which a pause, an external `rm`
	// or a half-finished teardown can all falsify between the two.
	if st, statErr := os.Stat(dir); statErr != nil || !st.IsDir() {
		return customCommandSpec{}, fmt.Sprintf("%s: %s is gone", quoteDesc(c.Description), dir)
	}

	var sessionName string
	if inst != nil {
		sessionName = inst.Title
	}
	return customCommandSpec{
		key:     c.Key,
		desc:    c.Description,
		script:  script,
		dir:     dir,
		session: sessionName,
		env:     customcmd.Env(ctx),
		argv:    c.LogArgv(),
	}, ""
}

// execCustomCommand runs the resolved script and records it.
//
// Bound to ctx, which is the app's, so quitting cancels a runaway. There is no
// wall-clock timeout on purpose: `just ci` legitimately runs for minutes, and this
// feature exists to run exactly that.
func execCustomCommand(ctx context.Context, spec customCommandSpec) tea.Msg {
	c := exec.CommandContext(ctx, "sh", "-c", spec.script)
	c.Dir = spec.dir
	c.Env = append(os.Environ(), spec.env...)
	// One writer for both streams: os/exec guarantees at most one goroutine writes at
	// a time when Stdout and Stderr are the same comparable value, which a pointer is.
	out := &tailBuffer{limit: customCommandOutputCap}
	c.Stdout, c.Stderr = out, out

	start := time.Now()
	err := c.Run()

	// Record the synthetic argv, NEVER the rendered script. cmdlog.Redact models one
	// NAME=VALUE per argv token, and a whole shell script in a single token defeats it
	// in both directions: a bearer token inside a -H flag has no leading NAME= and is
	// stored verbatim, while a leading FOO=bar matches at the first '=' and returns
	// everything before it, throwing the command away. Swapping Args costs nothing —
	// RecordCmd reads Args at call time and takes the exit code from the error and the
	// CPU time from ProcessState, neither of which this touches.
	c.Args = spec.argv
	cmdlog.RecordCmd(c, spec.session, start, out.bytes(), err)
	if err != nil {
		log.ErrorLog.Printf("custom command %q failed: %v", spec.key, err)
	}
	return customCommandDoneMsg{key: spec.key, desc: spec.desc, err: err}
}

// tailBuffer keeps only the last limit bytes written to it.
//
// A custom command can stream for minutes, and the command log stores a tail of the
// output anyway — so the head is all an unbounded buffer would be holding, and it
// would hold it in the TUI's heap for the whole run.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte { return t.buf }

// customCommandProblemsReport is the startup modal's text: which configured entries
// validation refused, and why.
//
// Bounded on both axes. The count is capped because a config can hold any number of
// broken entries and the modal is a single scrollable overlay; each line is clipped
// because two of the three fields in a Problem are user-authored.
func customCommandProblemsReport(problems []customcmd.Problem) string {
	if len(problems) == 0 {
		return ""
	}
	lines := []string{fmt.Sprintf(
		"%d custom command%s in config.json %s ignored:",
		len(problems), plural(len(problems)), wereOrWas(len(problems)))}
	shown := problems
	if len(shown) > customCommandProblemsShown {
		shown = shown[:customCommandProblemsShown]
	}
	for _, p := range shown {
		lines = append(lines, "  "+clipReportLine(p.Error()))
	}
	if len(problems) > len(shown) {
		lines = append(lines, fmt.Sprintf("  … and %d more", len(problems)-len(shown)))
	}
	lines = append(lines, "", "The rest still work. `atrium doctor` reports the same list.")
	return strings.Join(lines, "\n")
}

// clipReportLine bounds one problem line. The info modal wraps rather than
// overflowing, so this is about keeping the report readable rather than about the
// frame: a 400-character description would otherwise wrap over the whole overlay.
func clipReportLine(s string) string {
	const limit = 100
	if len([]rune(s)) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

// wereOrWas keeps the report's first line grammatical for one entry as well as many.
func wereOrWas(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// flushCustomCommandProblems opens the startup report once the screen is free, in
// the shape flushPendingLaunchCrash uses — nil while an overlay owns the screen, and
// the buffer is cleared as it fires so the preview tick cannot reopen it forever.
func (m *home) flushCustomCommandProblems() tea.Cmd {
	if len(m.pendingCustomCommandProblems) == 0 || m.state != stateDefault {
		return nil
	}
	problems := m.pendingCustomCommandProblems
	m.pendingCustomCommandProblems = nil
	return m.showInfo(customCommandProblemsReport(problems))
}

// quoteDesc quotes a user-authored description for a one-line message, matching the
// kill dialog's 'name' style.
func quoteDesc(s string) string { return "'" + s + "'" }

const (
	// customCommandOutputCap is how much of a command's output is kept for the log's
	// failure tail.
	customCommandOutputCap = 64 << 10
	// customCommandProblemsShown caps the startup report's list.
	customCommandProblemsShown = 5

	// customCommandNoticeDescWidth bounds the description inside the failure toast, in
	// display cells.
	customCommandNoticeDescWidth = 30
	// customCommandFailureSuffix closes the failure toast.
	customCommandFailureSuffix = "' failed — press L for the output"
	// customCommandNoticeWidth is the toast's worst case: the clipped description plus
	// customCommandFailureChrome. Counting the chrome by eye is how this shipped one
	// cell short the first time, so TestCustomCommandFailureNoticeFitsARow measures the
	// literal instead of trusting the number.
	customCommandNoticeWidth = customCommandNoticeDescWidth + customCommandFailureChrome
	// customCommandFailureChrome is the opening quote plus the suffix above.
	customCommandFailureChrome = 34
)
