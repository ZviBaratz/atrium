package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The retirement spool: `atrium kill` and `atrium pause` drop a record here and the
// running TUI's drain acts on it. Third producer shape in this package, and it exists
// for the reason the other two do — state.json has exactly one writer at any instant,
// and retiring a session removes it from that file, so no headless process can do it
// directly.
//
// It gets its own directory rather than a kind field on Message, and the package doc's
// argument for that separation is the reason: a message's (Title, Path) must name an
// existing session while a create request's must not, so a reader that got a
// discriminator wrong would act on a payload it never validated. A retirement's
// identity semantics match a message's — both must name a session that exists — which
// is why the discriminator INSIDE this record type is safe where one across the types
// would not be. Mode picks between two verbs applied to the same resolved target, not
// between two ways of reading who the target is.
//
// The dir nests under the prompt spool's exactly as create/ does, so listFiles keeps
// these records out of List by two independent guards: IsDir, and a name the record
// format does not match.
//
// There is no claim step, unlike the create spool. A claim exists there because
// building a session is long and a crash mid-build must not double-create; a
// retirement that is re-read after a crash finds its target already gone and is
// refused, which is the benign direction. Anything that changes that — a retirement
// that becomes long-running, or one whose partial application is not detectable —
// needs the claim protocol, not a comment saying it does not.
const (
	retireDirName = "retire"

	// retireVersion is the schema version WriteRetire stamps and ListRetires accepts.
	// A record carrying anything else came from a different atrium and is surfaced as
	// an error rather than decoded on a guess — a guess a teardown would act on.
	retireVersion = 1
)

// Mode is which retirement a record asks for.
//
// A string rather than an int so a record is readable on disk and so an unrecognised
// value stays unrecognisable, which is what lets readRetire refuse it. An int would
// make "the field is absent" and "the field says kill" the same bytes.
type Mode string

const (
	// ModeKill removes the worktree and deletes the branch. Reversible only through
	// the undo journal, and only for as long as that journal retains the ref.
	ModeKill Mode = "kill"
	// ModePause removes the worktree and keeps the branch, committing whatever was
	// uncommitted as a marker the resume unwinds.
	ModePause Mode = "pause"
)

// Retire is one queued retirement awaiting execution by the draining TUI.
//
// Title and Path together identify the target, for the reason Message documents:
// titles are unique only within a repo group, so the storage layer matches instances
// on the pair.
type Retire struct {
	Version int    `json:"version"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	// TmuxName is recorded for diagnostics only, never matched on — it is absent from
	// state files written before the field existed (see Message.TmuxName).
	TmuxName  string    `json:"tmux_name,omitempty"`
	Mode      Mode      `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
}

// Expired reports whether r is past the TTL horizon as of now. A retirement spooled a
// day ago describes a session that has moved on, and acting on it is worse than
// dropping it.
func (r Retire) Expired(now time.Time) bool {
	return expired(r.CreatedAt, now)
}

// RetireEntry is one file found in the retire spool. Err is set when the file could
// not be decoded into a usable Retire; Path is always populated so the caller can
// discard an unusable file rather than retrying it forever.
type RetireEntry struct {
	Path   string
	Retire Retire
	Err    error
}

// RetireDir returns the retire spool directory. It does not create it.
func RetireDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, retireDirName), nil
}

