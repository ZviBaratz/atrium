package outbox

// create_disclosure.go — the terminal form of a spent create request (#731, #732).
//
// A claim is the durable link between "an `atrium new` request was made" and "a branch,
// a worktree and a tmux session exist" (see Claim). Giving up on one used to break that
// link while the artifacts survived: Reject answers the caller, DiscardCreate takes the
// record and the claim, and nothing is left. No row either — a create that gets that far
// and still has no row is one whose persist failed, or one refused before any row could be
// written — so the branch, the worktree and the live agent belonged to nothing and were
// never mentioned again.
//
// A disclosure is what survives instead. It records what one spent request left behind
// and the reason its caller was given, so the next TUI can say so; and it is deliberately
// NOT a Request. That is the whole safety argument, and it is the one outbox.go's header
// makes for keeping the two spools in separate directories: a claim that survived in a
// "terminal" form would be one boolean away from being rebuilt by a later edit of
// classifyCreateClaim, whereas nothing that creates a session can decode this type at
// all. The caller has already been told the create failed; building it afterwards is the
// outcome this shape makes unrepresentable rather than merely guarded.
//
// It is keyed on the RECORD path, like the receipt and the claim, for ClaimPath's reason:
// one path per request is what lets every file belonging to a request be found from the one
// name its producer knows.
//
// Two things a disclosure is not:
//
//   - It is not a queue. The reader shows it once and then unlinks it if nothing is left
//     for it to guard (app.flushCreateDisclosures, ClearDisclosure), and SweepDisclosures is
//     the backstop for one no TUI ever got to. The log line written beside it is the
//     permanent record.
//   - It is not evidence anything still exists. The inventory is what the writer knew at
//     the instant it gave up, so a branch a person deletes in between is named by a
//     disclosure that outlives it. Over-reporting is the harmless direction here — the
//     reader's remedy is "delete this, or make a session on it", which costs nothing
//     against a branch already gone — and re-probing at read time would put git on the
//     startup path for a file that exists only after a failure.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
)

const (
	// disclosureSuffix marks the terminal record. A suffix on the record's own name
	// rather than a directory, for claimedSuffix's reason: isMessageFile is anchored at
	// both ends, so a "….json.disclosure" sibling is already invisible to listFiles, to
	// claimedRecordName (which cuts ".claimed" and then screens what is left) and to the
	// receipt sweep — no existing walk had to learn about this kind in order to keep
	// ignoring it.
	disclosureSuffix = ".disclosure"

	// disclosureVersion is the schema version Disclose stamps and readDisclosure
	// accepts. Independent of createVersion for the reason createVersion is independent
	// of currentVersion: no decoder reads both types, so neither has to move when the
	// other does. An unrecognised version is surfaced as an error rather than decoded on
	// a guess, and the reader's answer to that is to log it and clear it — nobody is owed
	// a receipt for a disclosure, so there is nothing to preserve. "Clear", not "unlink":
	// ClearDisclosure keeps a disclosure whose record or claim is still on disk, and that
	// applies to one this atrium cannot decode as much as to one it can. A version from
	// the future is still a terminal mark.
	disclosureVersion = 1
)

