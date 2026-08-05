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
	// hasSession reports whether a resumable native session exists for workingDir —
	// the same record the agent's resume flag (e.g. `claude --continue`) would pick up.
	hasSession(workingDir string, opts Options) bool
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

// HasResumable reports whether the agent program running in workingDir has a resumable
// native session — the record its resume flag (`claude --continue`) would pick up — and
// whether any adapter handles program at all. supported == false means resumability is
// unknowable here (no native-transcript adapter, e.g. codex/gemini); the caller should then
// defer to the agent's own resume handling rather than block it. It lets callers avoid
// launching a resume that the agent would abort on (claude prints "No conversation found to
// continue!" and exits when none exists).
func HasResumable(program, workingDir string, opts Options) (resumable, supported bool) {
	for _, a := range adapters {
		if a.supports(program) {
			return a.hasSession(workingDir, applyDefaults(opts)), true
		}
	}
	return false, false
}

// ProjectDir returns the directory the transcript readers would look in for
// (program, workingDir) — the identity two sessions collide on. Two sessions
// with the same ProjectDir read the same set of files, so newest-mtime picks
// arbitrarily between their conversations and neither one's reading can be
// trusted (#596's acceptance criterion 4). "" means there is nothing to
// collide on: no adapter handles program, or workingDir is unknown.
//
// It is deliberately the *resolved directory* rather than the working dir
// itself. Two distinct working dirs can map to one project dir — sanitizeCWD
// flattens every non-alphanumeric rune to '-', so /home/z/proj-a and
// /home/z/proj/a land in the same place — while two sessions on the same
// working dir under different CLAUDE_CONFIG_DIR roots (separate accounts) do
// NOT collide at all. Comparing working dirs gets both of those backwards; this
// is the value the reader actually opens.
func ProjectDir(program, workingDir string, opts Options) string {
	if workingDir == "" || !(claudeAdapter{}).supports(program) {
		return ""
	}
	return claudeProjectDir(applyDefaults(opts).Root, workingDir)
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
