package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/require"
)

func TestIsExecutableFile(t *testing.T) {
	dir := t.TempDir()

	execFile := filepath.Join(dir, "runnable")
	require.NoError(t, os.WriteFile(execFile, []byte("#!/bin/sh\n"), 0o755))
	require.True(t, isExecutableFile(execFile), "a 0755 file is executable")

	plainFile := filepath.Join(dir, "plain")
	require.NoError(t, os.WriteFile(plainFile, []byte("hi"), 0o644))
	require.False(t, isExecutableFile(plainFile), "a 0644 file is not executable")

	require.False(t, isExecutableFile(dir), "a directory is not an executable file")
	// Guards against GetClaudeCommand returning a shell-function body like "$?".
	require.False(t, isExecutableFile("$?"), "garbage paths are rejected")
	require.False(t, isExecutableFile(filepath.Join(dir, "nope")), "missing paths are rejected")
}

// TestNamerPreference pins the fallback order: the session's own agent leads
// when it supports headless naming; unsupported agents defer to the default
// order (claude, then gemini).
func TestNamerPreference(t *testing.T) {
	require.Equal(t, []agent.Key{agent.KeyClaude, agent.KeyGemini}, namerPreference(agent.KeyClaude))
	require.Equal(t, []agent.Key{agent.KeyGemini, agent.KeyClaude}, namerPreference(agent.KeyGemini))
	require.Equal(t, []agent.Key{agent.KeyClaude, agent.KeyGemini}, namerPreference(agent.KeyCodex))
	require.Equal(t, []agent.Key{agent.KeyClaude, agent.KeyGemini}, namerPreference(agent.KeyAider))
	require.Equal(t, []agent.Key{agent.KeyClaude, agent.KeyGemini}, namerPreference(agent.KeyGeneric))
}

func TestGenerateName(t *testing.T) {
	okExec := func(stdout string) cmd_test.MockCmdExec {
		return cmd_test.MockCmdExec{
			OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(stdout), nil },
		}
	}

	t.Run("returns the sanitized result on success", func(t *testing.T) {
		out := `{"type":"result","is_error":false,"result":"\"Retry backoff\""}`
		name, err := generateName(context.Background(), okExec(out), "claude", t.TempDir(), "add retry", nil)
		require.NoError(t, err)
		require.Equal(t, "Retry backoff", name)
	})

	t.Run("maps is_error to a failure instead of using the text as a name", func(t *testing.T) {
		out := `{"type":"result","is_error":true,"result":"Not logged in · Please run /login"}`
		name, err := generateName(context.Background(), okExec(out), "claude", t.TempDir(), "add retry", nil)
		require.Error(t, err)
		require.Empty(t, name)
		require.NotContains(t, name, "Not logged in")
	})

	t.Run("surfaces exec failures", func(t *testing.T) {
		failExec := cmd_test.MockCmdExec{
			OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, exec.ErrNotFound },
		}
		_, err := generateName(context.Background(), failExec, "claude", t.TempDir(), "add retry", nil)
		require.Error(t, err)
	})

	t.Run("errors on unparseable output", func(t *testing.T) {
		_, err := generateName(context.Background(), okExec("not json"), "claude", t.TempDir(), "add retry", nil)
		require.Error(t, err)
	})

	t.Run("gemini output is plain text, not json", func(t *testing.T) {
		name, err := generateNameGemini(context.Background(), okExec("HTTP Client Retry Logic\n"), "gemini", "add retry", nil)
		require.NoError(t, err)
		require.Equal(t, "HTTP Client Retry Logic", name)
	})

	t.Run("gemini surfaces exec failures", func(t *testing.T) {
		failExec := cmd_test.MockCmdExec{
			OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, exec.ErrNotFound },
		}
		_, err := generateNameGemini(context.Background(), failExec, "gemini", "add retry", nil)
		require.Error(t, err)
	})

	t.Run("refuses to name an empty session without calling claude", func(t *testing.T) {
		called := false
		probe := cmd_test.MockCmdExec{
			OutputFunc: func(*exec.Cmd) ([]byte, error) { called = true; return nil, nil },
		}
		_, err := generateName(context.Background(), probe, "claude", t.TempDir(), "  ", &git.DiffStats{})
		require.Error(t, err)
		require.False(t, called, "claude must not be invoked when there is nothing to summarize")
	})

	t.Run("builds the expected headless invocation", func(t *testing.T) {
		var gotArgs []string
		var gotStdin string
		var gotDir string
		var gotEnv []string
		inspect := cmd_test.MockCmdExec{
			OutputFunc: func(c *exec.Cmd) ([]byte, error) {
				gotArgs = c.Args
				gotDir = c.Dir
				gotEnv = c.Env
				if c.Stdin != nil {
					b, _ := io.ReadAll(c.Stdin)
					gotStdin = string(b)
				}
				return []byte(`{"is_error":false,"result":"Login form"}`), nil
			},
		}
		_, err := generateName(context.Background(), inspect, "/usr/bin/claude", "/tmp/neutral", "wire login form", nil)
		require.NoError(t, err)
		joined := strings.Join(gotArgs, " ")
		require.Contains(t, joined, "-p")
		require.Contains(t, joined, "--output-format json")
		require.Contains(t, joined, "--model haiku")
		require.Contains(t, joined, "--no-session-persistence")
		require.Equal(t, "/tmp/neutral", gotDir, "must run from the neutral cwd, not a session worktree")
		require.Contains(t, gotStdin, "wire login form")
		// Extended thinking is disabled so the call stays fast (~2s vs ~14s) and
		// emits the title directly instead of hundreds of reasoning tokens.
		require.Contains(t, gotEnv, "MAX_THINKING_TOKENS=0")
		// A title-generator directive is appended to (not replacing) the default
		// system prompt, so the headless agent answers with just a title instead of
		// the apologetic "constrained environment" preamble it otherwise emits.
		require.Contains(t, joined, "--append-system-prompt")
		require.Contains(t, gotArgs, nameSystemPrompt)
	})
}

