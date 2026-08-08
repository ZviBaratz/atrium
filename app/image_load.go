package app

// image_load.go reads an image off disk for the preview overlay (#398).
//
// Everything here runs inside a tea.Cmd, off the Bubble Tea goroutine, and that
// is the point: decoding a 4000×3000 PNG allocates ~48 MB and takes hundreds of
// milliseconds, which on the loop would stall every session's poll and every
// keystroke. The command hands back a bounded intermediate, so the overlay — and
// every resize after it — works from something small.

import (
	"fmt"
	"image"
	"os"
	"path/filepath"

	// Registers the decoders. This is the whole reason imageExts lists these
	// three and nothing else; a fourth format needs a dependency, not an entry.
	//
	// png and jpeg happen to be registered already by unrelated dependencies
	// (x/ansi/kitty and x/crypto/openpgp/packet), so deleting them here changes
	// nothing today. Keep them: that is an accident of two dependency trees, not
	// a property of this package.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/ZviBaratz/atrium/ui/imageview"

	tea "charm.land/bubbletea/v2"
)

const (
	// maxImageFileBytes refuses a file before opening it.
	maxImageFileBytes = 24 << 20 // 24 MB
	// maxImagePixels refuses a decode by the dimensions the file DECLARES,
	// which is the only cap image/png can enforce: it has no in-flight limit,
	// so a small file declaring a huge canvas allocates the whole thing before
	// anyone can object. 50 megapixels is well past any screenshot.
	maxImagePixels = 50_000_000
	// imageIntermediateMax bounds the decoded image the overlay holds.
	imageIntermediateMax = 1024
)

// imageLoadedMsg carries the result of a load back onto the loop.
type imageLoadedMsg struct {
	path string
	img  image.Image
	// width and height are the FILE's dimensions, not img's. img is the bounded
	// intermediate, so for anything large the two differ — and the title the user
	// reads is about the file. See overlay.Image.
	width, height int
	err           error
}

// loadImageCmd reads and shrinks path.
func loadImageCmd(path string) tea.Cmd {
	return func() tea.Msg {
		img, w, h, err := loadImage(path)
		return imageLoadedMsg{path: path, img: img, width: w, height: h, err: err}
	}
}

// loadImage stats, budget-checks, decodes and shrinks.
//
// The path comes from an agent's pane output. The agent is the user's own, but
// the text is still unvalidated input, so the budget is checked before anything
// is allocated on its behalf — the same posture internal/actions/open.go takes
// with a target handed to the opener.
func loadImage(path string) (img image.Image, srcW, srcH int, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	defer f.Close() //nolint:errcheck // read-only

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s is not an image this can read", filepath.Base(path))
	}
	if err := checkImageBudget(filepath.Base(path), st.Size(), cfg.Width, cfg.Height); err != nil {
		return nil, 0, 0, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, 0, 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s is not an image this can read", filepath.Base(path))
	}
	small := imageview.Downscale(src, imageIntermediateMax, imageIntermediateMax)
	if small == nil {
		return nil, 0, 0, fmt.Errorf("%s has no pixels", filepath.Base(path))
	}
	// DecodeConfig's dimensions, NOT the decoded image's — they disagree for GIF
	// and the decoded ones are wrong. DecodeConfig reports a GIF's logical screen
	// size; Decode returns frame 0, whose Rect may be a subrectangle of it.
	// Measured: a GIF with a 100×80 screen and a first frame at (10,5)-(50,35)
	// was captioned "40×30". The logical size is what an image viewer calls the
	// image's size, and it is what the user is asking about.
	return small, cfg.Width, cfg.Height, nil
}

// checkImageBudget refuses a file that is too big to be worth previewing, naming
// both the number the user has to act on and the one they have to get under.
//
// Naming the limit is not decoration. Reporting the size alone leaves "too big"
// unanswerable — under what? — and at the boundary it reads as a contradiction:
// integer MB truncates, so a file one byte over the cap used to report "shot.png
// is 24 MB, too big to preview", which is the cap itself, stated as the offence.
// The tenth of a megabyte is there for the same reason, so a size that rounds to
// the limit is not printed as if it equalled it.
//
// Split out from loadImage because it is the half worth asserting: the sizes
// that trip it cannot be built as fixtures cheaply — a 50-megapixel image is
// 200 MB of RGBA — so a test that had to go through a real file could only ever
// cover the cases that pass.
func checkImageBudget(name string, size int64, w, h int) error {
	if size > maxImageFileBytes {
		return fmt.Errorf("%s is %.1f MB, over the %d MB preview limit",
			name, float64(size)/(1<<20), maxImageFileBytes>>20)
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%s has no pixels", name)
	}
	if int64(w)*int64(h) > maxImagePixels {
		return fmt.Errorf("%s is %d×%d, over the %d megapixel preview limit",
			name, w, h, maxImagePixels/1_000_000)
	}
	return nil
}
