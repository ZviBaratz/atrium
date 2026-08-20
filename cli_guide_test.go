package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// guideSection returns the page text between two markers, so a scoping assertion cannot
// silently widen to the rest of the page. readmeSection states the reason for the end marker:
// a guard that fell back to the document would keep passing after the section it is about was
// renamed away. Here the same applies to a REORDER — the destructive section happens to be last
// today, so a heading-to-EOF slice and the real section coincide until someone moves one.
func guideSection(t *testing.T, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(guidePage, startMarker)
	require.GreaterOrEqual(t, start, 0, "the page is missing the %q heading", startMarker)
	rest := guidePage[start:]
	end := strings.Index(rest, endMarker)
	require.Greater(t, end, 0, "the page is missing %q after %q", endMarker, startMarker)
	return rest[:end]
}

// TestBriefAdvertisesARegisteredCommand holds the brief to a command this binary answers to.
//
// What it does NOT do is catch a rename: `Use: tmux.GuideSubcommand` means guideCmd.Name()
// returns the very constant the template interpolates, so both sides of the name comparison
// move together and that assertion cannot fail. Two earlier versions of this comment claimed
// otherwise, in two different ways, so the claim is dropped rather than restated a third time.
// What the comparison is still worth is the case where the copy stops interpolating and drifts
// to a stale literal; what the rest of the test is worth is the registration itself, which
// nothing else asserts — a declared-but-unregistered command is a build-clean "command not
// found" for every session the brief reaches.
func TestBriefAdvertisesARegisteredCommand(t *testing.T) {
	brief := tmux.RenderSessionBrief(tmux.SessionBrief{
		Name: "n", Origin: "/o", Branch: "b", WorktreesRoot: "/r",
	})
	require.NotEmpty(t, brief, "a complete set of facts renders a brief")
	require.Contains(t, brief, "`atrium "+guideCmd.Name()+"`",
		"the brief advertises a command this binary does not register under that name")

	var registered *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == guideCmd.Name() {
			registered = c
		}
	}
	require.NotNil(t, registered, "%q is declared but never added to rootCmd", guideCmd.Name())
	require.False(t, registered.Hidden, "the brief points an agent at %q, so it must not be hidden", guideCmd.Name())
}

// TestGuideCommandPrintsThePage drives the registered command through rootCmd rather than
// calling runGuide, because everything else here tests the page and the function while leaving
// the wiring between them unasserted: replacing RunE's body with `return nil` kept the whole
// package green. That is the CLI analogue of the drift-sites gap where nothing asserts a
// registered key has a case in handleKeyPress.
func TestGuideCommandPrintsThePage(t *testing.T) {
	restoreRootCmd(t)

	var out bytes.Buffer
	cmd := rootCmd
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"guide"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, guidePage+"\n", out.String(), "`atrium guide` must print the page")
}

// TestGuideRunMatchesItsHelp: `atrium guide` and `atrium guide --help` are two paths to the
// same page, and an agent told to run one must not get less than the other.
//
// It RENDERS the help path rather than comparing guideCmd.Long to the const, which is the
// same string by construction and so cannot disagree with itself. Cobra reaches Long through
// a help template, and a template override is the way this actually breaks: setting one that
// prints only Short left the package green while `atrium guide --help` printed a single line.
func TestGuideRunMatchesItsHelp(t *testing.T) {
	var run bytes.Buffer
	require.NoError(t, runGuide(&run))
	require.Equal(t, guidePage+"\n", run.String(), "the command prints the page and nothing else")

	var help bytes.Buffer
	guideCmd.SetOut(&help)
	t.Cleanup(func() { guideCmd.SetOut(nil) })
	require.NoError(t, guideCmd.Help())

	require.Contains(t, help.String(), guidePage,
		"`--help` must render the same page the command prints")
}

// TestGuideCarriesTheBriefsLoadBearingRules: the page is not only the brief's overflow, it is
// the ONLY carrier of these rules for a session that gets no brief — every codex, gemini and
// aider session, and every direct session. So each clause is pinned individually. Asserting a
// heading, or one clause standing for the section, is what let an earlier version of this test
// pass while the sibling-worktree prohibition was deleted as duplicative.
func TestGuideCarriesTheBriefsLoadBearingRules(t *testing.T) {
	// Each clause must sit on ONE line of the page: a command an agent copies must never be
	// split by the wrap, and an assertion spanning a break would pin the wrap rather than the
	// rule, failing on any reflow that leaves the text intact.
	for _, clause := range []string{
		"`git worktree remove` or `git worktree prune`",
		"sibling worktree beside it",
		"already checked out on the session branch",
		"create another branch",
		"Killing the session removes the worktree and deletes the branch",
		"removes the worktree and keeps the branch",
	} {
		require.Contains(t, guidePage, clause,
			"a rule that reaches non-claude sessions here or nowhere was dropped: %q", clause)
	}
}

