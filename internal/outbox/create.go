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
//
// The claim half of this file (Claim/Requeue/ListClaims) is atrium#716: a create
// is not consumed when it is read, it is *claimed* for the whole of the session
// build, and the claim is a file rather than a map entry so the link between a
// request and the session it produced outlives the process that made it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ZviBaratz/atrium/config"
)

const (
	// createVersion is the schema version WriteCreate stamps and ListCreates
	// accepts. It is independent of currentVersion: the two record types are
	// never read by the same decoder, so neither has to move when the other does.
	//
	// It stayed at 1 when the claim fields and Adopt were added (#716), and that is
	// a decision rather than an oversight. readCreate gates on exact equality, so a
	// bump makes every atrium reject every request the other writes — including the
	// ones already sitting in the spool across an upgrade, each with a receipt
	// ("the request could not be read") that misstates its cause. The new fields are
	// all omitempty and all additive: an atrium too old to know Adopt refuses a
	// re-queued orphan with "branch already exists", which is exactly what it did
	// before this feature existed. A strictly-no-worse degradation needs no gate.
	//
	// The asymmetry that argument does not cover is a *downgrade* while a claim is
	// on disk: an older atrium has no ListClaims, so it neither drains nor settles
	// that file, and a `--wait` blocked on it sees the record gone. Downgrading
	// mid-create is not a supported path; the claim is still there for the next
	// upgrade to reconcile.
	createVersion = 1

	createDirName = "create"

	// claimedSuffix marks a request some atrium has taken and is building right now.
	//
	// It is a suffix on the record's own name rather than a separate directory
	// because that is what makes it free: isMessageFile matches "<nanos>-<nonce>.json"
	// anchored at both ends, so a "….json.claimed" sibling is already invisible to
	// listFiles — no new screening, and the "we can only ever delete our own files"
	// argument above is untouched. It also keeps the receipt trio keyed on one path
	// per request (see ClaimPath).
	claimedSuffix = ".claimed"
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

	// Claim is the evidence the drain recorded when it took this request, and is
	// what separates "nothing was built yet" from "a branch exists and belongs to
	// nobody" after a crash. Nil on a request no atrium has reached yet.
	//
	// A pointer with omitempty, not four inline fields: encoding/json omits empty
	// values only for basic types, so an inline time.Time would be emitted as
	// year 1 on every unclaimed request (the trap InstanceData.StatusChangedAt
	// documents). A nil pointer is omitted, so an unclaimed record stays
	// byte-identical to one written before this field existed.
	//
	// It survives a Requeue rather than being cleared: recovery has already read
	// it by then, and keeping it means a re-queued request still says which build
	// it is the second attempt at.
	Claim *ClaimMeta `json:"claim,omitempty"`

	// Adopt lets this request take a session branch that already exists, instead of
	// being refused for it. Set by nothing but the startup reconcile, and only for a
	// branch it has proved belongs to no session (see app.reconcileCreateClaims):
	// the branch check exists because git.Worktree.Setup reads a pre-existing branch
	// as a resume, so this is a hole in a load-bearing guard and the evidence for it
	// is not derivable from the request alone.
	Adopt bool `json:"adopt,omitempty"`
}

