package transcript

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrUnsupported signals that no adapter handles the program; the caller is
// expected to fall back to the tmux capture path.
var ErrUnsupported = errors.New("transcript: program not supported")

// defaultMaxBytes caps how much of a transcript's tail is parsed and rendered.
// The constraint is render cost (width-wrapping megabytes of text), not parse
// cost; 512KB of recent conversation is plenty for a scrollback view.
const defaultMaxBytes = 512 * 1024

// Options controls transcript rendering.
type Options struct {
	// Root overrides the transcript root (default $CLAUDE_CONFIG_DIR, else
	// ~/.claude) — for tests.
	Root string
	// Width is the pane width to wrap to; <= 0 leaves lines unwrapped.
	Width int
	// MaxBytes caps the parsed tail of the file (0 = defaultMaxBytes).
	MaxBytes int64
}

// adapter renders one agent program's native transcript to plain preview text.
type adapter interface {
	// supports reports whether this adapter handles program (wrapper-aware).
	supports(program string) bool
	// render returns wrapped plain text for the session whose agent cwd is
	// workingDir, or an error (incl. missing/empty transcript) so the caller
	// falls back to the tmux capture.
	render(workingDir string, opts Options) (string, error)
}

var adapters = []adapter{claudeAdapter{}}

// Render renders the native transcript of the program running in workingDir.
// It returns ErrUnsupported when no adapter handles program; any other error
// means "transcript unavailable" and the caller should fall back to tmux.
func Render(program, workingDir string, opts Options) (string, error) {
	for _, a := range adapters {
		if a.supports(program) {
			return a.render(workingDir, applyDefaults(opts))
		}
	}
	return "", ErrUnsupported
}

func applyDefaults(opts Options) Options {
	if opts.Root == "" {
		// Claude Code relocates its whole data dir (incl. projects/) when
		// CLAUDE_CONFIG_DIR is set; resolve the same way it does.
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			opts.Root = dir
		} else if home, err := os.UserHomeDir(); err == nil {
			opts.Root = filepath.Join(home, ".claude")
		}
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	return opts
}
