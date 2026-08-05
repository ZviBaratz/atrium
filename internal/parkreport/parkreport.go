// Package parkreport carries a startup recovery's deferral report from the process
// that made it to the next TUI, which is the only one with a surface to show it on.
//
// Loading the fleet relaunches the agent of any session whose tmux session is gone,
// so it is rationed by the host session budget, and the sessions it cannot afford are
// parked as Paused (session.DeferredRecovery, #474). The autoyes daemon reaches that
// identical code with no UI at all, so it had nowhere to put the report and discarded
// it — while persisting the parks, which land in state.json as ordinary Paused rows.
// The next TUI then reattaches them without probing anything, so its own report comes
// back empty and the park has erased its own explanation: a fleet-wide silent
// Running→Paused, the state #270 ruled a defect (#622).
//
// The report cannot ride in state.json for the reason internal/outbox exists: that
// file has exactly one writer at any instant, and the daemon's exit save replays a
// snapshot taken at its own startup (#230). So this is a spool beside that one, and it
// deliberately does not re-derive what outbox already argued — commit-by-rename via
// config.WriteFileAtomic, a TTL horizon, drain-and-delete, and discarding a file
// nobody can decode. What it is NOT is a second outbox: an outbox message is an
// addressed prompt whose text gets typed into a session, while this is one fleet-level
// report with no target and nothing to inject.
//
// It is a single latest-wins file rather than a queue because at most one undrained
// report can exist. The daemon loads once for its whole lifetime, and the TUI stops
// any daemon before it starts and launches one only on exit (main.go), so two reports
// never overlap; if a second one is ever written it describes the newer state and
// should win.
package parkreport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
)

const (
	// TTL is how long an undelivered report stays worth showing. The horizon and its
	// argument are outbox.TTL's, not a fresh judgement: a report spooled a day ago
	// describes a fleet the user has since resumed, killed or rearranged, so
	// delivering it is worse than dropping it. Reconciliation (the caller's job)
	// catches the same staleness earlier and more precisely; this is the backstop for
	// a file that outlives every process that could have drained it.
	TTL = 24 * time.Hour

	// currentVersion is the schema version Write stamps and Read accepts. A report
	// carrying anything else came from a different atrium and is discarded rather
	// than decoded on a guess.
	currentVersion = 1

	fileName = "deferred-recovery.json"
)

// Session identifies one parked session on the wire: the (Title, Path) pair, which is
// what the storage layer matches instances on. The title alone is not enough — titles
// are unique only within a repo group, so a same-titled session in another repo would
// otherwise be able to answer for this one.
type Session struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// Report is one spooled deferral report: the sessions a load parked for want of host
// budget, and the cap it measured them against.
//
// It is this package's own wire type rather than session.DeferredRecovery, for the
// reason outbox.Message is its own: a persisted schema and an in-memory type change
// under different constraints, and a struct that is both ends up unable to move
// either way. Both ends convert.
type Report struct {
	Version   int       `json:"version"`
	Sessions  []Session `json:"sessions"`
	Limit     int       `json:"limit"`
	CreatedAt time.Time `json:"created_at"`
}

// Expired reports whether r is past the TTL horizon as of now. A report with no
// timestamp is never expired: treating a zero time as infinitely old would silently
// discard anything written by a future version that omits the field.
func (r Report) Expired(now time.Time) bool {
	if r.CreatedAt.IsZero() {
		return false
	}
	return now.Sub(r.CreatedAt) > TTL
}

// Path returns the spool file, <data dir>/deferred-recovery.json. It creates nothing.
// The directory is resolved through config.GetConfigDir so the name of the live data
// dir stays that function's business (a legacy install keeps ~/.claude-squad).
func Path() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("parkreport: resolve data dir: %w", err)
	}
	return filepath.Join(dir, fileName), nil
}

// Write commits r to the spool, stamping Version and — unless the caller supplied one
// — CreatedAt. An existing report is replaced: see the package doc on why the newer
// one wins.
//
// The write is committed by rename (config.WriteFileAtomic), so a reader sees a
// complete file or none, never a torn one, without either side taking a lock.
func Write(r Report) error {
	if len(r.Sessions) == 0 {
		return errors.New("parkreport: a report needs at least one parked session")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	r.Version = currentVersion

	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("parkreport: create data dir: %w", err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("parkreport: encode report: %w", err)
	}
	if err := config.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("parkreport: write report: %w", err)
	}
	return nil
}

// Read returns the spooled report. ok is false when there is nothing to deliver: no
// file (the steady state for anyone whose fleet always fit), or a file that cannot be
// used — unreadable, undecodable, from a different atrium, or past the TTL horizon.
//
// An unusable file is REMOVED here, because there is no other party to discard it and
// one nobody deletes would be re-read on every launch forever (outbox's argument for
// handing its own undecodable files to the caller). A usable one is deliberately left
// on disk: this package's whole reason for existing is that the park erased its own
// explanation, so the report must survive until it has actually been shown. The caller
// that delivers it calls Remove.
func Read(now time.Time) (Report, bool) {
	path, err := Path()
	if err != nil {
		return Report{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// An absent file is the steady state for anyone whose fleet always fit, so it
		// is silent. Present but unreadable — a permissions problem, a directory in
		// its place — is logged and left alone: unlinking would not fix either, and
		// this is a diagnostic the user has no other copy of.
		if !errors.Is(err, fs.ErrNotExist) {
			log.WarningLog.Printf("could not read the deferred-recovery report at %s: %v", path, err)
		}
		return Report{}, false
	}

	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return discard(path, fmt.Sprintf("report is not decodable: %v", err))
	}
	switch {
	case r.Version != currentVersion:
		return discard(path, fmt.Sprintf("report has version %d, this atrium understands %d", r.Version, currentVersion))
	case len(r.Sessions) == 0:
		return discard(path, "report names no parked session")
	case r.Expired(now):
		return discard(path, fmt.Sprintf("report was spooled %s ago, past the %s horizon",
			now.Sub(r.CreatedAt).Round(time.Minute), TTL))
	}
	return r, true
}

// discard drops an unusable report, so Read's callers get one (zero, false) answer for
// every shape of "nothing to deliver".
//
// The reason is logged rather than returned. A caller could not act on it — every
// branch that reaches here has already lost the report — while the log is the only
// place a dropped explanation leaves a trace at all, which is the failure this whole
// package exists to stop repeating.
func discard(path, reason string) (Report, bool) {
	log.WarningLog.Printf("discarding the deferred-recovery report: %s", reason)
	if err := remove(path); err != nil {
		log.WarningLog.Printf("could not remove the discarded report: %v", err)
	}
	return Report{}, false
}

// Remove discards a delivered report. A file that is already gone is not an error.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return remove(path)
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("parkreport: remove report: %w", err)
	}
	return nil
}
