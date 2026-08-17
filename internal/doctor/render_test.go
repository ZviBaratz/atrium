package doctor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/agent"
)

func TestRenderGatesEmpty(t *testing.T) {
	if out := RenderGates(nil); out != "" {
		t.Errorf("RenderGates(nil) = %q, want \"\" so the section does not render at all", out)
	}
}

// TestRenderGatesMatching pins that a matching gate still prints a row. Doctor is
// a diagnostic: "checked, and it matches" must not look like "the check found
// nothing to say".
func TestRenderGatesMatching(t *testing.T) {
	out := RenderGates([]GateResult{
		{Name: "Claude Code", Account: defaultAccount, Gate: "tengu_copper_thistle", Pinned: false, Actual: false, State: GateMatchesPin},
	})

	for _, want := range []string{"Feature gates:", "Claude Code", "tengu_copper_thistle", "default", "ok (false)"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderGates() output missing %q\n--- got ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "→") {
		t.Errorf("RenderGates() rendered a remediation hint with nothing flipped\n--- got ---\n%s", out)
	}
}

func TestRenderGatesFlipped(t *testing.T) {
	out := RenderGates([]GateResult{
		{Name: "Claude Code", Account: "personal", Gate: "tengu_copper_thistle", Pinned: false, Actual: true, State: GateFlipped},
		{Name: "Claude Code", Account: "work", Gate: "tengu_copper_thistle", Pinned: false, State: GateUnknown},
	})

	for _, want := range []string{
		"personal", "⚠ flipped (pinned false, resolved true)",
		"work", "unknown (no comparable value could be read)",
		"→ heuristics were verified on the other branch", "session/agent/registry.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderGates() output missing %q\n--- got ---\n%s", want, out)
		}
	}
	if n := strings.Count(out, "→"); n != 1 {
		t.Errorf("RenderGates() rendered %d hints, want 1 section-level hint\n--- got ---\n%s", n, out)
	}
}

func TestRender(t *testing.T) {
	// gemini's pair is TETHERED rather than self-contained: geminiWithinPin is the same
	// installed version check_test.go feeds, and the verified half is read off the live
	// registry. An earlier draft wrote 0.55.9/0.55.1 as literals under a comment observing
	// that self-contained literals are "exactly why they drift" — which is a diagnosis, not a
	// fix, and it left this file re-armed for the next pin bump after repairing the other file
	// that had the same bug. This row read 0.27.4/0.27 long after gemini's pin moved.
	geminiVerified := registryPin(t, agent.KeyGemini)
	require.NotEqual(t, geminiWithinPin, geminiVerified,
		"the Gemini row needs two DISTINCT versions or its column-order assertion below is "+
			"vacuous — both indices would be the same. If a pin bump made these equal, move "+
			"geminiWithinPin up within the minor rather than deleting the order check")

	out := Render([]Result{
		{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Verified: "2.1.170", Status: StatusDrifted},
		{Key: agent.KeyGemini, Name: "Gemini CLI", Installed: geminiWithinPin, Verified: geminiVerified, Status: StatusOK},
		{Key: agent.KeyCodex, Name: "Codex", Status: StatusNotInstalled},
		{Key: agent.KeyAider, Name: "Aider", Installed: "0.64.1", Status: StatusUnknown},
	})

	// Asserted PER ROW, not against the whole table. Round 3 of #715 added gemini's two version
	// strings here to close the hole that let this row sit at 0.27.4/0.27 long after the pin
	// moved, but added them to a flat Contains sweep over the entire output — which cannot tell
	// which row rendered them. A Render that printed gemini's versions in the codex row left
	// both literals somewhere in `out` and the test green, and finding the row first is what
	// catches that.
	//
	// It is NOT what catches a column swap, which an earlier draft of this comment also claimed
	// for it: a swap keeps both literals on the same row, so no per-row lookup can see one. The
	// `ordered` column below is the only thing here that can, which is why it is a field of the
	// table rather than a loop over one hand-picked row — the previous form scanned "Claude
	// Code" alone, leaving the Gemini row's two versions with no order assertion at all.
	for _, tc := range []struct {
		label   string
		wants   []string
		ordered []string // pairs that must appear left-to-right in this row
	}{
		{"Claude Code", []string{"2.1.179", "2.1.170", "drifted"}, []string{"2.1.179", "2.1.170"}},
		{"Gemini CLI", []string{geminiWithinPin, geminiVerified, "ok"}, []string{geminiWithinPin, geminiVerified}},
		{"Codex", []string{"not installed"}, nil},
		{"Aider", []string{"0.64.1", "unknown"}, nil},
	} {
		row := renderedRow(t, out, tc.label)
		for _, want := range tc.wants {
			require.Containsf(t, row, want,
				"Render() row %q is missing %q\n--- got ---\n%s", tc.label, want, out)
		}

		// Installed must precede Verified. Asserted with both indices proved present first: the
		// bare `Index(a) > Index(b)` this replaced is never true when the first needle is
		// missing (-1 loses to every real index), so it passed silently on a row that had
		// dropped a value — and with no found-flag its enclosing loop also passed silently on a
		// row Render had dropped entirely.
		for i := 0; i+1 < len(tc.ordered); i++ {
			first, second := strings.Index(row, tc.ordered[i]), strings.Index(row, tc.ordered[i+1])
			require.GreaterOrEqualf(t, first, 0, "row %q lost %q", tc.label, tc.ordered[i])
			require.GreaterOrEqualf(t, second, 0, "row %q lost %q", tc.label, tc.ordered[i+1])
			require.Lessf(t, first, second,
				"Render() put %q before %q on the %q row — a column swap leaves both literals "+
					"present and reverses what the row tells the user\n--- row ---\n%s",
				tc.ordered[i+1], tc.ordered[i], tc.label, row)
		}
	}
}