func TestSlugTitle(t *testing.T) {
	t.Run("returns a clean title for ordinary input", func(t *testing.T) {
		require.Equal(t, "Review box 123", SlugTitle("Review box 123"))
	})

	t.Run("returns empty for input with nothing usable", func(t *testing.T) {
		require.Empty(t, SlugTitle("   "))
		require.Empty(t, SlugTitle(""))
	})

	t.Run("truncates to the 32-char title cap on a word boundary", func(t *testing.T) {
		got := SlugTitle("The hub is failing with a migration error somewhere")
		require.LessOrEqual(t, len([]rune(got)), maxNameLen)
		require.Equal(t, "The hub is failing with a", got)
	})
}

func TestPrepareNamingHome(t *testing.T) {
	t.Run("isolates HOME with a credentials symlink when creds exist", func(t *testing.T) {
		src := t.TempDir()
		creds := filepath.Join(src, ".credentials.json")
		require.NoError(t, os.WriteFile(creds, []byte("{}"), 0o600))

		home, cleanup, err := prepareNamingHome(creds)
		require.NoError(t, err)
		require.NotEmpty(t, home, "an isolated home is created when creds exist")
		defer cleanup()

		// Credentials are symlinked (not copied) so a token refresh writes through to
		// the real file and the naming home never goes stale.
		link := filepath.Join(home, ".claude", ".credentials.json")
		target, err := os.Readlink(link)
		require.NoError(t, err, "credentials must be a symlink in the isolated home")
		require.Equal(t, creds, target)
		// The whole point: no global CLAUDE.md in the isolated home, so claude can't
		// load it and confabulate / pay its latency cost.
		_, err = os.Stat(filepath.Join(home, ".claude", "CLAUDE.md"))
		require.True(t, os.IsNotExist(err), "isolated home must not carry a CLAUDE.md")

		cleanup()
		_, err = os.Stat(home)
		require.True(t, os.IsNotExist(err), "cleanup removes the isolated home")
	})

	t.Run("skips isolation when the creds file is missing", func(t *testing.T) {
		home, cleanup, err := prepareNamingHome(filepath.Join(t.TempDir(), "nope.json"))
		require.NoError(t, err)
		require.Empty(t, home, "no isolation when there are no file creds (e.g. keychain auth)")
		require.NotNil(t, cleanup, "cleanup is always safe to call")
		cleanup()
	})

	t.Run("skips isolation on an empty creds path", func(t *testing.T) {
		home, cleanup, err := prepareNamingHome("")
		require.NoError(t, err)
		require.Empty(t, home)
		require.NotNil(t, cleanup)
		cleanup()
	})
}

