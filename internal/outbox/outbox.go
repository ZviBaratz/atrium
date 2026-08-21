// Package outbox implements the durable spools that carry work from an external
// atrium process to the running TUI: prompts from `atrium send` (this file) and
// session-create requests from `atrium new` (create.go).
//
// The spools exist because state.json has exactly one writer at any instant. The
// interactive TUI holds tui.lock for its whole life and rewrites the file whole
// on every save (issue #230), and the autoyes daemon does the same from a
// snapshot taken at its own startup. A second process appending a queued prompt
// directly would be clobbered by either — by a concurrent TUI save, or by the
// daemon's exit save replaying a snapshot that predates the append. So
// `atrium send` never writes state.json: it drops a message here, and whichever
// process legitimately owns the write drains it. `atrium new` is the same
// producer shape for a session that does not exist yet (#703).
//
// The two record types live in separate directories rather than sharing one with
// a kind field, because their identity semantics are opposites — a message's
// (Title, Path) must name an existing session, a request's must not — so a reader
// that got a discriminator wrong would type a first prompt into whatever session
// happens to share the title. Separate directories make that mis-route
// unrepresentable: a request is not in the directory List reads, and the only
// trace of it there is the "create" directory entry, which listFiles rejects both
// for being a directory and for not matching the record name format — so no
// atrium, however old, can read one spool as the other. See create.go for the
// versioning half of that argument.
//
// Readers need no lock. Every record is committed by rename
// (config.WriteFileAtomic), so a file is either absent or complete, never torn —
// the same contract the hidden `atrium hook` subcommand relies on for its own
// cross-process handoff.
package outbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
)

const (
	// TTL is how long an undelivered message stays valid. A prompt spooled a day
	// ago describes a tree that has moved on, so delivering it is worse than
	// dropping it. The horizon also bounds how long a killed-and-recreated
	// session could inherit a message meant for its predecessor.
	TTL = 24 * time.Hour

	// currentVersion is the schema version Write stamps and List accepts. A
	// message carrying anything else came from a different atrium and is
	// surfaced as an error rather than decoded on a guess.
	currentVersion = 1

	dirName = "outbox"

	// nonceBytes of randomness (8 hex characters) disambiguate two senders that
	// land in the same nanosecond.
	nonceBytes = 4
)

// Message is one queued prompt awaiting delivery to a session.
//
// Title and Path together identify the target. Neither alone is enough: titles
// are unique only within a repo group, which is why the storage layer also
// matches instances on the (Title, Path) pair.
type Message struct {
	Version int    `json:"version"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	// TmuxName is recorded for diagnostics only. It is never matched on, because
	// it is absent from state files written before the field existed.
	TmuxName  string    `json:"tmux_name,omitempty"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Expired reports whether m is past the TTL horizon as of now.
func (m Message) Expired(now time.Time) bool {
	return expired(m.CreatedAt, now)
}

// expired is the TTL rule both spools apply, in one place so a record type cannot
// acquire its own. A record with no timestamp is never expired: treating a zero
// time as infinitely old would silently discard anything written by a future
// version that omits the field.
//
// That branch is defence in depth rather than a live path today — read and
// readCreate both reject an unrecognised version before anything calls this, so a
// future version's record never reaches here through List or ListCreates. The
// versioning story deliberately does not lean on it (see create.go).
func expired(createdAt, now time.Time) bool {
	if createdAt.IsZero() {
		return false
	}
	return now.Sub(createdAt) > TTL
}

// Entry is one file found in the spool. Err is set when the file could not be
// decoded into a usable Message; Path is always populated so the caller can
// discard an unusable file rather than retrying it forever.
type Entry struct {
	Path    string
	Message Message
	Err     error
}

// Dir returns the spool directory, <data dir>/outbox. It does not create it.
func Dir() (string, error) {
	cfgDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("outbox: resolve data dir: %w", err)
	}
	return filepath.Join(cfgDir, dirName), nil
}

// Write commits m to the spool and returns the path it was written to. It stamps
// Version and, unless the caller supplied one, CreatedAt.
func Write(m Message) (string, error) {
	if m.Title == "" || m.Path == "" || m.Text == "" {
		return "", errors.New("outbox: a message needs a title, a path and text")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	m.Version = currentVersion

	dir, err := Dir()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("outbox: encode message: %w", err)
	}
	return writeRecord(dir, "message", m.CreatedAt, data)
}

