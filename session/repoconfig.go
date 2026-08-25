package session

// repoconfig.go — repo-local config enforcement (#814): the one place
// repo-authored .atrium.json bytes are allowed to become a repocfg.Script,
// gated by the per-repo trust ledger.
//
// This file sits at routeRepoScript's front door on purpose. That function is
// the single funnel from configuration to everything that executes or leaks —
// setup_script, run_command, session_env — and it is reached from paths that
// have no UI in the process at all (the autoyes daemon relaunches agents
// through startResuming → applySessionEnv), so the gate cannot live in app/.
// The TUI's create-time prompt is advisory: it records a grant. THIS check,
// against the bytes in the session's own worktree at the moment of use, is the
// authority — which is also what closes the prompt-to-execution TOCTOU, and
// what keeps a paused session inert when an agent edits .atrium.json on its
// branch before a resume re-materializes it.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
)

// RepoConfigState is where a session stands with its repository's own
// .atrium.json. The zero value means "not evaluated yet" — a session that has
// never resolved (a direct session, or one restored before its first sweep)
// — and no reader may treat it as a verdict; every decision about whether
// repo-local content RUNS comes from the ledger check below, never from this
// display state.
type RepoConfigState int

const (
	// RepoConfigUnset is the zero value: no resolution has run for this
	// session yet. Renderers show nothing for it, exactly as for None.
	RepoConfigUnset RepoConfigState = iota
	// RepoConfigNone means the worktree carries no .atrium.json (and no grant
	// expects one).
	RepoConfigNone
	// RepoConfigActive means the file is trusted for exactly these bytes and
	// its entry governs this session.
	RepoConfigActive
	// RepoConfigUntrusted means the file is present and was never granted —
	// inert.
	RepoConfigUntrusted
	// RepoConfigChanged means the file is present but differs from the granted
	// content — inert until re-granted.
	RepoConfigChanged
	// RepoConfigAbsentGranted means the ledger holds a grant for this repo but
	// the worktree has no file — nothing runs (there is nothing to run), and
	// the divergence is worth a word: the setup this repo was granted for is
	// not on this branch.
	RepoConfigAbsentGranted
	// RepoConfigInvalid means the file is present but unusable — undecodable,
	// not a regular file, over the size cap, or every entry refused — inert.
	RepoConfigInvalid
)

// Inert reports whether the state describes present-but-withheld repo config —
// the states a session surface should flag.
func (s RepoConfigState) Inert() bool {
	switch s {
	case RepoConfigUntrusted, RepoConfigChanged, RepoConfigAbsentGranted, RepoConfigInvalid:
		return true
	}
	return false
}

// RepoConfigStatus is the session's current repo-local config state, for the
// row and preview. Refreshed by every resolution (create, resume, the
// run-state sweep), so it tracks the live worktree rather than the moment of
// creation.
func (i *Instance) RepoConfigStatus() RepoConfigState {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.repoConfigState
}

// RepoConfigReport is the current state's user-facing explanation, or "" when
// there is nothing to explain (None/Active).
func (i *Instance) RepoConfigReport() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.repoConfigReport
}

// RepoConfigProblem is the one-shot report held for the app's flush-once
// modal, or "". It is armed only on the applying paths (resolveSetupRun: a
// worktree materialization or an environment apply), never by the read-only
// sweep — the sweep refreshing it would reopen the modal forever after the
// flush clears it (the same contract PortProblem keeps).
func (i *Instance) RepoConfigProblem() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.repoConfigProblem
}

// ClearRepoConfigProblem drops the one-shot report once it has been shown.
func (i *Instance) ClearRepoConfigProblem() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.repoConfigProblem = ""
}

// armRepoConfigProblem copies the current report into the one-shot slot. Called
// from the applying path right after routing, so the modal fires when inert
// config mattered (a script would have run, an environment would have applied)
// and not merely because a poll looked.
func (i *Instance) armRepoConfigProblem() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.repoConfigState.Inert() && i.repoConfigReport != "" {
		i.repoConfigProblem = i.repoConfigReport
	}
}

