package transcript

import (
	"strings"
	"testing"
)

// TestTrimOverlapDropsDivider: a multi-line prose tail shared by transcript and
// pane is trimmed from the transcript, and ok=true (caller drops the divider).
func TestTrimOverlapDropsDivider(t *testing.T) {
	transcript := strings.Join([]string{
		"● Older history that scrolled away.",
		"",
		"● The repo side is closed out now.",
		"  Let me check the few loose ends.",
		"  Then capture a learning.",
	}, "\n")
	pane := strings.Join([]string{
		"● The repo side is closed out now.",
		"  Let me check the few loose ends.",
		"  Then capture a learning.",
		"",
		"❯ ",
		"  ⏵⏵ auto mode on (shift+tab to cycle)",
	}, "\n")

	trimmed, ok := TrimOverlap(transcript, pane)
	if !ok {
		t.Fatalf("expected overlap, got ok=false\ntrimmed=%q", trimmed)
	}
	if strings.Contains(trimmed, "repo side is closed") {
		t.Errorf("overlapping tail not trimmed:\n%s", trimmed)
	}
	if !strings.Contains(trimmed, "Older history") {
		t.Errorf("history above the overlap was lost:\n%s", trimmed)
	}
}

// TestTrimOverlapChromeFiltered: live-only chrome between the prose lines (a
// spinner footer, the input box) must not block the match.
func TestTrimOverlapChromeFiltered(t *testing.T) {
	transcript := strings.Join([]string{
		"● First real paragraph of the answer here.",
		"● Second real paragraph that is the tail.",
	}, "\n")
	pane := strings.Join([]string{
		"● First real paragraph of the answer here.",
		"✻ Crunched for 1m 22s",
		"● Second real paragraph that is the tail.",
		"╭──────────────╮",
		"❯ ",
		"╰──────────────╯",
	}, "\n")

	trimmed, ok := TrimOverlap(transcript, pane)
	if !ok {
		t.Fatalf("chrome blocked the match\ntrimmed=%q", trimmed)
	}
	if strings.Contains(trimmed, "First real paragraph") {
		t.Errorf("overlap not fully trimmed despite chrome:\n%s", trimmed)
	}
}

// TestTrimOverlapNoOverlapKeepsDivider: a freshly /clear'd pane shares nothing
// with the transcript, so ok=false and the transcript is returned untouched.
func TestTrimOverlapNoOverlapKeepsDivider(t *testing.T) {
	transcript := "● A long-finished conversation about the parser refactor."
	pane := strings.Join([]string{
		"❯ brand new prompt after clearing",
		"● starting fresh on something unrelated",
	}, "\n")
	trimmed, ok := TrimOverlap(transcript, pane)
	if ok {
		t.Errorf("expected no overlap, got ok=true\ntrimmed=%q", trimmed)
	}
	if trimmed != transcript {
		t.Errorf("transcript mutated on no-overlap: %q", trimmed)
	}
}

// TestTrimOverlapShortLoneLineRejected: a single short shared line ("Done.") is
// too generic to anchor an overlap on its own.
func TestTrimOverlapShortLoneLineRejected(t *testing.T) {
	transcript := strings.Join([]string{
		"● Some unique earlier content about wrapping.",
		"● Done.",
	}, "\n")
	pane := strings.Join([]string{
		"● A totally different current screen.",
		"● Done.",
	}, "\n")
	if _, ok := TrimOverlap(transcript, pane); ok {
		t.Error("a lone short line should not anchor an overlap")
	}
}

// TestTrimOverlapLongLoneLineAccepted: a single shared line that is long and
// distinctive is a confident anchor.
func TestTrimOverlapLongLoneLineAccepted(t *testing.T) {
	line := "● The only remaining cleanup isn't mine to do — kill the session when you are done."
	transcript := "● earlier unique paragraph\n" + line
	pane := line + "\n❯ "
	trimmed, ok := TrimOverlap(transcript, pane)
	if !ok {
		t.Fatalf("a long distinctive line should anchor the overlap\ntrimmed=%q", trimmed)
	}
	if strings.Contains(trimmed, "remaining cleanup") {
		t.Errorf("long line not trimmed:\n%s", trimmed)
	}
}

