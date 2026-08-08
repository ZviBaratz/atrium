package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
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
// the two environment facts that explain a surprise.
type ImagePreviewResult struct {
	Preference  string // the resolved image_preview value, never empty
	Term        string // $TERM, "" when unset
	TermProgram string // $TERM_PROGRAM, "" when unset
	InTmux      bool   // $TMUX is set: this shell is inside a tmux client
	Recognized  bool   // the environment names a terminal Atrium tested
	Eligible    bool   // a transmission would be attempted
	Confirmed   bool   // always false; see the type comment
}

// CheckImagePreview reads the rungs available outside the TUI.
//
// environ and pref are parameters rather than lookups so the rule is a pure
// function of its input, matching CheckKeyboard and CheckScheme. Later entries
// win, matching os.Environ semantics for a duplicated name.
func CheckImagePreview(environ []string, pref string) ImagePreviewResult {
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

	switch pref {
	case config.ImagePreviewKitty:
		r.Eligible = true
	case config.ImagePreviewAuto:
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
	case r.Preference == config.ImagePreviewOff:
		fmt.Fprintf(&b, "  %-18s no overlay — hinting an image path only copies it\n", "pixels")
	case r.Preference == config.ImagePreviewGlyph:
		fmt.Fprintf(&b, "  %-18s not attempted — image_preview is set to glyph\n", "pixels")
	case r.InTmux && r.Preference == config.ImagePreviewAuto:
		fmt.Fprintf(&b, "  %-18s not attempted — inside tmux, which does not forward the\n", "pixels")
		fmt.Fprintf(&b, "  %-18s graphics protocol unless allow-passthrough is on\n", "")
		b.WriteString("         → set -g allow-passthrough on and image_preview: kitty to try anyway\n")
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
		b.WriteString("         → set image_preview: kitty to try anyway; an unsupported\n")
		b.WriteString("           terminal simply never answers and the glyphs stay\n")
	}

	b.WriteString("         → block glyphs are the fallback everywhere, including over SSH\n")
	return b.String()
}
