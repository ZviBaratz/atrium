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
//   - A placeholder cell cannot exist before the IMAGE it addresses. #398's
//     third acceptance criterion asks that Atrium never emit graphics payloads
//     blind; here the payload goes out first and the cells that reference it are
//     only ever built from an ID a terminal handed back. tea.Raw queues into the
//     program's output buffer via a full event-loop round trip, so the frame
//     from the Update that asked for a transmission may flush first — which
//     would be a hazard if that frame could carry placeholders. It cannot.
//   - The same is NOT true of the PLACEMENT, and the distinction is worth
//     keeping straight. handleKittyGraphics upgrades the overlay before it
//     returns placeKittyImageCmd, so the very next frame already carries
//     placeholder cells while the a=p escape is still one Cmd round trip away.
//     For that one frame the cells name an image that exists and a placement
//     that does not, and a terminal draws nothing for them. It costs a frame,
//     not correctness — but it is why "ordering stops mattering" would be too
//     strong a claim to hang a decision on.

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
//
// Duplicated names resolve LAST-WINS, matching os.Environ semantics and
// doctor.CheckImagePreview. Returning on the first match instead would let the
// two disagree about the same environment, which for a section whose whole job
// is to explain a surprise is the wrong way round.
func kittyTerminalEnv(environ []string) bool {
	var term, termProgram string
	var ownVar bool
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch name {
		case "TERM":
			term = value
		case "TERM_PROGRAM":
			termProgram = value
		case "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "GHOSTTY_BIN_DIR":
			if value != "" {
				ownVar = true
			}
		}
	}
	return term == "xterm-kitty" || termProgram == "ghostty" || ownVar
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
// Every input is a parameter rather than a read, so the rule is a pure function
// of its inputs, matching doctor.CheckKeyboard and imageRenderMode.
//
// Three of the four vetoes are about the cells being DRAWABLE, and each is a
// silent failure if it is not checked here — the picture would render
// "successfully" and be wrong:
//
//   - placeholdersOneCell. U+10EEEE is East-Asian Ambiguous, so under an
//     east-asian width rule a placeholder measures two columns while occupying
//     one. Every row of the picture then overflows the box, lipgloss wraps it,
//     and the frame accumulates the ghost rows theme.SanitizeWidth documents —
//     at full scale, on every row at once. This is the same measured predicate
//     the glyph rung ladders on, asked of this rung's own glyph.
//   - trueColor. The placeholder's foreground IS the image ID, so a downsampling
//     profile does not dim the picture, it rewrites the address: ultraviolet
//     runs every cell through ConvertStyle, and on an ANSI256 profile the cells
//     end up pointing at an image that does not exist. There is no fallback from
//     that — Placeholders succeeds, so the overlay never reverts to glyphs — so
//     it has to be refused up front.
//   - mono. NO_COLOR pins the profile to ASCII, which strips the colour
//     entirely. Kept beside trueColor rather than folded into it because Mono is
//     the in-process answer (theme.SetMono) while the profile is read from the
//     environment, and a test or a future caller can set one without the other.
//
// The fourth is the tmux veto, and it is not made redundant by the confirmation
// gate downstream. tmux inherits TERM_PROGRAM from the environment at server
// start, so the sniff is TRUE inside tmux while tmux neither implements the
// protocol nor forwards the payload. The confirmation would indeed never arrive
// and the overlay would stay on glyphs — but we would have written kilobytes of
// APC at a program that may print them.
//
// image_preview: kitty overrides that veto, and what it buys inside tmux today
// is NOTHING — measured, not assumed. tmux forwards a graphics payload only when
// it is wrapped in tmux's own DCS passthrough envelope, and this code sends the
// APC unwrapped (kitty.Options has a ChunkFormatter hook for exactly that, left
// nil). Setting allow-passthrough on does not change the outcome: probed in a
// real kitty with passthrough enabled, the transmission still drew no reply. So
// the override is for a terminal Atrium has not TESTED, not for tmux. Wrapping
// the payload is a real follow-up; until it lands, nothing here should tell a
// user that passthrough makes this work.
func kittyEligible(environ []string, pref string, mono, trueColor, placeholdersOneCell bool) bool {
	if mono || !trueColor || !placeholdersOneCell {
		return false
	}
	switch pref {
	case config.ImagePreviewKitty:
		// The explicit opt-in skips the terminal sniff and the tmux veto, which
		// is the whole point of having it: it is for a terminal Atrium has not
		// tested. It does NOT skip the three drawability vetoes above, because
		// those are about whether the cells can be rendered at all.
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
	// Counted only once the payload is real. Counting beside a transmission that
	// never went out would make the next stray graphics reply on this terminal
	// look like an answer to nothing.
	m.kittyOutstanding++
	// tea.Raw takes an `any` and runs it through fmt.Sprint, so a []byte would
	// print as "[27 95 71 …]" to the terminal. It must be handed a string.
	return tea.Raw(payload)
}

// handleKittyGraphics upgrades the open overlay to real pixels, or disposes of a
// reply that answers something else.
//
// The hard part is that Ghostty's replies carry NO image number (see
// kittyGraphicsConfirmed), so an untagged answer cannot be matched by tag. What
// makes it tractable is that a terminal answers transmissions in the order it
// received them, so an unanswered COUNT is enough: while more than one of ours is
// outstanding, the next reply is for the OLDEST, whatever it says about itself.
//
// A boolean "am I waiting" is NOT enough, and the sequence that proves it is
// open A → esc before its reply → open B. The window closes and reopens, so a
// flag is true again when A's late, untagged reply arrives, and B would show A's
// pixels permanently. The count is 2 there, and that is what tells them apart.
//
// Every reply that is not the current image's is still an image the terminal has
// decoded and stored, so the disposal path FREES it. Dropping it on the floor
// leaks a megapixel per abandoned transmission for the life of the terminal —
// which is the one case "closing the overlay deletes the image by construction"
// does not cover, because those cells never existed.
func (m *home) handleKittyGraphics(msg kittyGraphicsConfirmed) (tea.Model, tea.Cmd) {
	if m.kittyOutstanding == 0 {
		// Nothing of ours is unanswered, so this belongs to another program
		// sharing the terminal.
		return m, nil
	}
	// Numbers increment by one per transmission, so the unanswered ones are
	// exactly this range. A reply carrying a number outside it was tagged by
	// somebody else — image numbers are CLIENT-chosen, so another program can
	// pick any of them — and it must be left entirely alone: not counted against
	// our outstanding transmissions, and above all not deleted, because the image
	// it names is theirs.
	oldest := m.kittyNumber - m.kittyOutstanding + 1
	if msg.number != 0 && (msg.number < oldest || msg.number > m.kittyNumber) {
		return m, nil
	}
	m.kittyOutstanding--

	if msg.id == 0 {
		// An error reply. It answers a transmission — which is why the count is
		// still decremented — but it names no image, so there is nothing to show
		// and nothing to free.
		return m, nil
	}

	// Is this the newest transmission, the one the open box is waiting for? A
	// numbered reply says so directly. An untagged one can only be placed by
	// ORDER: it answers the oldest unanswered transmission, so it is the newest
	// only when it was also the only one left.
	current := msg.number == m.kittyNumber || (msg.number == 0 && m.kittyOutstanding == 0)
	if !current || m.imageOverlay == nil {
		// Ours, but abandoned — a picture the user has already moved past, or one
		// whose box closed while the terminal was decoding. The terminal is
		// holding it either way and only an explicit delete frees it.
		return m, tea.Raw(imageview.DeleteImage(int(msg.id)))
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
