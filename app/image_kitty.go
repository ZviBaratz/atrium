package app

// image_kitty.go owns the pixel rung of the image overlay (#398): deciding
// whether to try it, transmitting the image, and upgrading the open overlay when
// the terminal confirms.
//
// The shape to keep in mind is that this is an UPGRADE, never a mode. The
// overlay opens on a glyph rung and draws immediately; if a terminal answers the
// transmission with an image ID, the same overlay swaps its cells for
// placeholders. Nothing here has a timeout, a retry, or a failure path, because
// "no answer" is not an error state — it is the ladder's lower rung, which is
// already on screen. Two properties fall out of that and both are worth more
// than the code they save:
//
//   - A placeholder cell cannot exist before the image it addresses. #398's
//     third acceptance criterion asks that Atrium never emit graphics payloads
//     blind; here the payload goes out first and the cells that reference it are
//     only ever built from an ID a terminal handed back.
//   - tea.Raw's ordering stops mattering. It queues into the program's output
//     buffer via a full event-loop round trip, so the frame from the Update that
//     asked for a transmission may flush first — which would be a real hazard if
//     that frame could carry placeholders. It cannot.

import (
	"image"
	"math"
	"os"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui/imageview"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// kittyPlacementID is the placement every image of ours is put under.
//
// A constant is correct rather than lazy: exactly one image overlay is open at a
// time, and re-sending a placement with the same image and placement ID replaces
// the previous one instead of stacking a second — which is what makes a resize
// cost one short escape and no retransmission.
const kittyPlacementID = 1

// kittyTerminalEnv names the environment variables that identify a terminal with
// confirmed Unicode-placeholder support.
//
// This is an allowlist of WHAT WAS TESTED, not a claim about the ecosystem.
// Placeholder support is not probeable — the protocol's own query (a=q) answers
// for graphics as a whole, and a terminal can implement those and not these — so
// the honest options are a short list plus a documented override, or a wide guess.
// kitty and Ghostty are the two this was looked at in. Everyone else has
// image_preview: kitty.
func kittyTerminalEnv(environ []string) bool {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch name {
		case "TERM":
			if value == "xterm-kitty" {
				return true
			}
		case "TERM_PROGRAM":
			if value == "ghostty" {
				return true
			}
		case "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "GHOSTTY_BIN_DIR":
			if value != "" {
				return true
			}
		}
	}
	return false
}

// inTmux reports whether this process is inside a tmux client. Presence is the
// signal, as it is for tmux itself and for doctor.CheckKeyboard — an empty TMUX
// is not something tmux ever sets.
func inTmux(environ []string) bool {
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok && name == "TMUX" {
			return value != ""
		}
	}
	return false
}

// kittyEligible reports whether a transmission is worth attempting.
//
// environ and mono are parameters rather than reads so the rule is a pure
// function of its inputs, matching doctor.CheckKeyboard and imageRenderMode.
//
// The tmux veto is the one that has to be here, and it is not made redundant by
// the confirmation gate downstream. tmux inherits TERM_PROGRAM from the
// environment at server start, so the sniff is TRUE inside tmux while tmux
// neither implements the protocol nor forwards the payload unless
// allow-passthrough is on. The confirmation would indeed never arrive and the
// overlay would stay on glyphs — but we would have written kilobytes of APC at a
// program that may print them. Users with passthrough on opt back in by naming
// the rung.
//
// mono is a veto rather than a preference because the placeholder's foreground
// IS the image ID: NO_COLOR pins the profile to Ascii, which strips the colour
// and with it the address of the image, leaving cells that point at nothing.
func kittyEligible(environ []string, pref string, mono bool) bool {
	if mono {
		return false
	}
	switch pref {
	case config.ImagePreviewKitty:
		// The explicit opt-in skips the terminal sniff and the tmux veto, which
		// is the whole point of having it: it is what a terminal we did not test,
		// or a tmux with allow-passthrough on, is set to.
		return true
	case config.ImagePreviewAuto:
		return !inTmux(environ) && kittyTerminalEnv(environ)
	default: // glyph, off
		return false
	}
}

// graphicsEnviron is the environment the gate reads, defaulting to the process's
// own. Nil-defaulting rather than seeded in the constructor because most homes in
// this package's tests are built as a bare struct literal.
func (m *home) graphicsEnviron() []string {
	if m.kittyEnviron != nil {
		return m.kittyEnviron
	}
	return os.Environ()
}

// kittyGraphicsConfirmed is the terminal's answer to a transmission: the image
// number we sent, and the ID it assigned.
//
// It exists as its own message so the handler below is reachable from a test
// without an ultraviolet value — but production converts rather than
// synthesising, so the conversion itself stays on the tested path.
type kittyGraphicsConfirmed struct {
	number int
	id     uint32
}