// setRepoConfig publishes the state and its explanation together.
func (i *Instance) setRepoConfig(state RepoConfigState, report string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.repoConfigState, i.repoConfigReport = state, report
}

// ledgerKey is the repo's canonical trust identity, derived once per instance
// and then remembered — the same shape, and the same argument, as
// originRemote: the repo path is fixed at creation, and the derivation is the
// one git fork in the trust check. The VERDICT is never memoized with it; the
// ledger and the file are re-read at every resolution so a grant, a
// revocation, or an edit reaches a running session on the next poll.
//
// The empty answer is memoized too: a repo whose root cannot be resolved keys
// as "", which repotrust.Ledger.Granted refuses — the failing side is the
// refusing side.
func (i *Instance) ledgerKey(repoPath string) string {
	i.mu.RLock()
	key, known := i.repoKey, i.repoKeyKnown
	i.mu.RUnlock()
	if known {
		return key
	}
	key = ""
	if root, err := git.RepoRoot(i.baseContext(), repoPath); err == nil {
		if k, err := repotrust.CanonicalRoot(root); err == nil {
			key = k
		}
	}
	i.mu.Lock()
	i.repoKey, i.repoKeyKnown = key, true
	i.mu.Unlock()
	return key
}

// routeRepoLocal resolves the worktree's own .atrium.json, if the trust ledger
// grants exactly its current bytes. ok is false for every other condition —
// absent, untrusted, changed, unusable — with the session's RepoConfigStatus
// updated to say which; the caller then falls back to the user's global
// config.json, so untrusted repo config degrades to exactly the behavior the
// repo would get with no file at all.
//
// Trusted repo-local config WINS over a global entry that also matches this
// repo (the repo knows its own environment; gh-dash's precedence, adopted by
// #814/#815) — which is why this runs before, and independently of, the
// global list: a fresh install with an empty config.json still honors a
// trusted repo.
//
// The hash is of the same buffer that is parsed — never a second read — so
// whatever bytes the one bounded os.ReadFile returned are the bytes judged and
// the bytes that run. The Lstat shape-check ahead of it is best-effort, not
// atomic: an entry swapped between the two calls is still read once and hashed,
// so a raced swap can only land on the refusing side or on content that was
// granted anyway — never on unjudged bytes. Direct sessions never reach here
// (their caller gates on IsDirect): they run in the user's own checkout,
// which no worktree materializes, and are out of #814's scope by decision.
func (i *Instance) routeRepoLocal(dir, repoPath string) (repocfg.Script, bool) {
	path := filepath.Join(dir, repocfg.RepoLocalFileName)

	// Lstat, not Stat: Stat follows symlinks and reports the TARGET regular, so a
	// committed `.atrium.json -> anywhere` would pass the shape check below and
	// be read through — the exact laundering the check exists to refuse.
	fi, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf("repo config ignored: %s is unreadable (%v).", path, err))
			return repocfg.Script{}, false
		}
		// Absent. Worth a word only when a grant says this repo HAS setup: then
		// the branch this worktree checked out simply does not carry it. The key
		// derivation (a memoized git fork) is skipped while the ledger holds no
		// records at all, so a session in a repo using none of this pays one
		// stat and one ENOENT per sweep and forks nothing.
		state, report := RepoConfigNone, ""
		if ledger, _ := repotrust.Load(); len(ledger.Repos) > 0 {
			if _, has := ledger.Lookup(i.ledgerKey(repoPath)); has {
				state = RepoConfigAbsentGranted
				report = fmt.Sprintf(
					"This worktree has no %s, but the repo has a trusted one — the branch it checked out does not carry the setup you granted. Nothing was run.",
					repocfg.RepoLocalFileName)
			}
		}
		i.setRepoConfig(state, report)
		return repocfg.Script{}, false
	}
	if !fi.Mode().IsRegular() {
		// A fifo would block the read; a device file could feed it forever. A repo
		// can commit .atrium.json as a symlink, and following one ends somewhere
		// the grant's hash was never about — refusing the shape outright is
		// simpler to reason about than hashing whatever it resolves to today.
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf("repo config ignored: %s is not a regular file.", path))
		return repocfg.Script{}, false
	}
	if fi.Size() > repocfg.MaxRepoLocalBytes {
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf(
			"repo config ignored: %s is %d bytes, over the %d-byte cap.", path, fi.Size(), repocfg.MaxRepoLocalBytes))
		return repocfg.Script{}, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf("repo config ignored: %s could not be read (%v).", path, err))
		return repocfg.Script{}, false
	}
	if int64(len(data)) > repocfg.MaxRepoLocalBytes {
		// The file grew between Stat and ReadFile; refuse rather than work with
		// bytes the size gate never saw.
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf("repo config ignored: %s is over the %d-byte cap.", path, repocfg.MaxRepoLocalBytes))
		return repocfg.Script{}, false
	}

	hash := repotrust.HashBytes(data)
	key := i.ledgerKey(repoPath)
	ledger, ledgerErr := repotrust.Load()
	if !ledger.Granted(key, hash) {
		state, report := RepoConfigUntrusted, fmt.Sprintf(
			"Repo config ignored: %s carries a %s that is not trusted, so its setup was skipped and the session runs on your own config only.\n\nTo allow it: atrium trust allow %s — or re-create the session to be asked.",
			repoPath, repocfg.RepoLocalFileName, repoPath)
		if _, has := ledger.Lookup(key); has {
			state = RepoConfigChanged
			report = fmt.Sprintf(
				"Repo config ignored: %s's %s has CHANGED since you trusted it, so its setup was skipped and the session runs on your own config only.\n\nTo allow the new version: atrium trust allow %s — or re-create the session to be asked.",
				repoPath, repocfg.RepoLocalFileName, repoPath)
		}
		if ledgerErr != nil {
			// Zero grants while the ledger is unreadable: fail closed, and say why
			// the repo reads as untrusted even if the user remembers granting it.
			report += fmt.Sprintf("\n\n(The trust ledger could not be read: %v — atrium doctor has details.)", ledgerErr)
		}
		i.setRepoConfig(state, report)
		return repocfg.Script{}, false
	}

	parsed, err := repocfg.ParseRepoLocal(data)
	if err != nil {
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf("repo config ignored: %v.", err))
		return repocfg.Script{}, false
	}
	// First valid entry wins, same as global routing's first-match rule; a broken
	// sibling is logged (the same loudness routeRepoScript gives a refused global
	// entry) and stepped over. The index handed to ValidateOne is the entry's
	// position in the FILE, not in the filtered slice, so `repo_scripts[N]` in
	// the message is the N the user can find.
	for _, entry := range parsed.Entries {
		script, problem := repocfg.ValidateOne(entry.Index, entry.RepoScript)
		if problem == nil {
			i.setRepoConfig(RepoConfigActive, "")
			return script, true
		}
		log.WarningLog.Printf("repo-local config for %q: %s: %s", i.Title(), repocfg.RepoLocalFileName, problem.Error())
	}
	if len(parsed.Entries) == 0 && len(parsed.Problems) == 0 {
		// A file that declares nothing executable gates nothing.
		i.setRepoConfig(RepoConfigNone, "")
		return repocfg.Script{}, false
	}
	report := fmt.Sprintf("Repo config ignored: no entry in %s's %s is usable.", repoPath, repocfg.RepoLocalFileName)
	for _, p := range parsed.Problems {
		report += fmt.Sprintf("\n  %s: %s", repocfg.RepoLocalFileName, p.Error())
	}
	i.setRepoConfig(RepoConfigInvalid, report)
	return repocfg.Script{}, false
}
