// Package undo implements the durable journal that makes killing a session
// reversible.
//
// Killing a session removes its worktree and runs `git branch -D`, which leaves
// the branch's commits unreachable — and `git gc`, which the user's own work in
// the project repo triggers, prunes unreachable objects past a two-week grace
// period. So recording a head SHA is a promise git does not keep. Instead, before
// teardown, Atrium points a ref at the branch tip
// (refs/atrium/undo/<install>/<id>): objects under a ref stay reachable and
// gc-immune, and the ref is invisible to `git branch`, `git push` and
// `git fetch`. This journal is the index of those refs — what each one holds,
// which session it belonged to, and everything needed to rebuild that session.
//
// It lives in its own directory rather than inside state.json for the same
// reason the outbox does: state.json is rewritten whole by whichever process
// owns the write, and the autoyes daemon's exit save replays a snapshot taken at
// its own startup, so a record appended in between is silently erased. An undo
// record must survive exactly the crashes and races that make someone want it.
// One file per entry also means appending is a single atomic rename and
// discarding one is an unlink — there is no read-modify-write to lose.
//
// There is no locking, and the reason is the file layout rather than a threading
// rule: writes commit by rename, entry filenames are minted from a timestamp plus
// a nonce, and every read and delete is scoped to that filename shape. So a write
// landing during a sweep is benign, and a write landing during another write
// cannot collide. (Do not restate this as "only the Update loop writes" — it does
// not: journalKill runs inside the teardown's tea.Cmd goroutine and the startup
// sweep in its own. Only Remove and MarkSuperseded are on Update.)
//
// The autoyes daemon must never sweep: it would run `update-ref -d` inside user
// repositories, headless, against an instance snapshot it took at startup.
package undo

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ZviBaratz/atrium/config"
)

const (
	// TTL is how long a killed session stays restorable. The window has to span
	// "I realised the next morning", which is the case undo exists for, and it
	// bounds how long the retained objects sit in the user's repository.
	TTL = 7 * 24 * time.Hour

	// currentVersion is the schema version Write stamps and read accepts. An entry
	// carrying anything else came from a different atrium and is skipped rather
	// than decoded on a guess: a misread snapshot would restore something that is
	// not the session that was killed.
	currentVersion = 1

	dirName = "undo"

	// refRoot is the ref namespace retention refs live under. It is deliberately
	// outside refs/heads so `git branch` never lists a killed session's branch.
	refRoot = "refs/atrium/undo"

	// nonceBytes of randomness (8 hex characters) disambiguate two kills that land
	// in the same nanosecond.
	nonceBytes = 4

	// installIDLen is how much of the data-dir hash names the ref subtree. Twelve
	// hex characters is far past any realistic collision between the handful of
	// data dirs one machine has, and short enough to print in a refusal message.
	installIDLen = 12
)

