package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
)

// claudeResult is the subset of `claude -p --output-format json` we care about.
// is_error distinguishes a real result from a failure message: claude exits 0 and
// prints errors (e.g. "Not logged in") as result text, so the flag is the only
// reliable signal.
type claudeResult struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// runClaudeHeadless runs `claude -p` in headless one-shot JSON mode under a
// throwaway $HOME (see prepareNamingHome), returning the cleaned result text.
// Shared by GenerateName and GenerateDispatch: both build a prompt and pipe their
// context on stdin, then parse the returned text differently (sanitizeName vs
// parseDispatchReply). Keeping the subprocess/JSON/error plumbing in one place
// means a change to the invocation (a flag, the model, the env) can't drift
// between the two call sites.
//
// Being that shared chokepoint is also why claudeConfigDir is a parameter rather
// than something resolved here: which account this call bills is the caller's
// decision, and the two callers answer it differently. Naming knows the session and
// uses its account; dispatch runs before any project is chosen and uses the
// catch-all. "" means inherit the ambient environment.
func runClaudeHeadless(ctx context.Context, executor cmd.Executor, claudePath, workDir, claudeConfigDir, promptArg, systemPrompt, stdin string) (string, error) {
	namingHome, cleanup, _ := prepareNamingHome(headlessCredsPath(claudeConfigDir))
	defer cleanup()

	c := exec.CommandContext(ctx, claudePath,
		"-p", promptArg,
		"--append-system-prompt", systemPrompt,
		"--output-format", "json",
		"--model", "haiku",
		"--tools", "",
		"--no-session-persistence",
	)
	c.Dir = workDir
	c.Stdin = strings.NewReader(stdin)
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
	return res.Result, nil
}

// geminiHeadlessWorkspace creates the throwaway cwd for a headless gemini call under
// Atrium's own data dir. It exists so the directory's ANCESTORS are owner-controlled —
// see runGeminiHeadless for the .env walk that makes that the property which matters,
// rather than the mode of the leaf MkdirTemp already sets to 0700.
//
// An error here fails the call rather than falling back to os.TempDir(). The fallback
// is the bug: it would restore the world-writable chain silently, on the path where
// something about the data dir was already wrong. A failed naming call costs a default
// session title, which is the direction session/naming.go already degrades in.
//
// WHAT MOVING OUT OF /tmp COSTS, disclosed rather than left to be found: nothing sweeps
// this root. The caller's deferred RemoveAll covers a normal return and a panic, and the
// old location was reaped by the OS when it did not — so a kill during a naming call now
// leaks an entry here permanently, where doctor cannot see it and no reap knows the path.
// It is one directory per abnormally-terminated call, and normally an empty one: gemini
// keeps its own state under its home dir, which this path leaves at the ambient default
// (it appends GEMINI_CLI_TRUST_WORKSPACE and nothing else), so the cwd is not where the
// CLI writes. Normally, not never — runClaudeHeadless disables tools outright with an
// empty --tools, and this call has no equivalent, so a write into the cwd is what the
// naming prompt makes overwhelmingly unlikely rather than what the invocation forbids.
// 0.55.1 has --approval-mode plan for that, unset here because the pin was probed
// without it; #754 carries the trade.
//
// A sweeper is the wrong remedy either way: every sweep that could run here races an
// in-flight call from another process, and the TUI and the daemon both name sessions.
func geminiHeadlessWorkspace() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(configDir, "headless")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "gemini-")
}

