package tmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trustHome sandboxes HOME for one test and returns it plus the resulting
// ~/.claude.json path. EnsureWorktreesRootTrusted writes to the user-level
// claude config, so these tests must never see the real one.
func trustHome(t *testing.T) (home, claudeJSON string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, filepath.Join(home, ".claude.json")
}

// claudeFixture is a realistic ~/.claude.json shape: top-level unknown fields
// (one carrying an integer too large for float64 — re-encoding through
// float64 would corrupt it), an unrelated trusted project whose history holds
// HTML-special characters (a default JSON re-encode would escape them), and
// OAuth-ish material that must survive byte-for-byte semantically.
const claudeFixture = `{
  "firstStartTime": 1736159218941234567,
  "oauthAccount": {"accountUuid": "abc-123", "emailAddress": "user@example.com"},
  "projects": {
    "/home/user/other": {
      "hasTrustDialogAccepted": true,
      "allowedTools": ["Bash"],
      "history": ["fix <div> & run a->b"]
    }
  },
  "customSentinel": {"nested": ["keep", "me"]}
}`

func writeClaudeJSON(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// trustAccepted digs projects[root].hasTrustDialogAccepted out of a decoded
// claude.json map.
func trustAccepted(t *testing.T, m map[string]any, root string) bool {
	t.Helper()
	projects, ok := m["projects"].(map[string]any)
	if !ok {
		return false
	}
	entry, ok := projects[root].(map[string]any)
	if !ok {
		return false
	}
	accepted, _ := entry["hasTrustDialogAccepted"].(bool)
	return accepted
}

func TestEnsureWorktreesRootTrusted_SetsTrustAndPreservesContent(t *testing.T) {
	_, claudeJSON := trustHome(t)
	writeClaudeJSON(t, claudeJSON, claudeFixture, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrusted: %v", err)
	}

	m := readJSONMap(t, claudeJSON)
	if !trustAccepted(t, m, root) {
		t.Fatal("worktrees root not trusted after call")
	}
	// The unrelated project survives untouched.
	if !trustAccepted(t, m, "/home/user/other") {
		t.Fatal("pre-existing project entry was lost or modified")
	}
	// Unknown fields survive.
	if _, ok := m["customSentinel"]; !ok {
		t.Fatal("unknown top-level field was dropped on rewrite")
	}
	if _, ok := m["oauthAccount"]; !ok {
		t.Fatal("oauthAccount was dropped on rewrite")
	}
	// The large integer survives digit-for-digit (float64 would mangle it).
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "1736159218941234567") {
		t.Fatalf("large integer corrupted on rewrite; got: %s", data)
	}
	// HTML-special characters stay literal (SetEscapeHTML(false)) — escaping
	// them would make the file diff-noisy against claude's own rewrites.
	if !strings.Contains(string(data), `"fix <div> & run a->b"`) {
		t.Fatalf("HTML-special characters were escaped on rewrite; got: %s", data)
	}
	// Trailing newline matches how claude itself writes the file.
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("rewrite dropped the trailing newline")
	}
	// File mode preserved (claude.json carries OAuth tokens).
	info, err := os.Stat(claudeJSON)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestEnsureWorktreesRootTrusted_ExistingEntryKeysPreserved(t *testing.T) {
	_, claudeJSON := trustHome(t)
	root := "/home/user/.atrium/worktrees"
	writeClaudeJSON(t, claudeJSON,
		`{"projects": {"`+root+`": {"hasTrustDialogAccepted": false, "allowedTools": ["Edit"]}}}`, 0600)

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrusted: %v", err)
	}

	m := readJSONMap(t, claudeJSON)
	if !trustAccepted(t, m, root) {
		t.Fatal("existing entry's hasTrustDialogAccepted not flipped to true")
	}
	entry := m["projects"].(map[string]any)[root].(map[string]any)
	if _, ok := entry["allowedTools"]; !ok {
		t.Fatal("sibling key in the project entry was dropped")
	}
}

func TestEnsureWorktreesRootTrusted_AlreadyTrustedDoesNotRewrite(t *testing.T) {
	_, claudeJSON := trustHome(t)
	root := "/home/user/.atrium/worktrees"
	content := `{"projects": {"` + root + `": {"hasTrustDialogAccepted": true}}}`
	writeClaudeJSON(t, claudeJSON, content, 0600)
	// Backdate so an accidental rewrite is observable via mtime.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(claudeJSON, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrusted: %v", err)
	}

	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != content {
		t.Fatal("already-trusted file was rewritten")
	}
	info, err := os.Stat(claudeJSON)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatal("already-trusted file was touched (mtime changed)")
	}
}

func TestEnsureWorktreesRootTrusted_MissingFileIsNoop(t *testing.T) {
	_, claudeJSON := trustHome(t)

	if err := EnsureWorktreesRootTrusted("/anywhere/worktrees"); err != nil {
		t.Fatalf("missing ~/.claude.json must be a silent no-op, got: %v", err)
	}
	if _, err := os.Stat(claudeJSON); !os.IsNotExist(err) {
		t.Fatal("must never create ~/.claude.json (absence means claude is not onboarded)")
	}
}