// writeRecord commits one spool record to dir and returns the path it was written
// to. noun names the record in a write failure, which reaches the user as the
// producing command's error.
//
// The filename is the creation timestamp in zero-padded nanoseconds plus a random
// nonce. Zero padding is what makes the lexicographic order os.ReadDir returns
// identical to chronological order: without it a timestamp with fewer digits
// sorts after a longer one, and records would drain out of order. Both spools
// share this so the rule — and the padding the ordering depends on — has one
// definition, and so isMessageFile keeps matching whatever either of them writes.
func writeRecord(dir, noun string, createdAt time.Time, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("outbox: create spool dir: %w", err)
	}

	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("outbox: generate nonce: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%019d-%s.json", createdAt.UnixNano(), hex.EncodeToString(nonce)))
	if err := config.WriteFileAtomic(path, data, 0o644); err != nil {
		return "", fmt.Errorf("outbox: write %s: %w", noun, err)
	}
	return path, nil
}

// List returns every message in the spool, oldest first. A missing spool
// directory is the steady state for anyone who has never run `atrium send`, so
// it yields no entries and no error.
//
// Undecodable files are returned as entries carrying Err rather than being
// skipped or failing the whole call: the caller is the only party that can
// discard them, and a file nobody can decode and nobody deletes would be
// re-read on every poll forever.
func List() ([]Entry, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	paths, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, read(p))
	}
	return entries, nil
}

// listFiles returns the spool records in dir, oldest first — the paths of every
// file matching the name format this package writes, and nothing else.
//
// It is also where the create spool stays invisible to the prompt one, since the
// former is a subdirectory of the latter. Two independent guards reject that
// entry — IsDir, and a name the record format does not match — and neither is
// load-bearing alone. Pinned by TestListIgnoresTheCreateSubdirectory, which
// asserts the property rather than either mechanism.
func listFiles(dir string) ([]string, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("outbox: read spool dir: %w", err)
	}

	// os.ReadDir sorts by filename, which the filename format makes chronological.
	paths := make([]string, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() || !isMessageFile(de.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(dir, de.Name()))
	}
	return paths, nil
}

// Remove discards a drained or undeliverable message. A file that is already
// gone is not an error.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("outbox: remove message: %w", err)
	}
	return nil
}

// messageFileRe matches exactly the names Write produces: 19 digits of
// zero-padded nanoseconds, a hyphen, and the hex nonce.
var messageFileRe = regexp.MustCompile(`^\d{19}-[0-9a-f]+\.json$`)

// isMessageFile screens a spool directory down to files this package wrote. Both
// spools use it, because writeRecord gives both the same name format.
//
// Matching the full filename shape rather than just a ".json" suffix matters
// because List hands undecodable files to the caller to delete: anything that
// slips through this filter and fails to parse gets removed. Requiring our own
// name format means we can only ever delete our own files — never an editor's
// swap file, and never WriteFileAtomic's in-flight temp for a concurrent send,
// which lives in this same directory.
func isMessageFile(name string) bool {
	return messageFileRe.MatchString(name)
}

func read(path string) Entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{Path: path, Err: fmt.Errorf("read %s: %w", filepath.Base(path), err)}
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Entry{Path: path, Err: fmt.Errorf("decode %s: %w", filepath.Base(path), err)}
	}
	if m.Version != currentVersion {
		return Entry{Path: path, Err: fmt.Errorf(
			"message %s has version %d, this atrium understands %d", filepath.Base(path), m.Version, currentVersion)}
	}
	return Entry{Path: path, Message: m}
}

// rejectedSuffix names the receipt left behind when a record could not be acted
// on. It exists so a waiting `--wait` can tell "done" from "discarded": the drain
// removes the record either way, so the file's disappearance alone says only that
// some Atrium consumed it, not that any session received the prompt or that any
// session was created.
//
// Reject, Rejection and ClearRejection take a path and know nothing about the
// record behind it, which is why the create spool reuses them rather than growing
// a second copy of the write-the-receipt-before-the-unlink ordering below.
const rejectedSuffix = ".rejected"

// Reject records why a record could not be acted on and then removes it.
//
// The receipt is written before the record is unlinked so a waiting producer
// cannot observe the gap as a success. If the receipt cannot be written the
// record is still removed: an unreported failure is better than a record that is
// re-read, re-rejected, and never cleared.
//
// The path is screened for validRecord's reason, which the paragraph above ClaimPath
// spells out for this function in particular: a Reject aimed at a claim would write
// "….json.claimed.rejected", a name no walk in this package matches, so nothing lists it,
// nothing sweeps it and `atrium reset` cannot remove it. A receipt is the one derived file
// the create spool and the prompt spool both write, and both write it from a record path.
func Reject(path, reason string) error {
	if err := validRecord(path); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path+rejectedSuffix, []byte(reason), 0o644); err != nil {
		return errors.Join(fmt.Errorf("outbox: write rejection receipt: %w", err), Remove(path))
	}
	return Remove(path)
}

