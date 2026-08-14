package outbox

// create.go — the create-request spool that carries `atrium new` to the running
// TUI (#703).
//
// It is the same producer shape as the prompt spool for a session that does not
// exist yet: the CLI writes a request and exits, and the TUI — the sole session
// creator, because state.json has exactly one writer — drains it and creates the
// session through the same call the keypress reaches.
//
// It is a second directory rather than a kind field on Message, for three
// reasons:
//
//   - Bumping Message's currentVersion to carry a discriminator would break every
//     prompt already in the spool. read gates on exact equality, so a version-2
//     atrium rejects a version-1 message with "the message could not be read" —
//     a receipt that misstates the cause — and a version-1 atrium rejects
//     everything the new one writes.
//   - A discriminator added at version 1 would be worse. An atrium too old to
//     know the field decodes a request as a Message, matches it on (Title, Path),
//     and where a session of that title already exists in that repo types the
//     first prompt into it. A request addressed to a session that must NOT exist
//     would be delivered to one that does.
//   - So the versioning story is: neither type ever needs the other's version.
//     createVersion moves only when a Request's shape changes, and a Message
//     stays at 1 regardless. Nothing here relies on expired's zero-timestamp
//     branch, which readCreate's version gate makes unreachable anyway.
//
// What it shares with the prompt spool is everything that is not the payload:
// writeRecord's naming, isMessageFile's screening, and the receipt trio, whose
// write-before-unlink ordering is the kind of invariant a second copy would rot.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// createVersion is the schema version WriteCreate stamps and ListCreates
	// accepts. It is independent of currentVersion: the two record types are
	// never read by the same decoder, so neither has to move when the other does.
	createVersion = 1

	createDirName = "create"
)

// Request is one queued create-session request awaiting a TUI to execute it.
//
// Unlike a Message, whose (Title, Path) must name a session that already exists,
// a Request's must name one that does not — Title is the name the session will be
// created under, and because branch and tmux names derive from it
// (git.BranchNameForSession, tmux.QualifiedSessionName), it is also the branch the
// caller is choosing. The drain refuses a collision rather than suffixing, for
// that reason.
type Request struct {
	Version int `json:"version"`
	// Path is the repository the session is created in. A path that is not a git
	// repo is not an error: it makes a direct session, exactly as it does in the
	// create form.
	Path string `json:"path"`
	// Title is the session name, and so the stem of its branch and tmux names.
	Title string `json:"title"`
	// Program is the command to run. Empty means "whatever program the draining
	// TUI is configured with", which is what makes an unflagged `atrium new`
	// equivalent to pressing the new-session key.
	Program string `json:"program,omitempty"`
	// Branch is an existing base branch to start the session on. Empty branches
	// from the target's HEAD.
	Branch string `json:"branch,omitempty"`
	// Prompt is the optional first prompt, delivered on the create form's terms:
	// queued at creation and typed in once the agent is past its startup screen.
	Prompt string `json:"prompt,omitempty"`
	// Force answers in advance the two gates that would otherwise ask the user —
	// the host-derived soft session cap and a fully rate-limited account pool.
	// It deliberately does not reach the explicit hard cap, which refuses in the
	// TUI too.
	Force     bool      `json:"force,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Expired reports whether r is past the TTL horizon as of now. A request spooled a
// day ago names a tree and a branch point that have moved on, so creating from it
// is worse than dropping it.
func (r Request) Expired(now time.Time) bool {
	return expired(r.CreatedAt, now)
}

// CreateEntry is one file found in the create spool. Err is set when the file
// could not be decoded into a usable Request; Path is always populated so the
// caller can discard an unusable file rather than retrying it forever.
type CreateEntry struct {
	Path    string
	Request Request
	Err     error
}

// CreateDir returns the create spool, <data dir>/outbox/create. It does not
// create it.
//
// Nested inside the prompt spool rather than beside it so every spool artifact
// stays under one root. That is safe because listFiles rejects the "create"
// entry twice over — it is a directory, and its name is not the record format —
// so List never sees a request, including in an atrium that predates this file.
func CreateDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, createDirName), nil
}

// WriteCreate commits r to the create spool and returns the path it was written
// to. It stamps Version and, unless the caller supplied one, CreatedAt.
func WriteCreate(r Request) (string, error) {
	if r.Title == "" || r.Path == "" {
		return "", errors.New("outbox: a create request needs a title and a path")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	r.Version = createVersion

	dir, err := CreateDir()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("outbox: encode create request: %w", err)
	}
	return writeRecord(dir, "create request", r.CreatedAt, data)
}

// ListCreates returns every request in the create spool, oldest first. A missing
// directory is the steady state for anyone who has never run `atrium new`, so it
// yields no entries and no error.
//
// Undecodable files are returned as entries carrying Err rather than being skipped
// or failing the whole call, for List's reason: the caller is the only party that
// can discard them, and a file nobody can decode and nobody deletes would be
// re-read on every poll forever.
func ListCreates() ([]CreateEntry, error) {
	dir, err := CreateDir()
	if err != nil {
		return nil, err
	}
	paths, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]CreateEntry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, readCreate(p))
	}
	return entries, nil
}

func readCreate(path string) CreateEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return CreateEntry{Path: path, Err: fmt.Errorf("read %s: %w", filepath.Base(path), err)}
	}
	var r Request
	if err := json.Unmarshal(data, &r); err != nil {
		return CreateEntry{Path: path, Err: fmt.Errorf("decode %s: %w", filepath.Base(path), err)}
	}
	if r.Version != createVersion {
		return CreateEntry{Path: path, Err: fmt.Errorf(
			"create request %s has version %d, this atrium understands %d",
			filepath.Base(path), r.Version, createVersion)}
	}
	return CreateEntry{Path: path, Request: r}
}
