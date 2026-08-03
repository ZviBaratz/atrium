package customcmd

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ok is a minimal valid entry the rule tests mutate one field of at a time, so a
// failure names the rule under test rather than an unrelated omission.
func ok() config.CustomCommand {
	return config.CustomCommand{Key: "g", Description: "lazygit here", Command: "lazygit", Output: "background"}
}

func TestValidate_AcceptsAWellFormedEntry(t *testing.T) {
	got, problems := Validate([]config.CustomCommand{ok()})

	require.Empty(t, problems)
	require.Len(t, got, 1)
	assert.Equal(t, "g", got[0].Key)
	assert.Equal(t, "lazygit here", got[0].Description)
	assert.Equal(t, ContextSession, got[0].Context, "an omitted context defaults to session")
	assert.Equal(t, OutputBackground, got[0].Output)
}

func TestValidate_RejectsMalformedEntries(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*config.CustomCommand)
		wantMsg string
	}{
		{"empty key", func(c *config.CustomCommand) { c.Key = "" }, "key is required"},
		{"multi-rune key", func(c *config.CustomCommand) { c.Key = "gg" }, "must be a single character"},
		{"space key", func(c *config.CustomCommand) { c.Key = " " }, "cannot be the space bar"},
		{"esc key", func(c *config.CustomCommand) { c.Key = "\x1b" }, "must be a printable character"},
		{"empty description", func(c *config.CustomCommand) { c.Description = "" }, "description is required"},
		{"empty command", func(c *config.CustomCommand) { c.Command = "" }, "command is required"},
		{"unknown context", func(c *config.CustomCommand) { c.Context = "worktree" }, `context "worktree"`},
		{"missing output", func(c *config.CustomCommand) { c.Output = "" }, "output is required"},
		{"unknown output", func(c *config.CustomCommand) { c.Output = "popup" }, `output "popup"`},
		{"unparsable template", func(c *config.CustomCommand) { c.Command = "lazygit {{ .Session" }, "template"},
		{"unknown placeholder", func(c *config.CustomCommand) { c.Command = "lazygit {{.Session.Wortree}}" }, "Wortree"},
		{"unknown top-level field", func(c *config.CustomCommand) { c.Command = "x {{.Worktree}}" }, "Worktree"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := ok()
			tc.mutate(&entry)

			got, problems := Validate([]config.CustomCommand{entry})

			assert.Empty(t, got, "a rejected entry must not be bound")
			require.Len(t, problems, 1)
			assert.Equal(t, 0, problems[0].Index)
			assert.Contains(t, problems[0].Error(), tc.wantMsg)
		})
	}
}

// TestValidate_DuplicateKeyNamesBothParties is the AC's "message naming both
// parties", scoped as the leader-key design allows: a custom key can never shadow a
// built-in, so the only collision possible is custom-vs-custom.
func TestValidate_DuplicateKeyNamesBothParties(t *testing.T) {
	first, second := ok(), ok()
	first.Description = "lazygit here"
	second.Description = "just ci"

	got, problems := Validate([]config.CustomCommand{first, second})

	require.Len(t, got, 1, "the first claimant keeps the key")
	assert.Equal(t, "lazygit here", got[0].Description)
	require.Len(t, problems, 1)
	assert.Equal(t, 1, problems[0].Index, "the later entry is the one rejected")
	msg := problems[0].Error()
	assert.Contains(t, msg, "just ci", "names the rejected entry")
	assert.Contains(t, msg, "lazygit here", "names the entry it collides with")
}

// TestValidate_OneBadEntryDoesNotSinkTheRest pins "dropped, not bound": a config
// with a typo still gives the user every other command.
func TestValidate_OneBadEntryDoesNotSinkTheRest(t *testing.T) {
	bad := ok()
	bad.Key = "c"
	bad.Output = "popup"
	good := ok()

	got, problems := Validate([]config.CustomCommand{bad, good})

	require.Len(t, problems, 1)
	require.Len(t, got, 1)
	assert.Equal(t, "g", got[0].Key)
}

