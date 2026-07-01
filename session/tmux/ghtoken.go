package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// resolveGitHubToken returns the gh auth token for the account rooted at
// ghConfigDir (empty = the ambient gh account), the same GH_CONFIG_DIR Atrium
// injects into the session.
//
// It resolves the config dir's active user first (`gh config get user`) and then
// fetches that user's token with `gh auth token --user <user>`. The explicit
// --user matters: with several accounts in the OS keyring, a bare `gh auth token`
// can return a shared-default entry belonging to another account (the token is
// keyed per-user in the keyring, but the default pointer is not per-config-dir),
// which would inject the wrong account's token. --user pins the right one.
//
// It is a package var so tests can stub it without a real, authenticated gh on
// PATH, mirroring session/git's checkGHCLI. Both subprocesses read the local
// keyring/config only (no network). Any failure — gh missing, not authenticated,
// or an empty token — returns ("", err); start() then injects nothing. The token
// is never logged.
var resolveGitHubToken = func(ctx context.Context, ghConfigDir string) (string, error) {
	env := os.Environ()
	if ghConfigDir != "" {
		env = append(env, "GH_CONFIG_DIR="+ghConfigDir)
	}

	userCmd := exec.CommandContext(ctx, "gh", "config", "get", "-h", "github.com", "user")
	userCmd.Env = env
	userOut, err := userCmd.Output()
	if err != nil {
		return "", err
	}
	user := strings.TrimSpace(string(userOut))

	args := []string{"auth", "token"}
	if user != "" {
		args = append(args, "--user", user)
	}
	tokCmd := exec.CommandContext(ctx, "gh", args...)
	tokCmd.Env = env
	out, err := tokCmd.Output()
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token returned empty")
	}
	return tok, nil
}
