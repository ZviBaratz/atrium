package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// shellProbeTimeout bounds the shell invocation GetClaudeCommand uses to
// resolve the claude binary, so a hung profile script can't wedge startup.
const shellProbeTimeout = 10 * time.Second

// GetClaudeCommand attempts to find the "claude" command in the user's shell
// It checks in the following order:
// 1. Shell alias resolution: using "which" command
// 2. PATH lookup
//
// If both fail, it returns an error.
func GetClaudeCommand() (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash" // Default to bash if SHELL is not set
	}

	// Force the shell to load the user's profile and then run the command
	// For zsh, source .zshrc; for bash, source .bashrc
	var shellCmd string
	if strings.Contains(shell, "zsh") {
		shellCmd = "source ~/.zshrc &>/dev/null || true; which claude"
	} else if strings.Contains(shell, "bash") {
		shellCmd = "source ~/.bashrc &>/dev/null || true; which claude"
	} else {
		shellCmd = "which claude"
	}

	// One-shot startup probe with no ctx-bearing caller (config load runs before
	// any lifecycle context exists); Background capped at the probe timeout is
	// deliberate.
	ctx, cancel := context.WithTimeout(context.Background(), shellProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-c", shellCmd)
	output, err := cmd.Output()
	if err == nil {
		if program, ok := resolveClaudeCandidate(string(output)); ok {
			return program, nil
		}
	}

	// Otherwise, try to find in PATH directly
	claudePath, err := exec.LookPath("claude")
	if err == nil {
		return claudePath, nil
	}

	return "", fmt.Errorf("claude command not found in aliases or PATH")
}

// resolveClaudeCandidate interprets the output of `which claude` and returns a
// usable program path. The output may be a plain path, an alias definition
// (e.g. "claude: aliased to /usr/local/bin/claude"), or — when `claude` is a
// shell function — the full multi-line function body. We extract the alias
// target when present, then require the result to resolve to a real executable
// via exec.LookPath. If it does not (as happens with a function body, where the
// alias regex can capture a non-path token such as "$?"), we report no match so
// the caller falls back to a direct PATH lookup instead of persisting an
// unrunnable program as default_program — which otherwise causes new sessions to
// fail with "timed out waiting for tmux session ... (cleanup error: ...)".
func resolveClaudeCandidate(whichOutput string) (string, bool) {
	path := strings.TrimSpace(whichOutput)
	if path == "" {
		return "", false
	}

	// A shell function prints its entire multi-line body through `which`; that is
	// never a usable program path, and running the alias regex over it can capture
	// a stray token that happens to resolve (e.g. a binary name from an inline
	// "VAR=cmd" prefix, or "$?" from "local ret=$?"). Anything spanning multiple
	// lines is not a path, so reject it here and let the caller fall back to the
	// direct PATH lookup.
	if strings.ContainsAny(path, "\n\r") {
		return "", false
	}

	// Extract the target if the output is an alias definition.
	// Handle formats like "claude: aliased to /path/to/claude" or other shell-specific formats.
	aliasRegex := regexp.MustCompile(`(?:aliased to|->|=)\s*([^\s]+)`)
	if matches := aliasRegex.FindStringSubmatch(path); len(matches) > 1 {
		path = matches[1]
	}

	// Only trust the candidate if it actually resolves to an executable.
	if resolved, lookErr := exec.LookPath(path); lookErr == nil {
		return resolved, true
	}
	return "", false
}
