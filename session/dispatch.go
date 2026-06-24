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
	"github.com/ZviBaratz/atrium/session/agent"
)

// dispatchSystemPrompt mirrors nameSystemPrompt's role for the routing call: it
// suppresses the headless agent's apologetic preamble so the reply is the bare JSON
// object we parse, not an explanation.
const dispatchSystemPrompt = "You are routing and titling a new coding session. Reply with ONLY a JSON object {\"project\":\"<name or empty>\",\"title\":\"<2-4 word title>\"} — never an explanation, preamble, apology, or any mention of tools or your environment."

// maxDispatchCandidates bounds how many project names are offered to the model. The
// candidate list can hold thousands of background-scanned repos; sending all of them
// would balloon latency, cost, and the odds of a wrong pick. The list is MRU-ordered,
// so the head is the most likely target.
const maxDispatchCandidates = 40

// dispatchReply is the model's structured answer: the chosen project (a candidate
// basename, or empty) and a short title for the session.
type dispatchReply struct {
	Project string `json:"project"`
	Title   string `json:"title"`
}

// GenerateDispatch routes a free-form line to one of the candidate projects and
// proposes a title, via the same headless agent path as GenerateName (reusing the
// user's CLI auth, throwaway HOME, and Haiku). project is a candidate basename, or ""
// when none clearly fits; the caller maps it back to a path. It returns an error on
// any failure so the caller can fall back to deterministic/blank prefill.
func GenerateDispatch(ctx context.Context, program, line string, candidates []string) (project, title string, err error) {
	ctx, cancel := context.WithTimeout(ctx, nameGenTimeout)
	defer cancel()
	basenames := dispatchBasenames(candidates, maxDispatchCandidates)
	for _, key := range namerPreference(agent.Resolve(program).Key) {
		switch key {
		case agent.KeyClaude:
			claudePath, rerr := resolveClaudeBinary()
			if rerr != nil {
				continue
			}
			return generateDispatch(ctx, cmd.MakeExecutor(), claudePath, os.TempDir(), line, basenames)
		case agent.KeyGemini:
			geminiPath, rerr := exec.LookPath(string(agent.KeyGemini))
			if rerr != nil {
				continue
			}
			return generateDispatchGemini(ctx, cmd.MakeExecutor(), geminiPath, line, basenames)
		}
	}
	return "", "", fmt.Errorf("no agent with headless support found (smart dispatch needs claude or gemini)")
}

// generateDispatch is the dependency-injected core of GenerateDispatch (claude path),
// kept separate so tests can supply a mock executor and a fixed working directory.
func generateDispatch(ctx context.Context, executor cmd.Executor, claudePath, workDir, line string, basenames []string) (project, title string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("no description to route")
	}

	result, err := runClaudeHeadless(ctx, executor, claudePath, workDir, dispatchInstruction(basenames), dispatchSystemPrompt, line)
	if err != nil {
		return "", "", err
	}
	return parseDispatchReply(result, basenames)
}

// generateDispatchGemini is the gemini counterpart: `gemini -p` prints the bare reply
// text on stdout (no JSON envelope), so the output is parsed directly.
func generateDispatchGemini(ctx context.Context, executor cmd.Executor, geminiPath, line string, basenames []string) (project, title string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("no description to route")
	}

	result, err := runGeminiHeadless(ctx, executor, geminiPath, dispatchInstruction(basenames), line)
	if err != nil {
		return "", "", err
	}
	return parseDispatchReply(result, basenames)
}

// parseDispatchReply decodes the model's JSON answer and validates the project
// against the candidate basenames (case-insensitive) so a hallucinated name is
// dropped rather than routed. The title is bounded by the shared slug rule.
func parseDispatchReply(raw string, basenames []string) (project, title string, err error) {
	var reply dispatchReply
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &reply); err != nil {
		return "", "", fmt.Errorf("could not parse dispatch reply: %w", err)
	}
	chosen := strings.ToLower(strings.TrimSpace(reply.Project))
	project = ""
	for _, b := range basenames {
		if strings.ToLower(b) == chosen && chosen != "" {
			project = b
			break
		}
	}
	return project, SlugTitle(reply.Title), nil
}

// extractJSONObject pulls the JSON object out of a model reply that may be wrapped in
// markdown code fences or surrounded by stray prose. The claude path hands us a clean
// inner result, but gemini (`-p`, no JSON envelope) routinely adds ```json fences or a
// leading sentence, so narrowing to the first '{' … last '}' keeps that path usable.
// With no braces it returns the trimmed input, letting json.Unmarshal report the error.
func extractJSONObject(raw string) string {
	s := strings.TrimSpace(raw)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}

// dispatchInstruction builds the per-call routing prompt. The candidate basenames are
// dynamic, so the instruction is assembled rather than a const.
func dispatchInstruction(basenames []string) string {
	return fmt.Sprintf(
		"A user described a new coding session on stdin. Choose the single project it "+
			"belongs to from this list, or empty if none clearly fits: %s. Also write a "+
			"concise 2-4 word title (max 32 characters, no quotes, no punctuation). Reply "+
			"with ONLY this JSON and nothing else: {\"project\":\"<name from the list or empty>\",\"title\":\"<title>\"}.",
		strings.Join(basenames, ", "))
}

// dispatchBasenames reduces candidate paths to a deduped, capped list of basenames in
// first-seen (MRU) order, for the model's allowed-project set.
func dispatchBasenames(paths []string, limit int) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range paths {
		base := filepath.Base(p)
		if base == "" || base == "." || base == "/" || seen[base] {
			continue
		}
		seen[base] = true
		out = append(out, base)
		if len(out) >= limit {
			break
		}
	}
	return out
}
