package tmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trustDir returns a throwaway Claude config dir and the .claude.json path inside
// it. EnsureWorktreesRootTrustedIn writes a real claude config, so these tests
// must never see the user's (the package's TestMain also sandboxes HOME).
func trustDir(t *testing.T) (dir, claudeJSON string) {
	t.Helper()
	dir = t.TempDir()
	return dir, filepath.Join(dir, ".claude.json")
}

// claudeFixture is a realistic .claude.json shape: top-level unknown fields
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

func TestEnsureWorktreesRootTrustedIn_SetsTrustAndPreservesContent(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	writeClaudeJSON(t, claudeJSON, claudeFixture, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrustedIn: %v", err)
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

// The dir argument is the whole contract, and the regression guard for #359: this
// function must write where its caller says, never where $HOME happens to point.
// It used to resolve $HOME itself for the unrouted session, which silently missed
// the file claude actually reads whenever CLAUDE_CONFIG_DIR was exported.
func TestEnsureWorktreesRootTrustedIn_WritesTheGivenDirNotHome(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	writeClaudeJSON(t, claudeJSON, claudeFixture, 0600)
	home := t.TempDir()
	homeJSON := filepath.Join(home, ".claude.json")
	writeClaudeJSON(t, homeJSON, claudeFixture, 0600)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home) // both spellings of "somewhere else"
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrustedIn: %v", err)
	}

	// Assert the write landed first — without this the absence check below would
	// pass for a function that did nothing at all.
	if !trustAccepted(t, readJSONMap(t, claudeJSON), root) {
		t.Fatal("worktrees root not trusted in the dir the caller named")
	}
	if trustAccepted(t, readJSONMap(t, homeJSON), root) {
		t.Fatal("wrote $HOME/.claude.json instead of (or as well as) the dir it was given")
	}
}

func TestEnsureWorktreesRootTrustedIn_ExistingEntryKeysPreserved(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	root := "/home/user/.atrium/worktrees"
	writeClaudeJSON(t, claudeJSON,
		`{"projects": {"`+root+`": {"hasTrustDialogAccepted": false, "allowedTools": ["Edit"]}}}`, 0600)

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrustedIn: %v", err)
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

func TestEnsureWorktreesRootTrustedIn_AlreadyTrustedDoesNotRewrite(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	root := "/home/user/.atrium/worktrees"
	content := `{"projects": {"` + root + `": {"hasTrustDialogAccepted": true}}}`
	writeClaudeJSON(t, claudeJSON, content, 0600)
	// Backdate so an accidental rewrite is observable via mtime.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(claudeJSON, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrustedIn: %v", err)
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

func TestEnsureWorktreesRootTrustedIn_MissingFileIsNoop(t *testing.T) {
	dir, claudeJSON := trustDir(t) // dir exists, but no .claude.json inside

	if err := EnsureWorktreesRootTrustedIn(dir, "/anywhere/worktrees"); err != nil {
		t.Fatalf("missing .claude.json must be a silent no-op, got: %v", err)
	}
	if _, err := os.Stat(claudeJSON); !os.IsNotExist(err) {
		t.Fatal("must never create .claude.json (absence means claude is not onboarded)")
	}
}

// The inherit-env account names no dir of its own, and an unresolvable home dir
// yields "" too. Either way there is no path to write, and guessing one would be
// worse than doing nothing: filepath.Join("", ".claude.json") is the cwd-relative
// ".claude.json", so without the guard an empty dir writes whatever file happens
// to sit in the process's working directory.
//
// The test therefore runs in a temp cwd holding a real, untrusted .claude.json —
// asserting only that the call returns nil would pass with the guard removed,
// because the missing-file branch no-ops for its own reasons.
func TestEnsureWorktreesRootTrustedIn_EmptyConfigDirIsNoop(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	bystander := seedTrustFile(t, cwd)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn("", root); err != nil {
		t.Fatalf("empty config dir must be a silent no-op, got: %v", err)
	}

	if trustAccepted(t, readJSONMap(t, bystander), root) {
		t.Fatal("an empty config dir wrote the cwd-relative .claude.json")
	}
}

// A relative configDir is a misconfiguration rather than a location, and unlike
// the empty one it is reported: a hand-written config_dir (ResolvedConfigDir only
// expands a leading ~) or a relative $CLAUDE_CONFIG_DIR would otherwise have
// filepath.Join resolve it against ATRIUM's working directory, while the claude
// that reads it resolves the same relative path against its own session worktree.
// The file Atrium would rewrite is therefore never the file any session reads —
// the exact reason the read side refuses one too (internal/doctor's
// fileGateReader). We cannot know the cwd claude would have used, so we do not
// guess.
//
// Same temp-cwd shape as the empty-dir test but NOT for the same reason: there,
// the return is nil whether or not the guard runs, so only the bystander file can
// tell the two apart. Here dropping the guard turns the error into a nil, which
// the first assertion already catches. The bystander stays for the narrower shape
// the return value cannot see — a guard that reports the misconfiguration and
// writes anyway.
func TestEnsureWorktreesRootTrustedIn_RelativeConfigDirIsRefused(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	relative := ".claude-work"
	if err := os.Mkdir(filepath.Join(cwd, relative), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", relative, err)
	}
	bystander := seedTrustFile(t, filepath.Join(cwd, relative))
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn(relative, root); err == nil {
		t.Fatal("a relative config dir must be reported, not silently accepted")
	}

	// Existence first: "not trusted" alone would also hold for a file the call had
	// deleted, which would be a worse outcome than the one under test.
	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("the bystander .claude.json must be left in place: %v", err)
	}
	if trustAccepted(t, readJSONMap(t, bystander), root) {
		t.Fatal("a relative config dir wrote a cwd-relative .claude.json")
	}
}

// seedTrustFile writes an untrusted .claude.json into dir and returns its path.
func seedTrustFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ".claude.json")
	writeClaudeJSON(t, path, `{"projects": {}}`, 0600)
	return path
}

