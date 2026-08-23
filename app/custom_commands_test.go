package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validCommands builds validated commands from wire entries, failing the test on any
// entry validation refuses. Every fixture goes through Validate because Command's
// template is unexported: a composite literal is a Command that nil-panics on Render.
func validCommands(t *testing.T, entries ...config.CustomCommand) []customcmd.Command {
	t.Helper()
	cmds, problems := customcmd.Validate(entries)
	require.Emptyf(t, problems, "fixture must validate cleanly: %v", problems)
	require.Len(t, cmds, len(entries))
	return cmds
}

// oneCommand is the common single-entry fixture.
func oneCommand(t *testing.T, e config.CustomCommand) customcmd.Command {
	t.Helper()
	return validCommands(t, e)[0]
}

// runRecord is one call the exec seam received.
type runRecord struct{ spec customCommandSpec }

// stubRunner replaces the exec seam for the duration of the test and returns the
// calls it saw. reply is what each call returns to the update loop.
//
// It is the seam that makes "refused" testable at all: a gate that only suppresses
// the notice would satisfy every assertion about the screen while still running the
// user's shell command.
func stubRunner(t *testing.T, reply func(spec customCommandSpec) tea.Msg) *[]runRecord {
	t.Helper()
	var calls []runRecord
	prev := runCustomCommand
	runCustomCommand = func(_ context.Context, spec customCommandSpec) tea.Msg {
		calls = append(calls, runRecord{spec: spec})
		if reply == nil {
			return customCommandDoneMsg{key: spec.key, desc: spec.desc}
		}
		return reply(spec)
	}
	t.Cleanup(func() { runCustomCommand = prev })
	return &calls
}

// newCustomCommandHome builds a home with a selected session and the given commands
// configured.
//
// Its instance is NOT started — NewInstance alone cannot be, and reaching Started()
// takes a real tmux server. That is why the hermetic run-path tests below configure
// `context: repo`, which is live for any selection: a session-context row on this
// fixture is correctly refused as still starting, which is what the gate test
// exercises instead. TestCustomCommandSessionContextRunsInTheWorktree covers the
// session-context happy path against a real session, and self-skips without tmux.
func newCustomCommandHome(t *testing.T, cmds []customcmd.Command) (*home, *session.Instance) {
	t.Helper()
	h := newCreateFormHome(t)
	appState := config.DefaultState()
	storage, err := session.NewStorage(appState)
	require.NoError(t, err)
	h.appState = appState
	h.storage = storage
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.customCommands = cmds

	inst := newGateInstance(t, h, "live")
	inst.SetStatus(session.Ready)
	inst.SetBranch("zvi/live")
	return h, inst
}

// drain runs cmd and feeds each resulting message back through Update, the way the
// bubbletea runtime does, until the chain ends. Needed because a background action is
// three hops deep: the wrapper's message, the backgroundActionDoneMsg re-emit, and the
// inner customCommandDoneMsg the latch is released by.
//
// Only for RUN chains. A notice's cmd is scheduleNoticeHide, which sleeps for the
// toast's whole lifetime and then clears it — so draining one both stalls the test for
// five seconds and destroys the thing it was about to assert. Assert the notice first.
func drain(t *testing.T, h *home, cmd tea.Cmd) {
	t.Helper()
	for range 12 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				drain(t, h, sub)
			}
			return
		}
		_, cmd = h.Update(msg)
	}
	t.Fatal("message chain did not settle — a cmd is looping")
}

// TestCustomCommandSpecCarriesOnlyStrings is the argument for the type shape, asserted
// rather than commented.
//
// A spec field that could hold an *Instance — or anything else with a pointer into the
// model — would let the goroutine read the session's name and path while a rename or a
// restore writes them. For Path that is still a data race. For the identity family it is
// no longer one (#795), but it is still the wrong value: the command must run under the
// name the user launched it from, not whichever side of a concurrent rename it lands on.
// Either way the detector sees it only if a test happens to interleave the two, and the
// type shape is what makes it impossible instead of unlikely.
func TestCustomCommandSpecCarriesOnlyStrings(t *testing.T) {
	typ := reflect.TypeOf(customCommandSpec{})
	require.Positive(t, typ.NumField(), "the spec must have fields for this to prove anything")

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.String:
		case reflect.Slice:
			assert.Equalf(t, reflect.String, f.Type.Elem().Kind(),
				"field %q is a slice of %s — only strings may cross to the goroutine",
				f.Name, f.Type.Elem().Kind())
		default:
			t.Errorf("field %q is a %s — only strings and string slices may cross to "+
				"the goroutine, because a pointer into the model reads it at the wrong "+
				"instant — and, for Path, unguarded", f.Name, f.Type.Kind())
		}
	}
}

// TestCustomCommandCtxWithholdsTheWorktreeUntilItExists is the destroy-the-real-repo
// guard.
//
// WorkingDir() falls back to Instance.Path whenever the worktree pointer is nil,
// which it is before Start — and Path is the user's ORIGIN CHECKOUT. Since a
// repo-context row is gated on having a selection and nothing else,
// `git -C {{.Session.Worktree}} clean -xfd` fired at an unstarted session would run
// in the real repository. So the context must report no worktree, not a plausible one.
func TestCustomCommandCtxWithholdsTheWorktreeUntilItExists(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newGateInstance(t, h, "unstarted")
	inst.SetStatus(session.Ready) // Ready and NOT started is constructible — that is the trap

	require.False(t, inst.Started(), "the fixture must be unstarted for this to test anything")
	require.NotEmpty(t, inst.WorkingDir(),
		"WorkingDir must be returning the origin checkout — otherwise this guard is vacuous")

	ctx := customCommandCtx(inst)
	assert.Empty(t, ctx.Session.Worktree,
		"an unstarted session has no worktree; reporting its origin checkout as one is how "+
			"a clean -xfd lands in the user's real repo")
	assert.Equal(t, inst.Path, ctx.Repo.Path,
		"repo context is still resolvable — that is the point of keeping the two separate")
}