// Rejection returns the recorded reason a record was discarded, if there is one.
func Rejection(path string) (string, bool) {
	data, err := os.ReadFile(path + rejectedSuffix)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// ClearRejection discards a receipt the sender has already reported.
func ClearRejection(path string) error {
	return Remove(path + rejectedSuffix)
}

// SweepRejections deletes receipts past the TTL horizon, in both spools. A
// receipt is only ever read by a producer still blocked in --wait, so one this
// old has no reader left and would otherwise accumulate for the life of the data
// dir.
//
// Receipts only. The create spool holds a second terminal kind with a different reader
// and its own horizon (SweepDisclosures), and this is deliberately not the one function
// that sweeps both — see that one for the argument.
//
// Both, not just the prompt spool: `atrium new --wait` reads its receipts through
// the same Rejection call, so a create spool swept by nobody would leak one file
// per refused request forever.
//
// Age is the receipt file's own mtime, not the timestamp in its name. That name
// carries the *record's* CreatedAt, which for a receipt written because the record
// expired is already a full TTL in the past — keying off it would delete the
// receipt on the very next drain tick, seconds after Reject wrote it and long
// before a waiting producer could read the failure.
func SweepRejections(now time.Time) {
	for _, resolve := range []func() (string, error){Dir, CreateDir} {
		dir, err := resolve()
		if err != nil {
			continue
		}
		sweepReceipts(dir, now)
	}
}

// clearReason is what a producer blocked in --wait is told when `atrium reset` takes
// its queued record away. It has to be a refusal rather than a silent unlink: the
// record vanishing is the signal --wait reads as "done".
const clearReason = "atrium reset discarded every queued request"

// Clear discards every queued record in both spools, returning how many it removed.
// For `atrium reset`, which wipes Atrium's state and must not leave behind a create
// request that would rebuild some of it on the next launch.
//
// "Queued" includes a create some earlier atrium claimed and died building (#716).
// That is the case with teeth for reset in particular: a claim left on disk is what
// the next launch's reconcile reads as work to finish, so skipping it would have
// `atrium reset` wipe state.json and then watch a session reappear.
//
// It goes through Reject rather than unlinking, for the reason Reject exists: a
// producer blocked in `--wait` cannot tell a discard from a delivery except by the
// receipt, so a bare os.Remove here would have `atrium reset` report itself to a
// waiting `atrium new --wait` as a successful creation — exit 0, and a CI job
// proceeding against a session that does not exist.
//
// Existing receipts are therefore left alone rather than swept: a receipt is the only
// record of a refusal its producer may not have read yet, and deleting it is the same
// false success one step earlier. They are bounded already — SweepRejections drops
// them at the TTL horizon.
//
// A create disclosure is kept for the same reason one step further out, and reset is the
// one give-up path that has to both write one and edit one — a decision rather than a
// consequence of the walk below happening not to match the name.
//
// Written, because destroying a claim destroys the only link to the branch that claim's
// interrupted build left behind (see Disclose). Reset without one is the pre-#716 orphan
// with reset's name on it: state.json wiped, the branch still there, and nothing that
// mentions it.
//
// Edited, because reset removes some of what a disclosure already on disk names and not
// the rest, and the two halves do not follow from which artifact it is. What goes is the
// worktree DIRECTORY and the agent: git.CleanupWorktrees removes every top-level DIRECTORY
// under the data dir's worktrees/ tree, and tmux.CleanupSessions kills every session
// matching Prefix() — both walks unscoped by the rows reset just deleted. What stays is
// the BRANCH and git's stale worktree registration, because `branch -D` and
// `worktree prune` are the repo-scoped halves of CleanupWorktrees and an orphan's repo
// has no row to be enumerated through. So a disclosure carried across a reset unedited
// would send the next launch's reader after a directory and a tmux session that reset
// destroyed, under a report saying nothing here will clean them up.
//
// SweepDisclosures bounds whatever is left.
//
// It touches only files this package wrote (the record name format), never the
// directories, and never a stray file some other process put there. The count is of
// records discarded cleanly: one whose receipt could not be written is reported through
// the returned error and not counted, even though Reject removes it regardless — an
// unreported failure being better than a record that is re-read and re-rejected
// forever.
//
// A disclosure this walk could not write or could not trim is deliberately absent from that
// error, and the reason is what the caller does with it: `atrium reset` reads a non-nil
// error as "a record survived, and the next atrium will act on it" and says so on stderr,
// which would be false of a report that failed to be written. Nothing is stranded by that
// failure either — the branch the disclosure would have named is stranded by the reset
// itself, disclosed or not. And it cannot be the only signal of a broken spool: writing a
// disclosure and writing a receipt are the same config.WriteFileAtomic against the same
// directory, so a filesystem that refuses one refuses the other, and the other IS counted.
// That last point is also why this decision has no test: there is no writable sandbox in
// which Disclose fails and Reject succeeds.
func Clear() (int, error) {
	var removed int
	var firstErr error
	for _, resolve := range []func() (string, error){Dir, CreateDir} {
		dir, err := resolve()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = fmt.Errorf("outbox: read spool dir: %w", err)
			}
			continue
		}
		for _, de := range entries {
			name := de.Name()
			// A claimed create is discarded too, and through the same receipt: `atrium
			// reset` must not leave behind a request that the next launch's reconcile
			// would rebuild half of Atrium's state from, and a producer blocked in
			// --wait is watching the claim as well as the record (see ClaimPath), so
			// unlinking one in silence is the same false success a bare os.Remove would
			// be. The receipt goes to the record path because that is the path the
			// producer knows; DiscardCreate then takes both files.
			if record, claimed := claimedRecordName(de); claimed {
				path := filepath.Join(dir, record)
				// Disclose first, for the ordering Disclose documents: both calls
				// below destroy something, and the branch this claim's interrupted
				// build left is named nowhere else. Its failure is deliberately not
				// part of this walk's verdict, though — see below.
				_ = discloseClearedClaim(path, readCreate(ClaimPath(path)))
				err := errors.Join(Reject(path, clearReason), DiscardCreate(path))
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("outbox: discard %s: %w", name, err)
					}
					continue
				}
				removed++
				continue
			}
			if record, ok := disclosedRecordName(de); ok {
				// Kept, and trimmed to what reset leaves standing — see the header.
				_ = trimClearedDisclosure(filepath.Join(dir, record))
				continue
			}
			// Both guards, as in listFiles, and most of all here: this is the walk that
			// destroys what it matches. The name check alone already rejects the nested
			// create/ entry, so IsDir is redundant today — which is exactly the claim
			// listFiles makes about the pair, and a claim the package should be able to
			// make about every walk rather than about one of three.
			if de.IsDir() || !isMessageFile(name) {
				continue // a receipt, a nested spool dir, or not ours at all
			}
			if err := Reject(filepath.Join(dir, name), clearReason); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("outbox: discard %s: %w", name, err)
				}
				continue
			}
			removed++
		}
	}
	return removed, firstErr
}

