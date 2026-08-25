// Package repotrust is the per-repo trust ledger (#814): the record of which
// repositories the user has allowed to bring their own executable Atrium
// config, and of exactly which version of it. It exists because a repo-local
// .atrium.json turns `git clone && atrium` into arbitrary code execution by the
// repo's author — setup_script runs `sh -c` in the worktree before the agent
// launches, run_command hosts a long-running process, and session_env reaches
// the agent's environment — so nothing repo-authored may execute until the user
// has said yes to that repo, and said it about the bytes that will run (#629).
//
// The ledger answers #629's five questions, and the answers are load-bearing:
//
//   - WHERE: per repo, in the data dir (<data dir>/repo-trust.json), never in
//     the repo — a file the repo could edit would grant itself trust. Keyed by
//     the canonicalized origin-repo root (CanonicalRoot: absolute, symlinks
//     resolved), NOT by the origin remote: any local clone can claim any
//     remote, so a remote-keyed grant would extend to a malicious clone that
//     names yours. The remote is stored as display metadata only. (The two
//     identities the codebase already has are non-keys, per #629: the repo
//     group key is a bare basename that collides across checkouts, and
//     RecentPaths stores paths unnormalized.) An origin that is itself a
//     linked git worktree keys to its own toplevel, so two such checkouts of
//     one repository trust separately — each can hold different bytes.
//   - WHAT INVALIDATES: the content hash. A record holds one sha256 of the
//     .atrium.json bytes, and a grant is for that hash alone — trusting a repo
//     once must not trust every future edit to its setup script. A new grant
//     replaces the old (one hash per repo, latest wins).
//   - WHEN THE PROMPT HAPPENS: at create time in the TUI, where there is a
//     surface to ask on; never mid-Start (the setup script runs on a
//     background goroutine with no way to pose a question). Paths that cannot
//     ask — `atrium new`'s spool, the autoyes daemon — never prompt: the
//     session starts untrusted and says so, and `atrium trust allow` is the
//     headless grant.
//   - WHAT UNTRUSTED DOES: nothing, visibly. The whole repo-local entry is
//     inert — script, run command and environment together — and resolution
//     falls back to the user's own config.json. The refusal is surfaced, never
//     silent. Enforcement lives at the single resolution funnel in
//     session/setupscript.go, below the TUI, because the daemon reaches it
//     with no UI in the process at all; this package only keeps the records.
//   - PRECEDENCE: a trusted repo-local entry beats the user's global
//     config.json entry for the same repo (the repo knows its own
//     environment); the resolution site records that choice.
//
// The grant is advisory, the check is authoritative: whoever consults the
// ledger must hash the bytes it is about to use, at the moment of use. That is
// what closes the prompt-to-execution TOCTOU — a file that changed after the
// user said yes simply does not match any grant.
//
// Three older meanings of "trust" live in this codebase and this package is
// none of them: session/tmux/trust.go pre-accepts Claude Code's own
// workspace-trust dialog in .claude.json (config.TrustWorktreesRoot is its
// switch), and session/agent detects the folder-trust dialogs agents draw in
// their panes. Those are about what an AGENT trusts; this ledger is about what
// the USER trusts a repository to run.
//
// Unlike config.json/state.json, loading NEVER writes: no create-on-load, no
// temp-file sweep, no corrupt-file quarantine (the config.LoadState behaviors
// are wrong for a security artifact that doctor and the CLI must be able to
// inspect without mutating). A missing ledger is simply empty; an unreadable
// or undecodable one reads as empty too — zero grants, failing closed — with
// the error surfaced to the caller. Writes go through config.WriteFileAtomic
// (unlike internal/update's cache, which records why a torn write does not
// matter there; here it would tear a security record) at 0600: the ledger
// names private repo paths and gates code execution, so it is owner-only.
package repotrust

import (
	"crypto/sha256"
	"encoding/hex"
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
	// currentVersion is the schema version Save stamps and Load accepts.
	currentVersion = 1

	fileName = "repo-trust.json"
)

// ErrFutureVersion marks a ledger written by a newer atrium. It is refused on
// read (its grants cannot be interpreted on a guess — fail closed) and it must
// never be overwritten: Grant and Revoke refuse rather than destroy records a
// newer binary understands and this one does not.
var ErrFutureVersion = errors.New("repo-trust ledger was written by a newer atrium")

// ErrCorrupt marks a ledger that does not decode. It reads as zero grants
// (fail closed). Unlike ErrFutureVersion it does not block writes: the grants
// are already unrecoverable, so the next Grant starts a fresh ledger rather
// than leaving the feature bricked until someone hand-deletes the file.
var ErrCorrupt = errors.New("repo-trust ledger is not decodable")

// Record is one repo's grant: the hash of the .atrium.json content the user
// allowed, when, and the origin remote at that moment (display metadata only —
// never part of the key, see the package doc).
type Record struct {
	Hash      string    `json:"hash"`
	GrantedAt time.Time `json:"granted_at"`
	Remote    string    `json:"remote,omitempty"`
}

// Ledger is the on-disk artifact: every granted repo, keyed by canonical root.
type Ledger struct {
	Version int               `json:"version"`
	Repos   map[string]Record `json:"repos"`
}