func TestNamingEnv(t *testing.T) {
	t.Run("redirects HOME to the isolated home exactly once", func(t *testing.T) {
		env := namingEnv("/tmp/iso-home")
		require.Contains(t, env, "HOME=/tmp/iso-home")
		require.Contains(t, env, "MAX_THINKING_TOKENS=0")
		homes := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, "HOME=") {
				homes++
			}
		}
		require.Equal(t, 1, homes, "duplicate HOME entries resolve inconsistently across platforms")
	})

	t.Run("leaves HOME untouched when not isolating", func(t *testing.T) {
		env := namingEnv("")
		require.Contains(t, env, "MAX_THINKING_TOKENS=0")
		require.Contains(t, env, "HOME="+os.Getenv("HOME"))
	})
}

// TestGenerateNameIntegration exercises the whole path against the real `claude`
// binary (isolated HOME, MAX_THINKING_TOKENS=0, the diff-driven context). It is
// opt-in — set CS_NAMING_E2E=1 — so normal `go test`/CI never shells out or hits the
// network. Run it to eyeball name quality and latency after changing the prompt or
// flags.
func TestGenerateNameIntegration(t *testing.T) {
	if os.Getenv("CS_NAMING_E2E") == "" {
		t.Skip("set CS_NAMING_E2E=1 to run the live claude integration test")
	}
	diff := "diff --git a/config/brand.go b/config/brand.go\n" +
		"new file mode 100644\n+package config\n+const AppName = \"atrium\"\n" +
		"diff --git a/CLA.md b/CLA.md\ndeleted file mode 100644\n-Contributor License Agreement\n" +
		"diff --git a/session/tmux/atrium.conf b/session/tmux/atrium.conf\n+set -g prefix C-a\n"
	stats := &git.DiffStats{FilesChanged: 3, Added: 4, Removed: 1, Content: diff}

	start := time.Now()
	name, err := GenerateName(context.Background(), "claude", "", stats)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotEmpty(t, name)
	require.LessOrEqual(t, len(name), maxNameLen)
	t.Logf("generated %q in %s", name, elapsed.Round(100*time.Millisecond))
}