// Disclosure is what one spent create request left behind, plus the reason its caller
// was given for the failure.
//
// Branch, Worktree and TmuxName are each optional because the three go missing
// independently: a direct session has no branch, an interrupted build that never reached
// `git worktree add` has no worktree, and a request refused before Start has neither. A
// disclosure with none of them is still written — see Disclose for why the terminal mark
// must not be conditional on there being something to show.
type Disclosure struct {
	Version int `json:"version"`
	// Title and Repo name the request, so the reader can say which `atrium new` this
	// was without decoding a record that is already gone.
	Title string `json:"title"`
	Repo  string `json:"repo"`
	// Branch is the session branch left behind with no row pointing at it.
	Branch string `json:"branch,omitempty"`
	// Worktree is the directory `git worktree add` registered, which is the artifact
	// that blocks a retry rather than merely surviving one (StrandedWorktreeFor).
	Worktree string `json:"worktree,omitempty"`
	// TmuxName is the tmux session the agent is running in, if it was still there when
	// this was written. It is the one leftover a person can already enumerate for
	// themselves (`tmux -L <socket> ls`); it is recorded because the remedy is a command
	// naming it, and because the row that would have told them which title it belongs to
	// is the thing that went missing.
	TmuxName string `json:"tmux_name,omitempty"`
	// Reason is the wording the caller's rejection receipt carried, repeated here
	// because that receipt is consumed by whoever reads it (ClearRejection) and swept at
	// the TTL horizon, so it cannot be the record of why this happened.
	Reason string `json:"reason"`
	// CreatedAt is when the request was given up on, which is not derivable from anything
	// else on disk: the filename carries the REQUEST's own CreatedAt, and one that sat in
	// the spool for hours before failing is much older than its leftovers.
	//
	// The sweep does not read it — SweepDisclosures ages the file by its own mtime, for the
	// reason SweepRejections does: the same instant, needing no decode, and still an answer
	// for a file that cannot be decoded at all. Its reader is the report
	// (app.createDisclosureReport), where it is the only thing that tells two orphans from
	// different days apart.
	CreatedAt time.Time `json:"created_at"`
}

// Leftovers reports whether d names an artifact a person has to clean up by hand. A
// disclosure without one is a terminal mark and nothing more, and the reader shows only
// the ones that have something to say.
func (d Disclosure) Leftovers() bool {
	return d.Branch != "" || d.Worktree != "" || d.TmuxName != ""
}

// DisclosureEntry is one disclosure found in the create spool. Path is the RECORD path,
// as with ListClaims, because that is the name the rest of the protocol is keyed on. Err
// is set when the file could not be decoded, and the caller's only move then is to log
// it and clear it: nothing downstream can act on a disclosure, so an undecodable one has
// no reader to preserve it for.
type DisclosureEntry struct {
	Path       string
	Disclosure Disclosure
	Err        error
}

// disclosurePath returns the file a spent request's disclosure lives in, derived from
// the record path exactly as ClaimPath is.
func disclosurePath(record string) string { return record + disclosureSuffix }

// Disclose records what a spent create request left behind. It is written BEFORE the
// receipt and before the discard, and both orderings are load-bearing:
//
//   - before Reject, because Reject unlinks the record, and a crash in between would
//     otherwise leave the orphan with nothing that mentions it — the failure this type
//     exists for;
//   - before DiscardCreate, because that is what makes a discard failure harmless. A
//     claim that outlives a failed unlink now has a disclosure beside it, and
//     app.classifyCreateClaim reads that as "the caller has already been answered"
//     before it reads any evidence — so the claim can no longer be re-judged on a later
//     launch, against live git, into a verdict that builds the session its caller was
//     told had failed (#731).
//
// That second reason is why every giving-up on a CLAIM writes one, including one with
// nothing to show: the terminal mark is the guard, and a guard that only appears when there
// happens to be a branch to name is not one. A request refused at the drain's gates is the
// case that does not, and app.flushCreateDisclosures spells out why — it never built
// anything, so there is nothing to name and no rebuild to forbid.
// It stamps Version and CreatedAt on the Disclosure it is given rather than on a copy, so
// the caller that buffers the same value for this process's own report (app.discloseCreate‑
// Leftovers) shows the record that was written rather than one missing both fields.
func Disclose(record string, d *Disclosure) error {
	if err := validRecord(record); err != nil {
		return err
	}
	d.Version = disclosureVersion
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	data, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("outbox: encode create disclosure: %w", err)
	}
	if err := config.WriteFileAtomic(disclosurePath(record), data, 0o644); err != nil {
		return fmt.Errorf("outbox: write create disclosure: %w", err)
	}
	return nil
}