// TestGuideNamesTheHandoffCommand: the brief spends its one clause pointing here, so the page
// has to carry what the clause promised — the worked example, and the base-branch fact without
// which the example silently hands the next agent a worktree missing this agent's work.
//
// It asserts the example rather than the bare name, which is a substring of the
// `atrium new --help` pointer below and so was satisfied by a page that had deleted the whole
// section.
func TestGuideNamesTheHandoffCommand(t *testing.T) {
	section := guideSection(t, "HANDING OFF", "NOT YOURS TO RUN")
	require.Contains(t, section, `atrium new "fix the parser"`,
		"the page must show a worked handoff, not merely name the command")
	require.Contains(t, section, "not from yours",
		"the page must say the new branch does not start from this session's branch")
	require.Contains(t, section, "--branch",
		"and must name the flag that carries this session's work forward")
}

// TestGuideDefersTheTimingRules: when a queued create actually lands is the claim on this page
// most likely to be restated wrongly — twice already. It is long, it has a parked-TUI case that
// only a live lock probe answers (drainState), and the warning `new` prints is deliberately
// asymmetric under --wait (TestNewWaitStillWarnsWhileAtriumIsParked is the exception that broke
// the page's second attempt at summarising it). newCmd's Long owns all of it.
func TestGuideDefersTheTimingRules(t *testing.T) {
	require.Contains(t, guidePage, "atrium new --help",
		"the page must point at the help that owns the delivery-timing rules")
	require.NotContains(t, guidePage, "--wait",
		"the page must not describe --wait's semantics; two attempts at that sentence were wrong")
}

// TestGuideWarnsAboutSendStdin: `send` with the message omitted reads stdin, and messageText has
// no tty guard, so the form blocks forever for an agent whose shell inherits the session pty —
// long enough to kill the tool call. cli_new.go documents declining that behaviour for `new`
// precisely to avoid it. The page recommended the stdin form for one commit; this is what stops
// it coming back.
func TestGuideWarnsAboutSendStdin(t *testing.T) {
	require.Contains(t, guidePage, "Always pass `send` its message as an argument",
		"the page must tell an agent to pass the message inline")
	require.Contains(t, guidePage, "waits forever",
		"and must say what the omitted form does, not merely prefer the other one")
}

// TestGuideWarnsOffTheDestructiveCommands is the reason the brief points at this page rather
// than at `atrium --help`, which lists reset and reap beside ls and peek with nothing to say
// which of them belong to the person at the keyboard.
func TestGuideWarnsOffTheDestructiveCommands(t *testing.T) {
	section := guideSection(t, "NOT YOURS TO RUN", "Everything listed under")
	require.Contains(t, section, "atrium reset", "the page must warn an agent off reset")
	require.Contains(t, section, "atrium reap --kill", "and off the reap form that stops servers")
	require.Contains(t, section, "atrium update", "and off replacing the running binary")
}

// TestGuidePermitsWhatItLists: the closing sentence generalises from the three commands above it
// to "anything else is theirs", and the page's own entry point has to survive that rule. It did
// not for one commit: `guide` appeared nowhere in the page text, so the sweep classified the
// command every session is pointed at as the keyboard's, and a compliant agent would stop
// re-reading its own instructions.
func TestGuidePermitsWhatItLists(t *testing.T) {
	section := guideSection(t, "WHAT YOU CAN RUN", "HANDING OFF")
	require.Contains(t, section, "atrium "+guideCmd.Name(),
		"the page must list its own command among the ones an agent may run")
}

// TestGuideNamesOnlyRegisteredCommands holds the page to the CLI it describes. Every command it
// mentions lives in another file, which is exactly why a rename leaves the prose behind: nothing
// here fails to compile when `peek` becomes something else.
//
// The list is written out rather than parsed off the page, because a parser would have to decide
// what counts as a command name in prose and would quietly stop finding any of them. The cost is
// that it is one-directional: a page naming a command that does not exist passes, so long as it
// still names these. rootCmd's own coverage is readme_commands_test.go's job.
func TestGuideNamesOnlyRegisteredCommands(t *testing.T) {
	names := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}

	for _, name := range []string{"guide", "ls", "peek", "send", "new", "reset", "reap", "update"} {
		require.Contains(t, guidePage, "atrium "+name, "the page is expected to mention %q", name)
		require.True(t, names[name], "the page names `atrium %s`, which rootCmd does not register", name)
	}
}

// TestGuideFitsEightyColumns keeps the command table's alignment meaningful: the columns are runs
// of spaces, so a line past the terminal's width does not merely wrap, it wraps a description
// under the next command. Eighty is a convention rather than a measured pane width — nothing
// reflows this text, which runGuide writes straight to stdout — so this is a typographic budget,
// not a rendering guarantee.
//
// It measures display width rather than rune count, matching ui/list_sanitize_test.go: the page
// carries em dashes, which are East-Asian Ambiguous, and several lines sit at exactly the limit
// with no slack for a glyph that turns out to be wide.
func TestGuideFitsEightyColumns(t *testing.T) {
	for _, line := range strings.Split(guidePage, "\n") {
		require.LessOrEqual(t, runewidth.StringWidth(line), 80,
			"line exceeds the 80-column budget and will reflow: %q", line)
	}
}
