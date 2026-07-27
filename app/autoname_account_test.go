package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/require"
)

// Which account a headless call bills is decided at these two call sites, and
// neither is reachable from the tests one layer down: session.GenerateName is handed
// a config dir and can only be asserted to use what it was given. The plumbing being
// correct there says nothing about runAutoNameCmd passing the session's dir rather
// than "" — the exact defect #497 was, one function further out.
//
// So these drive the real command through a fake `claude` on PATH. The fake reads
// the isolated $HOME its parent built and records what the credentials symlink
// points at, which is the only observable that names an account: the throwaway home
// deliberately carries nothing else identifying, and it is deleted the moment the
// call returns.

// writeFakeClaude installs that recorder on PATH as `claude`, and answers with
// stdout — the whole `--output-format json` envelope the caller needs back. Naming
// parses result as a title while dispatch parses it as another JSON object, so one
// canned reply cannot serve both.
func writeFakeClaude(t *testing.T, record, stdout string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	require.NotContains(t, stdout, "'", "the reply is embedded in a single-quoted shell string")
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"readlink \"$HOME/.claude/.credentials.json\" > " + record + " 2>/dev/null || : > " + record + "\n" +
		"printf '%s' '" + stdout + "'\n"
	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const (
	fakeNameReply     = `{"is_error":false,"result":"Retry backoff"}`
	fakeDispatchReply = `{"is_error":false,"result":"{\"project\":\"hub\",\"title\":\"Review hub\"}"}`
)

func TestRunAutoNameCmd_BillsTheSessionsOwnAccount(t *testing.T) {
	acct := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(acct, ".credentials.json"), []byte("{}"), 0o600))
	record := filepath.Join(t.TempDir(), "creds")
	writeFakeClaude(t, record, fakeNameReply)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "a", Path: t.TempDir(), Program: "claude"})
	require.NoError(t, err)
	inst.SetClaudeAccount("work-2", acct, false)

	msg := runAutoNameCmd(context.Background(), inst, "wire the login form")()
	done, ok := msg.(autoNameDoneMsg)
	require.True(t, ok)
	require.NoError(t, done.err)

	got, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(acct, ".credentials.json"), strings.TrimSpace(string(got)),
		"naming a session must authenticate as the account that session runs under, not the ambient login")
}

// The inherit-env session is the case that must keep working unchanged: with no
// config dir of its own there is no account to prefer, so the ambient login is the
// right answer rather than a fallback. Pinning it stops a fix for the case above
// from turning "" into a broken path and failing every dormant install.
func TestRunAutoNameCmd_InheritEnvSessionUsesTheAmbientLogin(t *testing.T) {
	ambient := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ambient, ".credentials.json"), []byte("{}"), 0o600))
	t.Setenv("CLAUDE_CONFIG_DIR", ambient)
	record := filepath.Join(t.TempDir(), "creds")
	writeFakeClaude(t, record, fakeNameReply)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "a", Path: t.TempDir(), Program: "claude"})
	require.NoError(t, err)
	require.Empty(t, inst.ClaudeConfigDir(), "a session with no account routing injects nothing")

	msg := runAutoNameCmd(context.Background(), inst, "wire the login form")()
	require.NoError(t, msg.(autoNameDoneMsg).err)

	got, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(ambient, ".credentials.json"), strings.TrimSpace(string(got)))
}

// Smart dispatch runs before a project — and therefore before an account — has been
// chosen, so it asks routing with no remote and no path. That is not a placeholder
// argument: it is the call's actual situation, and it resolves to the catch-all, the
// account an unroutable session would get.
func TestSmartDispatchCmd_UsesTheCatchAllAccount(t *testing.T) {
	catchAll := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(catchAll, ".credentials.json"), []byte("{}"), 0o600))
	routed := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(routed, ".credentials.json"), []byte("{}"), 0o600))

	h := newAutoNameHome(t)
	h.program = "claude"
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		// Routed first, so a resolver that simply took the head of the list would
		// pick the wrong one and this could not pass by accident.
		{Name: "work", ConfigDir: routed, RemoteMatches: []string{"quantivly"}},
		{Name: "personal", ConfigDir: catchAll},
	}

	record := filepath.Join(t.TempDir(), "creds")
	writeFakeClaude(t, record, fakeDispatchReply)

	msg := h.runSmartDispatchCmd("review the hub", []string{"/tmp/hub"}, nil)()
	require.NoError(t, msg.(smartDispatchDoneMsg).err)

	got, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(catchAll, ".credentials.json"), strings.TrimSpace(string(got)),
		"a routing call bills the account an unrouted session would use")
}
