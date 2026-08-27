package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// The supported-agents list is a fact with several homes: the adapter registry,
// the binary names detection probes, two help strings in main.go, the welcome
// overlay's zero-agents hint, the website's copy and the README's tagline. Every
// one of those but the registry was written out by hand until #887, and the
// drift had reached the first thing a user runs — `atrium --help` advertised an
// agent no adapter recognizes and no probe looks for, while naming neither
// Gemini nor Antigravity.
//
// The Go-side sites are derived now (supportedAgentSentence, knownAgentBinList,
// overlay.installableAgents), so they cannot drift on their own. The two that
// cannot be derived — a Next.js page and a Markdown tagline, neither of which
// this binary imports — are held to the registry here instead.

// agentDocs are the hand-written prose sites that name the agents. Each is read
// from the module root, so a rename fails as a missing file rather than as a
// silently empty haystack.
var agentDocs = []string{
	"web/src/app/page.tsx",
	"web/src/app/layout.tsx",
}

// TestSupportedAgentsAreNamedInTheDocs requires every adapter's DisplayName to
// appear in the website's copy and in the README's tagline.
//
// One-directional on purpose, in the direction that rots: it fails when an
// adapter the registry carries goes unmentioned (adding an adapter, or deleting
// a name from a file), and says nothing about a doc naming an agent that does
// not exist. The second half is what a `git grep` finds and the first is what
// nothing does — the website named three of the five adapters in the registry.
//
// The match is exact and case-sensitive, so the README's link text has to spell
// the adapter's own name ("Gemini CLI", not "Gemini"). That is deliberate: the
// DisplayName is what the TUI paints beside a session, and a docs page calling
// it something else is the same drift one level out.
func TestSupportedAgentsAreNamedInTheDocs(t *testing.T) {
	names := agent.DisplayNames()
	require.NotEmpty(t, names, "the registry is empty; this guard would pass vacuously")

	for _, doc := range agentDocs {
		t.Run(doc, func(t *testing.T) {
			body := moduleFile(t, doc)
			for _, name := range names {
				// require.True rather than require.Contains: the haystack is a whole
				// source file, and Contains prints it in full on failure, burying the
				// one word the message is about.
				require.True(t, strings.Contains(body, name),
					"%s does not name the %q adapter; the docs have to follow the registry", doc, name)
			}
		})
	}

	t.Run("README tagline", func(t *testing.T) {
		tagline := readmeTagline(t)
		for _, name := range names {
			require.Contains(t, tagline, name,
				"the README's tagline does not name the %q adapter: %s", name, tagline)
		}
	})
}

// readmeTagline returns the README's opening sentence — the first non-empty line
// after the `# Atrium` heading, which is the paragraph GitHub renders under the
// badges and the one the repository description mirrors.
func readmeTagline(t *testing.T) string {
	t.Helper()
	lines := strings.Split(moduleFile(t, "README.md"), "\n")
	heading := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "# Atrium") {
			heading = i
			break
		}
	}
	require.GreaterOrEqual(t, heading, 0, "README has no `# Atrium` heading to read the tagline under")

	for _, line := range lines[heading+1:] {
		if strings.TrimSpace(line) != "" {
			require.True(t, strings.HasPrefix(line, "Atrium is "),
				"the first paragraph under the heading is not the tagline: %s", line)
			return line
		}
	}
	t.Fatal("README has nothing under its heading")
	return ""
}

// TestKnownAgentBinsMatchTheRegistry pins the two agent vocabularies to each
// other. config.KnownAgentBins is what a user types and what detection probes;
// agent.Adapters is what Atrium classifies a running pane with. They are
// separate lists in separate packages, and the help text derived from each
// (knownAgentBinList, supportedAgentSentence) reads as one set to whoever runs
// `atrium --help`.
//
// Both directions, because each omission ships a different defect: a probed
// binary with no adapter is a session whose status is read by the Generic
// fallback, and an adapter no binary probes is an agent the welcome overlay
// never offers to install.
func TestKnownAgentBinsMatchTheRegistry(t *testing.T) {
	bins := config.KnownAgentBins()
	require.NotEmpty(t, bins, "detection probes nothing; this guard would pass vacuously")

	matched := map[*agent.Adapter]string{}
	for _, bin := range bins {
		a := agent.Resolve(bin)
		require.NotSame(t, agent.Generic, a,
			"detection probes %q, which no adapter recognizes: its sessions would be classified by the Generic fallback", bin)
		require.NotContains(t, matched, a,
			"%q and %q both resolve to the %s adapter", bin, matched[a], a.DisplayName)
		matched[a] = bin
	}

	for _, a := range agent.Adapters() {
		require.Contains(t, matched, a,
			"the %s adapter is in the registry but no name in config.KnownAgentBins resolves to it, "+
				"so detection never offers it", a.DisplayName)
	}
}

