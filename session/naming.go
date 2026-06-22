package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"
)

// namingInstruction is the prompt handed to `claude -p`; the session context is
// piped on stdin and appended to it.
const namingInstruction = "Generate a concise 2-4 word title (max 32 characters, no quotes, no punctuation) for a coding session based on the context provided on stdin. Reply with ONLY the title and nothing else."

// nameSystemPrompt is appended to claude's default system prompt to suppress the
// agentic preamble the headless call otherwise emits ("I apologize… constrained
// environment… I need the full file contents…") or an empty reply. Appending (not
// replacing, via --system-prompt) keeps the default coding context the model uses to
// read a diff, which yields specific names ("Automatic session naming") over generic
// ones ("Auto Name Session"). Measured against the user's claude: lifts usable replies
// from 1/5 to 5/5 with no latency penalty.
const nameSystemPrompt = "You are titling a coding session. Reply with ONLY a 2-4 word title (max 32 chars, no quotes, no punctuation). Output the title alone — never an explanation, preamble, apology, or any mention of tools or your environment."

// nameGenTimeout caps a single naming call so a hung subprocess can't wedge the
// action; the work runs off the UI goroutine, but we still want a hard ceiling.
const nameGenTimeout = 30 * time.Second

// claudeResult is the subset of `claude -p --output-format json` we care about.
// is_error distinguishes a real title from a failure message: claude exits 0 and
// prints errors (e.g. "Not logged in") as result text, so the flag is the only
// reliable signal.
type claudeResult struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// GenerateName produces a short display name for a session from its initial
// prompt and diff stats by invoking an agent CLI in headless one-shot mode,
// reusing the user's existing auth. program is the session's own agent: it is
// preferred when it supports headless naming, so a gemini session is named by
// gemini; otherwise the first installed agent with naming support is used
// (claude, then gemini). It returns an error (rather than a fallback name) on
// any failure so the caller can surface it and leave the session's name
// untouched. Only binary resolution falls through to the next agent — a real
// invocation failure (e.g. "Not logged in") is reported, not papered over.
func GenerateName(ctx context.Context, program, prompt string, stats *git.DiffStats) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, nameGenTimeout)
	defer cancel()
	for _, key := range namerPreference(agent.Resolve(program).Key) {
		switch key {
		case agent.KeyClaude:
			claudePath, err := resolveClaudeBinary()
			if err != nil {
				continue
			}
			// Run from a neutral directory so a session worktree's CLAUDE.md can't bloat
			// every naming call; all the context we need is supplied on stdin.
			return generateName(ctx, cmd.MakeExecutor(), claudePath, os.TempDir(), prompt, stats)
		case agent.KeyGemini:
			geminiPath, err := exec.LookPath(string(agent.KeyGemini))
			if err != nil {
				continue
			}
			return generateNameGemini(ctx, cmd.MakeExecutor(), geminiPath, prompt, stats)
		}
	}
	return "", fmt.Errorf("no agent with headless naming support found (auto-naming needs claude or gemini)")
}

// namerPreference orders the naming-capable agents (agent.NamerKeys, the
// adapter table's HeadlessNamer entries in registry order): the session's own
// agent first when it is capable, then the rest. Agents without a verified
// headless mode defer to whichever capable agent is installed.
func namerPreference(own agent.Key) []agent.Key {
	keys := agent.NamerKeys()
	out := make([]agent.Key, 0, len(keys))
	for _, k := range keys {
		if k == own {
			out = append(out, k)
		}
	}
	for _, k := range keys {
		if k != own {
			out = append(out, k)
		}
	}
	return out
}

// resolveClaudeBinary finds a runnable claude executable for the headless call.
// It prefers a PATH lookup: the user's `claude` is often a shell function or alias
// that ultimately wraps the PATH binary, and a shell function can't be exec'd
// anyway. It only falls back to the shell-resolved command (which honors aliases
// to non-PATH locations) when claude isn't on PATH — and validates that result,
// since GetClaudeCommand can return a shell-function body (e.g. "$?") rather than a
// path when `which` prints a function definition.
func resolveClaudeBinary() (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	p, err := config.GetClaudeCommand()
	if err != nil {
		return "", err
	}
	if !isExecutableFile(p) {
		return "", fmt.Errorf("could not resolve a runnable claude binary (got %q)", p)
	}
	return p, nil
}