// TestCustomCommandSessionRowNamesTheRightRefusal pins WHICH check refuses a
// session-context row on a Ready-but-unstarted session, not merely that one does.
//
// Two independent guards cover that case — the Started() gate and the empty-directory
// check behind it — and the second masks a mutation of the first: swap Started() for
// the palette's GetStatus() == Loading and the row is still refused, just for a
// different reason. Asserting the reason is what makes each of them individually
// falsifiable. It matters because the two disagree exactly where the hazard is:
// `newGateInstance` builds Ready && !Started, and for one of those WorkingDir() is the
// user's origin checkout.
func TestCustomCommandSessionRowNamesTheRightRefusal(t *testing.T) {
	c := oneCommand(t, config.CustomCommand{
		Key: "s", Description: "session ctx", Command: "true", Output: "background",
	})
	h := newCreateFormHome(t)
	inst := newGateInstance(t, h, "ready-not-started")
	inst.SetStatus(session.Ready)
	require.NotEqual(t, session.Loading, inst.GetStatus(),
		"the fixture must NOT be Loading — that is the whole difference between the two predicates")
	require.False(t, inst.Started())

	assert.Equal(t, stillStartingReason, customCommandInertReason(c, inst, customCommandCtx(inst)),
		"an unstarted session must be refused by the started gate, by name")
}

// TestCustomCommandCtxCarriesTheSelection pins the rest of the mapping.
func TestCustomCommandCtxCarriesTheSelection(t *testing.T) {
	assert.Equal(t, customcmd.Ctx{}, customCommandCtx(nil), "no selection, no context")

	h := newCreateFormHome(t)
	inst := newGateInstance(t, h, "mapped")
	inst.SetBranch("zvi/mapped")

	ctx := customCommandCtx(inst)
	assert.Equal(t, "mapped", ctx.Session.Title)
	assert.Equal(t, inst.DisplayName(), ctx.Session.Name)
	assert.Equal(t, "zvi/mapped", ctx.Session.Branch)
	assert.Equal(t, inst.GroupKey(), ctx.Repo.Name)
}

// TestCustomCommandMissingReasonsCoverEveryField keeps the reason table honest against
// what MissingFields can actually report. A path with no entry falls back to a
// generated string, so a new context field would otherwise ship a refusal in a
// different voice with nothing failing.
func TestCustomCommandMissingReasonsCoverEveryField(t *testing.T) {
	// A template naming every field, rendered against a wholly empty context, so
	// MissingFields reports the complete set.
	c := oneCommand(t, config.CustomCommand{
		Key: "a", Description: "names every field", Output: "background",
		Command: "echo {{.Session.Title}} {{.Session.Name}} {{.Session.Branch}} " +
			"{{.Session.Worktree}} {{.Session.Port}} {{.Repo.Path}} {{.Repo.Name}}",
	})
	missing := c.MissingFields(customcmd.Ctx{})
	require.NotEmpty(t, missing, "an empty context must report missing fields")

	for _, path := range missing {
		reason, ok := customCommandMissingReasons[path]
		assert.Truef(t, ok, "MissingFields can report %q, which has no reason in the table — "+
			"add one rather than letting it fall back to a generated string", path)
		assert.NotEmptyf(t, reason, "%q needs a reason", path)
		assert.LessOrEqualf(t, len([]rune(reason)), 30,
			"%q's reason must fit a menu row's tail", path)
	}
	// The other direction: a reason for a path MissingFields cannot report is dead.
	for path := range customCommandMissingReasons {
		assert.Containsf(t, missing, path,
			"the table has a reason for %q, which MissingFields never reports", path)
	}
}

