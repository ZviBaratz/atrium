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
//     branch — and note that the version gate is not what keeps it out of reach:
//     it screens a *different* version, so a version-1 record that simply omits
//     created_at passes it untouched. What keeps that branch off this spool's
//     live path is readCreate's own created_at check.
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
	"strings"
	"time"
	"unicode"
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

// FirstControlRune returns the first control character in title and whether there was
// one, so a caller can name the offending character rather than say "invalid".
//
// A title is rendered as one row of the session list and stored verbatim
// (session.NewInstance keeps it as given), and until `atrium new` no producer could
// put a control character in one: the create form's field is a bubbles textinput,
// which collapses newlines and tabs to spaces on the way in, and the derived branch
// and tmux names are sanitized separately, so Title is the single unsanitized sink. A
// raw argv string is the first path that reaches it — and `atrium new "$(gh issue view
// N --json title -q .title)"`, or a title taken from a commit body, is exactly how a
// newline arrives. One embedded newline splits the row across two lines, leaving the
// second without the selection indicator or status glyph and shifting every mouse zone
// below it.
//
// unicode.IsControl covers C0 and C1, so tab, carriage return and the escape that
// would let a title write its own ANSI are all caught by the one test.
func FirstControlRune(title string) (rune, bool) {
	for _, r := range title {
		if unicode.IsControl(r) {
			return r, true
		}
	}
	return 0, false
}

// WriteCreate commits r to the create spool and returns the path it was written
// to. It stamps Version and, unless the caller supplied one, CreatedAt.
func WriteCreate(r Request) (string, error) {
	if strings.TrimSpace(r.Title) == "" || !filepath.IsAbs(r.Path) {
		return "", errors.New("outbox: a create request needs a title and an absolute path")
	}
	if bad, ok := FirstControlRune(strings.TrimSpace(r.Title)); ok {
		return "", fmt.Errorf("outbox: a create request title cannot contain %q", bad)
	}
	// Normalised on the way out, and again on the way back in: a padded title is not
	// blank, so it passes every check and then reaches tmux.QualifiedSessionName and
	// git.BranchNameForSession with its padding intact — producing a session no
	// `atrium new` invocation can address, because the CLI trims what it is given.
	r.Title = strings.TrimSpace(r.Title)
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
	// What WriteCreate refuses to write, refused on the way back in. Nothing this
	// package produces can fail these; a file written by hand can, and a Request that
	// gets past here is executed exactly like any other.
	//
	// A blank title is not caught downstream — titleConflictIn deliberately returns "no
	// conflict" for one, so the drain would build a session whose row renders empty.
	//
	// The path is required to be absolute, not merely non-empty, because the hazard is
	// not blankness: filepath.Abs resolves ANY relative path against the *draining
	// TUI's* working directory, with a nil error. `""` yields that directory itself and
	// `"web"` a child of it, and both mean the same thing — a worktree built wherever
	// atrium happened to be launched from rather than where the writer meant. The
	// producer always spools an absolute path (resolveNewTarget calls filepath.Abs), so
	// nothing legitimate is turned away.
	//
	// A missing created_at is rejected rather than tolerated. WriteCreate stamps it, so
	// its absence means a hand-written file — and expired() treats a zero time as never
	// expired, deliberately, which would make such a record immortal AND executable: it
	// would survive every TTL sweep and be built by whichever TUI started next, weeks
	// later, against a branch point long moved on. The version gate does not stand in
	// for this one; it screens a different version, not a missing field at this one.
	//
	// A control character in the title is rejected for the reason FirstControlRune
	// gives: nothing downstream removes one, and a title is the one field that reaches
	// the renderer unsanitized. TrimSpace does not stand in for it — it takes the
	// leading and trailing whitespace and leaves an interior newline exactly where it
	// does the damage.
	r.Title = strings.TrimSpace(r.Title) // see WriteCreate: padding survives into the branch name
	badRune, hasControl := FirstControlRune(r.Title)
	switch {
	case r.Title == "":
		return CreateEntry{Path: path, Err: fmt.Errorf(
			"create request %s has no title", filepath.Base(path))}
	case hasControl:
		return CreateEntry{Path: path, Err: fmt.Errorf(
			"create request %s has %q in its title", filepath.Base(path), badRune)}
	case !filepath.IsAbs(r.Path):
		return CreateEntry{Path: path, Err: fmt.Errorf(
			"create request %s has no absolute path (%q)", filepath.Base(path), r.Path)}
	case r.CreatedAt.IsZero():
		return CreateEntry{Path: path, Err: fmt.Errorf(
			"create request %s has no created_at, so its age cannot be judged", filepath.Base(path))}
	}
	return CreateEntry{Path: path, Request: r}
}
