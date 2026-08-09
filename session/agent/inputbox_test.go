package agent

import (
	"reflect"
	"testing"
)

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

// A live composer holding a collapsed paste: claude renders a ≥4-line bracketed paste as a
// "[Pasted text #N +L lines]" placeholder chip instead of the literal text (captured live from
// claude 2.1.207, 2026-07-13). The literal first line never appears, which is why the delivery
// signature check needs the chip as its landing signal.
const liveCollapsedPasteComposer = "" +
	"  some earlier transcript line\n" +
	liveRule + "\n" +
	"❯ [Pasted text #1 +29 lines]\n" +
	liveRule + "\n" +
	"  ? for shortcuts · ← for agents\n"

// The same composer after the delivery retry re-pasted before the fix: chips accumulate on one
// line. The chip predicate must still recognize this as collapsed paste.
const liveAccumulatedPasteComposer = "" +
	"  some earlier transcript line\n" +
	liveRule + "\n" +
	"❯ [Pasted text #1 +29 lines][Pasted text #2 +28 lines]\n" +
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
		text, ok := inputBoxText(emptyBox, defaultPrompts)
		if !ok {
			t.Fatal("an on-screen composer must be detected as present")
		}
		if text != "" {
			t.Fatalf("an empty composer must read back as empty, got %q", text)
		}
	})

	t.Run("typed text is read back across wrapped rows", func(t *testing.T) {
		text, ok := inputBoxText(typedBox, defaultPrompts)
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
		if _, ok := inputBoxText(preBoxFrame, defaultPrompts); ok {
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
		if _, ok := inputBoxText(content, defaultPrompts); ok {
			t.Fatal("a '>' outside the bottom window must not count as the input box")
		}
	})

	t.Run("live borderless empty composer reads back its ghost hint", func(t *testing.T) {
		// A real empty composer is not literally blank — it shows claude's dim suggestion.
		// found must be true (a box is on screen) and the readback is that hint, which the
		// delivery check tolerates because it matches against the prompt signature, not "".
		text, ok := inputBoxText(liveEmptyComposer, defaultPrompts)
		if !ok {
			t.Fatal("a live composer must be detected even when it only shows ghost text")
		}
		if text != `Try "how do I log an error?"` {
			t.Fatalf("readback = %q, want the ghost hint", text)
		}
	})

	t.Run("live borderless multi-line prompt is joined across rule-wrapped rows", func(t *testing.T) {
		text, ok := inputBoxText(liveTypedComposer, defaultPrompts)
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
		if _, ok := inputBoxText(liveTrustGate, defaultPrompts); !ok {
			t.Fatal("the menu selector reads as a box line; this test pins that known limit")
		}
	})
}

func TestClaudePasteCollapsed(t *testing.T) {
	// The chip is read back through the same input-box parser the delivery check uses, so
	// assert on inputBoxText's output — the exact string claudePasteCollapsed will see.
	t.Run("inputBoxText reads back the collapsed-paste chip verbatim", func(t *testing.T) {
		text, ok := inputBoxText(liveCollapsedPasteComposer, defaultPrompts)
		if !ok {
			t.Fatal("a composer showing a collapsed paste must be detected as present")
		}
		if text != "[Pasted text #1 +29 lines]" {
			t.Fatalf("readback = %q, want the collapsed-paste chip", text)
		}
	})

	t.Run("inputBoxText reads back accumulated chips from a re-pasted composer", func(t *testing.T) {
		// The pre-fix retry loop could stack chips on one composer line; the parser must read
		// the whole run back so the delivery check still recognizes it as a collapsed paste.
		text, ok := inputBoxText(liveAccumulatedPasteComposer, defaultPrompts)
		if !ok {
			t.Fatal("a composer showing accumulated collapsed pastes must be detected as present")
		}
		if text != "[Pasted text #1 +29 lines][Pasted text #2 +28 lines]" {
			t.Fatalf("readback = %q, want the accumulated chips", text)
		}
		if !claudePasteCollapsed(text) {
			t.Error("accumulated chips read back from the pane must be recognized as a collapsed paste")
		}
	})

	collapsed := []struct {
		name, box string
	}{
		{"single chip", "[Pasted text #1 +29 lines]"},
		{"no index", "[Pasted text +5 lines]"},
		{"singular line", "[Pasted text +1 line]"},
		{"accumulated chips", "[Pasted text #1 +29 lines][Pasted text #2 +28 lines]"},
	}
	for _, c := range collapsed {
		t.Run("collapsed: "+c.name, func(t *testing.T) {
			if !claudePasteCollapsed(c.box) {
				t.Errorf("%q must be recognized as a collapsed paste", c.box)
			}
		})
	}

	notCollapsed := []struct {
		name, box string
	}{
		{"empty box", ""},
		{"typed text", "refactor the parser and add a regression test"},
		{"mentions pasted but no chip", "explain how bracketed paste works"},
		{"live ghost hint", `Try "how do I log an error?"`},
	}
	for _, c := range notCollapsed {
		t.Run("plain: "+c.name, func(t *testing.T) {
			if claudePasteCollapsed(c.box) {
				t.Errorf("%q must not be recognized as a collapsed paste", c.box)
			}
		})
	}

	// End-to-end through the adapter wiring: claude has the predicate, others leave it nil.
	if claude := Resolve("claude"); claude.PasteCollapsed == nil {
		t.Error("the claude adapter must wire PasteCollapsed")
	} else if text, _ := claude.InputBoxText(liveCollapsedPasteComposer); !claude.PasteCollapsed(text) {
		t.Error("the claude adapter must recognize its own collapsed-paste chip")
	}
	// Derived from Adapters(), not a hardcoded name list: the literal one this replaced
	// read {"codex","gemini","aider"} and had silently skipped agy since #512 added it.
	for _, a := range Adapters() {
		if a.Key == KeyClaude {
			continue
		}
		if a.PasteCollapsed != nil {
			t.Errorf("%s renders pastes inline and must leave PasteCollapsed nil", a.Key)
		}
	}
}