// TestCustomCommandGatesAgreeWithDispatch is the twin of
// TestPaletteGatesAgreeWithDispatch, and it exists for the same reason: without it the
// dimming is a second opinion rather than a checked one.
//
// For every selection the app can be in, and every command shape, a dimmed row must
// refuse — and refuse without spawning a process. The last clause is the one that
// needs the seam: a gate that suppressed the notice while still running `sh -c` would
// pass every assertion about the screen.
//
// Which layer this actually exercises, since the name invites a stronger reading: TWO
// independent refusals stand between a dimmed row and a subprocess, and the row never
// reaches the second here. The overlay declines an inert row itself
// (CustomCommandsOverlay.HandleKeyPress returns shouldClose=false, so launchCustomCommand
// is never called), and customCommandSpec re-gates on the live selection for the case the
// selection moved under an open menu — which is TestCustomCommandStaleSelectionIsRegated's
// job, and disabling that re-gate leaves THIS test green. So the process witnesses below
// assert the composed end-to-end invariant, not either layer on its own: whatever leaks,
// nothing spawns.
func TestCustomCommandGatesAgreeWithDispatch(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "s", Description: "session ctx", Command: "true", Output: "background"},
		config.CustomCommand{Key: "r", Description: "repo ctx", Context: "repo", Command: "true", Output: "background"},
		config.CustomCommand{Key: "b", Description: "needs a branch", Command: "echo {{.Session.Branch}}", Output: "background"},
		config.CustomCommand{Key: "w", Description: "needs a worktree", Context: "repo", Command: "echo {{.Session.Worktree}}", Output: "background"},
		// The same shapes in terminal mode, because the gate is only proven for the modes
		// it is driven through — and terminal mode's refusal has strictly more to get
		// wrong: it suspends the whole event loop, so a row that ran when it should have
		// been refused takes the screen away as well as running the command.
		config.CustomCommand{Key: "S", Description: "session ctx, terminal", Command: "true", Output: "terminal"},
		config.CustomCommand{Key: "W", Description: "needs a worktree, terminal", Context: "repo",
			Command: "echo {{.Session.Worktree}}", Output: "terminal"},
	)

	checked := 0
	for _, sc := range gateScenarios() {
		for _, c := range cmds {
			t.Run(sc.name+"/"+c.Key, func(t *testing.T) {
				h, inst := newGateHome(t, sc)
				h.customCommands = cmds
				calls := stubRunner(t, nil)
				// Both seams, so a refusal that leaked past either one is visible. A
				// terminal command spawns from inside attachCommand.Run, which is a
				// different path from the background goroutine and needs its own witness.
				terminalCalls := stubTerminalRunner(t, nil)

				if customCommandInertReason(c, inst, customCommandCtx(inst)) == "" {
					t.Skip("runnable in this scenario — the agreement being checked is about refusals")
				}
				checked++

				_, _ = h.handleKeyPress(runeKey("!"))
				require.Equal(t, stateCustomCommands, h.state, "! must open the menu")
				_, cmd := h.handleKeyPress(runeKey(c.Key))
				// DRAINED, which is what makes the terminal witness below able to fail at
				// all. A terminal command's cmd is a tea.Exec that spawns nothing until the
				// runtime processes it, so an undrained one leaves *terminalCalls empty
				// whether the gate held or leaked. (A refusal returns no run cmd, so on the
				// passing path there is nothing to drain — that is the point.)
				drain(t, h, cmd)

				assert.Empty(t, *calls,
					"a dimmed row must not run — the refusal has to stop the process, not just the toast")
				assert.Empty(t, *terminalCalls,
					"nor may it take the terminal — a suspended loop is worse than a stray subprocess")
				assert.Equal(t, stateCustomCommands, h.state,
					"the menu stays up to hold its own answer")
				assert.Contains(t, xansi.Strip(h.customCommandsOverlay.Render()), c.Key+" —",
					"the refusal must name the key that was pressed")
				assert.False(t, h.actionInFlight, "a refused row gates nothing")
				assert.Empty(t, h.runningCustomCommand, "a refused row claims no slot")
			})
		}
	}
	require.Positive(t, checked, "no scenario dimmed any row — the fixtures cannot exercise the gate")
}

// TestCustomCommandRunsWithTheResolvedContext is the happy path, end to end through
// the key handler: the rendered script, the working directory and the $ATRIUM_*
// environment all reach the seam.
func TestCustomCommandRunsWithTheResolvedContext(t *testing.T) {
	cmds := validCommands(t, config.CustomCommand{
		Key: "r", Description: "echo the repo", Context: "repo", Output: "background",
		Command: "echo {{ quote .Repo.Path }}",
	})
	h, inst := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	require.Equal(t, stateCustomCommands, h.state)
	_, cmd := h.handleKeyPress(runeKey("r"))

	require.Equal(t, stateDefault, h.state, "running closes the menu")
	require.Nil(t, h.customCommandsOverlay, "and drops the overlay, as dismissCommandPalette does")
	require.NotNil(t, cmd, "running must return work to do")
	drain(t, h, cmd)

	require.Len(t, *calls, 1)
	spec := (*calls)[0].spec
	assert.Equal(t, "echo '"+inst.Path+"'", spec.script, "quote must have shell-escaped the path")
	assert.Equal(t, inst.Path, spec.dir, "repo context runs in the repository root")
	assert.Equal(t, inst.Title(), spec.session, "the record must be attributed to the session")
	assert.Contains(t, spec.env, "ATRIUM_REPO="+inst.Path)
	assert.Contains(t, spec.env, "ATRIUM_TITLE=live")
}

