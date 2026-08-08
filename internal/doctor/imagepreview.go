package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/colorprofile"
)

// ImagePreviewResult is what doctor can determine about the image overlay's
// pixel rung — the kitty graphics protocol Atrium draws agent screenshots and
// plots with where a terminal supports it (#398).
//
// Confirmed is always false, and, as with KeyboardResult.Probed and
// SchemeResult.OSC11Probed, that is the point rather than a stub. The rung is
// established by a transmission the terminal answers mid-session, which needs
// the running TUI: Atrium sends the image and waits to be handed an ID back, and
// only then does a single placeholder cell exist. So doctor can report that the
// rung is ELIGIBLE and never that it works, and the distinction is the whole
// reason this section is worth printing — a user whose terminal is on the list
// and who still sees glyphs has learned something specific.
//
// Deliberately absent is a table of which terminals implement the protocol.
// Unicode placeholder support is not probeable — the protocol's own query
// answers for graphics as a whole — so any such table is a guess that goes stale
// between releases, and it would read as authority. What is printed instead is
// the environment Atrium resolved and the first veto that stopped it.
type ImagePreviewResult struct {
	Preference  string // the resolved image_preview value, never empty
	Term        string // $TERM, "" when unset
	TermProgram string // $TERM_PROGRAM, "" when unset
	InTmux      bool   // $TMUX is set: this shell is inside a tmux client
	Recognized  bool   // the environment names a terminal Atrium tested
	Mono        bool   // NO_COLOR: the foreground the image ID rides in is stripped
	TrueColor   bool   // the profile carries the 24-bit foreground that IS the image ID
	OneCell     bool   // a placeholder measures the one column it occupies
	Eligible    bool   // a transmission would be attempted
	Confirmed   bool   // always false; see the type comment
}

// CheckImagePreview reads the rungs available outside the TUI.
//
// environ and pref are parameters rather than lookups so the rule is a pure
// function of its input, matching CheckKeyboard and CheckScheme.
//
// A duplicated name resolves differently depending on WHICH name, and the two
// rules are worth stating because a reader assumes one of them: TERM,
// TERM_PROGRAM and TMUX are last-wins, matching os.Environ; the terminal's own
// variables are not, because hasNonEmpty answers "any occurrence is non-empty"
// and a later empty one does not take it back. app.kittyTerminalEnv resolves
// both halves the same way, which is the property that matters and the one
// TestKittyTerminalEnv_LastWinsLikeDoctor pins. os.Environ does not hand out
// duplicates, so the difference is reachable only from a synthesized slice.
//
// placeholdersOneCell is a parameter for a different reason: it is MEASURED, not
// read. Whether U+10EEEE occupies the column it measures cannot be derived from
// the environment by any rule both of Atrium's width libraries agree on — that
// divergence is the entire finding behind imageview.PlaceholdersMeasureOneCell —
// so the caller measures and hands the answer in.
//
// All three drawability vetoes have to be replicated here, not just the one this
// started with. A section that reports "eligible" where the TUI silently refuses
// tells the exact user it exists for — on the list, still seeing glyphs — the
// opposite of the truth, and sends them looking for a fault in their terminal.
// TestDoctorAgreesWithTheTUI (app/image_kitty_test.go) sweeps the two rules
// against each other so this cannot drift again.
func CheckImagePreview(environ []string, pref string, placeholdersOneCell bool) ImagePreviewResult {
	r := ImagePreviewResult{Preference: pref}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch name {
		case "TERM":
			r.Term = value
		case "TERM_PROGRAM":
			r.TermProgram = value
		case "TMUX":
			// Presence is the signal, as it is for tmux itself.
			r.InTmux = value != ""
		}
	}
	r.Recognized = r.Term == "xterm-kitty" || r.TermProgram == "ghostty" ||
		hasNonEmpty(environ, "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "GHOSTTY_BIN_DIR")
	// The three drawability vetoes, in app.kittyEligible's own order.
	// theme.NoColorRequested and colorprofile.Env are the same rules the TUI
	// resolves these with.
	r.Mono = theme.NoColorRequested(environ)
	r.TrueColor = colorprofile.Env(environ) == colorprofile.TrueColor
	r.OneCell = placeholdersOneCell

	switch {
	case r.Mono || !r.TrueColor || !r.OneCell:
		// None of these is a preference, so image_preview: kitty does not override
		// them: the opt-in says "this terminal is not on your list", never "draw it
		// wrong".
		r.Eligible = false
	case pref == config.ImagePreviewKitty:
		r.Eligible = true
	case pref == config.ImagePreviewAuto:
		r.Eligible = !r.InTmux && r.Recognized
	}
	return r
}

// hasNonEmpty reports whether any of names is set to a non-empty value.
func hasNonEmpty(environ []string, names ...string) bool {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		for _, want := range names {
			if name == want {
				return true
			}
		}
	}
	return false
}

