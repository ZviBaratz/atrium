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
// That second reason is why every giving-up writes one, including one with nothing to show:
// the terminal mark is the guard, and a guard that only appears when there happens to be a
// branch to name is not one.
//
// Every giving-up, and not only every giving-up on a claim. A record can outlive the Reject
// meant to remove it just as a claim can, and several of the drain's gates are facts about
// the FLEET rather than about the request: a rate limit resets, a session cap is raised, a
// title is freed by a kill. Re-drained hours later against a fleet that has moved on, the
// same record passes and the session is built for a caller that read the refusal and exited
// non-zero. The mark is what makes an unlink that failed harmless in that direction too;
// app.flushCreateDisclosures is where an inventory-less one stops short of a modal.
//
// A record refused at those gates usually has nothing to name, and that is a usually rather
// than a rule: one the startup reconcile re-queued to finish an interrupted build carries
// the branch that build made, in an evidence block that outlives the licence to adopt it
// (app.rejectCreateRequest). What a giving-up owes is separately the mark and, when it has
// one, the inventory — which is why neither is inferred from the other here.
//
// What does NOT write one is a giving-up that stays true however often it is repeated. An
// expired record is expired again on the next tick and an undecodable one is still
// undecodable, so the arms that dispose of those get no guard from a mark and would mint a
// file per record per tick for a cron backlog (app.drainCreateRequests). They write one only
// when they have an inventory to report, where the file is a report and not a guard.
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

// DisclosureState is what DisclosureFor could establish about a record's terminal mark.
// Three values rather than a bool because the two questions a caller has are not the same
// question: "is there a mark" and "could I find out". Both consumers act destructively on
// the first — the drain answers the caller and unlinks the record, the startup reconcile
// spends a stranded claim's one-shot recovery — so an answer that folds "I could not look"
// into either direction destroys something on a filesystem hiccup.
type DisclosureState int

const (
	// DisclosureUnknown means the mark could not be looked for. Iota-0 deliberately, so the
	// zero value of a var, a struct field or a map miss is the one that licenses nothing —
	// recordStillSpooled's rule, and app.adoptionState's.
	DisclosureUnknown DisclosureState = iota
	// NoDisclosure means there is no mark: the record or claim may be executed.
	NoDisclosure
	// HasDisclosure means there is one. The request has been given up on and its caller
	// answered, whether or not the file could be decoded.
	HasDisclosure
)

// DisclosureFor returns the disclosure recorded for record, and what could be established
// about it.
//
// Presence is a stat and content is a read, and separating them is the point. os.ReadFile
// opens a file descriptor, so under fd exhaustion it can answer EMFILE for a path that
// does not exist — and a predicate that read every non-ENOENT error as "there is a mark"
// then reports every queued request terminal, which is a receipt and an unlink apiece for
// requests nothing was ever wrong with. os.Stat allocates no descriptor and so cannot give
// that answer.
//
// A file that exists and cannot be decoded is HasDisclosure with a zero Disclosure: the
// question is whether the request has already been given up on, and a mark nobody can read
// still answers that. Only a stat that fails for some other reason (a stale NFS handle, an
// I/O error) is DisclosureUnknown, and the callers hold rather than guess.
//
// The read comes FIRST and the stat only settles a read that failed, which is the ordering
// and not an incidental arrangement. Stat-then-read leaves a window: another atrium's reader
// clears a delivered disclosure between the two calls, and the read then fails for a file the
// stat had just seen. There is no honest answer to give from inside that arm — reported
// terminal, it answers a healthy request with a receipt saying an earlier atrium gave up
// without recording why, and unlinks it; reported absent, it contradicts a stat that was true
// when it ran. This way round the stat is the last word in every ambiguous case, so the arm
// does not exist: a file present at the read is decoded, and one that is not present is
// whatever the stat says about it afterwards.
//
// No test distinguishes the two orders and none can — the arm is reachable only through a real
// race between two processes — so this is a claim about the shape rather than one the suite
// holds. What the suite does hold is the answer given to each way a read can fail, which is
// what that arm would have got wrong. The order is also one syscall rather than two on the
// path that finds a mark, which is the path both consumers take when they are about to act.
func DisclosureFor(record string) (Disclosure, DisclosureState) {
	// Screened here as well as in DisclosureMark, and not redundantly: every ambiguous case
	// below defers to that function and inherits its screen, but a successful READ does not.
	// A decodable file at "<record>.claimed.disclosure" would come back from this as a mark
	// terminal at a name no walk in the package matches — licensing the destruction of a
	// record on the strength of a file nothing here can ever clear.
	if err := validRecord(record); err != nil {
		return Disclosure{}, DisclosureUnknown
	}
	e := readDisclosure(disclosurePath(record))
	if e.Err == nil {
		return e.Disclosure, HasDisclosure
	}
	// The read failed and cannot say why in a way that may be acted on: os.ReadFile opens a
	// descriptor, so under fd exhaustion it answers EMFILE for a path that does not exist.
	// os.Stat allocates none and so cannot give that answer — see DisclosureMark. A file that
	// exists and merely cannot be DECODED comes back HasDisclosure from there, which is the
	// right answer: the question is whether the request has been given up on, and a mark
	// nobody can read still answers it.
	return Disclosure{}, DisclosureMark(record)
}