// TestTrimOverlapAnchoredAtPaneTop: the overlap must anchor at the pane's top
// (where transcript history flows into the live screen). A transcript tail line
// that also appears *lower* in the pane — with a different, newer line at the
// pane top — must NOT splice, or history would be reordered/duplicated. This is
// the "never a wrong splice" guarantee.
func TestTrimOverlapAnchoredAtPaneTop(t *testing.T) {
	transcript := strings.Join([]string{
		"● A first earlier paragraph that is unique to history here.",
		"● B second earlier paragraph that is unique to history here.",
		"● C the distinctive trailing line long enough to anchor on its own.",
	}, "\n")
	// The distinctive tail line C appears at pane[1], not pane[0]; pane[0] is a
	// newer line the transcript does not end with.
	pane := strings.Join([]string{
		"● D a brand new live line shown at the very top of the screen now.",
		"● C the distinctive trailing line long enough to anchor on its own.",
		"❯ ",
	}, "\n")
	trimmed, ok := TrimOverlap(transcript, pane)
	if ok {
		t.Errorf("a tail match below the pane top must not splice\ntrimmed=%q", trimmed)
	}
	if trimmed != transcript {
		t.Errorf("transcript mutated on rejected splice: %q", trimmed)
	}
}

// TestTrimOverlapWrapMismatch is the regression for the user-reported scroll
// duplication: the same shared paragraph is wrapped one way in the transcript
// (our "● "/hanging-indent renderer) and another way in the pane (Claude's live
// render at the same cell width but a different left margin), so the seam's
// prose lines never line up character-for-character. A robust overlap must key
// on the shared *words*, not the shared line breaks, and still splice — leaving
// the paragraph exactly once. Line-level matching falls to the divider here and
// shows the block twice.
func TestTrimOverlapWrapMismatch(t *testing.T) {
	// Transcript tail: unique history, then the insight, wrapped narrow.
	transcript := strings.Join([]string{
		"● Older unique history that scrolled above the screen.",
		"",
		"★ Insight ─────",
		"The PR is still BLOCKED but the reason has cleanly",
		"shifted from required_conversation_resolution to the",
		"required_pull_request_reviews gate instead.",
	}, "\n")
	// Pane top: the same insight, wrapped wider (different break points), then
	// newer live content the transcript does not yet have.
	pane := strings.Join([]string{
		"★ Insight ─────",
		"The PR is still BLOCKED but the reason has cleanly shifted from",
		"required_conversation_resolution to the required_pull_request_reviews gate instead.",
		"",
		"❯ Any update?",
	}, "\n")

	trimmed, ok := TrimOverlap(transcript, pane)
	if !ok {
		t.Fatalf("differently-wrapped shared paragraph should still splice\ntrimmed=%q", trimmed)
	}
	if strings.Contains(trimmed, "BLOCKED") || strings.Contains(trimmed, "★ Insight") {
		t.Errorf("the shared insight was not trimmed (it would render twice):\n%s", trimmed)
	}
	if !strings.Contains(trimmed, "Older unique history") {
		t.Errorf("unique history above the overlap was lost:\n%s", trimmed)
	}
}

// TestTrimOverlapOSC8Survives: a transcript line carrying an OSC 8 hyperlink
// normalizes to its visible text and still matches the plain pane line.
func TestTrimOverlapOSC8Survives(t *testing.T) {
	const visible = "● See the PR at the project tracker for details here."
	osc8 := "● See the PR at \x1b]8;;https://example.com/pr/1\x1b\\the project tracker\x1b]8;;\x1b\\ for details here."
	transcript := "● earlier unique line about something\n" + osc8
	pane := visible + "\n❯ "
	trimmed, ok := TrimOverlap(transcript, pane)
	if !ok {
		t.Fatalf("OSC8 link broke normalization match\ntrimmed=%q", trimmed)
	}
	if strings.Contains(trimmed, "project tracker") {
		t.Errorf("OSC8 line not trimmed:\n%s", trimmed)
	}
}