func TestRender(t *testing.T) {
	ctx := Ctx{
		Session: SessionCtx{Title: "issue-375", Name: "Issue #375", Branch: "zvi/issue-375", Worktree: "/wt/a b"},
		Repo:    RepoCtx{Path: "/home/zvi/Projects/atrium", Name: "atrium"},
	}

	cases := []struct{ name, tmpl, want string }{
		{"session fields", "echo {{.Session.Title}} {{.Session.Name}} {{.Session.Branch}}", "echo issue-375 Issue #375 zvi/issue-375"},
		{"repo fields", "echo {{.Repo.Path}} {{.Repo.Name}}", "echo /home/zvi/Projects/atrium atrium"},
		{"quote shell-escapes a path with a space", "lazygit -p {{ quote .Session.Worktree }}", `lazygit -p '/wt/a b'`},
		{"an unquoted value is interpolated raw", "lazygit -p {{ .Session.Worktree }}", "lazygit -p /wt/a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := ok()
			entry.Command = tc.tmpl
			cmds, problems := Validate([]config.CustomCommand{entry})
			require.Empty(t, problems)

			got, err := cmds[0].Render(ctx)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestQuoteDefeatsInjection is the reason quote exists: a value carrying shell
// metacharacters must reach the command as one argument, not as a second command.
func TestQuoteDefeatsInjection(t *testing.T) {
	entry := ok()
	entry.Command = "ls {{ quote .Session.Branch }}"
	cmds, problems := Validate([]config.CustomCommand{entry})
	require.Empty(t, problems)

	got, err := cmds[0].Render(Ctx{Session: SessionCtx{Branch: "a'; rm -rf /; echo '"}})

	require.NoError(t, err)
	assert.Equal(t, `ls 'a'"'"'; rm -rf /; echo '"'"''`, got)
}

func TestEnv(t *testing.T) {
	ctx := Ctx{
		Session: SessionCtx{Title: "issue-375", Name: "Issue #375", Branch: "zvi/issue-375", Worktree: "/wt/a"},
		Repo:    RepoCtx{Path: "/repo", Name: "atrium"},
	}

	got := Env(ctx)

	assert.ElementsMatch(t, []string{
		"ATRIUM_TITLE=issue-375",
		"ATRIUM_SESSION=Issue #375",
		"ATRIUM_BRANCH=zvi/issue-375",
		"ATRIUM_WORKTREE=/wt/a",
		"ATRIUM_REPO=/repo",
		"ATRIUM_REPO_NAME=atrium",
	}, got)
}

// TestMissingFields closes AC1's run-time half. A populated probe proves a
// placeholder EXISTS; it cannot prove it is non-empty for the session in hand. A
// direct session has no branch, so `gh pr create --head {{.Session.Branch}}` would
// render to a trailing space and act on whatever is checked out.
func TestMissingFields(t *testing.T) {
	direct := Ctx{Session: SessionCtx{Title: "scratch", Name: "scratch", Worktree: "/dir"}, Repo: RepoCtx{Path: "/dir", Name: "dir"}}

	cases := []struct {
		name string
		tmpl string
		want []string
	}{
		{"references only populated fields", "lazygit -p {{.Session.Worktree}}", nil},
		{"references the empty branch", "gh pr create --head {{.Session.Branch}}", []string{"Session.Branch"}},
		{"references it twice, reported once", "git log {{.Session.Branch}}..{{.Session.Branch}}", []string{"Session.Branch"}},
		{"references no fields at all", "just ci", nil},
		// A whole-struct reference renders (Go prints the struct) and is legal, but
		// it names no leaf, so there is nothing to call empty. Reporting it missing
		// would dim a row that runs perfectly well.
		{"references a struct rather than a leaf", "echo {{.Session}}", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := ok()
			entry.Command = tc.tmpl
			cmds, problems := Validate([]config.CustomCommand{entry})
			require.Empty(t, problems)

			assert.Equal(t, tc.want, cmds[0].MissingFields(direct))
		})
	}
}

// TestLogArgv pins that the command log never sees the rendered script.
// cmdlog.Redact models one NAME=VALUE per argv token, so a whole shell script in one
// token is both un-redactable (a bearer token in a -H flag has no leading NAME=) and
// destructively truncated (a leading FOO=bar match returns everything before the
// first '='). The synthetic argv sidesteps both.
func TestLogArgv(t *testing.T) {
	entry := ok()
	entry.Command = `curl -H "Authorization: token ghp_realsecret" https://example.com`
	cmds, problems := Validate([]config.CustomCommand{entry})
	require.Empty(t, problems)

	argv := cmds[0].LogArgv()

	joined := ""
	for _, a := range argv {
		joined += a + " "
	}
	assert.NotContains(t, joined, "ghp_realsecret", "the rendered script must never reach the log")
	assert.Contains(t, joined, "lazygit here", "the log names the command by its description")
	assert.Contains(t, joined, "g", "and by its key")
}