// runGeminiHeadless runs `gemini -p` from a freshly created empty workspace dir
// (gemini scans its cwd as workspace context, so an empty dir keeps the call fast
// and the context clean) and returns the bare stdout text — gemini emits no JSON
// envelope, so the caller parses the raw reply directly. Shared by the gemini
// naming and dispatch paths.
//
// GEMINI_CLI_TRUST_WORKSPACE is what makes the empty dir usable at all from 0.55
// on (#744). The CLI refuses to run in a directory its trustedFolders.json does
// not list, and a dir created by MkdirTemp on this call can never be listed —
// so without this the command exits 55 with empty stdout and "Gemini CLI is not
// running in a trusted directory" on stderr, and every gemini session title falls
// back. Measured both ways at 0.55.1 during #736's drive: the same call with the
// variable set returned a title and exit 0. The CLI's own error names this
// variable as the remedy for headless use (docs/cli/trusted-folders.md).
//
// WHAT TRUST ACTUALLY WIDENS, which is not what an earlier draft of this comment
// said. That draft enumerated GEMINI.md, settings and extensions and concluded the
// empty dir made them moot. It missed the one that is not about the workspace dir at
// all: loadEnvironment calls findEnvFile, which walks from the cwd to "/" looking for
// a .env, and trust decides what is done with the first one it finds. Untrusted, only
// AUTH_ENV_VAR_WHITELIST's four keys apply and each is stripped to [a-zA-Z0-9-_./];
// trusted, EVERY key applies unsanitized, and a <dir>/.gemini/.env probe is added at
// each level that untrusted does not make at all. Measured at 0.55.1: a GEMINI_SANDBOX
// planted in an ancestor's .env is ignored without the variable and exits 44 with it,
// from either spelling of the file.
//
// That is why the workspace is rooted HERE and not in os.TempDir(). The walk is over
// the cwd's ANCESTORS, so what matters is not this directory's mode but every mode
// above it, and MkdirTemp("") puts the chain's first link in a world-writable /tmp
// where any local uid can plant /tmp/.env or /tmp/.gemini/.env. Atrium's data dir is
// owner-writable the whole way up. The walk still ends at $HOME's own .env, which is
// unchanged: the /tmp-rooted walk reached the same file through findEnvFile's home
// fallback, so this moves no boundary except the one that was shared.
//
// --ignore-env is NOT the remedy it looks like. 0.55.1's yargs is .strict() and exits
// 1 on the flag, and even where it parses it gates only the plain .env branch, leaving
// the trusted-only .gemini/.env probe live at every level.
func runGeminiHeadless(ctx context.Context, executor cmd.Executor, geminiPath, promptArg, stdin string) (string, error) {
	workDir, err := geminiHeadlessWorkspace()
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	c := exec.CommandContext(ctx, geminiPath, "-p", promptArg)
	c.Dir = workDir
	c.Stdin = strings.NewReader(stdin)
	c.Env = append(os.Environ(), "GEMINI_CLI_TRUST_WORKSPACE=true")

	out, err := executor.Output(c)
	if err != nil {
		return "", fmt.Errorf("gemini invocation failed: %w", err)
	}
	return string(out), nil
}

// runAgyHeadless runs `agy -p` from a freshly created empty workspace dir
// (like gemini) and returns the bare stdout text. Shared by the agy
// naming and dispatch paths.
//
// WHY THIS ONE DOES NOT SET A TRUST VARIABLE, and what that is and is not
// evidence of. It is the same shape as runGeminiHeadless, which #744 had to fix
// because 0.55.x gemini refuses an untrusted cwd — so the question is fair and
// the answer is only partial.
//
// agy is a Go binary, not the gemini-cli JS bundle, so #744's specific mechanism
// cannot apply: its string table carries none of GEMINI_CLI_TRUST_WORKSPACE,
// trustedFolders, isWorkspaceTrusted, or "not running in a trusted directory".
// That negative is worth something only because the instrument is calibrated on
// the same binary — the identical grep DOES find agy's own "Yes, I trust this
// folder" and "esc to cancel", both of which it demonstrably renders, so the
// table is reachable and the absences are real absences.
//
// But agy HAS a trust concept of its own ("Do you trust the contents of this
// project?") and its own print-mode path ("slash command is not available in
// print mode"). Whether `agy -p` refuses an untrusted cwd the way gemini does is
// UNTESTED — it needs one headless agy run from a temp dir, which #752 tracks.
// Absence of gemini's mechanism is not presence of agy's permissiveness.
func runAgyHeadless(ctx context.Context, executor cmd.Executor, agyPath, promptArg, stdin string) (string, error) {
	workDir, err := os.MkdirTemp("", "cs-headless-agy-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	c := exec.CommandContext(ctx, agyPath, "-p", promptArg)
	c.Dir = workDir
	c.Stdin = strings.NewReader(stdin)

	out, err := executor.Output(c)
	if err != nil {
		return "", fmt.Errorf("agy invocation failed: %w", err)
	}
	return string(out), nil
}