// Entry is one reversible teardown: everything needed to decide whether a killed
// session can come back, and to rebuild it if so.
//
// Snapshot is raw JSON rather than a typed session.InstanceData on purpose. It
// keeps this package free of a dependency on session (which depends on config,
// which this package uses), it mirrors how state.json already carries its
// instances, and it means a field added to InstanceData tomorrow cannot make
// today's journal undecodable — an older binary reading a newer journal degrades
// to "no undo available" instead of failing.
type Entry struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	// BatchID groups the entries a single visual-mode kill produced, so undoing
	// one user action restores the whole group rather than one session per press.
	BatchID string    `json:"batch_id,omitempty"`
	At      time.Time `json:"at"`
	Title   string    `json:"title"`
	Display string    `json:"display,omitempty"`
	// Path is the project directory. Title and Path together identify the session,
	// because titles are unique only within a repo group.
	Path     string `json:"path"`
	RepoPath string `json:"repo_path,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// TmuxName is matched, not merely recorded: restoring into a name that is live
	// again would attach the rebuilt session to another session's pane.
	TmuxName string `json:"tmux_name,omitempty"`
	Direct   bool   `json:"direct,omitempty"`
	// Ref is the full retention refname; empty for a direct session, which has no
	// repository. SHA is what it pointed at when the session was killed, so a
	// restore can tell a branch that came back from a branch that moved on.
	Ref string `json:"ref,omitempty"`
	SHA string `json:"sha,omitempty"`
	// Dirty records that the session had uncommitted changes when it was killed, and
	// Committed that the teardown managed to fold them into the retained commits.
	// Dirty && !Committed is the one shape where a restore comes back incomplete —
	// `git worktree remove -f` destroyed work the retained commits do not hold — so
	// the post-restore notice reads both and says so rather than implying otherwise.
	Dirty     bool `json:"dirty,omitempty"`
	Committed bool `json:"committed,omitempty"`
	// Superseded marks an entry whose identity has been reused by a new session.
	// Creation wins — a user who kills a session to free its name must not be
	// locked out — so the entry stops being offered while its ref stays retained.
	Superseded bool            `json:"superseded,omitempty"`
	Snapshot   json.RawMessage `json:"snapshot,omitempty"`
}

// Expired reports whether e is past the retention horizon as of now. An entry
// with no timestamp is never expired: treating a zero time as infinitely old
// would delete the ref holding the only copy of the user's work on the strength
// of a field a future version happened to omit.
func (e Entry) Expired(now time.Time) bool {
	if e.At.IsZero() {
		return false
	}
	return now.Sub(e.At) > TTL
}

// Restorable reports whether e may still be offered as an undo target as of now.
//
// A non-direct entry with no SHA is a record of a kill whose commits could not be
// pinned — the repository had moved, or the ref could not be written. The teardown
// keeps the record anyway (so it expires normally rather than lingering), but
// offering it would promise a restore that fails after the confirmation. A direct
// session legitimately has neither ref nor SHA: its snapshot is the whole record.
func (e Entry) Restorable(now time.Time) bool {
	if !e.Direct && e.SHA == "" {
		return false
	}
	return !e.Superseded && !e.Expired(now) && len(e.Snapshot) > 0
}

// Dir returns the journal directory, <data dir>/undo. It does not create it.
func Dir() (string, error) {
	cfgDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("undo: resolve data dir: %w", err)
	}
	return filepath.Join(cfgDir, dirName), nil
}

// RefPrefix returns the ref subtree this install owns, with a trailing slash.
//
// The subtree is keyed on the data directory because two installs — ~/.atrium and
// a legacy ~/.claude-squad, or a sandboxed test HOME — can retain into the same
// project repository, and refs/atrium/ is not exclusively ours to begin with (a
// PR-review workflow writes refs/atrium/pr-NNN into this very repo). Anything
// that enumerates or deletes refs keys off this prefix, so sharing one would let
// one install destroy another's only copy of a killed branch.
func RefPrefix() (string, error) {
	cfgDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("undo: resolve data dir: %w", err)
	}
	abs, err := filepath.Abs(cfgDir)
	if err != nil {
		abs = cfgDir
	}
	sum := sha256.Sum256([]byte(abs))
	return fmt.Sprintf("%s/%s/", refRoot, hex.EncodeToString(sum[:])[:installIDLen]), nil
}

// Ref returns the full retention refname for an entry ID.
func Ref(id string) (string, error) {
	prefix, err := RefPrefix()
	if err != nil {
		return "", err
	}
	return prefix + id, nil
}

// NewID mints an entry ID for a kill happening at t.
//
// The ID is the timestamp in zero-padded nanoseconds plus a random nonce, and it
// is also the filename stem. Zero padding is what makes the lexicographic order
// os.ReadDir returns identical to chronological order: without it a timestamp
// with fewer digits sorts after a longer one, and Latest would offer the wrong
// session.
func NewID(t time.Time) (string, error) {
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("undo: generate nonce: %w", err)
	}
	return fmt.Sprintf("%019d-%s", t.UnixNano(), hex.EncodeToString(nonce)), nil
}

// Write commits e to the journal and returns it with the fields Write stamped.
//
// It fills in At, ID, Ref and Version when the caller left them blank, so a
// caller that needs the refname before the entry exists can mint the ID itself
// and pass it through unchanged.
func Write(e Entry) (Entry, error) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.ID == "" {
		id, err := NewID(e.At)
		if err != nil {
			return Entry{}, err
		}
		e.ID = id
	}
	if e.Ref == "" && !e.Direct {
		ref, err := Ref(e.ID)
		if err != nil {
			return Entry{}, err
		}
		e.Ref = ref
	}
	e.Version = currentVersion

	dir, err := Dir()
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Entry{}, fmt.Errorf("undo: create journal dir: %w", err)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("undo: encode entry: %w", err)
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, e.ID+".json"), data, 0o600); err != nil {
		return Entry{}, fmt.Errorf("undo: write entry: %w", err)
	}
	return e, nil
}

// Load returns every readable entry, oldest first. Entries this binary cannot
// understand — corrupt, or stamped with another schema version — are skipped, so
// one bad file never costs the user the records that are still good. A missing
// journal directory is the steady state for anyone who has never killed a
// session, so it yields no entries and no error.
func Load() ([]Entry, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	names, err := entryFiles(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		e, err := read(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// Latest returns the newest entry that may still be restored as of now, skipping
// any whose ID is in skip (nil skips nothing).
//
// The horizon is checked here, on read, and not only by Sweep: a session killed a
// week before the TUI was last opened must never be offered, however long it is
// until the next sweep runs.
//
// skip exists because a refusal is not a state this package can see. An entry
// whose repository has been deleted is refused every time and removed never, so
// without a way to step past it the newest record wedges the stack and hides every
// older one for the whole retention window. The caller passes the entries it has
// already been refused; keeping that in the caller rather than on disk means a
// relaunch re-offers them, which is right — the world may have changed.
func Latest(now time.Time, skip map[string]bool) (Entry, bool) {
	entries, err := Load()
	if err != nil {
		return Entry{}, false
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Restorable(now) && !skip[entries[i].ID] {
			return entries[i], true
		}
	}
	return Entry{}, false
}

// LatestBatch returns the newest restorable entry together with the rest of its
// batch, oldest first. A visual-mode kill of five sessions is one user action, so
// undo reverses it as one.
func LatestBatch(now time.Time, skip map[string]bool) ([]Entry, bool) {
	newest, ok := Latest(now, skip)
	if !ok {
		return nil, false
	}
	if newest.BatchID == "" {
		return []Entry{newest}, true
	}
	entries, err := Load()
	if err != nil {
		return []Entry{newest}, true
	}
	group := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.BatchID == newest.BatchID && e.Restorable(now) && !skip[e.ID] {
			group = append(group, e)
		}
	}
	return group, len(group) > 0
}

// Remove discards a consumed entry. A file that is already gone is not an error,
// so a retry after a partial failure is safe.
func Remove(id string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("undo: remove entry: %w", err)
	}
	return nil
}

// MarkSuperseded flags every entry matching pred so it is no longer offered,
// without touching its retention ref. The commits stay reachable, so a refusal
// can still name the ref and turn "no" into "not automatically".
func MarkSuperseded(pred func(Entry) bool) error {
	entries, err := Load()
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if e.Superseded || !pred(e) {
			continue
		}
		e.Superseded = true
		if _, err := Write(e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sweep discards entries past the horizon, calling dropRef for each so the caller
// can delete its retention ref first. Ref before entry: if the entry went first,
// a crash in between would leak a ref with nothing left that names it.
//
// It also deletes files it cannot decode at all, but only once they are older
// than the horizon — deleting immediately would race an entry being written this
// instant. A file that decodes as JSON but carries a foreign schema version is
// left alone: it names a ref this binary cannot read, and deleting the only
// pointer to those objects is worse than leaving a file a newer atrium will
// understand.
func Sweep(now time.Time, dropRef func(Entry)) {
	dir, err := Dir()
	if err != nil {
		return
	}
	names, err := entryFiles(dir)
	if err != nil {
		return
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		e, err := read(path)
		if err != nil {
			if errors.Is(err, errForeignVersion) {
				continue
			}
			if info, statErr := os.Stat(path); statErr == nil && now.Sub(info.ModTime()) > TTL {
				_ = os.Remove(path)
			}
			continue
		}
		if !e.Expired(now) {
			continue
		}
		if dropRef != nil {
			dropRef(e)
		}
		_ = os.Remove(path)
	}
}

// Clear discards the whole journal, calling dropRef for each entry first. It is
// what `atrium reset` needs: CleanupWorktrees enumerates branches from
// `git worktree list`, which cannot see a ref outside refs/heads, so without this
// a reset would leave every retained object alive in the user's repository
// forever — a permanent leak caused by the command whose entire job is cleanup.
func Clear(dropRef func(Entry)) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	names, err := entryFiles(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		path := filepath.Join(dir, name)
		if e, err := read(path); err == nil && dropRef != nil {
			dropRef(e)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("undo: remove entry: %w", err))
		}
	}
	return errors.Join(errs...)
}

// entryFileRe matches exactly the names Write produces: 19 digits of zero-padded
// nanoseconds, a hyphen, and the hex nonce.
//
// Matching the full shape rather than a ".json" suffix is what keeps Sweep and
// Clear from deleting a file this package did not write — an editor swap file, or
// WriteFileAtomic's in-flight temp for a concurrent write, which lands in this
// same directory.
var entryFileRe = regexp.MustCompile(`^\d{19}-[0-9a-f]+\.json$`)

// entryFiles lists the journal's own files, oldest first. os.ReadDir sorts by
// name, which the filename format makes chronological. A missing directory is
// not an error.
func entryFiles(dir string) ([]string, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("undo: read journal dir: %w", err)
	}
	names := make([]string, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() || !entryFileRe.MatchString(de.Name()) {
			continue
		}
		names = append(names, de.Name())
	}
	return names, nil
}

// errForeignVersion distinguishes an entry from another atrium — readable JSON,
// unreadable meaning — from a file that is simply corrupt. Sweep deletes the
// second and preserves the first.
var errForeignVersion = errors.New("undo: entry was written by a different atrium version")

func read(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("undo: read %s: %w", filepath.Base(path), err)
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, fmt.Errorf("undo: decode %s: %w", filepath.Base(path), err)
	}
	if e.Version != currentVersion {
		return Entry{}, fmt.Errorf("%w: %s has version %d, this atrium understands %d",
			errForeignVersion, filepath.Base(path), e.Version, currentVersion)
	}
	// The ID is a path component — Write and Remove both join it onto the journal
	// directory — so it is checked against the filename it arrived in rather than
	// trusted. A hand-edited or half-restored file whose id disagrees with its name
	// would otherwise make Remove a silent no-op (offering a consumed record again),
	// and an id containing a traversal would reach outside the journal entirely.
	if e.ID+".json" != filepath.Base(path) {
		return Entry{}, fmt.Errorf("undo: %s carries id %q, which is not its filename",
			filepath.Base(path), e.ID)
	}
	return e, nil
}