func TestBuildContext(t *testing.T) {
	t.Run("includes the initial prompt", func(t *testing.T) {
		ctx := buildContext("add a retry helper with backoff", nil)
		require.Contains(t, ctx, "add a retry helper with backoff")
	})

	t.Run("includes a one-line diff summary", func(t *testing.T) {
		ctx := buildContext("", &git.DiffStats{FilesChanged: 2, Added: 48, Removed: 3})
		require.Contains(t, ctx, "2 files")
		require.Contains(t, ctx, "+48")
		require.Contains(t, ctx, "-3")
	})

	t.Run("combines prompt and diff", func(t *testing.T) {
		ctx := buildContext("wire the login form", &git.DiffStats{FilesChanged: 1, Added: 10, Removed: 0})
		require.Contains(t, ctx, "wire the login form")
		require.Contains(t, ctx, "1 file")
	})

	t.Run("empty prompt and empty stats yield empty context", func(t *testing.T) {
		require.Empty(t, buildContext("   ", nil))
		require.Empty(t, buildContext("", &git.DiffStats{}))
	})

	t.Run("truncates an overlong prompt", func(t *testing.T) {
		ctx := buildContext(strings.Repeat("x", 10000), nil)
		require.LessOrEqual(t, len(ctx), 2200)
	})

	t.Run("includes a digest of changed lines per file", func(t *testing.T) {
		content := "diff --git a/app/app.go b/app/app.go\n" +
			"@@ -1 +1 @@\n-old\n+new\n" +
			"diff --git a/keys/keys.go b/keys/keys.go\n" +
			"@@ -2 +2 @@\n-foo\n+bar\n"
		ctx := buildContext("", &git.DiffStats{FilesChanged: 2, Added: 2, Removed: 2, Content: content})
		require.Contains(t, ctx, "app/app.go")
		require.Contains(t, ctx, "keys/keys.go")
		require.Contains(t, ctx, "Changed lines:")
		require.Contains(t, ctx, "+new")
	})

	// The real worked-session case: Prompt is cleared once sent to the agent, so the
	// diff Content is the only signal. It must still produce rich context (regression
	// guard for generic names like "Feature implementation").
	t.Run("empty prompt with populated Content still yields rich context", func(t *testing.T) {
		content := "diff --git a/session/naming.go b/session/naming.go\n@@ -1 +1 @@\n+func GenerateName() {}\n"
		ctx := buildContext("", &git.DiffStats{FilesChanged: 1, Added: 1, Content: content})
		require.NotEmpty(t, ctx)
		require.Contains(t, ctx, "session/naming.go")
	})

	t.Run("caps changed lines per file in the digest", func(t *testing.T) {
		// A single huge file used to monopolize the entire window under head-truncation;
		// the per-file cap now bounds its contribution and leaves room for other files.
		var sb strings.Builder
		sb.WriteString("diff --git a/big.txt b/big.txt\n")
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(&sb, "+line %d\n", i)
		}
		ctx := buildContext("", &git.DiffStats{FilesChanged: 1, Added: 1000, Content: sb.String()})
		require.Contains(t, ctx, "+line 0")
		require.NotContains(t, ctx, fmt.Sprintf("+line %d", maxDigestLinesPerFile),
			"per-file cap drops the (maxDigestLinesPerFile+1)th changed line")
	})

	t.Run("respects the overall digest budget for sprawling diffs", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 200; i++ {
			fmt.Fprintf(&sb, "diff --git a/f%03d.go b/f%03d.go\n", i, i)
			for j := 0; j < maxDigestLinesPerFile; j++ {
				fmt.Fprintf(&sb, "+padded line %d-%d %s\n", i, j, strings.Repeat("x", 60))
			}
		}
		ctx := buildContext("", &git.DiffStats{FilesChanged: 200, Added: 200 * maxDigestLinesPerFile, Content: sb.String()})
		// Slack covers the surrounding prelude (Files changed / Changes / header).
		require.LessOrEqual(t, len(ctx), maxDigestLen+1000, "overall context stays bounded for sprawling diffs")
	})
}

func TestChangedFiles(t *testing.T) {
	t.Run("extracts post-image paths from diff headers", func(t *testing.T) {
		content := "diff --git a/app/app.go b/app/app.go\n@@ x\n" +
			"diff --git a/keys/keys.go b/keys/keys.go\n@@ y\n"
		require.Equal(t, []string{"app/app.go", "keys/keys.go"}, changedFiles(content))
	})

	t.Run("empty content yields no files", func(t *testing.T) {
		require.Empty(t, changedFiles(""))
	})

	t.Run("caps the number of files", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < maxDiffFiles+10; i++ {
			fmt.Fprintf(&sb, "diff --git a/f%d.go b/f%d.go\n", i, i)
		}
		require.Len(t, changedFiles(sb.String()), maxDiffFiles)
	})
}