// isExecutableFile reports whether path is an existing, non-directory file with at
// least one executable bit set.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// generateName is the dependency-injected core of GenerateName, kept separate so
// tests can supply a mock executor and a fixed working directory.
func generateName(ctx context.Context, executor cmd.Executor, claudePath, workDir, prompt string, stats *git.DiffStats) (string, error) {
	sessionContext := buildContext(prompt, stats)
	if sessionContext == "" {
		return "", fmt.Errorf("no session content to name yet")
	}

	// Run claude under a throwaway $HOME so it can't load the user's global
	// ~/.claude/CLAUDE.md. That memory file roughly doubles call latency (it's a large
	// input) and, when the diff signal is weak, derails the title toward whatever it
	// emphasizes — a packaging session once got named "Refactor Memory System". The
	// isolated home holds only a symlink to the real credentials, so OAuth still works
	// and token refreshes write through. If we can't build one (e.g. keychain-only
	// auth, no creds file), namingHome is "" and we fall back to the real $HOME — we
	// give up the optimization, never correctness.
	namingHome, cleanup, _ := prepareNamingHome(realCredsPath())
	defer cleanup()

	c := exec.CommandContext(ctx, claudePath,
		"-p", namingInstruction,
		"--append-system-prompt", nameSystemPrompt,
		"--output-format", "json",
		"--model", "haiku",
		"--tools", "",
		"--no-session-persistence",
	)
	c.Dir = workDir
	c.Stdin = strings.NewReader(sessionContext)
	c.Env = namingEnv(namingHome)

	out, err := executor.Output(c)
	if err != nil {
		return "", fmt.Errorf("claude invocation failed: %w", err)
	}

	var res claudeResult
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("could not parse claude output: %w", err)
	}
	if res.IsError {
		return "", fmt.Errorf("claude reported an error: %s", strings.TrimSpace(res.Result))
	}
	return sanitizeName(res.Result)
}

// generateNameGemini is the gemini counterpart of generateName: `gemini -p`
// prints the bare response text on stdout (verified against gemini-cli 0.27;
// auth notices and workspace warnings go to stderr), so the output feeds
// sanitizeName directly — no JSON envelope. It runs from a freshly created
// empty directory rather than os.TempDir() because gemini scans its cwd as a
// workspace context; an empty dir keeps the call fast and the context clean.
func generateNameGemini(ctx context.Context, executor cmd.Executor, geminiPath, prompt string, stats *git.DiffStats) (string, error) {
	sessionContext := buildContext(prompt, stats)
	if sessionContext == "" {
		return "", fmt.Errorf("no session content to name yet")
	}

	workDir, err := os.MkdirTemp("", "cs-name-gemini-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	c := exec.CommandContext(ctx, geminiPath, "-p", namingInstruction)
	c.Dir = workDir
	c.Stdin = strings.NewReader(sessionContext)

	out, err := executor.Output(c)
	if err != nil {
		return "", fmt.Errorf("gemini invocation failed: %w", err)
	}
	return sanitizeName(string(out))
}

// realCredsPath returns the path to the user's claude credentials file — the one
// piece of $HOME the headless naming call needs to authenticate. It returns "" when
// the home directory can't be determined, which prepareNamingHome treats as "don't
// isolate".
func realCredsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", ".credentials.json")
}