// transmitImageCmd sends the decoded image and asks the terminal to name it.
//
// pixels is the same bounded intermediate the glyph rung draws, so nothing is
// decoded or scaled twice and the transmission inherits that budget.
//
// The number is bumped per transmission and is what makes a stale reply
// harmless: an answer to the image the user was looking at before this one no
// longer matches, so it cannot upgrade the overlay to somebody else's pixels.
func (m *home) transmitImageCmd(pixels image.Image) tea.Cmd {
	if pixels == nil {
		return nil
	}
	m.kittyNumber++
	payload, err := imageview.TransmitPNG(pixels, m.kittyNumber)
	if err != nil {
		// Nothing to tell the user: the picture they asked for is already on
		// screen, drawn with glyphs. This only means it will not get sharper.
		log.ErrorLog.Printf("kitty transmit: %v", err)
		return nil
	}
	// tea.Raw takes an `any` and runs it through fmt.Sprint, so a []byte would
	// print as "[27 95 71 …]" to the terminal. It must be handed a string.
	return tea.Raw(payload)
}

// handleKittyGraphics upgrades the open overlay to real pixels.
//
// Three things have to hold, and each rejects a reply that is genuinely not
// ours:
//
//   - the number must match the transmission we are waiting on, because another
//     program sharing this terminal can be mid-transmission too;
//   - the terminal must have assigned an ID. An error reply carries none, and
//     the case that makes this load-bearing rather than defensive is an error
//     arriving AFTER a success for the same number: without the check it would
//     reset a confirmed ID to zero and drop a working picture back to glyphs;
//   - the overlay must still be open. A transmission whose reply lands after the
//     user pressed esc has nothing left to upgrade, and its image is already
//     being deleted.
//
// The open check is the nil pointer alone, deliberately. state and imageOverlay
// are set together by openImagePreview and cleared together by
// closeImagePreview, so a state comparison beside it would be a second spelling
// of the same fact — and one no test could distinguish, which is how a condition
// comes to look guarded without being.
func (m *home) handleKittyGraphics(msg kittyGraphicsConfirmed) (tea.Model, tea.Cmd) {
	if msg.number != m.kittyNumber || msg.id == 0 {
		return m, nil
	}
	if m.imageOverlay == nil {
		return m, nil
	}
	m.kittyID = msg.id
	m.imageOverlay.SetKittyImage(msg.id)
	return m, m.placeKittyImageCmd()
}

// placeKittyImageCmd creates or replaces the virtual placement the overlay's
// placeholder cells refer to.
//
// The cell rectangle comes from the overlay rather than being recomputed here,
// because the cells and the placement must describe the same grid — see
// ImageOverlay.KittyCells.
func (m *home) placeKittyImageCmd() tea.Cmd {
	if m.kittyID == 0 || m.imageOverlay == nil {
		return nil
	}
	cols, rows, ok := m.imageOverlay.KittyCells()
	if !ok {
		return nil
	}
	if cols == m.kittyCols && rows == m.kittyRows {
		return nil // the resize did not change the grid
	}
	m.kittyCols, m.kittyRows = cols, rows
	return tea.Raw(imageview.PlaceVirtual(int(m.kittyID), kittyPlacementID, cols, rows))
}

// releaseKittyImageCmd frees the terminal-side image when the overlay closes.
//
// The placements go with the cells by construction — they stop existing when the
// frame no longer draws them — so this is only about the pixels, which the
// terminal would otherwise hold for the rest of its life.
func (m *home) releaseKittyImageCmd() tea.Cmd {
	if m.kittyID == 0 {
		return nil
	}
	id := m.kittyID
	m.kittyID, m.kittyCols, m.kittyRows = 0, 0, 0
	return tea.Raw(imageview.DeleteImage(int(id)))
}

// kittyGraphicsEventFrom converts ultraviolet's response event, which Bubble Tea
// passes through untranslated.
//
// This is why ultraviolet is a direct dependency; see the note in app/scheme.go
// for why that was declined once and paid for here.
//
// The range check is not ceremony for the linter. Options.ID is an int parsed
// out of bytes the TERMINAL sent, and ultraviolet's parser discards
// UnmarshalText's error (decoder.go), so a malformed or hostile reply reaches
// here as whatever that parse produced — including a negative number, which
// would wrap to a large uint32 and address an image belonging to some other
// program. Anything outside a valid image ID is reported as zero, which is the
// same "no pixels" the handler already drops.
func kittyGraphicsEventFrom(ev uv.KittyGraphicsEvent) kittyGraphicsConfirmed {
	id := ev.Options.ID
	if id <= 0 || id > math.MaxUint32 {
		return kittyGraphicsConfirmed{number: ev.Options.Number}
	}
	return kittyGraphicsConfirmed{number: ev.Options.Number, id: uint32(id)}
}