func TestDiffDigest(t *testing.T) {
	t.Run("emits @@ headers and only +/- content lines", func(t *testing.T) {
		content := "diff --git a/a.go b/a.go\n" +
			"index 1234..5678 100644\n" +
			"--- a/a.go\n+++ b/a.go\n" +
			"@@ -1,2 +1,2 @@\n context\n-old\n+new\n"
		d := diffDigest(content)
		require.Contains(t, d, "@@ a.go", "files are introduced by an @@ <path> header")
		require.Contains(t, d, "-old")
		require.Contains(t, d, "+new")
		require.NotContains(t, d, " context", "unchanged context lines are dropped")
		require.NotContains(t, d, "--- a/a.go", "file marker lines are dropped")
		require.NotContains(t, d, "+++ b/a.go", "file marker lines are dropped")
		require.NotContains(t, d, "index 1234..5678", "index lines are dropped")
	})

	t.Run("caps changed lines per file at maxDigestLinesPerFile", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("diff --git a/big.go b/big.go\n")
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&sb, "+l%d\n", i)
		}
		d := diffDigest(sb.String())
		require.Contains(t, d, "+l0")
		require.Contains(t, d, fmt.Sprintf("+l%d", maxDigestLinesPerFile-1))
		require.NotContains(t, d, fmt.Sprintf("+l%d", maxDigestLinesPerFile),
			"the (cap+1)th changed line is dropped to leave room for other files")
	})

	t.Run("truncates long lines to maxDigestLineLen runes", func(t *testing.T) {
		long := "+" + strings.Repeat("a", maxDigestLineLen+50)
		content := "diff --git a/long.go b/long.go\n" + long + "\n"
		d := diffDigest(content)
		for _, line := range strings.Split(d, "\n") {
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				require.LessOrEqual(t, len([]rune(line)), maxDigestLineLen, "long +/- lines are truncated")
			}
		}
	})

	t.Run("respects the overall budget", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 300; i++ {
			fmt.Fprintf(&sb, "diff --git a/f%03d.go b/f%03d.go\n", i, i)
			for j := 0; j < maxDigestLinesPerFile; j++ {
				fmt.Fprintf(&sb, "+%s\n", strings.Repeat("p", 80))
			}
		}
		d := diffDigest(sb.String())
		// Slack accommodates the in-progress file's trailing header line.
		require.LessOrEqual(t, len(d), maxDigestLen+200, "digest stays within the overall budget plus minor slack")
	})

	t.Run("empty content yields empty digest", func(t *testing.T) {
		require.Empty(t, diffDigest(""))
	})

	// The reported failure ("Project setup" on a 74-file rebrand): under head-truncation,
	// alphabetically-early boilerplate files monopolized the window and the substantive
	// late files never surfaced. The per-file cap guarantees breadth — encode that.
	t.Run("surfaces late substantive files despite boilerplate-first ordering", func(t *testing.T) {
		var sb strings.Builder
		// An early file with far more than maxDigestLinesPerFile changes would have
		// eaten the entire head-1500 window under the old scheme.
		sb.WriteString("diff --git a/a_early.txt b/a_early.txt\n")
		for i := 0; i < 500; i++ {
			sb.WriteString("+boilerplate boilerplate boilerplate\n")
		}
		sb.WriteString("diff --git a/z_late.go b/z_late.go\n")
		sb.WriteString("+SUBSTANTIVE_MARKER\n")
		d := diffDigest(sb.String())
		require.Contains(t, d, "z_late.go", "late files must be enumerated in the digest")
		require.Contains(t, d, "SUBSTANTIVE_MARKER",
			"late files' changed lines must surface despite boilerplate-first ordering")
	})
}

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "clean title passes through", raw: "Retry exponential backoff", want: "Retry exponential backoff"},
		{name: "strips surrounding double quotes", raw: "\"Add login form\"", want: "Add login form"},
		{name: "strips surrounding backticks", raw: "`Add login form`", want: "Add login form"},
		{name: "strips trailing punctuation", raw: "Add login form.", want: "Add login form"},
		{name: "trims surrounding whitespace", raw: "  Add login form  ", want: "Add login form"},
		{name: "collapses internal whitespace", raw: "Add    login\tform", want: "Add login form"},
		{name: "takes only the first line", raw: "Add login form\nHere is your title", want: "Add login form"},
		{name: "truncates to 32 chars without trailing space", raw: "This title is definitely much longer than allowed", want: "This title is definitely much"},
		{name: "empty input errors", raw: "", wantErr: true},
		{name: "whitespace-only input errors", raw: "   \n  ", wantErr: true},
		{name: "punctuation/quotes-only input errors", raw: "\"\".", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sanitizeName(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.LessOrEqual(t, len(got), 32)
		})
	}
}