// RenderImagePreview formats the report under an "Image preview:" header,
// parallel to RenderKeyboard.
func RenderImagePreview(r ImagePreviewResult) string {
	var b strings.Builder
	b.WriteString("Image preview:\n")

	fmt.Fprintf(&b, "  %-18s %s\n", "image_preview", r.Preference)
	fmt.Fprintf(&b, "  %-18s %s\n", "TERM", orUnset(r.Term))
	fmt.Fprintf(&b, "  %-18s %s\n", "TERM_PROGRAM", orUnset(r.TermProgram))

	switch {
	case r.Mono:
		fmt.Fprintf(&b, "  %-18s not attempted — NO_COLOR strips the foreground colour, and\n", "pixels")
		fmt.Fprintf(&b, "  %-18s that colour is how a cell names the image it shows\n", "")
	case !r.TrueColor:
		fmt.Fprintf(&b, "  %-18s not attempted — this environment reports fewer than 24-bit\n", "pixels")
		fmt.Fprintf(&b, "  %-18s colours, and the image ID rides in a 24-bit foreground\n", "")
		b.WriteString("         → set COLORTERM=truecolor if your terminal supports it\n")
	case !r.OneCell:
		// Measured, not read off RUNEWIDTH_EASTASIAN — see the parameter's note on
		// CheckImagePreview. Naming the variable anyway because it is the one a
		// user can actually act on, and it is how this state is nearly always
		// reached.
		fmt.Fprintf(&b, "  %-18s not attempted — a placeholder cell measures two columns\n", "pixels")
		fmt.Fprintf(&b, "  %-18s here, so every row of the picture would overflow the box\n", "")
		b.WriteString("         → unset RUNEWIDTH_EASTASIAN, or use a non-east-asian locale\n")
	case r.Preference == config.ImagePreviewOff:
		fmt.Fprintf(&b, "  %-18s no overlay — hinting an image path only copies it\n", "pixels")
	case r.Preference == config.ImagePreviewGlyph:
		fmt.Fprintf(&b, "  %-18s not attempted — image_preview is set to glyph\n", "pixels")
	// NOT gated on auto. image_preview: kitty skips the tmux veto in
	// CheckImagePreview, so a kitty user inside tmux who sets it comes out
	// Eligible — and would otherwise fall to the "eligible" arm below and be told
	// this configuration might work, when it is the one measured NOT to. The
	// panel in ui/overlay leaves the tmux caveat to this section on the grounds
	// that doctor knows whether the user is actually in tmux, so this arm has to
	// answer for both preferences or that reasoning has no home.
	case r.InTmux:
		fmt.Fprintf(&b, "  %-18s not attempted — inside tmux, which does not forward the\n", "pixels")
		fmt.Fprintf(&b, "  %-18s graphics protocol to the terminal underneath\n", "")
		// Deliberately NOT "turn on allow-passthrough". Measured: with passthrough
		// on, an unwrapped payload still draws no reply, because tmux forwards
		// only what is inside its own DCS envelope — which Atrium does not emit
		// yet. Printing that as an actionable arrow would send a user to change
		// two settings for no change at all.
		b.WriteString("         → detach and run Atrium directly for pixels; inside tmux the\n")
		b.WriteString("           glyph rung is the whole feature\n")
	case r.Eligible:
		// Never "supported", never "working". The transmission is sent and the
		// picture only becomes pixels if a terminal answers it; a terminal on the
		// list that stays quiet leaves the glyphs on screen and says nothing.
		fmt.Fprintf(&b, "  %-18s eligible — not confirmed here, because confirming it means\n", "pixels")
		fmt.Fprintf(&b, "  %-18s sending the image and waiting for the terminal to answer,\n", "")
		fmt.Fprintf(&b, "  %-18s which the running TUI does when a picture opens\n", "")
	default:
		fmt.Fprintf(&b, "  %-18s not attempted — this environment names no terminal Atrium\n", "pixels")
		fmt.Fprintf(&b, "  %-18s has tested placeholder support in (kitty, Ghostty)\n", "")
		// NOT "an unsupported terminal simply never answers". That is true of a
		// terminal with no graphics protocol and false of the ones this arrow is
		// actually for: a terminal that implements kitty GRAPHICS but not Unicode
		// PLACEHOLDERS answers the transmission, so the upgrade fires and then it
		// draws the cells as tofu or as nothing. Placeholder support is not
		// probeable, so there is no way to detect that and no way back from it
		// except the setting — which is why the escape hatch is named here rather
		// than a no-op promised.
		b.WriteString("         → set image_preview: kitty to try anyway; if the picture goes\n")
		b.WriteString("           blank or turns to boxes, that terminal has the graphics\n")
		b.WriteString("           protocol but not placeholders — set image_preview: glyph\n")
	}

	// Every arm above leaves the glyph rung standing EXCEPT off, which has no
	// rung to fall back to: hinting an image path there copies it and opens
	// nothing (app/app_hints.go). Printing this line under off would answer a
	// question the line two rows above has already answered the other way.
	if r.Preference != config.ImagePreviewOff {
		b.WriteString("         → block glyphs are the fallback everywhere, including over SSH\n")
	}
	return b.String()
}