// composerPanes is one on-screen composer per adapter. Every adapter in the registry must
// have an entry: an adapter whose InputBoxVisible is unasserted is an adapter that can ship
// with prompt delivery silently dead, which is exactly what #510 was. Codex's "› " had been
// written into this package's own fixtures for as long as the adapter existed, and the only
// test that exercised InputBoxVisible resolved "claude" and checked nothing else — so the
// glyph was in the tree, in this very package, and never once fed to the predicate.
//
// Provenance differs per entry and is stated at each fixture's declaration; two of these
// (gemini's, and claude's bordered pair) are composed from source rather than driven. The
// table's job is not to certify each glyph — it is to make a missing assertion a build
// failure instead of an absence nobody can see.
var composerPanes = map[Key]string{
	KeyClaude: liveEmptyComposer,
	KeyCodex:  codexTypedComposerPane120,
	KeyGemini: geminiIdlePane,
	KeyAider:  aiderIdlePane,
	KeyAgy:    agyIdlePane,
}

func TestAdapterInputBoxVisible(t *testing.T) {
	adapters := Adapters()
	if len(adapters) != len(composerPanes) {
		t.Fatalf("Adapters() has %d entries, composerPanes has %d: a new adapter must bring a "+
			"composer fixture, or its prompt delivery is unasserted (#510)",
			len(adapters), len(composerPanes))
	}
	for _, a := range adapters {
		pane, ok := composerPanes[a.Key]
		if !ok {
			t.Fatalf("adapter %q has no composer fixture", a.Key)
		}
		t.Run(string(a.Key), func(t *testing.T) {
			if !a.InputBoxVisible(pane) {
				t.Error("this agent's live composer must read as an input box; while it does " +
					"not, AwaitingInput is permanently false and queued prompts are neither " +
					"delivered nor expired (#510)")
			}
			if a.InputBoxVisible(preBoxFrame) {
				t.Error("a startup frame carrying no prompt glyph at all must not read as a box")
			}
		})
	}

	// Claude's other supported box shape, kept from this test's claude-only original: the
	// "│"-bordered build, empty and with typed text.
	claude := Resolve("claude")
	if !claude.InputBoxVisible(emptyBox) {
		t.Error("the empty composer must be reported visible")
	}
	if !claude.InputBoxVisible(typedBox) {
		t.Error("the composer with typed text must be reported visible")
	}
}

// The one optional Adapter field whose zero value must not mean "off". A per-adapter glyph
// set that defaulted to empty would transplant #510 onto whichever adapter inherited it —
// agy and aider both depend on ">" being accepted, and agy's dependence is new as of #512,
// so the regression would land the same day. Generic is a bare &Adapter{} that is
// deliberately NOT in registry, so Adapters() cannot see it; it and a freshly drafted
// zero-value adapter are checked here for that reason.
func TestInputBoxPromptsNeverResolveEmpty(t *testing.T) {
	subjects := append([]*Adapter{}, Adapters()...)
	subjects = append(subjects, Generic, &Adapter{})
	for _, a := range subjects {
		if len(a.inputBoxPrompts().chars) == 0 {
			t.Errorf("adapter %q resolves to an empty prompt set: InputBoxVisible would be "+
				"permanently false and its queued prompts never delivered (#510)", a.Key)
		}
	}
	if got := (&Adapter{}).inputBoxPrompts().chars; !reflect.DeepEqual(got, defaultPrompts.chars) {
		t.Errorf("a newly drafted adapter resolves to %q, want the default %q",
			got, defaultPrompts.chars)
	}
}

// Detection and stripping are two loops over the same set, and they were two independently
// hardcoded literal pairs before #510. A glyph accepted but not stripped leaks into the
// readback — and boxHoldsPrompt (session/prompt.go) compares by substring, so it would
// still match and nothing downstream would notice. Only a parity check sees it.
func TestEveryAcceptedPromptGlyphIsAlsoStripped(t *testing.T) {
	subjects := append([]*Adapter{}, Adapters()...)
	subjects = append(subjects, Generic)
	for _, a := range subjects {
		p := a.inputBoxPrompts()
		for _, g := range p.chars {
			line := g + " hello world"
			if !isInputBoxLine(line, p) {
				t.Errorf("%s: %q is in the accepted set but %q does not read as a box line", a.Key, g, line)
				continue
			}
			if got := stripBoxInterior(line, p); got != "hello world" {
				t.Errorf("%s: %q accepted but not stripped — readback %q, want %q",
					a.Key, g, got, "hello world")
			}
		}
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