// ClaimMeta is what the drain knows at the instant it claims a request and a later
// process cannot re-derive from the request alone.
//
// SessionBranch is the whole point. Recovery could recompute it from Title and the
// configured branch prefix, but the prefix is a config value: one edited between the
// crash and the next launch would have recovery probe for a branch nobody made, read
// the orphan as "nothing was built", and create a second session beside it. Recorded,
// the name is the one that was actually used.
type ClaimMeta struct {
	// At is when the claim was taken. No omitempty (it would be inert on a
	// time.Time) and none is wanted: a ClaimMeta only exists once claimed.
	At time.Time `json:"at"`
	// SessionBranch is the branch git.Worktree.Setup was about to create — the
	// resolved git.BranchNameForSession output, not the caller's base branch.
	// Empty for a direct (non-git) session, which has no branch to strand.
	SessionBranch string `json:"session_branch,omitempty"`
	// BranchExisted is whether SessionBranch was already present at the instant the
	// claim was taken, MEASURED there rather than inferred from the gates that ran
	// before it. That is what makes it usable as evidence: false means a branch found
	// at recovery time appeared after the claim, so the interrupted build is the only
	// thing that can have made it, and adopting it takes nobody's work.
	//
	// True is reachable and is not by itself a refusal — a request already carrying
	// Adopt is on its second attempt at a branch an earlier reconcile vetted, and is
	// expected to find it there. True WITHOUT Adopt is the foreign-branch case, and
	// is the one thing that must never be adopted.
	BranchExisted bool `json:"branch_existed,omitempty"`
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

// ClaimPath returns the file a claimed request lives in. Every other function here
// — Claim, Requeue, ReleaseClaim, and the receipt trio a caller reaches for a claimed
// request — takes the RECORD path, the one WriteCreate returned and the one
// `atrium new --wait` is watching. One path per request is what keeps a rejection
// receipt readable by the process that is blocked on it: a Reject aimed at the claim
// file would write "….json.claimed.rejected", which nothing ever looks for.
func ClaimPath(record string) string { return record + claimedSuffix }

// Claim marks a request as taken and being built, recording meta as the evidence a
// later recovery needs. The record leaves the spool — ListCreates no longer returns
// it — so a claimed request cannot be executed twice, expired out from under its own
// running session, or re-created by the next process to start.
//
// Two writes, both atomic, and that ordering is the crash story. The enriched record
// is committed in place first (config.WriteFileAtomic renames it into position), and
// only then is the record renamed to the claim path. A crash between them leaves an
// ordinary, drainable request carrying extra fields that executeCreateRequest ignores
// — a state indistinguishable from "not claimed yet", so the next drain simply claims
// it again. The obvious alternative (write the claim, then unlink the record) has a
// window where BOTH exist, which is a third state to recognise everywhere; this has
// none.
func Claim(record string, meta ClaimMeta) error {
	entry := readCreate(record)
	if entry.Err != nil {
		return fmt.Errorf("outbox: claim %s: %w", filepath.Base(record), entry.Err)
	}
	r := entry.Request
	r.Claim = &meta
	if err := writeRequestInPlace(record, r); err != nil {
		return err
	}
	if err := os.Rename(record, ClaimPath(record)); err != nil {
		return fmt.Errorf("outbox: claim %s: %w", filepath.Base(record), err)
	}
	return nil
}

// Requeue returns a claimed request to the spool for another attempt, so the next
// drain tick executes it as an ordinary request. With adopt set it is also marked to
// take the session branch a previous, interrupted attempt already created.
//
// Write-then-rename, for Claim's reason: a crash between the two leaves the claim
// exactly as recovery found it, and the next recovery reaches the same verdict from
// the same evidence.
func Requeue(record string, adopt bool) error {
	claim := ClaimPath(record)
	entry := readCreate(claim)
	if entry.Err != nil {
		return fmt.Errorf("outbox: requeue %s: %w", filepath.Base(record), entry.Err)
	}
	r := entry.Request
	r.Adopt = adopt
	if err := writeRequestInPlace(claim, r); err != nil {
		return err
	}
	if err := os.Rename(claim, record); err != nil {
		return fmt.Errorf("outbox: requeue %s: %w", filepath.Base(record), err)
	}
	return nil
}

// DiscardCreate drops a create request that has been accounted for, in whichever form
// it is on disk — the record, its claim, or both. A file that is already gone is not an
// error, as in Remove.
//
// Both, unconditionally, rather than "the claim, because it must have been claimed".
// Claim can fail (a full disk, a read-only data dir) and the drain deliberately builds
// the session anyway rather than refusing one whose worktree is already going up — so
// "in flight" does not imply "claimed", and a settle that removed only the claim would
// leave the record for the next launch to execute a second time. One session becomes
// two, on one branch, and nothing in the protocol notices.
//
// Callers that owe their producer a reason use Reject(record, …) and then this: the
// receipt belongs at the record path (see ClaimPath) and both files have to go either
// way.
func DiscardCreate(record string) error {
	return errors.Join(Remove(record), removeClaim(record))
}

func removeClaim(record string) error {
	if err := os.Remove(ClaimPath(record)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("outbox: remove claim: %w", err)
	}
	return nil
}

// ListClaims returns every claimed request in the create spool, oldest first.
//
// Each entry's Path is the RECORD path rather than the claim file's, because that is
// the path the rest of the protocol is keyed on — the receipt a producer reads, and
// the file `--wait` stats. Reach the file itself through ClaimPath.
//
// Only a startup reconcile has any business calling this. A claim is written by a
// live drain and removed when its start settles, so one still on disk when a process
// starts was left by a process that died — an inference that holds because tui.lock
// (main.go) admits one interactive atrium per data dir at a time, and the kernel frees
// an flock on process death. Nothing here would be safe against a live peer.
func ListClaims() ([]CreateEntry, error) {
	dir, err := CreateDir()
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("outbox: read create spool dir: %w", err)
	}
	// os.ReadDir sorts by filename, which the record name format makes chronological;
	// the shared suffix does not disturb that order.
	entries := make([]CreateEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		record, ok := claimedRecordName(de)
		if !ok {
			continue
		}
		recordPath := filepath.Join(dir, record)
		e := readCreate(ClaimPath(recordPath))
		e.Path = recordPath
		entries = append(entries, e)
	}
	return entries, nil
}

// claimedRecordName reports the record name behind a claim file, and whether de is
// one at all. Both guards that screen the spool elsewhere apply: a directory is never
// a claim, and the name under the suffix must be one writeRecord produced — so this
// can only ever surface, and its callers only ever delete, our own files.
func claimedRecordName(de fs.DirEntry) (string, bool) {
	if de.IsDir() {
		return "", false
	}
	base, ok := strings.CutSuffix(de.Name(), claimedSuffix)
	if !ok || !isMessageFile(base) {
		return "", false
	}
	return base, true
}

// writeRequestInPlace commits r over an existing spool file. Atomic (WriteFileAtomic
// renames into position), so a reader of that path sees either the old record or the
// new one and never a torn write — which is what lets Claim and Requeue treat their
// two steps as two states rather than three.
func writeRequestInPlace(path string, r Request) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("outbox: encode create request: %w", err)
	}
	if err := config.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("outbox: write create request: %w", err)
	}
	return nil
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
