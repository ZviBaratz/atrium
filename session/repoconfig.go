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
	// the worktree has no file — nothing repo-local runs (there is nothing to
	// run; the user's global config.json still applies via the fallback), and
	// the divergence is worth one word at materialization: the setup this repo
	// was granted for is not on this branch.
	RepoConfigAbsentGranted
	// RepoConfigInvalid means the file is present but unusable — undecodable,
	// not a regular file, over the size cap, more than one entry, or its one
	// entry refused (structurally or by post-trust validation) — inert.
	RepoConfigInvalid
)

// Inert reports whether the state is one where repo config the user might
// expect to apply did not — the states whose report the one-shot modal flags.
// The persistent row line is narrower (ui's repoConfigLine): it carries the
// REFUSALS (untrusted/changed/invalid), while AbsentGranted — a benign
// divergence, common when working a branch that predates the file — gets the
// modal once per materialization and then leaves the row's git line alone.
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

// repoConfigReportSnapshot is the current state's user-facing explanation, or
// "" when there is nothing to explain (None/Active). Unexported on purpose:
// the surfaces read the one-shot RepoConfigProblem, and an exported live
// report with no reader would be dead API.
func (i *Instance) repoConfigReportSnapshot() string {
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
// Only a SUCCESSFUL derivation is memoized. originRemote can cache its empty
// answer because "no remote" is a real state of the repo; here "" is produced
// only by failure (git off PATH mid-upgrade, a fork error, a timeout), and
// pinning one transient failure for the life of the instance would read every
// later resolution as untrusted while `atrium trust status` — deriving the key
// itself, successfully — insists the repo is current. The failing side still
// refuses (repotrust.Ledger.Granted rejects the empty key); it just also
// retries, at the cost of one extra fork per resolution only while broken.
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
	if key != "" {
		i.mu.Lock()
		i.repoKey, i.repoKeyKnown = key, true
		i.mu.Unlock()
	}
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
		// the branch this worktree checked out simply does not carry it. The
		// ledger is only READ once a ledger file exists at all (one Stat), and
		// the key derivation (a memoized git fork) only once it holds records —
		// so a session in a repo using none of this pays one Lstat and one Stat
		// per sweep and forks nothing. A ledger that exists but cannot be read
		// stays None: nothing can execute from an absent file either way, and
		// the failure is reported where it gates (the file-present path below)
		// and by doctor.
		state, report := RepoConfigNone, ""
		if repotrust.Exists() {
			if ledger, err := repotrust.Load(); err == nil && len(ledger.Repos) > 0 {
				if _, has := ledger.Lookup(i.ledgerKey(repoPath)); has {
					state = RepoConfigAbsentGranted
					report = fmt.Sprintf(
						"This worktree has no %s, but the repo has a trusted one — the branch it checked out does not carry the setup you granted, so that setup was not used. Your own config.json still applies as usual.",
						repocfg.RepoLocalFileName)
				}
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

	// Parse BEFORE the trust check — ParseRepoLocal is pure JSON, safe on
	// untrusted bytes (only ValidateOne, below the grant check, touches the
	// template engine) — because what the file DECLARES decides whether trust is
	// even a question. A file that declares nothing usable gates nothing, so an
	// ungranted `{}` or a #815-only shape (carry_files, say) must read as None,
	// not sit as a permanent "untrusted" nag whose named remedies both refuse:
	// `atrium trust allow` has nothing to trust, and re-creating never prompts.
	parsed, parseErr := repocfg.ParseRepoLocal(data)
	if parseErr != nil {
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf("Repo config ignored: %v. Fix the file to use it.", parseErr))
		return repocfg.Script{}, false
	}
	if len(parsed.Entries) == 0 {
		if len(parsed.Problems) == 0 {
			// Declares nothing executable (an empty list, or only keys a future
			// atrium reads): exactly the no-file behavior.
			i.setRepoConfig(RepoConfigNone, "")
			return repocfg.Script{}, false
		}
		report := fmt.Sprintf("Repo config ignored: the entry in %s's %s is not usable. Fix the file to use it.", repoPath, repocfg.RepoLocalFileName)
		for _, p := range parsed.Problems {
			report += fmt.Sprintf("\n  %s: %s", repocfg.RepoLocalFileName, p.Error())
		}
		i.setRepoConfig(RepoConfigInvalid, report)
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

	// Exactly one entry can exist here (ParseRepoLocal's one-entry rule), and it
	// is the entry the trust prompt described — so a late validation failure
	// refuses the file rather than stepping over the entry, which is what keeps
	// "the script the user was shown" and "the script that runs" the same script.
	// The index handed to ValidateOne is the entry's position in the FILE, so
	// `repo_scripts[N]` in the message is the N the user can find.
	entry := parsed.Entries[0]
	script, problem := repocfg.ValidateOne(entry.Index, entry.RepoScript)
	if problem != nil {
		log.WarningLog.Printf("repo-local config for %q: %s: %s", i.Title(), repocfg.RepoLocalFileName, problem.Error())
		i.setRepoConfig(RepoConfigInvalid, fmt.Sprintf(
			"Repo config ignored: the trusted entry in %s's %s does not validate, so nothing from it ran.\n  %s: %s\nFix the file to use it (the fix re-prompts — its content will have changed).",
			repoPath, repocfg.RepoLocalFileName, repocfg.RepoLocalFileName, problem.Error()))
		return repocfg.Script{}, false
	}
	i.setRepoConfig(RepoConfigActive, "")
	return script, true
}