// WriteRetire commits r to the retire spool and returns the path it was written to.
// It stamps Version and, unless the caller supplied one, CreatedAt.
//
// The validations are the ones a drain cannot make good on its own. An unaddressable
// target is refused here because the producer is the last place with somebody to tell —
// and the path must be ABSOLUTE rather than merely non-empty, since filepath.Abs resolves
// a relative one against the *draining TUI's* working directory with a nil error, naming a
// repo the writer never meant. An unrecognised Mode is refused because it is the field
// that chooses between deleting a branch and keeping it, and there is no safe default to
// fall back on. A control character in the title is refused for the reason
// FirstControlRune documents, applied to the other record type that takes a title
// straight from argv. And a CreatedAt in the future is refused because this function
// takes the caller's value verbatim, which is what lets a test stage an aged record and
// would equally let a caller stage a record nothing can ever expire — see
// futureTimestamp.
func WriteRetire(r Retire) (string, error) {
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" || !filepath.IsAbs(r.Path) {
		return "", errors.New("outbox: a retirement needs a title and an absolute path")
	}
	if bad, ok := FirstControlRune(r.Title); ok {
		return "", fmt.Errorf("outbox: a retirement title cannot contain a control character (%q)", bad)
	}
	if !r.Mode.valid() {
		return "", fmt.Errorf("outbox: %q is not a retirement mode", r.Mode)
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if futureTimestamp(r.CreatedAt, time.Now()) {
		return "", fmt.Errorf("outbox: a retirement cannot be stamped in the future (%s)",
			r.CreatedAt.Format(time.RFC3339))
	}
	r.Version = retireVersion

	dir, err := RetireDir()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("outbox: encode retirement: %w", err)
	}
	return writeRecord(dir, "retirement", r.CreatedAt, data)
}

// ListRetires returns every retirement in the spool, oldest first. A missing
// directory is the steady state for anyone who has never retired a session from the
// CLI, so it yields no entries and no error.
//
// Undecodable files come back as entries carrying Err rather than being skipped, for
// List's reason: the caller is the only party that can discard them, and a file
// nobody can decode and nobody deletes would be re-read on every poll forever.
func ListRetires() ([]RetireEntry, error) {
	dir, err := RetireDir()
	if err != nil {
		return nil, err
	}
	paths, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]RetireEntry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, readRetire(p))
	}
	return entries, nil
}

// valid reports whether m is a mode this atrium knows how to act on.
func (m Mode) valid() bool { return m == ModeKill || m == ModePause }

// Gerund names what is happening for a progress row or a "nothing is X-ing this yet"
// warning. Here rather than at either call site because both the producing command and
// the draining TUI need it, in different packages, and two copies of a two-word mapping
// is two places for a third mode to be forgotten.
//
// A mode this build does not recognise gets the neutral word, not the default arm of an
// if. Falling through to "killing" meant announcing the more destructive of the two verbs
// for a record that retireVerb refuses to act on at all — a progress row promising
// something worse than what happens, which is the one direction a label must not be
// wrong in.
func (m Mode) Gerund() string {
	switch m {
	case ModeKill:
		return "killing"
	case ModePause:
		return "pausing"
	default:
		return "retiring"
	}
}

// readRetire decodes one record, screening on the way in for exactly what
// WriteRetire refuses on the way out. Nothing this package produces can fail these;
// a file written by hand or by a different atrium can, and a record that gets past
// here is executed.
//
// A missing created_at is refused rather than tolerated, and for this record type
// that matters more than for a create. expired() treats a zero time as never expired
// — deliberately, so a future version that omits the field is not discarded — which
// would leave a hand-written record both immortal AND executable: it would survive
// every sweep and be acted on by whichever TUI started next, tearing down a session
// weeks after anyone asked for it. The version gate does not stand in for this; it
// screens a different version, not a missing field at this one.
func readRetire(path string) RetireEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return RetireEntry{Path: path, Err: fmt.Errorf("read %s: %w", filepath.Base(path), err)}
	}
	var r Retire
	if err := json.Unmarshal(data, &r); err != nil {
		return RetireEntry{Path: path, Err: fmt.Errorf("decode %s: %w", filepath.Base(path), err)}
	}
	if r.Version != retireVersion {
		return RetireEntry{Path: path, Err: fmt.Errorf(
			"retirement %s has version %d, this atrium understands %d",
			filepath.Base(path), r.Version, retireVersion)}
	}
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" || !filepath.IsAbs(r.Path) {
		return RetireEntry{Path: path, Err: fmt.Errorf(
			"retirement %s names no addressable session", filepath.Base(path))}
	}
	if bad, ok := FirstControlRune(r.Title); ok {
		return RetireEntry{Path: path, Err: fmt.Errorf(
			"retirement %s names a title containing a control character (%q)",
			filepath.Base(path), bad)}
	}
	if !r.Mode.valid() {
		return RetireEntry{Path: path, Err: fmt.Errorf(
			"retirement %s asks for %q, which is not a retirement mode", filepath.Base(path), r.Mode)}
	}
	if r.CreatedAt.IsZero() {
		return RetireEntry{Path: path, Err: fmt.Errorf(
			"retirement %s carries no created_at, so nothing can expire it", filepath.Base(path))}
	}
	if futureTimestamp(r.CreatedAt, time.Now()) {
		return RetireEntry{Path: path, Err: fmt.Errorf(
			"retirement %s is stamped in the future (%s), so nothing can expire it",
			filepath.Base(path), r.CreatedAt.Format(time.RFC3339))}
	}
	return RetireEntry{Path: path, Retire: r}
}

