package tmux

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnsureWorktreesRootTrusted pre-accepts Claude Code's workspace-trust dialog
// for worktreesRoot by setting projects[worktreesRoot].hasTrustDialogAccepted
// in ~/.claude.json. Claude's trust check walks up parent directories, so
// trusting the root once covers every session worktree beneath it — the
// per-worktree dialog never appears and project-scoped skills/hooks/MCP
// servers load immediately.
//
// There is no sanctioned interface for this: hasTrustDialogAccepted is a
// Claude-internal key, verified in production but undocumented. Degradation is
// graceful — if the schema ever changes, the trust dialog simply reappears and
// the agent-gate detection holds queued prompts as before.
//
// ~/.claude.json is Claude's file, holds OAuth tokens, and is rewritten by
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
func EnsureWorktreesRootTrusted(worktreesRoot string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	return ensureRootTrustedAt(filepath.Join(home, ".claude.json"), worktreesRoot)
}

// EnsureAccountWorktreesRootTrusted pre-accepts the trust dialog for worktreesRoot in
// a specific Claude account's config dir. Sessions routed to an account via
// CLAUDE_CONFIG_DIR (#261/#262) read trust from $CLAUDE_CONFIG_DIR/.claude.json, not
// ~/.claude.json, so the trust_worktrees_root opt-in must be written into each
// account's file too. Same conservative posture as EnsureWorktreesRootTrusted: a
// missing file (account not onboarded) is a silent no-op.
func EnsureAccountWorktreesRootTrusted(configDir, worktreesRoot string) error {
	if configDir == "" {
		return nil // inherit-env account: its trust lives in ~/.claude.json, handled separately
	}
	return ensureRootTrustedAt(filepath.Join(configDir, ".claude.json"), worktreesRoot)
}

// ensureRootTrustedAt sets projects[worktreesRoot].hasTrustDialogAccepted=true in the
// .claude.json at claudeJSONPath, with all the conservative safety documented on
// EnsureWorktreesRootTrusted (symlink-follow, UseNumber, defensive shape checks, 0600
// temp, mode match, stat race-guard, atomic rename).
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