// DisclosureMark answers only whether a record has been given up on, without reading the
// file. It is the half of DisclosureFor that decides, and the whole of what a caller needs
// when it has already established that it will not act this tick.
//
// Separated because the read is not free and the decision does not need it.
// app.drainCreateRequests probes every record in the spool on a walk that runs twice a
// second, and its disposal budget means most of a backlog is skipped after the probe — so
// folding the read in paid a ReadFile and an Unmarshal per skipped record, on the Bubble Tea
// update goroutine, for a body it discarded. The stat is what says the record must not be
// touched; the body is only wording for the receipt.
//
// An invalid record path is DisclosureUnknown rather than an error, which keeps this a
// two-line call at both sites. Every derived name (a claim, a receipt, another disclosure)
// answers "I could not establish this", so a caller that passes one licenses nothing — the
// iota-0 rule, applied where the screen would otherwise have to be a third return value.
func DisclosureMark(record string) DisclosureState {
	if err := validRecord(record); err != nil {
		return DisclosureUnknown
	}
	if _, err := os.Stat(disclosurePath(record)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NoDisclosure
		}
		return DisclosureUnknown
	}
	return HasDisclosure
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
//
// removed is false for the kept case and for a removal that failed, and true only when the
// file is gone. Reported because "nil error" covers both of the first two, and one caller
// has to tell them apart: app.withdrawUnrecordedCreates retries a withdrawal until it
// lands, and reading the kept case as done drops the only handle it had — leaving a
// disclosure that says a live session's branch belongs to nothing.
func ClearDisclosure(record string) (removed bool, err error) {
	// Screened for validRecord's reason, and here it is not only about what a bad path
	// could MINT: os.Remove on a name that can never exist returns ENOENT, which the rule
	// below reports as "the withdrawal landed". app.withdrawUnrecordedCreates reads that as
	// done and drops the only handle it had on a disclosure naming a live session's branch,
	// worktree and tmux session. A caller holding both a record path and its claim path a
	// few lines apart (app.applyCreateClaim) is one typo from that, so the guard is here
	// rather than trusted to the call sites.
	if err := validRecord(record); err != nil {
		return false, err
	}
	if recordStillSpooled(record) {
		return false, nil
	}
	if err := os.Remove(disclosurePath(record)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("outbox: remove create disclosure: %w", err)
	}
	return true, nil
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

// discloseClearedRequest records what an `atrium reset` is about to strand by destroying a
// create request, and returns without writing when nothing is owed.
//
// Both arms of Clear write one, and neither conditions it on having something to name. The
// mark is what keeps a spool file whose unlink failed from being executed by the next
// atrium, and Disclose's whole argument is that a guard appearing only when there happens to
// be a branch to name is not one. The claim arm needs it against a re-judged rebuild; the
// record arm needs it because reset's own message to the operator is "discarded", and a
// record that survives its Reject is one the next TUI takes through the gates for a caller
// that has already exited non-zero. Neither is idempotent the way an expiry is (see
// Disclose), so neither can go without.
//
// A record CAN name a branch, and that is the case the record arm exists for beyond the
// mark: an interrupted build that reconcileCreateClaims re-queued to adopt its own orphan is
// back at the record name, with the claim's evidence block still on it, and applyCreateClaim
// released that branch's worktree registration on its way past. Reset it in that window and
// the branch has no row, no claim, no request and no registration — #731's hole 1 with
// reset's name on it.
//
// The branch is the only artifact named because it is the only one a spool file records.
// ClaimMeta carries SessionBranch and BranchExisted; the worktree directory and the tmux
// name were never on the wire, so this cannot name them whatever their fate. It is not a
// claim that they are gone: Clear runs BEFORE tmux.CleanupSessions and git.CleanupWorktrees
// in reset's own sequence, either of which can fail and abort the reset with the directory
// and the agent still standing. What reset does NOT clear even when it completes is git's
// stale registration of that directory — `worktree prune` is repo-scoped through rows reset
// has just deleted — so the reason says so, because `git branch -D` on such a branch fails
// with a path that no longer exists and nothing else would tell the reader why.
//
// Any disclosure already there wins, readable or not. The pair "claim plus disclosure" is a
// refusal whose unlink failed, and that refusal wrote its account with the full inventory in
// hand, so overwriting it with this one's single field would trade a complete account for a
// partial one. One this atrium cannot decode wins for the sharper reason: it may be a newer
// atrium's, and replacing it would be answering "I cannot read this" by destroying it. A
// state that could not be established wins too — the same rule one step out.
func discloseClearedRequest(record string, e CreateEntry) error {
	if DisclosureMark(record) != NoDisclosure {
		return nil
	}
	branch := ""
	if e.Err == nil && e.Request.Claim != nil {
		branch = e.Request.Claim.SessionBranch
	}
	d := Disclosure{Reason: clearReason}
	if e.Err == nil {
		// An undecodable file yields no identity, and the mark is still owed. Nothing else
		// records which request it was either: reset prints a count and logs a count, so an
		// undecodable record is cleared with the file name as the only trace, in the spool
		// directory the operator can go and look at. The mark is written for its guard job
		// alone, and Leftovers() keeps it out of the reader's report.
		d.Title, d.Repo = e.Request.Title, e.Request.Path
	}
	if branch != "" {
		d.Branch = branch
		d.Reason += ", and this one had already created its session branch. If `git branch -D` " +
			"reports that branch checked out at a directory that is no longer there, run " +
			"`git worktree prune` in that repository first"
	}
	return Disclose(record, &d)
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