// futureTimestamp reports whether createdAt is far enough ahead of now that expired()
// could never fire for it.
//
// It is the second half of the same guard the zero-time screen is the first half of, and
// it has the identical consequence: expired() is now.Sub(createdAt) > TTL, which is
// negative for anything ahead of the clock, so such a record survives every sweep and
// every reset-era horizon check while remaining perfectly executable — and because a
// record's filename is its UnixNano, it also sorts to the tail of the spool and is drained
// last, after everything an operator was watching. One TUI acts on it weeks after anyone
// asked.
//
// The slack is what keeps this from being a liability of its own. Two processes stamping
// and reading a time disagree by microseconds, and NTP nudges a clock while Atrium runs,
// so refusing anything strictly ahead of now would make the spool unreliable on a healthy
// machine. What it has to catch is a clock that was wrong by enough to outlive the
// horizon, and anything past a minute of skew is that.
func futureTimestamp(createdAt, now time.Time) bool {
	return createdAt.After(now.Add(clockSkewSlack))
}

// clockSkewSlack is how far ahead of the reader's clock a record's timestamp may sit
// before it is treated as a wrong clock rather than ordinary disagreement between two
// processes. See futureTimestamp.
const clockSkewSlack = time.Minute

// clearRetires discards every queued retirement, returning how many it removed. For
// `atrium reset`, whose wipe takes away the sessions these records name.
//
// It goes through Reject rather than unlinking, for the reason Reject exists: a
// producer blocked in `--wait` cannot tell a discard from a delivery except by the
// receipt, so a bare os.Remove would report reset to a waiting `atrium kill --wait`
// as a successful kill — exit 0, and a caller proceeding as though a session it can
// still see is gone.
//
// It is a walk of its own rather than a third resolver inside Clear because Clear's
// loop carries create-spool machinery — the claim rename and the disclosure a
// destroyed request needs — and neither has a meaning here: there are no claims, and
// a discarded retirement strands nothing that needs naming. Sharing that loop would mean
// building a disclosure for every record, which for a retirement is a paragraph about a
// session nobody was creating.
func clearRetires() (int, error) {
	dir, err := RetireDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("outbox: read spool dir: %w", err)
	}

	var removed int
	var firstErr error
	for _, de := range entries {
		name := de.Name()
		// Both guards, as in listFiles, and most of all here: this is a walk that
		// destroys what it matches. Anything else in the directory is a receipt, or is
		// not ours at all — an editor's swap file, or WriteFileAtomic's in-flight temp
		// for a concurrent kill.
		if de.IsDir() || !isMessageFile(name) {
			continue
		}
		if err := Reject(filepath.Join(dir, name), clearReason); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("outbox: discard %s: %w", name, err)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