// TestAgentHintsNameEveryKnownBin covers the help strings #887 found naming a
// subset of the probed binaries. They are derived today, so this fails only if
// one is written out again by hand — which is how the stale ones got there.
func TestAgentHintsNameEveryKnownBin(t *testing.T) {
	for _, tc := range []struct {
		site string
		text string
	}{
		{"atrium profiles detect --help", profilesDetectCmd.Long},
		{"atrium doctor --help", doctorCmd.Long},
	} {
		t.Run(tc.site, func(t *testing.T) {
			for _, bin := range config.KnownAgentBins() {
				require.Contains(t, tc.text, bin, "%s does not name the %q agent", tc.site, bin)
			}
		})
	}

	// Both halves of the root command, held to the whole derived sentence rather
	// than merely to containing each name: that is what fails on a hand-written
	// list, which is how the one this replaced came to advertise an agent no
	// adapter has ever supported.
	//
	// The Long is the half a user actually reads. Cobra's default help template is
	// `{{with (or .Long .Short)}}` and nothing in this repo calls SetHelpTemplate,
	// so a command carrying both renders only its Long — which is why asserting
	// this on the Short alone would guard text `atrium --help` never prints.
	t.Run("atrium --help", func(t *testing.T) {
		for _, tc := range []struct {
			half string
			text string
		}{
			{"Long", rootCmd.Long},
			{"Short", rootCmd.Short},
		} {
			require.Contains(t, tc.text, supportedAgentSentence(),
				"the root %s no longer renders the registry's agents verbatim", tc.half)
			for _, name := range agent.DisplayNames() {
				require.Contains(t, tc.text, name, "the root %s does not name %q", tc.half, name)
			}
		}
	})
}

// backtickedCommand matches an `atrium <name>` invocation inside backticks — the
// form the root help uses to point at a command.
var backtickedCommand = regexp.MustCompile("`atrium ([a-z][a-z-]*)`")

// TestRootHelpFitsEightyColumns is TestGuideFitsEightyColumns' rule applied to the
// root help, and it exists for the one line rootLong does not author: the agent
// list is as long as the registry makes it. Eighty is a typographic budget rather
// than a rendering guarantee — cobra reflows nothing, so an over-long line is
// soft-wrapped by the terminal wherever it happens to end.
//
// Display width, not rune count: the text carries em dashes, which are East-Asian
// Ambiguous.
func TestRootHelpFitsEightyColumns(t *testing.T) {
	for _, line := range strings.Split(rootCmd.Long, "\n") {
		require.LessOrEqual(t, runewidth.StringWidth(line), 80,
			"line exceeds the 80-column budget and will reflow: %q", line)
	}
}

// TestRootLongNamesOnlyRegisteredCommands is TestGuideNamesOnlyRegisteredCommands
// for the page a HUMAN reads. `atrium --help` is the first thing a new user runs,
// so a command it names has to exist; nothing else fails when one is renamed,
// because the Long is a string.
//
// Unlike the guide's guard this one parses, and can afford to: the Long is short
// and every command it names is backticked, so the pattern has a shape to key on
// rather than having to decide what counts as a command name in free prose. The
// non-empty check is what stops a rewrite that drops the pointers from passing
// silently.
func TestRootLongNamesOnlyRegisteredCommands(t *testing.T) {
	registered := map[string]bool{}
	for _, c := range userFacingCommands(t) {
		registered[c.Name()] = true
	}

	matches := backtickedCommand.FindAllStringSubmatch(rootCmd.Long, -1)
	require.NotEmpty(t, matches,
		"the root help names no command; it exists to point a first run at `atrium doctor`")

	for _, m := range matches {
		require.True(t, registered[m[1]],
			"`atrium --help` names `atrium %s`, which rootCmd does not register", m[1])
	}
}
