package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ZviBaratz/atrium/cmd"
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
func runClaudeHeadless(ctx context.Context, executor cmd.Executor, claudePath, workDir, promptArg, systemPrompt, stdin string) (string, error) {
	namingHome, cleanup, _ := prepareNamingHome(realCredsPath())
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

// runGeminiHeadless runs `gemini -p` from a freshly created empty workspace dir
// (gemini scans its cwd as workspace context, so an empty dir keeps the call fast
// and the context clean) and returns the bare stdout text — gemini emits no JSON
// envelope, so the caller parses the raw reply directly. Shared by the gemini
// naming and dispatch paths.
func runGeminiHeadless(ctx context.Context, executor cmd.Executor, geminiPath, promptArg, stdin string) (string, error) {
	workDir, err := os.MkdirTemp("", "cs-headless-gemini-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	c := exec.CommandContext(ctx, geminiPath, "-p", promptArg)
	c.Dir = workDir
	c.Stdin = strings.NewReader(stdin)

	out, err := executor.Output(c)
	if err != nil {
		return "", fmt.Errorf("gemini invocation failed: %w", err)
	}
	return string(out), nil
}