// DisclosureFor returns the disclosure recorded for record, if there is one. A file that
// cannot be decoded reports true with a zero Disclosure: the caller asking this question
// wants to know whether the request has already been given up on, and a disclosure it
// cannot read still answers that.
func DisclosureFor(record string) (Disclosure, bool) {
	e := readDisclosure(disclosurePath(record))
	if e.Err != nil {
		if errors.Is(e.Err, fs.ErrNotExist) {
			return Disclosure{}, false
		}
		return Disclosure{}, true
	}
	return e.Disclosure, true
}

// ListDisclosures returns every disclosure in the create spool, oldest first.
//
// Unlike ListClaims this is safe to call at any time and against any peer: a disclosure
// is terminal, so there is no live process whose in-flight work it could describe, and
// nothing here creates, adopts or re-queues anything. What the caller owes it is a read
// and an unlink.
func ListDisclosures() ([]DisclosureEntry, error) {
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
	// the shared suffix does not disturb that order. (ListClaims' point, and the reason
	// the order is by the REQUEST's timestamp rather than the disclosure's — near enough
	// for a report, and free.)
	entries := make([]DisclosureEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		record, ok := disclosedRecordName(de)
		if !ok {
			continue
		}
		recordPath := filepath.Join(dir, record)
		e := readDisclosure(disclosurePath(recordPath))
		e.Path = recordPath
		entries = append(entries, e)
	}
	return entries, nil
}

// ClearDisclosure drops a disclosure the reader has shown, UNLESS the record or claim it
// marks terminal is still on disk. A file that is already gone is not an error, as in
// Remove; a disclosure still doing its second job is not one either, and reports no error
// because there is nothing for the caller to fix.
//
// The two jobs are what makes this conditional. A disclosure is a report, consumed once,
// and it is the mark that keeps the file beside it from being executed — and only the
// report is finished when the reader has shown it. A refusal whose DiscardCreate failed
// leaves claim + disclosure, and clearing the disclosure on the same launch that showed it
// would hand the next launch a bare claim to re-judge against live git, into a verdict that
// builds the session its caller was already told had failed. That is the hole Disclose is
// ordered before DiscardCreate to close, re-entered from the reader's side.
//
// So the mark outlives the report, and the cost is that the report repeats on every launch
// until the unlink that failed succeeds. What it repeats is still true of artifacts still
// stranded, and every launch retries that unlink (app.applyCreateClaim's claimAnswered arm),
// so the repetition ends when the condition does.
func ClearDisclosure(record string) error {
	if recordStillSpooled(record) {
		return nil
	}
	if err := os.Remove(disclosurePath(record)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("outbox: remove create disclosure: %w", err)
	}
	return nil
}

