package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Stamp identifies a transcript file's state for cheap change detection: a
// caller keeps the Stamp from its last extraction and passes it back so an
// unchanged transcript is answered by a single os.Stat, never a re-parse.
type Stamp struct {
	Path    string
	ModTime time.Time
	Size    int64
}

// Equal compares stamps; use this, never ==, because ModTime is a time.Time
// (Path/Size compare by value, ModTime via time.Time.Equal).
func (s Stamp) Equal(o Stamp) bool {
	return s.Path == o.Path && s.Size == o.Size && s.ModTime.Equal(o.ModTime)
}

// modelMaxBytes caps the tail parsed for model extraction. The model rides
// every assistant entry, so a much smaller window than the render path's 512KB
// suffices; 128KB still spans a long tool-heavy stretch.
const modelMaxBytes = 128 * 1024

// syntheticModel is the placeholder Claude Code writes on assistant entries it
// fabricates for API errors — never a real model, so never worth showing.
const syntheticModel = "<synthetic>"

// LatestModel returns the message.model of the newest non-sidechain,
// non-synthetic assistant entry in the newest transcript for (program,
// workingDir) — the model that actually answered the session's last turn.
//
// When the newest transcript's (path, mtime, size) equal prev it returns
// ("", prev, nil) without reading the file. A parsed tail containing no
// qualifying assistant entry returns ("", advancedStamp, nil): the caller
// keeps its last value and, because the stamp advanced, won't re-parse the
// same bytes. Non-claude programs return ErrUnsupported, like Render.
//
// The headless auto-namer runs `claude -p` cwd'd to the session's worktree —
// it would shadow the session's transcript here, except it passes
// --no-session-persistence (see session/naming.go). Any future headless claude
// invocation cwd'd to a worktree must keep that flag.
func LatestModel(program, workingDir string, prev Stamp, opts Options) (string, Stamp, error) {
	if !(claudeAdapter{}).supports(program) {
		return "", prev, ErrUnsupported
	}
	// Apply the model-sized tail cap before applyDefaults, which would fill a
	// zero MaxBytes with the render path's larger default.
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = modelMaxBytes
	}
	opts = applyDefaults(opts)

	path, err := newestTranscript(filepath.Join(opts.Root, "projects", sanitizeCWD(workingDir)))
	if err != nil {
		return "", prev, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", prev, err
	}
	stamp := Stamp{Path: path, ModTime: info.ModTime(), Size: info.Size()}
	if stamp.Equal(prev) {
		return "", prev, nil
	}

	model := ""
	if _, err := scanTail(path, opts.MaxBytes, func(line []byte) {
		if m, ok := decodeModel(line); ok {
			model = m // last qualifying entry wins
		}
	}); err != nil {
		return "", prev, err
	}
	return model, stamp, nil
}

// decodeModel returns the model of one JSONL line when it is a non-sidechain,
// non-synthetic assistant entry; ok is false for everything else (malformed
// lines included — the render path tolerates them the same way).
func decodeModel(line []byte) (string, bool) {
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return "", false
	}
	if raw.IsSidechain || raw.Type != "assistant" {
		return "", false
	}
	var msg rawMessage
	if err := json.Unmarshal(raw.Message, &msg); err != nil {
		return "", false
	}
	if msg.Model == "" || msg.Model == syntheticModel {
		return "", false
	}
	return msg.Model, true
}
