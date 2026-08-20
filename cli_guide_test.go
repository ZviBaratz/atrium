package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// TestBriefAdvertisesARegisteredCommand closes the gap between the two files that both spell
// the guide's name: session/tmux/brief.go, which writes it into the copy Claude reads at every
// session start, and this package, which registers the command. Nothing connects them at
// compile time — the brief is a string — and a string naming a command that does not exist
// fails as a "command not found" the agent absorbs quietly, never as a build error.
//
// The direction it holds is registration → copy: the name rootCmd actually answers to must
// appear in the RENDERED brief, so a rename reaching the registration and not the template
// fails here. The reverse — the template dropping `+ GuideSubcommand +` for the identical
// literal — is invisible to this or any test, since both render the same bytes.
func TestBriefAdvertisesARegisteredCommand(t *testing.T) {
	brief := tmux.RenderSessionBrief(tmux.SessionBrief{
		Name:          "n",
		Origin:        "/o",
		Branch:        "b",
		WorktreesRoot: "/r",
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
// calling runGuide, because everything else in this file tests the page and the function while
// leaving the wiring between them unasserted: replacing RunE's body with `return nil` keeps the
// whole package green. That is the CLI analogue of the drift-sites gap where nothing asserts a
// registered key has a case in handleKeyPress, and TestNewCommandFlagsAreAllWired is the same
// guard for `new`'s flags — an `atrium guide` that prints nothing while the brief keeps pointing
// every claude session at it is the failure both exist to stop.
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

// TestGuideCarriesTheBriefsLoadBearingRules: the page is not only the brief's overflow, it is
// the ONLY carrier of these rules for a session that gets no brief at all. ensureHookSettings
// injects one solely for an adapter declaring HookSupport, so a codex, gemini or aider agent
// reaches them here or nowhere — which is why the page repeats rather than references them, and
// why dropping a repetition as redundant would silently strip those sessions of it.
func TestGuideCarriesTheBriefsLoadBearingRules(t *testing.T) {
	require.Contains(t, guidePage, "git worktree remove",
		"the ownership rule this whole channel exists for must be on the page")
	require.Contains(t, guidePage, "git worktree prune",
		"both halves of the ownership rule, not just the first")
	require.Contains(t, guidePage, "already checked out on the session branch",
		"the branch instruction: the failure mode is branching on top of the session branch")
}

// TestGuideNamesTheHandoffCommand: the brief spends its one clause pointing here, so the page
// has to carry the thing the clause promised. Without `atrium new` on it the pointer costs
// every session, in every repo, on every compaction, and leads nowhere.
//
// It asserts the worked example rather than the bare name, which is a substring of the
// `atrium new --help` pointer the next test requires and so was satisfied by a page that had
// deleted the entire handing-off section.
func TestGuideNamesTheHandoffCommand(t *testing.T) {
	require.Contains(t, guidePage, "HANDING OFF",
		"the page must keep the section that tells an agent to create the next session")
	require.Contains(t, guidePage, `atrium new "fix the parser"`,
		"the page must show a worked handoff, not merely name the command")
}

// TestGuideDefersTheTimingRules: when a queued create actually lands is the claim on this page
// most likely to be restated wrongly. It is long, it has a parked-TUI case that only a live lock
// probe answers (drainState), and newCmd's Long already owns all of it. So the page sends an
// agent there instead of paraphrasing, and this is what stops a paraphrase reappearing.
func TestGuideDefersTheTimingRules(t *testing.T) {
	require.Contains(t, guidePage, "atrium new --help",
		"the page must point at the help that owns the delivery-timing rules")
}

// TestGuideShowsWaitWithADuration guards a difference the page cannot state safely by accident:
// --wait is a DurationVar with no NoOptDefVal, so a bare `--wait` is a usage error that fails
// before anything is spooled. The page is read by an agent deciding what to type, without the
// flag list beside it that makes newCmd's Long unambiguous, so it must show a value.
//
// The NoOptDefVal assertion is the live half: give --wait an implied value and this fails,
// which is the moment to revisit the wording rather than years later.
func TestGuideShowsWaitWithADuration(t *testing.T) {
	flag := newCmd.Flags().Lookup("wait")
	require.NotNil(t, flag, "`new` must still carry the --wait flag the page describes")
	require.Empty(t, flag.NoOptDefVal,
		"--wait now has an implied value, so a bare --wait works and the page can be relaxed")

	require.Contains(t, guidePage, "`--wait 30s`",
		"a bare --wait is a usage error, so the page must show it with a duration")
}

// TestGuideWarnsOffTheDestructiveCommands is the reason the brief points at this page rather
// than at `atrium --help`, which lists reset and reap beside ls and peek with nothing to say
// which of them belong to the person at the keyboard.
//
// The heading is located before it is sliced on. strings.Index returns -1 when it is missing,
// and guidePage[-1:] panics rather than failing — which aborts the whole package binary and
// takes every other test's result with it, so a one-word edit to a heading would read as a
// crash somewhere else entirely.
func TestGuideWarnsOffTheDestructiveCommands(t *testing.T) {
	const heading = "NOT YOURS TO RUN"
	start := strings.Index(guidePage, heading)
	require.GreaterOrEqual(t, start, 0, "the page has no %s section", heading)

	section := guidePage[start:]
	require.Contains(t, section, "atrium reset", "the page must warn an agent off reset")
	require.Contains(t, section, "atrium reap --kill", "and off the reap form that stops servers")
	require.Contains(t, section, "atrium update", "and off replacing the running binary")
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

	for _, name := range []string{"ls", "peek", "send", "new", "reset", "reap", "update"} {
		require.Contains(t, guidePage, "atrium "+name, "the page is expected to mention %q", name)
		require.True(t, names[name], "the page names `atrium %s`, which rootCmd does not register", name)
	}
}

// TestGuideRunMatchesItsHelp: `atrium guide` and `atrium guide --help` are two paths to the same
// page, and an agent told to run one must not get less than the other. Cobra prints Long for the
// help path, so pointing both at one const is what makes them agree — this fails if a later edit
// gives RunE its own copy.
func TestGuideRunMatchesItsHelp(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runGuide(&out))

	require.Equal(t, guidePage+"\n", out.String(), "the command prints the page and nothing else")
	require.Equal(t, guidePage, guideCmd.Long, "`--help` must render the same page the command prints")
}

// TestGuideFitsAnEightyColumnPane: the command table is column-aligned with runs of spaces, so a
// line over the pane width does not merely wrap — it wraps the description under the next
// command and the alignment stops meaning anything. An agent reads this through tmux at whatever
// width the session was created at, and nothing else here measures rendered width.
func TestGuideFitsAnEightyColumnPane(t *testing.T) {
	for _, line := range strings.Split(guidePage, "\n") {
		require.LessOrEqual(t, len([]rune(line)), 80,
			"line exceeds an 80-column pane and will reflow: %q", line)
	}
}
