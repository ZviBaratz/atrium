package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Checkpoint is one native Claude Code file-history checkpoint: the snapshot
// Claude takes of every file it has touched, immediately before a user prompt.
//
// MessageID is the identity — the uuid of the user message the checkpoint
// precedes, and the value Claude's own `--rewind-files` takes. Nothing in Atrium
// restores a checkpoint (see the package comment on LoadCheckpoints), but the id
// is what makes a row nameable and is the handle any future restore would need.
type Checkpoint struct {
	// MessageID is the uuid of the user message this checkpoint precedes.
	MessageID string
	// At is the anchor user message's timestamp, or the snapshot's own when that
	// message is not in the transcript.
	At time.Time
	// Label is a cleaned one-line summary of the prompt; "" when the prompt
	// carried no extractable text (a tool-result turn, an image-only turn).
	Label string
	// Files is how many distinct files the session had touched by the end of this
	// checkpoint's turn — its own snapshot's tracked set plus the files that turn
	// went on to touch. It rises down the list, so a jump is where the work was.
	// Legitimately 0 for a turn that edited nothing.
	//
	// It is deliberately not "what rewinding here would restore". Claude's rewind
	// acts on the session's entire tracked set whatever checkpoint you pick, so no
	// per-row number describes it; this one describes the work instead.
	Files int
	// Outside is how many of those Files sit outside the session's working
	// directory. Claude stores a tracked path relative when it is under the cwd
	// at capture time and absolute otherwise, so this is that test read backwards.
	Outside int
	// Provisional marks a snapshot Claude flagged preCheckpoint — one it may
	// still rewrite. Recorded for completeness; the overlay does not distinguish
	// them.
	Provisional bool
}

// Checkpoints is the checkpoint enumeration for one session.
type Checkpoints struct {
	// SessionID is the transcript's uuid, which is also the session id Claude's
	// own `--resume` takes.
	SessionID string
	// Path is the transcript that was read.
	Path string
	// List is every checkpoint in the transcript, oldest first (file order).
	List []Checkpoint
	// Blobs reports whether <root>/file-history/<SessionID> still exists. Claude
	// sweeps that directory on its own retention schedule while leaving the
	// transcript records in place, so a listed checkpoint outlives the file
	// copies it would restore from.
	Blobs bool
}