// validRecord screens the path a spool write is about to act on down to a name
// writeRecord produced.
//
// It closes a whole class rather than a live bug. Claim, Requeue, DiscardCreate, Reject
// and Disclose all derive a second path by concatenation (ClaimPath, disclosurePath,
// rejectedSuffix), so a caller that passed one of those derived paths back in would mint
// "….json.claimed.claimed", "….json.claimed.rejected" or "….json.claimed.disclosure" — a
// file no walk in this package matches, and therefore one nothing lists, sweeps, clears or
// `atrium reset` can ever remove. No caller does that today; the guard is here so none can
// (#731).
//
// Here rather than in create.go because Reject serves both spools, and the record name
// format it screens for is the one both share (writeRecord).
func validRecord(record string) error {
	if base := filepath.Base(record); !isMessageFile(base) {
		return fmt.Errorf("outbox: %q is not a spool record", base)
	}
	return nil
}

func sweepReceipts(dir string, now time.Time) {
	sweepSuffixed(dir, rejectedSuffix, now, nil)
}

// sweepSuffixed drops every file in dir whose name is one writeRecord produced plus
// suffix, and whose own mtime is past the horizon. Shared by the receipt sweep and
// SweepDisclosures so the two terminal kinds cannot drift apart on the part that is
// identical — the screening, and the choice of mtime over the timestamp in the name.
//
// keep, when non-nil, is asked about the RECORD path behind a file old enough to drop and
// vetoes the unlink. It exists because the two kinds differ on what age means: a receipt is
// addressed to a producer that has either read it or gone away, so the horizon is the whole
// of its lifetime, while a disclosure is also the mark that keeps a surviving record or
// claim from being executed (see Disclose) and outliving the horizon is not what makes that
// job finished.
//
// Best-effort throughout: a directory that cannot be read, an entry that vanished between
// ReadDir and Info, and a file that cannot be unlinked are all left for the next sweep.
// Nothing downstream depends on a sweep having happened, so a failure costs one file that
// stays a little longer.
func sweepSuffixed(dir, suffix string, now time.Time, keep func(record string) bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range entries {
		name := de.Name()
		base, ok := strings.CutSuffix(name, suffix)
		if de.IsDir() || !ok || !isMessageFile(base) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue // vanished between ReadDir and Info; nothing left to sweep
		}
		if now.Sub(info.ModTime()) <= TTL {
			continue
		}
		if keep != nil && keep(filepath.Join(dir, base)) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