// TestCustomCommandGatesBothFormsOfEveryField is the guard for the hazard's second
// door.
//
// The README calls the template and the environment forms interchangeable, and
// recommends the environment one. But MissingFields asks the template renderer which
// placeholders reached the output, so a `$ATRIUM_*` name the SHELL expands is invisible
// to it — and a repo-context row is gated on having a selection and nothing else. So the
// two forms of the same value behaved differently, and the recommended one was the
// unguarded one.
//
// The consequence is the one the withheld worktree exists to prevent, arriving the other
// way: `rm -rf "$ATRIUM_WORKTREE"/build` becomes `rm -rf /build`, and
// `cd "$ATRIUM_WORKTREE" && rm -rf *` runs in the working directory — which for a repo
// row is the repository root — because `cd ""` succeeds and stays put.
func TestCustomCommandGatesBothFormsOfEveryField(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{"template form", `rm -rf {{ quote .Session.Worktree }}/build`},
		{"environment form", `rm -rf "$ATRIUM_WORKTREE"/build`},
		{"braced environment form", `rm -rf "${ATRIUM_WORKTREE}/build"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds := validCommands(t, config.CustomCommand{
				Key: "r", Description: "clean the build dir", Context: "repo",
				Command: tc.command, Output: "background",
			})
			// A repo-context row on a session with no worktree: gated on the selection
			// alone, so nothing but the emptiness check can refuse it.
			h, inst := newCustomCommandHome(t, cmds)
			require.False(t, inst.Started())
			calls := stubRunner(t, nil)

			assert.Equal(t, noWorktreeReason,
				customCommandInertReason(cmds[0], inst, customCommandCtx(inst)),
				"both forms of the same empty field must dim the row")

			_, _ = h.handleKeyPress(runeKey("!"))
			_, _ = h.handleKeyPress(runeKey("r"))
			assert.Empty(t, *calls,
				"and neither form may reach a shell — an empty expansion makes this "+
					"`rm -rf /build`")
		})
	}
}

// TestCustomCommandEnvGateDoesNotOverReachAPopulatedField keeps the new refusal from
// becoming a blanket one: a command reading variables this selection DOES carry must
// still run.
func TestCustomCommandEnvGateDoesNotOverReachAPopulatedField(t *testing.T) {
	cmds := validCommands(t, config.CustomCommand{
		Key: "r", Description: "log the repo", Context: "repo",
		Command: `git -C "$ATRIUM_REPO" log --oneline "$ATRIUM_BRANCH"`, Output: "background",
	})
	h, inst := newCustomCommandHome(t, cmds)
	require.NotEmpty(t, inst.Branch(), "the fixture must carry a branch for this to test anything")
	calls := stubRunner(t, nil)

	assert.Empty(t, customCommandInertReason(cmds[0], inst, customCommandCtx(inst)))

	_, _ = h.handleKeyPress(runeKey("!"))
	_, cmd := h.handleKeyPress(runeKey("r"))
	require.NotNil(t, cmd)
	drain(t, h, cmd)
	assert.Len(t, *calls, 1, "a command whose variables are all populated must run")
}

// TestCustomCommandRefusalsFitARow covers EVERY refusal a run can produce, not just
// the failure toast.
//
// The rule it enforces has two halves, and macOS CI found the case that needed both: the
// message must be bounded, and the reason must come last. The vanished-directory refusal
// interpolated the resolved path AFTER the reason — so on a runner whose temp paths are
// long, the hint bar (which truncates from the right) deleted "is gone" and left a
// refusal that named a path and no reason. Bounding alone would not have caught it;
// asserting the reason survives is what does.
func TestCustomCommandRefusalsFitARow(t *testing.T) {
	long := strings.Repeat("an unbounded user-authored description ", 20)
	wide := strings.Repeat("日本語", 200)
	// A path as long as a macOS temp dir, which is what exposed this.
	longDir := "/var/folders/df/djsxfhc17x95674wsm_g8s980000gn/T/" +
		strings.Repeat("TestSomethingWithAVeryLongName", 3)

	// Iterated from the source's own tails, so a new message shape has to join the set
	// rather than quietly escaping the rule.
	tails := map[string]string{
		"failed":             customCommandFailedTail(),
		"vanished directory": customCommandNoDirTail,
		"unrenderable":       customCommandUnrenderableTail,
		// Terminal mode's two. The formatted one joins as its WIDEST instantiation, not
		// as the format string: a wait status is 0-255, and measuring "%d" would count
		// two cells where three can appear.
		"exited":      fmt.Sprintf(customCommandExitedTailFmt, 255),
		"interrupted": customCommandInterruptedTail,
		"died":        customCommandDiedTail,
	}

	widest := 0
	for name, tail := range tails {
		t.Run(name, func(t *testing.T) {
			for _, desc := range []string{"short", long, wide, longDir} {
				msg := customCommandLabel(desc) + tail
				assert.LessOrEqualf(t, ansi.PrintableRuneWidth(msg), customCommandNoticeWidth,
					"the refusal must fit its declared bound: %q", msg)
				// The half a bound alone does not give: the reason has to be the part
				// that survives, so nothing unbounded may follow it.
				assert.Truef(t, strings.HasSuffix(msg, tail),
					"the reason must come last, so truncation from the right cannot delete "+
						"it: %q", msg)
			}
		})
		if w := ansi.PrintableRuneWidth("''" + tail); w > widest {
			widest = w
		}
	}

	// The chrome is derived from the literals, never counted. Hand-counting it is how
	// the bound shipped one cell short on the first pass, and a bound stated one cell
	// short is a bound that does not hold.
	assert.Equal(t, customCommandNoticeChrome, widest,
		"customCommandNoticeChrome must be the widest tail plus its quotes")

	// And the one message built through the helper rather than by appending a tail.
	assert.Equal(t, customCommandLabel("x")+customCommandFailedTail(),
		customCommandFailureNotice("x"),
		"the failure notice must go through the same bounded label")
}

// TestCustomCommandRefusesAVanishedDirectory covers the stat behind the gate.
//
// The gate proves the session SHOULD have a directory; only this proves it is there.
// A pause, an external rm, or a half-finished teardown all falsify the first without
// touching the second — and the gate cannot see any of them, because every fact it
// reads still says the row is fine. Without this test the stat is unguarded: a
// mutation that deletes it survives the whole suite.
func TestCustomCommandRefusesAVanishedDirectory(t *testing.T) {
	cmds := validCommands(t, config.CustomCommand{
		Key: "r", Description: "in the repo", Context: "repo", Command: "true", Output: "background",
	})
	h, inst := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	// Everything the gate reads still says this row is runnable...
	require.Empty(t, customCommandInertReason(cmds[0], inst, customCommandCtx(inst)))
	// ...and the directory is gone anyway.
	require.NoError(t, os.RemoveAll(inst.Path))

	_, _ = h.handleKeyPress(runeKey("!"))
	_, _ = h.handleKeyPress(runeKey("r"))

	assert.Empty(t, *calls,
		"a command whose directory has vanished must not run — sh -c would fall back to "+
			"whatever cwd the process happens to have")
	assert.True(t, h.menu.HasNotice(), "and must say so")
	// The reason, not the path: the bar truncates from the right, and a temp dir long
	// enough (macOS CI) deleted the reason entirely when the path came after it.
	assert.Contains(t, xansi.Strip(h.menu.String()), "its directory is gone")
	assert.NotContains(t, xansi.Strip(h.menu.String()), inst.Path,
		"the unbounded path belongs in the log, not in a one-line refusal")
}

// TestCustomCommandSerializesRuns pins the reason serialization exists:
// ui.BusyBackground is one shared slot, so two concurrent runs make the progress row
// name one command while the other finishes and clears it.
func TestCustomCommandSerializesRuns(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "a", Description: "first", Context: "repo", Command: "true", Output: "background"},
		config.CustomCommand{Key: "b", Description: "second", Context: "repo", Command: "true", Output: "background"},
	)
	h, _ := newCustomCommandHome(t, cmds)
	// Never reports done, so the first run stays in flight.
	calls := stubRunner(t, func(customCommandSpec) tea.Msg { return nil })

	_, _ = h.handleKeyPress(runeKey("!"))
	_, first := h.handleKeyPress(runeKey("a"))
	require.NotNil(t, first)
	require.NotNil(t, first(), "the first run must produce work")
	require.Len(t, *calls, 1)
	require.Equal(t, "a", h.runningCustomCommand)

	_, _ = h.handleKeyPress(runeKey("!"))
	// Not drained: the refusal's cmd is only the toast's expiry timer.
	_, _ = h.handleKeyPress(runeKey("b"))
	assert.Len(t, *calls, 1, "the second command must not start while the first is running")
	assert.True(t, h.menu.HasNotice(), "and the user must be told why nothing happened")
	// Named the way it was invoked. The latch holds the KEY, so quoting it like a
	// description ("'a' is still running") reads as a name the user has to map back to a
	// row — and "! a" is exactly what the ? screen lists.
	assert.Contains(t, xansi.Strip(h.menu.String()), "! a is still running",
		"the refusal must name the running command as the user typed it")
	assert.Equal(t, "a", h.runningCustomCommand, "the slot still belongs to the first")
}

// TestCustomCommandLatchSurvivesASilentRunner is the wedge this design would otherwise
// have shipped.
//
// beginBackgroundAction passes its inner result through Update, and the runtime DROPS
// a nil message. A seam path that returned nil would therefore leave the single-flight
// latch set for the rest of the process — and because ClearBusy has already run, the
// progress row is empty, so from the outside it is indistinguishable from a bug. The
// guarantee has to live in the wrapper, not in each return inside the seam.
func TestCustomCommandLatchSurvivesASilentRunner(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "a", Description: "silent", Context: "repo", Command: "true", Output: "background"},
	)
	h, _ := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, func(customCommandSpec) tea.Msg { return nil })

	_, _ = h.handleKeyPress(runeKey("!"))
	_, cmd := h.handleKeyPress(runeKey("a"))
	require.NotNil(t, cmd)
	drain(t, h, cmd)

	assert.Empty(t, h.runningCustomCommand,
		"a runner that says nothing must still release the slot, or every later command "+
			"is refused as busy with an empty progress row")
	assert.Len(t, *calls, 1)

	// And the next one really can start.
	_, _ = h.handleKeyPress(runeKey("!"))
	_, again := h.handleKeyPress(runeKey("a"))
	require.NotNil(t, again)
	drain(t, h, again)
	assert.Len(t, *calls, 2)
}

// TestCustomCommandRefusedWhileBusy is the positive test for a deliberate omission:
// KeyCustomCommands is absent from keyAllowedWhileBusy, which is a manual, unguarded
// list. Without this the omission is a comment rather than a rule.
func TestCustomCommandRefusedWhileBusy(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "a", Description: "anything", Context: "repo", Command: "true", Output: "background"},
	)
	h, _ := newCustomCommandHome(t, cmds)
	h.actionInFlight = true

	_, _ = h.handleKeyPress(runeKey("!"))

	assert.Equal(t, stateDefault, h.state,
		"! opens an overlay, so it waits like every other opener while an action is in flight")
	assert.Nil(t, h.customCommandsOverlay)
}

// TestCustomCommandConfirmAsksFirstAndStartsFromUpdate pins the routing a confirmed
// run needs, which neither of handleConfirmState's two paths gives directly: a named
// busyLabel would set actionInFlight for the whole run, and an instantAction closure
// runs synchronously on the update thread. So the closure returns a marker and Update
// starts the work.
func TestCustomCommandConfirmAsksFirstAndStartsFromUpdate(t *testing.T) {
	cmds := validCommands(t, config.CustomCommand{
		Key: "d", Description: "dangerous", Context: "repo", Command: "true", Output: "background", Confirm: true,
	})
	h, _ := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	_, _ = h.handleKeyPress(runeKey("d"))

	require.Equal(t, stateConfirm, h.state, "confirm: true must ask before running")
	require.NotNil(t, h.confirmationOverlay, "and the dialog must survive the menu's dismissal")
	require.Empty(t, *calls, "nothing runs before the answer")
	assert.Empty(t, h.pendingConfirmBusyLabel,
		"a named label would route through beginAsyncAction, whose actionInFlight gate is "+
			"exactly what a background command must not take")

	_, cmd := h.handleKeyPress(runeKey("y"))
	require.NotNil(t, cmd)
	assert.False(t, h.actionInFlight, "approving must not freeze the keyboard")
	drain(t, h, cmd)

	require.Len(t, *calls, 1, "approving runs it")
	assert.Equal(t, "d", (*calls)[0].spec.key)
}

// TestCustomCommandConfirmDialogFitsTheFrame is the dialog's half of the bounding rule.
//
// It is the one surface deliberately given more than the one-row bound, on the grounds
// that it wraps — and "it wraps" turned out to be true only up to a point. The dialog
// grows a row per wrapped line, PlaceOverlay clips what does not fit, and past roughly
// 900 characters the composed frame loses the "Press y to run" line: a modal the user
// can see and cannot answer. Nothing caps a description in config, so this asserts the
// outcome at the 80-column floor rather than trusting the chosen width.
func TestCustomCommandConfirmDialogFitsTheFrame(t *testing.T) {
	for _, desc := range []string{
		"short",
		strings.Repeat("an unbounded user-authored description ", 60), // ~2300 cells
		strings.Repeat("日本語", 400),                                    // wide runes, 2400 cells
	} {
		cmds := validCommands(t, config.CustomCommand{
			Key: "d", Description: desc, Context: "repo",
			Command: "true", Output: "background", Confirm: true,
		})
		h, _ := newCustomCommandHome(t, cmds)
		h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

		_, _ = h.handleKeyPress(runeKey("!"))
		_, _ = h.handleKeyPress(runeKey("d"))
		require.Equal(t, stateConfirm, h.state)

		view := xansi.Strip(h.View().Content)
		assert.Containsf(t, view, "Press y to run",
			"a confirmation the user cannot answer is worse than none (desc %d cells)",
			len([]rune(desc)))

		lines := strings.Split(view, "\n")
		assert.LessOrEqual(t, len(lines), 24)
		for i, l := range lines {
			assert.Equalf(t, 80, ansi.PrintableRuneWidth(l), "line %d is the wrong width", i)
		}
	}
}

func TestCustomCommandConfirmDeclinedRunsNothing(t *testing.T) {
	cmds := validCommands(t, config.CustomCommand{
		Key: "d", Description: "dangerous", Context: "repo", Command: "true", Output: "background", Confirm: true,
	})
	h, _ := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	_, _ = h.handleKeyPress(runeKey("d"))
	require.Equal(t, stateConfirm, h.state)

	_, cmd := h.handleKeyPress(runeKey("n"))
	if cmd != nil {
		drain(t, h, cmd)
	}
	assert.Equal(t, stateDefault, h.state)
	assert.Empty(t, *calls, "declining must run nothing")
}

// TestCustomCommandRecordsTheSyntheticArgv is why LogArgv exists.
//
// cmdlog.Redact models one NAME=VALUE per argv token, and a whole shell script in a
// single token defeats it in both directions: a bearer token inside a -H flag has no
// leading NAME= and is stored verbatim, while a leading FOO=bar matches at the first
// '=' and returns everything before it — throwing the command away. So the log must
// never see the rendered script.
func TestCustomCommandRecordsTheSyntheticArgv(t *testing.T) {
	cmdlog.Reset()
	t.Cleanup(cmdlog.Reset)

	dir := t.TempDir()
	const secret = "ghp_averyrealsecrettoken"
	spec := customCommandSpec{
		key:     "p",
		desc:    "publish",
		script:  `echo "Authorization: token ` + secret + `"; exit 3`,
		dir:     dir,
		session: "live",
		argv:    []string{"atrium", "custom-command", "p", "publish"},
	}

	msg := execCustomCommand(context.Background(), spec)
	done, ok := msg.(customCommandDoneMsg)
	require.True(t, ok, "the seam must report a done message")
	require.Error(t, done.err, "exit 3 is a failure")

	recs := cmdlog.Snapshot()
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.NotContains(t, rec.Argv, secret,
		"the rendered script must never reach the log — Redact cannot defend a single token")
	assert.NotContains(t, rec.Argv, "echo")
	assert.Contains(t, rec.Argv, "custom-command")
	assert.Contains(t, rec.Argv, "publish", "the record must still identify which command ran")
	assert.Equal(t, 3, rec.Exit, "the exit code must survive the argv swap")
	assert.Equal(t, "live", rec.Session)
	assert.True(t, rec.Err)
}

// TestCustomCommandRunsInItsDirectoryWithTheEnvironment drives the real seam.
func TestCustomCommandRunsInItsDirectoryWithTheEnvironment(t *testing.T) {
	cmdlog.Reset()
	t.Cleanup(cmdlog.Reset)

	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	spec := customCommandSpec{
		key: "w", desc: "write",
		script: `printf '%s\n%s\n' "$PWD" "$ATRIUM_TITLE" > out.txt`,
		dir:    dir,
		env:    []string{"ATRIUM_TITLE=from-the-env"},
		argv:   []string{"atrium", "custom-command", "w", "write"},
	}

	msg := execCustomCommand(context.Background(), spec)
	require.NoError(t, msg.(customCommandDoneMsg).err)

	body, err := os.ReadFile(out)
	require.NoError(t, err, "the command must have run in spec.dir")
	lines := strings.Fields(string(body))
	require.Len(t, lines, 2)
	// macOS resolves TMPDIR through /private, so compare the resolved paths.
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(lines[0])
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
	assert.Equal(t, "from-the-env", lines[1], "$ATRIUM_* must reach the shell")
}

// TestCustomCommandFailureIsSurfaced: the output went to a buffer the user never sees,
// so a failure has to speak — on the hint bar, not by taking the screen.
func TestCustomCommandFailureIsSurfaced(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)
	h.runningCustomCommand = "x"

	_, cmd := h.handleCustomCommandDone(customCommandDoneMsg{
		key: "x", desc: "broken", err: fmt.Errorf("exit status 1"),
	})
	assert.Empty(t, h.runningCustomCommand, "a failure still releases the slot")
	require.NotNil(t, cmd)
	// Asserted before the cmd is run: handleError has already placed the message, and
	// the cmd it returns is only the toast's expiry timer.
	assert.True(t, h.menu.HasNotice(), "a failed run must reach the user")
	assert.Equal(t, stateDefault, h.state,
		"and must NOT take the screen — the user put this command in the background")
	assert.Contains(t, xansi.Strip(h.menu.String()), "press L",
		"and must point at where the output went")
}

// TestCustomCommandFailureNoticeFitsARow pins the bound the comment claims.
//
// It is the difference between a toast and a modal: handleError sends anything the
// hint-bar row cannot show to the persistent info overlay, so an unbounded description
// makes every background failure steal the screen. The description is user-authored and
// has no ceiling, so the message has to impose one.
func TestCustomCommandFailureNoticeFitsARow(t *testing.T) {
	for _, desc := range []string{
		"short",
		strings.Repeat("an unbounded user-authored description ", 20),
		strings.Repeat("日本語", 200), // wide runes, in case a bound was counted in bytes
	} {
		notice := customCommandFailureNotice(desc)
		assert.LessOrEqualf(t, ansi.PrintableRuneWidth(notice), customCommandNoticeWidth,
			"the failure notice must fit its declared bound: %q", notice)
		assert.Contains(t, notice, "press L for the output")
	}

	// And the bound really does keep it out of the modal at the narrowest size.
	h, _ := newCustomCommandHome(t, nil)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, _ = h.handleCustomCommandDone(customCommandDoneMsg{
		key: "x", desc: strings.Repeat("long ", 40), err: fmt.Errorf("exit status 7"),
	})
	assert.Equal(t, stateDefault, h.state, "even a 200-character description stays a toast at 80x24")
	assert.True(t, h.menu.HasNotice())
}

// TestCustomCommandSuccessIsQuiet is the other half of the notice rule: the command
// log has the record, and a toast per run would be noise for the case this feature
// exists for.
func TestCustomCommandSuccessIsQuiet(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)
	h.runningCustomCommand = "x"

	_, cmd := h.handleCustomCommandDone(customCommandDoneMsg{key: "x", desc: "fine"})
	assert.Empty(t, h.runningCustomCommand)
	assert.Nil(t, cmd, "a clean run says nothing")
}

// TestCustomCommandMenuGoneBeforeAnythingRuns pins the ordering. Reversed, the
// dismissal would reset stateConfirm to stateDefault and orphan the dialog, and a
// refusal notice would reach flashNotice while the hint bar is still hidden — which
// recomputes the layout under a live overlay.
func TestCustomCommandMenuGoneBeforeAnythingRuns(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "a", Description: "runs", Context: "repo", Command: "true", Output: "background"},
	)
	h, _ := newCustomCommandHome(t, cmds)
	stubRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	require.NotNil(t, h.customCommandsOverlay)
	require.Len(t, h.customCommandRows, 1)

	_, _ = h.handleKeyPress(runeKey("a"))

	assert.Nil(t, h.customCommandsOverlay, "the overlay pointer must be dropped")
	assert.Nil(t, h.customCommandRows, "and so must the row table — a stale index must not resolve")
	assert.Equal(t, stateDefault, h.state)
}

// TestCustomCommandEscLeavesNothingBehind covers the other close.
func TestCustomCommandEscLeavesNothingBehind(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "a", Description: "runs", Context: "repo", Command: "true", Output: "background"},
	)
	h, _ := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	_, _ = h.handleKeyPress(keyMsg("esc"))

	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.customCommandsOverlay)
	assert.Empty(t, *calls, "esc runs nothing")
}

// TestCustomCommandStaleSelectionIsRegated: the selection can move under an open menu,
// and the palette's first rule is that the handler stays authoritative. A reason
// recorded when the menu opened is not one the run may trust.
func TestCustomCommandStaleSelectionIsRegated(t *testing.T) {
	cmds := validCommands(t, config.CustomCommand{
		Key: "a", Description: "needs a branch", Context: "repo",
		Command: "echo {{.Session.Branch}}", Output: "background",
	})
	h, inst := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	require.Empty(t, h.customCommandRows[0].inert, "the row opened runnable")

	// The selection stops satisfying the command while the menu is up.
	inst.SetBranch("")
	_, _ = h.handleKeyPress(runeKey("a"))

	assert.Empty(t, *calls, "the run must re-check the gate, not trust the row it was drawn from")
	assert.True(t, h.menu.HasNotice(), "and say why — from stateDefault, where the bar is back")
	assert.Contains(t, xansi.Strip(h.menu.String()), noBranchReason)
}

// TestCustomCommandSessionContextRunsInTheWorktree is the session-context happy path,
// which needs a real tmux session because Started() cannot be reached without one —
// and Started() is exactly the predicate the gate turns on.
func TestCustomCommandSessionContextRunsInTheWorktree(t *testing.T) {
	testutil.RequireTmux(t)

	cmds := validCommands(t, config.CustomCommand{
		Key: "s", Description: "in the worktree", Output: "background",
		Command: "true",
	})
	h, _ := newCustomCommandHome(t, cmds)
	calls := stubRunner(t, nil)

	// A direct session skips git worktree setup; a real Start still creates a live
	// tmux session and flips Started() true, so WorkingDir() is a live directory.
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "started", Path: t.TempDir(), Program: "sleep 300", Direct: true,
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	require.True(t, inst.Started())
	h.list.AddInstance(inst)()
	h.list.SetSelectedInstance(1)
	require.Same(t, inst, h.list.GetSelectedInstance())

	_, _ = h.handleKeyPress(runeKey("!"))
	require.Empty(t, h.customCommandRows[0].inert,
		"a started, unpaused session must make a session-context row runnable")
	_, cmd := h.handleKeyPress(runeKey("s"))
	require.NotNil(t, cmd)
	drain(t, h, cmd)

	require.Len(t, *calls, 1)
	assert.Equal(t, inst.WorkingDir(), (*calls)[0].spec.dir,
		"session context runs in the agent's working directory")
	assert.Contains(t, (*calls)[0].spec.env, "ATRIUM_WORKTREE="+inst.WorkingDir())
}

// TestCustomCommandProblemsReportIsBounded: two of a Problem's three fields are
// user-authored, and a config can hold any number of broken entries.
func TestCustomCommandProblemsReportIsBounded(t *testing.T) {
	assert.Empty(t, customCommandProblemsReport(nil), "nothing wrong, nothing to say")

	var problems []customcmd.Problem
	for i := 0; i < customCommandProblemsShown+3; i++ {
		problems = append(problems, customcmd.Problem{
			Index: i, Key: "k", Msg: strings.Repeat("a very long explanation ", 20),
		})
	}
	report := customCommandProblemsReport(problems)

	assert.Contains(t, report, fmt.Sprintf("… and %d more", 3),
		"a long list must be capped and say so")
	for _, line := range strings.Split(report, "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 110, "every line must be clipped: %q", line)
	}
	assert.Contains(t, report, "The rest still work.",
		"the report must say a bad entry does not cost the good ones")
}

func TestCustomCommandProblemsReportSpeaksOfOne(t *testing.T) {
	report := customCommandProblemsReport([]customcmd.Problem{{Index: 0, Key: "g", Msg: "output is required"}})

	assert.Contains(t, report, "1 custom command in config.json was ignored:")
	assert.NotContains(t, report, "… and")
}

// TestCustomCommandProblemsFlushWaitsForTheScreen mirrors flushPendingLaunchCrash: a
// modal opened while an overlay owns the screen would clobber it, and a buffer that is
// only read reopens the modal on every 100ms tick.
func TestCustomCommandProblemsFlushWaitsForTheScreen(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)
	h.pendingCustomCommandProblems = []customcmd.Problem{{Index: 0, Key: "g", Msg: "output is required"}}

	h.state = stateHelp
	assert.Nil(t, h.flushCustomCommandProblems(), "it must wait while an overlay owns the screen")
	assert.NotEmpty(t, h.pendingCustomCommandProblems, "and stay buffered")

	h.state = stateDefault
	h.flushCustomCommandProblems()
	assert.Equal(t, stateInfo, h.state, "then open the persistent modal")
	assert.Contains(t, xansi.Strip(h.textOverlay.Render()), "output is required")
	assert.Empty(t, h.pendingCustomCommandProblems,
		"and clear the buffer, or the preview tick reopens it forever")

	h.state = stateDefault
	assert.Nil(t, h.flushCustomCommandProblems(), "a second tick must find nothing to do")
}

// TestAssembleHomeValidatesCustomCommands wires the two ends together: the config's
// good entries bind and its bad ones are buffered for the startup modal.
func TestAssembleHomeValidatesCustomCommands(t *testing.T) {
	h := newCreateFormHome(t)
	h.appConfig.CustomCommands = []config.CustomCommand{
		{Key: "g", Description: "good", Command: "true", Output: "background"},
		{Key: "b", Description: "no output mode", Command: "true"},
	}
	// assembleHome is what production calls; reproduce its one line here rather than
	// rebuilding a whole home, and pin that the line is the same pass doctor runs.
	cmds, problems := customcmd.Validate(h.appConfig.CustomCommands)

	require.Len(t, cmds, 1)
	assert.Equal(t, "g", cmds[0].Key, "a broken entry must not cost the good ones")
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "output is required")
}
