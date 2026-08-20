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
// It reads the name back off the RENDERED brief rather than off tmux.GuideSubcommand, so a
// template that stopped interpolating the constant and hardcoded a spelling still fails here.
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

// TestGuideNamesTheHandoffCommand: the brief spends its one clause pointing here, so the page
// has to carry the thing the clause promised. Without `atrium new` on it the pointer costs
// every session, in every repo, on every compaction, and leads nowhere.
func TestGuideNamesTheHandoffCommand(t *testing.T) {
	require.Contains(t, guidePage, "atrium new",
		"the page must name the command that creates the follow-up session")
}

// TestGuideDefersTheTimingRules: when a queued create actually lands is the claim on this page
// most likely to be restated wrongly. It is long, it has a parked-TUI case that only a live lock
// probe answers (drainState), and newCmd's Long already owns all of it. So the page sends an
// agent there instead of paraphrasing, and this is what stops a paraphrase reappearing.
func TestGuideDefersTheTimingRules(t *testing.T) {
	require.Contains(t, guidePage, "atrium new --help",
		"the page must point at the help that owns the delivery-timing rules")
}

// TestGuideWarnsOffTheDestructiveCommands is the reason the brief points at this page rather
// than at `atrium --help`, which lists reset and reap beside ls and peek with nothing to say
// which of them belong to the person at the keyboard.
func TestGuideWarnsOffTheDestructiveCommands(t *testing.T) {
	section := guidePage[strings.Index(guidePage, "NOT YOURS TO RUN"):]
	require.NotEmpty(t, section, "the page has no NOT YOURS TO RUN section")

	require.Contains(t, section, "atrium reset", "the page must warn an agent off reset")
	require.Contains(t, section, "atrium reap --kill", "and off the reap form that stops servers")
}

// TestGuideNamesOnlyRegisteredCommands holds the page to the CLI it describes. Every command it
// mentions lives in another file, which is exactly why a rename leaves the prose behind: nothing
// here fails to compile when `peek` becomes something else.
//
// The list is written out rather than parsed off the page, because a parser would have to decide
// what counts as a command name in prose and would quietly stop finding any of them.
func TestGuideNamesOnlyRegisteredCommands(t *testing.T) {
	names := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}

	for _, name := range []string{"ls", "peek", "send", "new", "reset", "reap"} {
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