// prepareNamingHome builds a throwaway $HOME for the headless naming call containing
// only a symlink to the real credentials at credsPath. Pointing claude at it keeps
// the user's global ~/.claude/CLAUDE.md out of the naming context (see generateName).
// The credentials are symlinked, not copied, so a token refresh writes through to the
// real file. cleanup is always non-nil and safe to call. When credsPath is empty or
// missing — or the home can't be built — it returns an empty homeDir so the caller
// leaves $HOME untouched, trading the optimization for guaranteed auth.
func prepareNamingHome(credsPath string) (homeDir string, cleanup func(), err error) {
	noop := func() {}
	if credsPath == "" {
		return "", noop, nil
	}
	if _, statErr := os.Stat(credsPath); statErr != nil {
		return "", noop, nil
	}

	dir, err := os.MkdirTemp("", "cs-name-home-")
	if err != nil {
		return "", noop, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	claudeDir := filepath.Join(dir, ".claude")
	if err := os.Mkdir(claudeDir, 0o700); err != nil {
		cleanup()
		return "", noop, err
	}
	if err := os.Symlink(credsPath, filepath.Join(claudeDir, ".credentials.json")); err != nil {
		cleanup()
		return "", noop, err
	}
	return dir, cleanup, nil
}

// namingEnv builds the environment for the headless call: the parent environment (so
// PATH and the claude auth resolve) plus MAX_THINKING_TOKENS=0 to disable extended
// thinking. Naming is a one-shot completion, but with thinking on haiku emits
// hundreds of reasoning tokens before the title (~1500 output tokens, ~14s); output
// tokens dominate latency, so suppressing them takes a call from ~14s to ~2s with no
// loss in name quality. When home is non-empty, HOME is redirected to the isolated
// naming home — replaced in place rather than appended, because duplicate HOME
// entries are resolved inconsistently across platforms.
func namingEnv(home string) []string {
	base := append(os.Environ(), "MAX_THINKING_TOKENS=0")
	if home == "" {
		return base
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

const (
	// maxPromptLen bounds how much of the initial prompt we feed the model, keeping
	// token cost predictable regardless of how long the user's prompt was.
	maxPromptLen = 2000
	// maxDigestLen is the overall char budget for the per-file changed-line digest
	// (see diffDigest). It supersedes the old head-truncated raw-diff slice, which
	// let alphabetically-early boilerplate files monopolize the window on large
	// diffs and produced bland names like "Project setup" for substantive sessions.
	maxDigestLen = 4000
	// maxDigestLinesPerFile caps how many +/- changed lines the digest takes from any
	// one file, guaranteeing breadth: no single early file (often noisy CI/packaging
	// configs) can eat the whole budget and starve the late files that carry intent.
	maxDigestLinesPerFile = 8
	// maxDigestLineLen truncates each emitted +/- line so a single very long change
	// (e.g. a minified token or a long URL) doesn't dominate its file's allotment.
	maxDigestLineLen = 120
	// maxDiffFiles caps the changed-file list so a sprawling diff can't flood the
	// context; the names alone are high signal at a tiny token cost.
	maxDiffFiles = 25
)

// buildContext assembles a compact, token-bounded description of a session for the
// namer: the (truncated) initial prompt, the changed-file list, a one-line diff
// summary, and a bounded slice of the raw diff. In practice Instance.Prompt is
// empty by the time a session is nameable (it is cleared once sent to the agent at
// startup), so the diff content is the primary signal — feeding it is what turns
// generic names into specific ones. It returns "" when there is nothing to
// summarize, letting the caller refuse to name an empty session rather than ask the
// model to invent one.
func buildContext(prompt string, stats *git.DiffStats) string {
	var b strings.Builder

	if p := strings.TrimSpace(prompt); p != "" {
		if r := []rune(p); len(r) > maxPromptLen {
			p = string(r[:maxPromptLen])
		}
		fmt.Fprintf(&b, "Initial prompt: %s\n", p)
	}

	if stats != nil {
		if files := changedFiles(stats.Content); len(files) > 0 {
			fmt.Fprintf(&b, "Files changed: %s\n", strings.Join(files, ", "))
		}

		if stats.FilesChanged > 0 || stats.Added > 0 || stats.Removed > 0 {
			noun := "files"
			if stats.FilesChanged == 1 {
				noun = "file"
			}
			fmt.Fprintf(&b, "Changes: %d %s changed, +%d -%d lines\n", stats.FilesChanged, noun, stats.Added, stats.Removed)
		}

		if digest := diffDigest(stats.Content); digest != "" {
			fmt.Fprintf(&b, "Changed lines:\n%s\n", digest)
		}
	}

	return strings.TrimSpace(b.String())
}

// diffDigest summarizes a full `git diff` for the namer by sampling the first few
// changed (+/-) lines from every file, bounded by an overall char budget. It
// replaces a previous head-truncation of the raw diff, which on large
// alphabetically-ordered diffs let the lowest-signal files (.github/, capitalized
// configs) monopolize the window and yielded bland names like "Project setup" for
// substantive sessions. The per-file cap guarantees breadth so the -old/+new pairs
// that carry intent surface from every changed file, not just whichever sorts
// first.
//
// Each file is introduced by an "@@ <path>" header (reusing the post-image path
// changedFiles extracts). +++/--- file-marker lines, index/mode metadata, hunk
// headers, and unchanged context lines are all dropped; only +/- content lines —
// truncated to maxDigestLineLen runes each — make it through, up to
// maxDigestLinesPerFile per file and maxDigestLen total. Returns "" for empty
// content.
func diffDigest(content string) string {
	var b strings.Builder
	perFile := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			rest := strings.TrimPrefix(line, "diff --git ")
			idx := strings.LastIndex(rest, " b/")
			if idx < 0 {
				continue
			}
			header := "@@ " + rest[idx+len(" b/"):] + "\n"
			if b.Len()+len(header) > maxDigestLen {
				break
			}
			b.WriteString(header)
			perFile = 0
			continue
		}
		// File-marker lines look like +/- content but carry no signal; skip first
		// so the +/- check below sees only real changed lines.
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		if perFile >= maxDigestLinesPerFile {
			continue
		}
		if r := []rune(line); len(r) > maxDigestLineLen {
			line = string(r[:maxDigestLineLen])
		}
		if b.Len()+len(line)+1 > maxDigestLen {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		perFile++
	}
	return strings.TrimRight(b.String(), "\n")
}

// changedFiles extracts the changed paths from a full `git diff` by reading the
// post-image (b/) side of each "diff --git a/<path> b/<path>" header. Such headers
// start at column 0, so added/removed content lines can't be miscounted (the same
// invariant countDiffFiles relies on). The list is capped at maxDiffFiles.
func changedFiles(content string) []string {
	var files []string
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		rest := strings.TrimPrefix(line, "diff --git ")
		if idx := strings.LastIndex(rest, " b/"); idx >= 0 {
			files = append(files, rest[idx+len(" b/"):])
			if len(files) >= maxDiffFiles {
				break
			}
		}
	}
	return files
}

// maxNameLen mirrors the 32-char cap the new-session/rename title input enforces
// (see ui/overlay newTitleInput), so a generated name fits the same field.
const maxNameLen = 32

// SlugTitle turns a raw line into a clean, bounded session title using the same
// rules (and 32-char cap) the new-session/rename inputs enforce, returning "" when
// nothing usable remains. It is the shared definition of that rule for callers
// outside this package (e.g. smart-dispatch prefill).
func SlugTitle(raw string) string {
	name, err := sanitizeName(raw)
	if err != nil {
		return ""
	}
	return name
}

// sanitizeName turns a model's raw response into a clean, bounded display name.
// It keeps only the first line, strips surrounding quotes and trailing
// punctuation, collapses internal whitespace, and truncates to maxNameLen on a
// word boundary. It returns an error when nothing usable remains, so callers can
// fail loudly rather than apply an empty or junk name.
func sanitizeName(raw string) (string, error) {
	// Only the first line: a well-behaved model replies with just the title, but
	// guard against trailing commentary.
	firstLine := raw
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		firstLine = raw[:i]
	}

	// Collapse all internal whitespace runs to single spaces.
	name := strings.Join(strings.Fields(firstLine), " ")

	// Peel surrounding quotes and trailing punctuation until stable, so combined
	// decorations like `"Add login".` are fully removed.
	for {
		prev := name
		name = strings.TrimRight(name, ".,!?:; ")
		name = strings.TrimSpace(name)
		if len(name) >= 2 {
			f, l := name[0], name[len(name)-1]
			if (f == '"' && l == '"') || (f == '\'' && l == '\'') || (f == '`' && l == '`') {
				name = name[1 : len(name)-1]
			}
		}
		if name == prev {
			break
		}
	}

	if runes := []rune(name); len(runes) > maxNameLen {
		truncated := string(runes[:maxNameLen])
		// Back off to the last word boundary so we don't cut a word mid-way.
		if idx := strings.LastIndex(truncated, " "); idx > 0 {
			truncated = truncated[:idx]
		}
		name = strings.TrimSpace(truncated)
	}

	if name == "" {
		return "", fmt.Errorf("generated name was empty after sanitizing")
	}
	return name, nil
}