func TestEnsureWorktreesRootTrusted_MalformedLeftUntouched(t *testing.T) {
	_, claudeJSON := trustHome(t)
	writeClaudeJSON(t, claudeJSON, `{"projects": {broken`, 0600)

	if err := EnsureWorktreesRootTrusted("/anywhere/worktrees"); err != nil {
		t.Fatalf("malformed ~/.claude.json must be a silent no-op, got: %v", err)
	}
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != `{"projects": {broken` {
		t.Fatal("malformed file was modified")
	}
}

func TestEnsureWorktreesRootTrusted_UnexpectedShapesLeftUntouched(t *testing.T) {
	for name, content := range map[string]string{
		"projects null":    `{"projects": null}`,
		"projects array":   `{"projects": [1, 2]}`,
		"entry non-object": `{"projects": {"/anywhere/worktrees": "weird"}}`,
		"top-level array":  `[1, 2]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, claudeJSON := trustHome(t)
			writeClaudeJSON(t, claudeJSON, content, 0600)

			if err := EnsureWorktreesRootTrusted("/anywhere/worktrees"); err != nil {
				t.Fatalf("unexpected shape must be a silent no-op, got: %v", err)
			}
			data, err := os.ReadFile(claudeJSON)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(data) != content {
				t.Fatalf("file with unexpected shape was modified: %s", data)
			}
		})
	}
}

func TestEnsureWorktreesRootTrusted_MissingProjectsKeyCreatesIt(t *testing.T) {
	_, claudeJSON := trustHome(t)
	writeClaudeJSON(t, claudeJSON, `{"firstStartTime": 123}`, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrusted: %v", err)
	}

	m := readJSONMap(t, claudeJSON)
	if !trustAccepted(t, m, root) {
		t.Fatal("projects key not created for a config without one")
	}
	if _, ok := m["firstStartTime"]; !ok {
		t.Fatal("existing top-level field dropped")
	}
}

func TestEnsureWorktreesRootTrusted_PreservesSymlink(t *testing.T) {
	home, claudeJSON := trustHome(t)
	// Dotfile-manager layout: ~/.claude.json is a symlink to a managed target.
	target := filepath.Join(home, "dotfiles", "claude.json")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatalf("mkdir dotfiles: %v", err)
	}
	writeClaudeJSON(t, target, `{"projects": {}}`, 0600)
	if err := os.Symlink(target, claudeJSON); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrusted: %v", err)
	}

	// The symlink must survive (not be replaced by a regular file) ...
	if fi, err := os.Lstat(claudeJSON); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("~/.claude.json is no longer a symlink (err=%v)", err)
	}
	// ... and the managed target carries the update.
	if !trustAccepted(t, readJSONMap(t, target), root) {
		t.Fatal("symlink target not updated")
	}
}

func TestEnsureAccountWorktreesRootTrusted_WritesAccountConfigDir(t *testing.T) {
	// An account-routed session reads trust from $CLAUDE_CONFIG_DIR/.claude.json,
	// not ~/.claude.json, so the per-account entrypoint must write there.
	configDir := t.TempDir()
	accountJSON := filepath.Join(configDir, ".claude.json")
	writeClaudeJSON(t, accountJSON, claudeFixture, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureAccountWorktreesRootTrusted(configDir, root); err != nil {
		t.Fatalf("EnsureAccountWorktreesRootTrusted: %v", err)
	}

	m := readJSONMap(t, accountJSON)
	if !trustAccepted(t, m, root) {
		t.Fatal("worktrees root not trusted in the account config dir")
	}
	// The account's own OAuth material and unknown fields survive (each account
	// dir carries a distinct oauthAccount).
	if _, ok := m["oauthAccount"]; !ok {
		t.Fatal("account oauthAccount was dropped on rewrite")
	}
	data, err := os.ReadFile(accountJSON)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "1736159218941234567") {
		t.Fatalf("large integer corrupted on rewrite; got: %s", data)
	}
}

func TestEnsureAccountWorktreesRootTrusted_MissingFileIsNoop(t *testing.T) {
	configDir := t.TempDir() // exists, but no .claude.json inside
	if err := EnsureAccountWorktreesRootTrusted(configDir, "/anywhere/worktrees"); err != nil {
		t.Fatalf("account dir without .claude.json must be a silent no-op, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("must never create the account .claude.json (absence means the account is not onboarded)")
	}
}

func TestEnsureAccountWorktreesRootTrusted_EmptyConfigDirIsNoop(t *testing.T) {
	// The inherit-env account has no config dir; its trust lives in ~/.claude.json,
	// written separately, so the account entrypoint must do nothing.
	if err := EnsureAccountWorktreesRootTrusted("", "/anywhere/worktrees"); err != nil {
		t.Fatalf("empty config dir must be a silent no-op, got: %v", err)
	}
}

func TestEnsureWorktreesRootTrusted_SecondCallIsNoop(t *testing.T) {
	_, claudeJSON := trustHome(t)
	writeClaudeJSON(t, claudeJSON, claudeFixture, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("first call: %v", err)
	}
	after, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read after first call: %v", err)
	}

	if err := EnsureWorktreesRootTrusted(root); err != nil {
		t.Fatalf("second call: %v", err)
	}
	again, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read after second call: %v", err)
	}
	if string(after) != string(again) {
		t.Fatal("second call rewrote an already-trusted file")
	}
}
