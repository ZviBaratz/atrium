# Antigravity CLI Integration

**Date:** 2026-07-23
**Status:** Approved

## Motivation

Atrium supports multiple AI coding agents, including Claude Code, Codex, Aider, and Gemini. However, the `gemini-cli` is deprecated and users are shifting to Google's AI-first development platform, Antigravity, via its command-line interface, `agy`.

To maintain optimal support for Gemini models and provide the best user experience, Atrium needs to natively recognize and orchestrate the `agy` CLI as a first-class agent alongside Claude and Aider. The `agy` CLI shares some similarities with `gemini-cli` (e.g., standard input/output formatting for print commands) but requires its own explicit registration, routing, and headless command mappings to function correctly within Atrium.

## Scope

**In scope:**
- A new `KeyAgy` constant and an `agy` Adapter definition in `session/agent/registry.go`.
- Auto-detection of the `agy` binary in `config/agents.go` (`knownAgentBins`).
- Headless execution support (`agy -p`) for Atrium's auto-naming (`GenerateName`) and smart routing (`GenerateDispatch`) features.
- Update `README.md` to indicate Antigravity support.

**Out of scope:**
- Removing the deprecated Gemini CLI support (kept for backwards compatibility for users who still have it installed).
- Polling for Antigravity-specific busy markers or interactive prompts (unless they are documented, we rely on the generic fallback / content-change detection).

## Approach

### Data model
Add the `agy` adapter in `session/agent/registry.go`:

```go
var agy = &Adapter{
	Key:           KeyAgy,
	DisplayName:   "Antigravity",
	aliases:       []string{"agy", "antigravity"},
	Resume:        func(program string) string { return program + " --continue" },
	ResumeProbe:   "--continue",
	HeadlessNamer: true,
}
```

Include it in `registry` array:
```go
var registry = []*Adapter{claude, codex, gemini, aider, agy}
```

Register the key in `session/agent/agent.go`:
```go
const KeyAgy Key = "agy"
```

Add to `config/agents.go` auto-detection array:
```go
var knownAgentBins = []string{"claude", "codex", "gemini", "aider", "agy"}
```

### Headless Operations (Naming and Dispatch)
`agy` supports `-p` for non-interactive execution (e.g., `echo "context" | agy -p "prompt"`). This is identical to the deprecated `gemini-cli` interface. We'll duplicate the Gemini headless path for `agy`.

**1. Headless Invocation (`session/headless.go`)**
```go
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
```

**2. Auto-naming (`session/naming.go`)**
Add case to `GenerateName` for `KeyAgy`:
```go
		case agent.KeyAgy:
			agyPath, err := exec.LookPath(string(agent.KeyAgy))
			if err != nil {
				continue
			}
			return generateNameAgy(ctx, cmd.MakeExecutor(), agyPath, prompt, stats)
```
Add `generateNameAgy` mirroring `generateNameGemini`:
```go
func generateNameAgy(ctx context.Context, executor cmd.Executor, agyPath, prompt string, stats *git.DiffStats) (string, error) {
	sessionContext := buildContext(prompt, stats)
	if sessionContext == "" {
		return "", fmt.Errorf("no session content to name yet")
	}

	result, err := runAgyHeadless(ctx, executor, agyPath, namingInstruction, sessionContext)
	if err != nil {
		return "", err
	}
	return sanitizeName(result)
}
```

**3. Smart Dispatch (`session/dispatch.go`)**
Add case to `GenerateDispatch` for `KeyAgy`:
```go
		case agent.KeyAgy:
			agyPath, rerr := exec.LookPath(string(agent.KeyAgy))
			if rerr != nil {
				continue
			}
			return generateDispatchAgy(ctx, cmd.MakeExecutor(), agyPath, line, basenames)
```
Add `generateDispatchAgy`:
```go
func generateDispatchAgy(ctx context.Context, executor cmd.Executor, agyPath, line string, basenames []string) (project, title string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("no description to route")
	}

	result, err := runAgyHeadless(ctx, executor, agyPath, dispatchInstruction(basenames), line)
	if err != nil {
		return "", "", err
	}
	return parseDispatchReply(result, basenames)
}
```

### Documentation
Update `README.md` to reference the new support, appending Antigravity (`agy`) in documentation where Gemini is listed.

## Verification
- Test detection via `atrium profiles detect` with `agy` installed.
- Verify session runs with `atrium -p agy`.
- Create a test session and verify background naming works (inspect the TUI list to see if the session was renamed from the original prompt).