// Granted reports whether key's repo is trusted for exactly this content hash.
// Empty inputs are never granted: a caller that failed to derive a key or a
// hash must land on the refusing side.
func (l Ledger) Granted(key, hash string) bool {
	if key == "" || hash == "" {
		return false
	}
	rec, ok := l.Repos[key]
	return ok && rec.Hash == hash
}

// Lookup returns key's record, and whether one exists — for the surfaces that
// distinguish "never granted" from "granted for different content".
func (l Ledger) Lookup(key string) (Record, bool) {
	rec, ok := l.Repos[key]
	return rec, ok
}

// Path returns the ledger file, <data dir>/repo-trust.json. It creates
// nothing. The directory is resolved through config.GetConfigDir so the name
// of the live data dir stays that function's business (a legacy install keeps
// ~/.claude-squad).
func Path() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("repotrust: resolve data dir: %w", err)
	}
	return filepath.Join(dir, fileName), nil
}

// Load reads the ledger. It never writes, whatever it finds.
//
// A missing file is the steady state (empty ledger, nil error). Everything
// else that is not a healthy ledger — unreadable, undecodable, a future
// version — also returns an EMPTY ledger, so every caller that consults grants
// fails closed by construction, plus the error saying why. Callers on a hot
// path use the empty ledger and move on; doctor and the CLI show the error.
func Load() (Ledger, error) {
	path, err := Path()
	if err != nil {
		return emptyLedger(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return emptyLedger(), nil
		}
		return emptyLedger(), fmt.Errorf("repotrust: read ledger: %w", err)
	}
	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return emptyLedger(), fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	if l.Version > currentVersion {
		return emptyLedger(), fmt.Errorf("%w (version %d, this atrium understands %d)", ErrFutureVersion, l.Version, currentVersion)
	}
	if l.Repos == nil {
		l.Repos = map[string]Record{}
	}
	return l, nil
}

func emptyLedger() Ledger {
	return Ledger{Version: currentVersion, Repos: map[string]Record{}}
}

// Grant records that key's repo is trusted for the content hashing to hash,
// replacing any earlier grant for the same repo. remote is display metadata.
//
// The read-modify-write is last-writer-wins across processes (the CLI and the
// TUI can both grant); each write is atomic, so a concurrent grant loses
// cleanly rather than tearing the file.
func Grant(key, hash, remote string, now time.Time) error {
	if key == "" {
		return errors.New("repotrust: a grant needs a repo key")
	}
	if hash == "" {
		return errors.New("repotrust: a grant needs a content hash")
	}
	l, err := loadForWrite()
	if err != nil {
		return err
	}
	l.Repos[key] = Record{Hash: hash, GrantedAt: now, Remote: remote}
	return save(l)
}

// Revoke removes key's grant, reporting whether one existed.
func Revoke(key string) (bool, error) {
	l, err := loadForWrite()
	if err != nil {
		return false, err
	}
	if _, ok := l.Repos[key]; !ok {
		return false, nil
	}
	delete(l.Repos, key)
	return true, save(l)
}

// RevokeAll removes every grant, reporting how many there were.
func RevokeAll() (int, error) {
	l, err := loadForWrite()
	if err != nil {
		return 0, err
	}
	n := len(l.Repos)
	if n == 0 {
		return 0, nil
	}
	return n, save(emptyLedger())
}

// loadForWrite is Load for the mutating verbs. The one difference: a corrupt
// ledger does not block the write (its grants are already unrecoverable, and
// the fresh ledger self-heals), while every other load failure does — a
// future-version file holds records a newer atrium understands, and an
// unreadable one may hold grants this process simply cannot see. Overwriting
// either would destroy state the user did not ask to lose.
func loadForWrite() (Ledger, error) {
	l, err := Load()
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return Ledger{}, err
	}
	return l, nil
}

func save(l Ledger) error {
	l.Version = currentVersion
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("repotrust: create data dir: %w", err)
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("repotrust: encode ledger: %w", err)
	}
	if err := config.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("repotrust: write ledger: %w", err)
	}
	return nil
}

// CanonicalRoot derives the ledger key for a repo root: absolute, symlinks
// resolved, cleaned. Symlink resolution is what makes /home/me/link-to-proj
// and /home/me/proj one repo with one grant — and what stops a symlink flipped
// at a granted path from answering for the repo it now points at, because the
// flipped link resolves to the OTHER repo's key, whose record must still match
// the actual bytes.
//
// When the path cannot be resolved (it no longer exists, say), EvalSymlinks is
// skipped rather than failed: the cleaned absolute path is still a usable key,
// and a key derived from a vanished path matches no grant made while it was
// resolvable — the refusing side, which is the safe one.
//
// The empty path is refused outright: filepath.Clean("") is ".", which would
// silently key some repo to the process's working directory.
func CanonicalRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("repotrust: cannot derive a repo key from an empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("repotrust: absolutize %q: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

// HashBytes is the ledger's content hash: sha256, lowercase hex, of the exact
// bytes given. Callers must hash the bytes they are about to act on — the same
// buffer they parse — never a second read of the file.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
