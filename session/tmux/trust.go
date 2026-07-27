package tmux

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnsureWorktreesRootTrustedIn pre-accepts Claude Code's workspace-trust dialog
// for worktreesRoot by setting projects[worktreesRoot].hasTrustDialogAccepted in
// configDir/.claude.json. Claude's trust check walks up parent directories, so
// trusting the root once covers every session worktree beneath it — the
// per-worktree dialog never appears and project-scoped skills/hooks/MCP servers
// load immediately.
//
// configDir is the config dir the session's claude actually reads: the ambient one
// (config.AmbientClaudeConfigDir — $CLAUDE_CONFIG_DIR if set, else home) for an
// unrouted session, or a routed account's own dir (#261/#262). It must be
// absolute. An empty configDir is a silent no-op (nothing is configured, and
// there is no path to write); a relative one is refused with an error, because
// that is a misconfiguration worth reporting rather than a missing value. Either
// way this function never guesses at a path — see the guard below.
//
// There is no sanctioned interface for this: hasTrustDialogAccepted is a
// Claude-internal key, verified in production but undocumented. Degradation is
// graceful — if the schema ever changes, the trust dialog simply reappears and
// the agent-gate detection holds queued prompts as before.
//
// #488 re-probed the parent-walk premise above rather than trusting it, because
// an observation on one machine suggested it was gone: across 35 `projects` keys
// and 458 lifetime sessions, not one was a worktree path. It is NOT gone. Driving
// a real claude in a worktree under a never-trusted root, with a fresh config dir
// (isolated HOME, one tmux socket per cell), on claude 2.0.77, 2.1.170 and
// 2.1.220:
//
//   - with nothing trusted, the dialog appears — on all three;
//   - with only the worktrees root trusted, it does not — on all three. This
//     function still does exactly what it claims.
//
// What DID change, at some point between 2.0.77 and 2.1.170, is the key claude
// RECORDS a session under: 2.0.77 wrote the worktree's own path (cwd), while
// 2.1.x writes the enclosing repo — the git common dir's checkout — so a worktree
// path never appears as a `projects` key again. That is why the evidence above
// looked conclusive and was not: the recording key and the trust check are
// different mechanisms, and "cwd never appears as a key" does not mean "trust is
// no longer keyed by cwd". The 2.1.x change does mean a worktree of an
// already-trusted repo is not prompted (it was, at 2.0.77) — so what this buys on
// a current claude is the first session on a repo claude has not seen before.
//
// configDir/.claude.json is Claude's file, holds OAuth tokens, and is rewritten by
// every live claude process, so this function is deliberately conservative:
//
//   - missing, malformed, or unexpectedly-shaped files are left untouched
//     (nil return — absence just means claude is not onboarded);
//   - unknown fields and large integers survive byte-exact via
//     map[string]any + json.Number decoding;
//   - a dotfile-managed symlink is followed, never replaced by a regular file;
//   - the temp file is born 0600 (token material is never world-readable),
//     then matched to the original mode, and renamed atomically;
//   - a stat re-check aborts the rename if another writer (a live claude
//     session) touched the file mid-rewrite — losing this write is fine, it
//     self-heals on the next startup; clobbering claude's write is not.
//
// No flock: concurrent writers of this key write the same value (last-wins is
// correct), claude itself takes no lock anyway, and the stat guard covers the
// realistic race without a unix-only dependency.
func EnsureWorktreesRootTrustedIn(configDir, worktreesRoot string) error {
	if configDir == "" {
		return nil // unresolvable: nothing to write, and never a guessed path
	}
	// A relative dir would have filepath.Join resolve it against ATRIUM's working
	// directory and rewrite a real .claude.json there, while the claude that reads
	// it resolves the same relative path against its own session worktree — so the
	// file written is never the file read. This mirrors the read side, which
	// refuses one for the same reason (internal/doctor's fileGateReader): the cwd
	// claude would have resolved it against is genuinely unknown to us.
	if !filepath.IsAbs(configDir) {
		return fmt.Errorf("claude config dir must be absolute, got %q", configDir)
	}
	return ensureRootTrustedAt(filepath.Join(configDir, ".claude.json"), worktreesRoot)
}

// ensureRootTrustedAt sets projects[worktreesRoot].hasTrustDialogAccepted=true in the
// .claude.json at claudeJSONPath, with all the conservative safety documented on
// EnsureWorktreesRootTrustedIn (symlink-follow, UseNumber, defensive shape checks,
// 0600 temp, mode match, stat race-guard, atomic rename).
func ensureRootTrustedAt(claudeJSONPath, worktreesRoot string) error {
	// Follow a dotfile-manager symlink to the real file: the temp+rename below
	// must replace the target, not the link.
	path, err := filepath.EvalSymlinks(claudeJSONPath)
	if err != nil {
		return nil // no .claude.json here: claude not onboarded; never create it
	}

	before, err := os.Stat(path)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// UseNumber keeps integers as their original literals — claude stores
	// timestamps past float64's 53-bit integer precision, and a re-encode
	// through float64 would silently corrupt them.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil // malformed: claude's file, claude's problem — never touch it
	}

	// Navigate/extend the projects map defensively: a damaged file can carry
	// null or non-object values anywhere, and we must bail rather than panic
	// or "repair" structure claude may rely on.
	var projects map[string]any
	switch v := root["projects"].(type) {
	case nil:
		if _, present := root["projects"]; present {
			return nil // explicit null: unexpected shape, leave alone
		}
		projects = map[string]any{}
		root["projects"] = projects
	case map[string]any:
		projects = v
	default:
		return nil
	}

	var entry map[string]any
	switch v := projects[worktreesRoot].(type) {
	case nil:
		entry = map[string]any{}
		projects[worktreesRoot] = entry
	case map[string]any:
		entry = v
	default:
		return nil
	}

	if accepted, _ := entry["hasTrustDialogAccepted"].(bool); accepted {
		return nil // already trusted: no write at all
	}
	entry["hasTrustDialogAccepted"] = true

	// Encode through an Encoder rather than MarshalIndent: SetEscapeHTML(false)
	// keeps <, >, & literal (project history entries routinely contain them, and
	// rewriting each one as a \uXXXX escape would make the file needlessly
	// diff-noisy against claude's own rewrites), and Encode's trailing newline
	// matches how claude itself writes the file.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	out := buf.Bytes()

	// CreateTemp creates the file 0600, so token material is never readable by
	// other users even transiently; match the original's mode before the swap.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.atrium-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), before.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	// Abort if anything rewrote the file since we read it (live claude
	// sessions survive Atrium restarts and rewrite ~/.claude.json freely).
	// Skipping is harmless — the write self-heals on the next startup — but
	// renaming over a concurrent update would clobber claude's own state.
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		return fmt.Errorf("%s changed during rewrite; leaving it to the concurrent writer", path)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