// registryPin returns the adapter's VerifiedVersion from the LIVE registry, so a fixture that
// must match the pin cannot go stale against it — the failure #713 found in this package twice.
func registryPin(t *testing.T, key agent.Key) string {
	t.Helper()
	for _, a := range agent.Adapters() {
		if a.Key == key {
			return a.VerifiedVersion
		}
	}
	t.Fatalf("no adapter registered for %q", key)
	return ""
}

// TestGateUnknownLabelClaimsOnlyWhatWasEstablished guards the wording, because the wording
// is the whole content of an unknown row.
//
// The label read "unknown (no resolved value on disk)", which is a claim about the disk
// this code has not earned on two of the five paths to GateUnknown, for two different
// reasons. A relative or empty config dir is refused before any file is opened
// (TestFileGateReaderRejectsRelativeDir), so nothing about the disk was established; a
// malformed .claude.json is read and then fails at the parse (TestFileGateReaderUnreadable),
// so a resolved value may well be sitting in it. Reporting a failed read as a determined
// absence is the conflation RenderGates' own doc comment forbids — in the label of the
// section that states the rule.
//
// It also has to stay an unknown rather than drifting into a verdict: a gate check is a
// report, and GatesFlipped deliberately ignores this state.
func TestGateUnknownLabelClaimsOnlyWhatWasEstablished(t *testing.T) {
	got := GateResult{State: GateUnknown, Pinned: true}.label()

	if strings.Contains(got, "on disk") {
		t.Errorf("label = %q, want no claim about the disk: nothing was read on two of the "+
			"five paths that reach GateUnknown", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("label = %q, want it to still read as unknown", got)
	}
	if !strings.Contains(got, "could be read") {
		t.Errorf("label = %q, want it to name what was established — GateUnknown's own "+
			"doc comment says \"no comparable value could be read\"", got)
	}
	for _, verdict := range []string{"ok", "flipped", "⚠"} {
		if strings.Contains(got, verdict) {
			t.Errorf("label = %q, want no verdict %q: an unread gate is not a finding", got, verdict)
		}
	}
}