func TestEnsureWorktreesRootTrustedIn_MalformedLeftUntouched(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	writeClaudeJSON(t, claudeJSON, `{"projects": {broken`, 0600)

	if err := EnsureWorktreesRootTrustedIn(dir, "/anywhere/worktrees"); err != nil {
		t.Fatalf("malformed .claude.json must be a silent no-op, got: %v", err)
	}
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != `{"projects": {broken` {
		t.Fatal("malformed file was modified")
	}
}

func TestEnsureWorktreesRootTrustedIn_UnexpectedShapesLeftUntouched(t *testing.T) {
	for name, content := range map[string]string{
		"projects null":    `{"projects": null}`,
		"projects array":   `{"projects": [1, 2]}`,
		"entry non-object": `{"projects": {"/anywhere/worktrees": "weird"}}`,
		"top-level array":  `[1, 2]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir, claudeJSON := trustDir(t)
			writeClaudeJSON(t, claudeJSON, content, 0600)

			if err := EnsureWorktreesRootTrustedIn(dir, "/anywhere/worktrees"); err != nil {
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

func TestEnsureWorktreesRootTrustedIn_MissingProjectsKeyCreatesIt(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	writeClaudeJSON(t, claudeJSON, `{"firstStartTime": 123}`, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrustedIn: %v", err)
	}

	m := readJSONMap(t, claudeJSON)
	if !trustAccepted(t, m, root) {
		t.Fatal("projects key not created for a config without one")
	}
	if _, ok := m["firstStartTime"]; !ok {
		t.Fatal("existing top-level field dropped")
	}
}

func TestEnsureWorktreesRootTrustedIn_PreservesSymlink(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	// Dotfile-manager layout: .claude.json is a symlink to a managed target.
	target := filepath.Join(dir, "dotfiles", "claude.json")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatalf("mkdir dotfiles: %v", err)
	}
	writeClaudeJSON(t, target, `{"projects": {}}`, 0600)
	if err := os.Symlink(target, claudeJSON); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("EnsureWorktreesRootTrustedIn: %v", err)
	}

	// The symlink must survive (not be replaced by a regular file) ...
	if fi, err := os.Lstat(claudeJSON); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".claude.json is no longer a symlink (err=%v)", err)
	}
	// ... and the managed target carries the update.
	if !trustAccepted(t, readJSONMap(t, target), root) {
		t.Fatal("symlink target not updated")
	}
}

func TestEnsureWorktreesRootTrustedIn_SecondCallIsNoop(t *testing.T) {
	dir, claudeJSON := trustDir(t)
	writeClaudeJSON(t, claudeJSON, claudeFixture, 0600)
	root := "/home/user/.atrium/worktrees"

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
		t.Fatalf("first call: %v", err)
	}
	after, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read after first call: %v", err)
	}

	if err := EnsureWorktreesRootTrustedIn(dir, root); err != nil {
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
