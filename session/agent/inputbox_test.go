package agent

import "testing"

// A realistic empty claude composer: rounded box with the "❯" prompt and the live footer
// below it. No gate, no blocking prompt — keystrokes typed here land in the box.
const emptyBox = "" +
	"  Some earlier transcript line\n" +
	"\n" +
	"╭──────────────────────────────────────────────╮\n" +
	"│ ❯                                              │\n" +
	"╰──────────────────────────────────────────────╯\n" +
	"  ? for shortcuts\n"

// The same composer with a typed (but unsubmitted) prompt, wrapped across two interior rows.
const typedBox = "" +
	"╭──────────────────────────────────────────────╮\n" +
	"│ ❯ refactor the parser and add a regression    │\n" +
	"│   test for the nested case                     │\n" +
	"╰──────────────────────────────────────────────╯\n" +
	"  ? for shortcuts\n"

// A startup frame before the box has painted: a banner, no "❯" composer.
const preBoxFrame = "" +
	"  ✻ Welcome to Claude Code\n" +
	"\n" +
	"  Booting…\n"

// Fixtures captured from a live claude 2.1.x session, which draws the composer with a
// borderless interior wrapped by "─" horizontal rules (no "│" side borders) and shows a
// dim ghost suggestion in an otherwise-empty box. These pin the rendering the detection
// must actually parse — the bordered fixtures above are the other supported shape.
const liveRule = "────────────────────────────────────────────────────────────────────"

// An empty live composer still carries claude's ghost-text hint; the readback reflects it.
const liveEmptyComposer = "" +
	"  some earlier transcript line\n" +
	"                                                       ● high · /effort\n" +
	liveRule + "\n" +
	"❯ Try \"how do I log an error?\"\n" +
	liveRule + "\n" +
	"  ? for shortcuts · ← for agents\n"

// A live composer holding a multi-line prompt: the rows wrap with no side borders and are
// terminated by the bottom rule, above the footer.
const liveTypedComposer = "" +
	"                                              ctrl+g to edit in VS Code\n" +
	liveRule + "\n" +
	"❯ refactor the parser module\n" +
	"  and add a regression test\n" +
	"  for the nested case\n" +
	liveRule + "\n" +
	"  ? for shortcuts · ← for agents\n"

// The live folder-trust gate: a "❯ 1. …" selector, which reads as a box line even though it
// is a gate. This is exactly why AwaitingInput cannot rely on box presence alone to exclude
// it — GateUp must.
const liveTrustGate = "" +
	" Quick safety check: Is this a project you created or one you trust?\n" +
	"\n" +
	" ❯ 1. Yes, I trust this folder\n" +
	"   2. No, exit\n" +
	"\n" +
	" Enter to confirm · Esc to cancel\n"

func TestInputBoxText(t *testing.T) {
	t.Run("empty box is found with no text", func(t *testing.T) {
		text, ok := inputBoxText(emptyBox)
		if !ok {
			t.Fatal("an on-screen composer must be detected as present")
		}
		if text != "" {
			t.Fatalf("an empty composer must read back as empty, got %q", text)
		}
	})

	t.Run("typed text is read back across wrapped rows", func(t *testing.T) {
		text, ok := inputBoxText(typedBox)
		if !ok {
			t.Fatal("a composer with text must be detected as present")
		}
		// The two interior rows are joined; the exact spacing is normalized, so assert on
		// the squashed-whitespace content the delivery check actually compares.
		want := "refactor the parser and add a regression test for the nested case"
		if text != want {
			t.Fatalf("readback = %q, want %q", text, want)
		}
	})

	t.Run("no composer on a pre-box startup frame", func(t *testing.T) {
		if _, ok := inputBoxText(preBoxFrame); ok {
			t.Fatal("a frame without an input box must not be detected as a composer")
		}
	})

	t.Run("a quoted '>' in transcript far above the bottom is not the box", func(t *testing.T) {
		// A ">" at the very top, then many non-empty lines, then no real box: the bottom
		// WindowPrompt budget must keep the stray quote from being read as the composer.
		content := "  > not the box, this is a quoted shell line\n"
		for i := 0; i < WindowPrompt+5; i++ {
			content += "  plain transcript line\n"
		}
		if _, ok := inputBoxText(content); ok {
			t.Fatal("a '>' outside the bottom window must not count as the input box")
		}
	})

	t.Run("live borderless empty composer reads back its ghost hint", func(t *testing.T) {
		// A real empty composer is not literally blank — it shows claude's dim suggestion.
		// found must be true (a box is on screen) and the readback is that hint, which the
		// delivery check tolerates because it matches against the prompt signature, not "".
		text, ok := inputBoxText(liveEmptyComposer)
		if !ok {
			t.Fatal("a live composer must be detected even when it only shows ghost text")
		}
		if text != `Try "how do I log an error?"` {
			t.Fatalf("readback = %q, want the ghost hint", text)
		}
	})

	t.Run("live borderless multi-line prompt is joined across rule-wrapped rows", func(t *testing.T) {
		text, ok := inputBoxText(liveTypedComposer)
		if !ok {
			t.Fatal("a live composer with text must be detected as present")
		}
		want := "refactor the parser module and add a regression test for the nested case"
		if text != want {
			t.Fatalf("readback = %q, want %q (rows joined up to the bottom rule, footer excluded)", text, want)
		}
	})

	t.Run("a menu-style gate's selector reads as a box (why GateUp, not the box, excludes it)", func(t *testing.T) {
		// The trust gate's "❯ 1. …" line satisfies the box check on its own. This documents
		// the limit AwaitingInput works around by also consulting GateUp/DetectPrompt.
		if _, ok := inputBoxText(liveTrustGate); !ok {
			t.Fatal("the menu selector reads as a box line; this test pins that known limit")
		}
	})
}

func TestAdapterInputBoxVisible(t *testing.T) {
	claude := Resolve("claude")
	if !claude.InputBoxVisible(emptyBox) {
		t.Error("the empty composer must be reported visible")
	}
	if !claude.InputBoxVisible(typedBox) {
		t.Error("the composer with typed text must be reported visible")
	}
	if !claude.InputBoxVisible(liveEmptyComposer) {
		t.Error("a live borderless composer must be reported visible")
	}
	if claude.InputBoxVisible(preBoxFrame) {
		t.Error("a pre-box startup frame must not be reported visible")
	}
}

// The menu-style trust gate reads as a box (InputBoxVisible is true), so the only thing that
// keeps a queued prompt off it is GateUp matching the gate's wording. Pin both halves so a
// reword that breaks the gate match can't quietly start typing onto the trust screen.
func TestTrustGateExcludedByGateNotBox(t *testing.T) {
	claude := Resolve("claude")
	if !claude.InputBoxVisible(liveTrustGate) {
		t.Fatal("precondition: the menu selector reads as a box, so the box check can't exclude it")
	}
	if _, gated := claude.GateUp(liveTrustGate); !gated {
		t.Error("the folder-trust gate must be recognized by GateUp, the only thing excluding it")
	}
}