// recordStillSpooled reports whether a create record or its claim is on disk, which is what
// a disclosure beside them is still guarding.
//
// Either file, because either one is executable: the record by drainCreateRequests and the
// claim by reconcileCreateClaims. A stat that fails for any reason other than "not there"
// answers yes — the question is "may I destroy the only thing stopping this from being
// built", and the safe answer to an unreadable spool is no.
func recordStillSpooled(record string) bool {
	for _, path := range []string{record, ClaimPath(record)} {
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// SweepDisclosures deletes disclosures past the TTL horizon.
//
// Separate from SweepRejections rather than folded into it, because the two answer to
// different readers and only one of them is in the prompt spool: a receipt is read by a
// producer still blocked in --wait, a disclosure by the next TUI. Merging them would
// leave one of the two names describing half of what its function does, which is the
// comment that rots.
//
// The horizon is the backstop, not the mechanism. A disclosure is normally shown and
// unlinked by the first TUI to start after it was written; this is for one written by a
// process whose user never came back, and the log line it was written beside outlives it
// either way.
//
// It defers to recordStillSpooled for ClearDisclosure's reason, which is the other half of
// why the two kinds cannot share one sweep: a horizon that dropped the mark while the claim
// it guards was still there would put the rebuild a day away rather than a launch away.
func SweepDisclosures(now time.Time) {
	dir, err := CreateDir()
	if err != nil {
		return
	}
	sweepSuffixed(dir, disclosureSuffix, now, recordStillSpooled)
}

// discloseClearedClaim records what an `atrium reset` is about to strand by destroying a
// claim, and is a no-op when there is nothing it can name or a disclosure already says it.
//
// Only the branch, and only from the claim's own evidence block. It is what survives a
// reset (see Clear) and it is the one artifact reset leaves with nothing pointing at it;
// naming the worktree directory or the agent here would name two things reset destroys on
// its way past. An undecodable claim yields no evidence and so no disclosure — reset still
// takes the file, and the log line Clear's caller writes is the only account there can be.
//
// Any disclosure already there wins, readable or not. The pair "claim plus disclosure" is a
// refusal whose unlink failed, and that refusal wrote its account with the full inventory in
// hand, so overwriting it with this one's single field would trade a complete account for a
// partial one. One this atrium cannot decode wins for the sharper reason: it may be a newer
// atrium's, and replacing it would be answering "I cannot read this" by destroying it.
func discloseClearedClaim(record string, claim CreateEntry) error {
	if claim.Err != nil || claim.Request.Claim == nil || claim.Request.Claim.SessionBranch == "" {
		return nil
	}
	if _, already := DisclosureFor(record); already {
		return nil
	}
	d := Disclosure{
		Title:  claim.Request.Title,
		Repo:   claim.Request.Path,
		Branch: claim.Request.Claim.SessionBranch,
		Reason: clearReason + ", and this one had already created its session branch",
	}
	return Disclose(record, &d)
}

// trimClearedDisclosure drops the two fields an `atrium reset` invalidates as it runs — the
// worktree directory and the tmux session — from a disclosure it is carrying across.
//
// Rewritten in place rather than deleted, because the branch it may also name is exactly
// what reset does not remove. A disclosure left with nothing to name is still kept: it may
// be the mark holding a record or claim terminal, and that job is recordStillSpooled's to
// end rather than this one's. An unreadable one is left alone for the reader to log and
// clear on its own terms.
func trimClearedDisclosure(record string) error {
	e := readDisclosure(disclosurePath(record))
	if e.Err != nil {
		return nil
	}
	if e.Disclosure.Worktree == "" && e.Disclosure.TmuxName == "" {
		return nil
	}
	d := e.Disclosure
	d.Worktree, d.TmuxName = "", ""
	if err := Disclose(record, &d); err != nil {
		return fmt.Errorf("outbox: trim create disclosure: %w", err)
	}
	return nil
}

// disclosedRecordName reports the record name behind a disclosure file, and whether de is
// one at all — claimedRecordName's screening, for claimedRecordName's reason: a directory
// is never a disclosure, and the name under the suffix must be one writeRecord produced,
// so this can only ever surface, and its callers only ever delete, our own files.
func disclosedRecordName(de fs.DirEntry) (string, bool) {
	if de.IsDir() {
		return "", false
	}
	base, ok := strings.CutSuffix(de.Name(), disclosureSuffix)
	if !ok || !isMessageFile(base) {
		return "", false
	}
	return base, true
}

func readDisclosure(path string) DisclosureEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		// Wrapped with %w rather than reworded: DisclosureFor separates "there is none"
		// from "there is one I cannot read" with errors.Is, and a formatted-away
		// fs.ErrNotExist would collapse the two.
		return DisclosureEntry{Path: path, Err: fmt.Errorf("read %s: %w", filepath.Base(path), err)}
	}
	var d Disclosure
	if err := json.Unmarshal(data, &d); err != nil {
		return DisclosureEntry{Path: path, Err: fmt.Errorf("decode %s: %w", filepath.Base(path), err)}
	}
	if d.Version != disclosureVersion {
		return DisclosureEntry{Path: path, Err: fmt.Errorf(
			"create disclosure %s has version %d, this atrium understands %d",
			filepath.Base(path), d.Version, disclosureVersion)}
	}
	return DisclosureEntry{Path: path, Disclosure: d}
}