// LoadCheckpoints enumerates the native checkpoints of the newest transcript for
// (program, workingDir). Non-claude programs return ErrUnsupported, like Render.
//
// This is deliberately a read-only enumeration. Claude does expose a headless
// file rewind, but it restores the session's *entire* tracked set — deleting
// files created since the checkpoint — and in practice that set reaches well
// outside the session's worktree, so Atrium lists checkpoints and leaves
// restoring to Claude's own Esc-Esc, which can rewind code and conversation
// together. See the findings on #385.
//
// Unlike the poll-path readers this one reads the *whole* file: checkpoints are
// spread across the transcript, with the earliest near the top, so a tail window
// would silently show a fraction of them. opts.MaxBytes is honored when set
// (tests use it) but is never defaulted — hence the deliberate detour around
// applyDefaults below.
//
// It honors ctx: an already-cancelled context returns before any filesystem I/O,
// and a cancellation mid-scan aborts the read.
func LoadCheckpoints(ctx context.Context, program, workingDir string, opts Options) (Checkpoints, error) {
	if !(claudeAdapter{}).supports(program) {
		return Checkpoints{}, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Checkpoints{}, err
	}
	// Take MaxBytes from the caller verbatim — applyDefaults would fill a zero
	// with the render path's 512KB cap, which is exactly the truncation this
	// reader must not have. Only Root is wanted from it.
	maxBytes := opts.MaxBytes
	root := applyDefaults(opts).Root

	path, err := newestTranscript(claudeProjectDir(root, workingDir))
	if err != nil {
		return Checkpoints{}, err
	}

	var (
		snaps   []rawSnapshot               // in file order, deduped by messageId
		byID    = map[string]int{}          // messageId → index in snaps
		anchors = map[string]anchorRecord{} // user uuid → label/timestamp
		deltas  = map[string][]string{}     // snapshotMessageId → tracked paths
	)
	if _, err := scanTail(ctx, path, maxBytes, func(line []byte) {
		if raw, ok := decodeSnapshot(line); ok {
			// Last record for a messageId wins: Claude rewrites a snapshot in
			// place (isSnapshotUpdate) rather than superseding it with a new id.
			if i, seen := byID[raw.MessageID]; seen {
				snaps[i] = raw
				return
			}
			byID[raw.MessageID] = len(snaps)
			snaps = append(snaps, raw)
			return
		}
		if id, trackingPath, ok := decodeDelta(line); ok {
			deltas[id] = append(deltas[id], trackingPath)
			return
		}
		if uuid, rec, ok := decodeAnchor(line); ok {
			anchors[uuid] = rec
		}
	}); err != nil {
		return Checkpoints{}, err
	}

	// One accumulating set walked in file order. The snapshot maps are cumulative
	// and monotone already, so a row's own deltas are what its map is still missing
	// — and folding them in is what keeps the newest row, whose turn no later
	// snapshot has absorbed, from reading as though nothing happened.
	list := make([]Checkpoint, 0, len(snaps))
	touched := map[string]struct{}{}
	for _, raw := range snaps {
		for trackedPath := range raw.Snapshot.TrackedFileBackups {
			touched[trackedPath] = struct{}{}
		}
		for _, trackedPath := range deltas[raw.MessageID] {
			touched[trackedPath] = struct{}{}
		}
		cp := Checkpoint{
			MessageID:   raw.MessageID,
			Provisional: raw.Snapshot.PreCheckpoint,
			At:          parseStamp(raw.Snapshot.Timestamp),
			Files:       len(touched),
			Outside:     countOutside(touched),
		}
		// Snapshot and anchor records appear in either order, so the join waits
		// until the whole file has been read.
		if rec, ok := anchors[cp.MessageID]; ok {
			cp.Label = rec.label
			if !rec.at.IsZero() {
				cp.At = rec.at
			}
		}
		list = append(list, cp)
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	blobs := false
	if info, err := os.Stat(filepath.Join(root, "file-history", sessionID)); err == nil {
		blobs = info.IsDir()
	}
	return Checkpoints{SessionID: sessionID, Path: path, List: list, Blobs: blobs}, nil
}

// anchorRecord is the part of a user entry a checkpoint row needs.
type anchorRecord struct {
	label string
	at    time.Time
}

// rawSnapshot is Claude's file-history-snapshot line. trackedFileBackups decodes
// to RawMessage values because only the keys — the tracked paths — are read.
type rawSnapshot struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	Snapshot  struct {
		Timestamp          string                     `json:"timestamp"`
		PreCheckpoint      bool                       `json:"preCheckpoint"`
		TrackedFileBackups map[string]json.RawMessage `json:"trackedFileBackups"`
	} `json:"snapshot"`
}

// rawDelta is Claude's file-history-delta line: one file backed up during the turn
// that follows snapshotMessageId's checkpoint.
type rawDelta struct {
	Type              string `json:"type"`
	SnapshotMessageID string `json:"snapshotMessageId"`
	TrackingPath      string `json:"trackingPath"`
}

// rawAnchor is the part of a user entry needed to label a checkpoint.
type rawAnchor struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

// snapshotMarker prefilters lines before any JSON work. It matches the type
// value itself, so it cannot exclude a snapshot line — but it can admit one, in
// particular a tool_result that quotes a transcript back at us, which is why
// decodeSnapshot re-checks the decoded top-level type.
var snapshotMarker = []byte(`file-history-snapshot`)

// deltaMarker prefilters file-history-delta lines the same way.
var deltaMarker = []byte(`file-history-delta`)

// anchorMarker prefilters candidate user entries the same way.
var anchorMarker = []byte(`"type":"user"`)

// decodeSnapshot decodes one file-history-snapshot line.
//
// The type check after the prefilter is not belt-and-braces: a tool_result payload
// can quote a transcript — including a snapshot record — back into the log, and one
// such line exists in a real transcript here. The prefilter admits it and only the
// decoded top-level type rejects it.
func decodeSnapshot(line []byte) (rawSnapshot, bool) {
	if !bytes.Contains(line, snapshotMarker) {
		return rawSnapshot{}, false
	}
	var raw rawSnapshot
	if err := json.Unmarshal(line, &raw); err != nil {
		return rawSnapshot{}, false
	}
	if raw.Type != "file-history-snapshot" || raw.MessageID == "" {
		return rawSnapshot{}, false
	}
	return raw, true
}

// decodeDelta decodes one file-history-delta line into the snapshot it belongs to
// and the path it tracked. Deltas name their snapshot explicitly, so attribution
// never depends on where they sit in the file.
func decodeDelta(line []byte) (snapshotID, trackingPath string, ok bool) {
	if !bytes.Contains(line, deltaMarker) {
		return "", "", false
	}
	var raw rawDelta
	if err := json.Unmarshal(line, &raw); err != nil {
		return "", "", false
	}
	if raw.Type != "file-history-delta" || raw.SnapshotMessageID == "" || raw.TrackingPath == "" {
		return "", "", false
	}
	return raw.SnapshotMessageID, raw.TrackingPath, true
}

// countOutside is how many of the tracked paths lie outside the session's working
// directory.
func countOutside(touched map[string]struct{}) int {
	n := 0
	for trackedPath := range touched {
		if pathOutsideCWD(trackedPath) {
			n++
		}
	}
	return n
}

// decodeAnchor decodes one user entry into the label and timestamp a checkpoint
// row shows. Sidechain (subagent) entries are skipped: a checkpoint is never
// anchored to one, and skipping them keeps the anchor map to the main thread.
func decodeAnchor(line []byte) (string, anchorRecord, bool) {
	if !bytes.Contains(line, anchorMarker) {
		return "", anchorRecord{}, false
	}
	var raw rawAnchor
	if err := json.Unmarshal(line, &raw); err != nil {
		return "", anchorRecord{}, false
	}
	if raw.Type != "user" || raw.UUID == "" || raw.IsSidechain {
		return "", anchorRecord{}, false
	}
	return raw.UUID, anchorRecord{
		label: checkpointLabel(raw.Message),
		at:    parseStamp(raw.Timestamp),
	}, true
}

// checkpointLabel extracts a one-line label from a user entry's message.
//
// It reads at most the first content block, rather than reusing decodeContent:
// an anchor prompt is either a plain string or an array whose first block is
// text, so anything else is a tool-result turn with nothing to label — and
// flattening those would parse megabytes of tool output per transcript for a
// label that is thrown away.
func checkpointLabel(message json.RawMessage) string {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return cleanLabel(s)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(msg.Content, &blocks); err != nil || len(blocks) == 0 {
		return ""
	}
	var first rawBlock
	if err := json.Unmarshal(blocks[0], &first); err != nil || first.Type != "text" {
		return ""
	}
	return cleanLabel(first.Text)
}

// tagMarkup matches the pseudo-tags Claude wraps around non-typed prompts —
// <command-name>, <bash-input>, <local-command-stdout> and friends. Roughly a
// tenth of real checkpoints are anchored to one of those, so stripping the
// markup is what makes their rows readable rather than cosmetic polish.
var tagMarkup = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9-]*>`)

// cleanLabel reduces prompt text to a single display line.
func cleanLabel(s string) string {
	s = tagMarkup.ReplaceAllString(s, " ")
	for _, line := range strings.Split(s, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			return strings.Join(fields, " ")
		}
	}
	return ""
}

// pathOutsideCWD reports whether a tracked path lies outside the session's
// working directory. Claude relativizes a path stored under the cwd at capture
// time and leaves everything else absolute, so an absolute key — or one that
// climbs out with ".." — is exactly a path the session reached outside its own
// tree.
func pathOutsideCWD(path string) bool {
	if filepath.IsAbs(path) {
		return true
	}
	return path == ".." || strings.HasPrefix(path, "../")
}

// parseStamp parses a transcript timestamp, returning the zero time for a
// missing or malformed one so callers fall back rather than fail.
func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
