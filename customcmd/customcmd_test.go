package customcmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextLeavesByReflection enumerates Ctx's leaf string fields as dotted paths,
// derived from the type rather than restated, so the drift guard cannot go stale
// alongside the thing it guards.
func contextLeavesByReflection(t *testing.T) []string {
	t.Helper()
	var out []string
	outer := reflect.TypeOf(Ctx{})
	for i := 0; i < outer.NumField(); i++ {
		group := outer.Field(i)
		require.Equalf(t, reflect.Struct, group.Type.Kind(), "Ctx.%s must be a struct of string leaves", group.Name)
		for j := 0; j < group.Type.NumField(); j++ {
			leaf := group.Type.Field(j)
			require.Equalf(t, reflect.String, leaf.Type.Kind(), "Ctx.%s.%s must be a string", group.Name, leaf.Name)
			out = append(out, group.Name+"."+leaf.Name)
		}
	}
	return out
}

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
	assert.Equal(t, "lazygit", got[0].Source(), "Source is the template as configured, unrendered")
}

// TestValidate_AcceptsEveryContext covers the accepting half of parseContext. `repo`
// is the one worth pinning: it is the context whose whole purpose is outliving a
// pause, so a rule that silently stopped accepting it would strand exactly the
// commands that are supposed to keep working on a paused session.
func TestValidate_AcceptsEveryContext(t *testing.T) {
	for _, tc := range []struct {
		configured string
		want       Context
	}{
		{"", ContextSession},
		{"session", ContextSession},
		{"repo", ContextRepo},
	} {
		t.Run("context="+tc.configured, func(t *testing.T) {
			entry := ok()
			entry.Context = tc.configured

			got, problems := Validate([]config.CustomCommand{entry})

			require.Empty(t, problems)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Context)
		})
	}
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
		// A lone continuation byte decodes to U+FFFD with size 1 — which equals
		// len(key), and U+FFFD is printable — so it passes every other rule here and
		// binds a key no keypress can ever produce.
		{"undecodable byte as key", func(c *config.CustomCommand) { c.Key = "\xff" }, "not usable text"},
		// The case that actually reaches a user: encoding/json replaces invalid UTF-8
		// with U+FFFD, so binary garbage in config.json arrives here already valid and
		// three bytes wide. A size-based check waves this through; it is the one a
		// real config produces.
		{"replacement char as key", func(c *config.CustomCommand) { c.Key = "�" }, "not usable text"},
		// Two bad bytes decode the same way but fail the single-character test too,
		// so this one pins the check ORDER: reversed, it is reported as being too
		// long rather than as not being text.
		{"undecodable multibyte key", func(c *config.CustomCommand) { c.Key = "\xff\xfe" }, "not usable text"},
		// The same outcome as U+FFFD, one rung up: a combining mark is a single rune,
		// is printable, and is not a space, so it clears every rule above and binds a
		// key no keypress produces. U+0301 is the combining acute; U+FE0F is the
		// variation selector a copied emoji drags along.
		{"combining mark as key", func(c *config.CustomCommand) { c.Key = "\u0301" }, "key U+0301 is a combining mark"},
		{"variation selector as key", func(c *config.CustomCommand) { c.Key = "\ufe0f" }, "key U+FE0F is a combining mark"},
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

// TestValidate_AcceptsTheKeysAKeyboardProduces is the over-rejection guard for the
// key rules, which are all refusals and therefore all capable of taking a key the
// user wanted. Two are at risk. The precomposed accent is a letter, not a combining
// mark, so a rule written against "accents" instead of the mark categories would
// refuse it. U+093E is a SPACING mark (Mc), the category the mark check deliberately
// leaves alone because an Inscript keyboard emits it from one keystroke — without
// this case, widening that check to Mc passes every test while taking the key away
// from every Indic-keyboard user.
func TestValidate_AcceptsTheKeysAKeyboardProduces(t *testing.T) {
	for _, key := range []string{"g", "G", "1", "?", "/", "!", "é", "λ", "🔥", "ा"} {
		t.Run("key="+key, func(t *testing.T) {
			entry := ok()
			entry.Key = key

			got, problems := Validate([]config.CustomCommand{entry})

			assert.Empty(t, problems)
			require.Len(t, got, 1)
			assert.Equal(t, key, got[0].Key)
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

// TestValidate_AnEntryTooBrokenToNameIsRejectedOnItsOwnTerms keeps the
// duplicate-key message honest. The whole point of that message is naming both
// parties, so it must not run before the check that guarantees this entry HAS a
// name — otherwise the collision reports `so "" is ignored`.
func TestValidate_AnEntryTooBrokenToNameIsRejectedOnItsOwnTerms(t *testing.T) {
	first := ok()
	nameless := ok()
	nameless.Description = ""

	got, problems := Validate([]config.CustomCommand{first, nameless})

	require.Len(t, got, 1)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "description is required")
	assert.NotContains(t, problems[0].Error(), `""`, "a nameless entry is rejected for being nameless, not reported as an anonymous collision")
}

// TestFieldAccessCoversEveryContextLeaf is the drift guard for the accessor table
// MissingFields substitutes through: a leaf added to Ctx without a matching entry
// would silently never be reported empty.
func TestFieldAccessCoversEveryContextLeaf(t *testing.T) {
	covered := map[string]bool{}
	for _, f := range fieldAccess {
		covered[f.path] = true
	}

	for _, leaf := range contextLeavesByReflection(t) {
		assert.Truef(t, covered[leaf], "Ctx leaf %s has no fieldAccess entry", leaf)
	}
	assert.Len(t, fieldAccess, len(contextLeavesByReflection(t)), "fieldAccess must not name a leaf Ctx does not have")
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
		// The template engine has more than one way to reach a field, and a check
		// that reimplements its name resolution will miss whichever forms it forgot.
		// These two reach Session.Branch without ever spelling it as one path.
		{"reaches the branch through with", "{{with .Session}}gh pr create --head {{.Branch}}{{end}}", []string{"Session.Branch"}},
		{"reaches the branch through a chain", "gh pr create --head {{(.Session).Branch}}", []string{"Session.Branch"}},
		// Printing the whole struct puts every field in the command line, empty ones
		// included — so the empty branch really does reach the shell here, and saying
		// so is right even though no path is spelled out.
		{"prints a struct containing an empty field", "echo {{.Session}}", []string{"Session.Branch"}},
		// A field behind a branch the render does not take never reaches the command,
		// so it is not missing. This is the payoff of asking the renderer instead of
		// the parse tree: "unused on this path" and "used" stop being the same answer.
		{"references the branch only in an untaken branch", "{{if .Session.Name}}just ci{{else}}git log {{.Session.Branch}}{{end}}", nil},
		// The known limit of substitution: a sentinel is non-empty, so a template
		// that tests the field's OWN emptiness takes the opposite branch under
		// probing and the field reads as used. That errs toward dimming a row that
		// would have run, which is the safe direction — a false dim is visible and
		// explains itself, where a false pass runs a command with a hole in it.
		{"guards on the field it is missing", "{{if .Session.Branch}}git log {{.Session.Branch}}{{end}}", []string{"Session.Branch"}},
		// The value reaches the output through whatever the template does to it, so a
		// sentinel any of these can re-encode is a sentinel the substring search
		// misses — and a miss here is a FALSE PASS: the row runs with `--head ""`
		// and nothing dims it. printf %q is the one a real user writes, being the
		// obvious way to shell-quote without discovering `quote`. These four are why
		// sentinelFor is plain letters rather than the NUL-delimited string it reads
		// more safely as.
		{"survives printf %q", `gh pr create --head {{ printf "%q" .Session.Branch }}`, []string{"Session.Branch"}},
		{"survives quote", "gh pr create --head {{ quote .Session.Branch }}", []string{"Session.Branch"}},
		{"survives js", "gh pr create --head {{ js .Session.Branch }}", []string{"Session.Branch"}},
		{"survives urlquery", "gh pr create --head {{ urlquery .Session.Branch }}", []string{"Session.Branch"}},
		// The complement, and the reason the rule is "does the VALUE reach the
		// command" rather than "is the field mentioned": len puts a number in the
		// command, never the empty branch, so there is no hole to dim for.
		{"a field consumed without being emitted is not missing", "echo {{ len .Session.Branch }}", nil},
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

// MissingEnv closes the half of AC1 that MissingFields structurally cannot see.
//
// The two forms this package documents as interchangeable were not: MissingFields asks
// the template renderer which fields reached the output, so a $ATRIUM_* name the SHELL
// expands is invisible to it. `rm -rf {{ quote .Session.Worktree }}/build` was refused on
// a session with no worktree while `rm -rf "$ATRIUM_WORKTREE"/build` ran — as
// `rm -rf /build`.
func TestMissingEnv(t *testing.T) {
	// Every field populated except the worktree, which is what a session before Start
	// (or after a pause) looks like.
	ctx := Ctx{
		Session: SessionCtx{Title: "t", Name: "n", Branch: "b"},
		Repo:    RepoCtx{Path: "/repo", Name: "atrium"},
	}

	for _, tc := range []struct {
		name    string
		command string
		want    []string
	}{
		{"bare reference", `rm -rf "$ATRIUM_WORKTREE"/build`, []string{"Session.Worktree"}},
		{"braced reference", `rm -rf "${ATRIUM_WORKTREE}/build"`, []string{"Session.Worktree"}},
		{"at end of script", `cd $ATRIUM_WORKTREE`, []string{"Session.Worktree"}},
		{"unquoted", `ls $ATRIUM_WORKTREE`, []string{"Session.Worktree"}},
		{"populated fields are not reported", `git -C "$ATRIUM_REPO" log "$ATRIUM_BRANCH"`, nil},
		{"a name it does not carry", `echo "$HOME $PATH"`, nil},
		{"no variables at all", `just ci`, nil},
		// The prefix collision this is not strings.Contains for: ATRIUM_REPO is a prefix
		// of ATRIUM_REPO_NAME. Reported against an empty Repo.Path, a substring check
		// would refuse this command AND name the wrong field.
		{"a longer name that starts with a shorter one", `echo "$ATRIUM_REPO_NAME"`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := ok()
			e.Command = tc.command
			cmds, problems := Validate([]config.CustomCommand{e})
			require.Empty(t, problems)
			require.Len(t, cmds, 1)

			assert.ElementsMatch(t, tc.want, cmds[0].MissingEnv(ctx))
		})
	}
}

// TestMissingEnv_ReportsTheRightFieldOnAPrefixCollision is the other direction of the
// same trap: $ATRIUM_REPO_NAME must be reported when Repo.NAME is the empty one.
func TestMissingEnv_ReportsTheRightFieldOnAPrefixCollision(t *testing.T) {
	e := ok()
	e.Command = `echo "$ATRIUM_REPO_NAME" > "$ATRIUM_REPO/out"`
	cmds, problems := Validate([]config.CustomCommand{e})
	require.Empty(t, problems)

	// Only the name is empty.
	ctx := Ctx{Session: SessionCtx{Title: "t", Name: "n", Branch: "b", Worktree: "/wt"},
		Repo: RepoCtx{Path: "/repo"}}
	assert.Equal(t, []string{"Repo.Name"}, cmds[0].MissingEnv(ctx))

	// Only the path is empty.
	ctx = Ctx{Session: SessionCtx{Title: "t", Name: "n", Branch: "b", Worktree: "/wt"},
		Repo: RepoCtx{Name: "atrium"}}
	assert.Equal(t, []string{"Repo.Path"}, cmds[0].MissingEnv(ctx))
}

// TestMissingEnv_OverReportsRatherThanUnderReports documents the known limit, in the
// direction that is safe. A name the shell would never expand — single-quoted, or in a
// comment — still counts, because the alternative is implementing shell quoting and
// being wrong about it runs the command.
func TestMissingEnv_OverReportsRatherThanUnderReports(t *testing.T) {
	e := ok()
	e.Command = `echo '$ATRIUM_WORKTREE is not expanded here'`
	cmds, problems := Validate([]config.CustomCommand{e})
	require.Empty(t, problems)

	assert.Equal(t, []string{"Session.Worktree"},
		cmds[0].MissingEnv(Ctx{Session: SessionCtx{Title: "t", Name: "n", Branch: "b"},
			Repo: RepoCtx{Path: "/repo", Name: "atrium"}}),
		"a single-quoted name is over-reported on purpose: a false dim is visible and "+
			"carries its reason, where a false pass runs the command")
}

// TestEnvNamesMatchTheFieldTable is what keeps Env and MissingEnv from drifting: they
// must agree about which variable carries which field, or the emptiness gate covers a
// name the command never reads and misses the one it does.
func TestEnvNamesMatchTheFieldTable(t *testing.T) {
	// A context whose every value is its own path, so each exported pair is traceable.
	var ctx Ctx
	for _, f := range fieldAccess {
		f.set(&ctx, f.path)
	}

	got := Env(ctx)
	require.Len(t, got, len(fieldAccess))
	for _, f := range fieldAccess {
		assert.Containsf(t, got, f.env+"="+f.path,
			"Env must export %s for %s", f.env, f.path)
		assert.NotEmptyf(t, f.env, "%s has no environment variable", f.path)
		assert.Truef(t, strings.HasPrefix(f.env, "ATRIUM_"),
			"%s must be namespaced: %q", f.path, f.env)
	}
}

// TestParseOutput_AcceptsEveryDeclaredMode is the over-rejection guard the refusal
// table cannot be: every case there proves something is refused, and a mode that is
// silently refused ships as a feature nobody can reach.
//
// Case sensitivity is asserted rather than left implicit. The values are a closed set
// compared as strings, so "Terminal" is a typo — and a typo that validated would take
// over the user's terminal from an entry they wrote expecting a background run.
func TestParseOutput_AcceptsEveryDeclaredMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Output
	}{
		{"background", OutputBackground},
		{"terminal", OutputTerminal},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseOutput(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			// And through the whole validator, since that is what binds it.
			entry := ok()
			entry.Output = tc.in
			cmds, problems := Validate([]config.CustomCommand{entry})
			require.Empty(t, problems)
			require.Len(t, cmds, 1)
			assert.Equal(t, tc.want, cmds[0].Output)
		})
	}

	for _, bad := range []string{"Terminal", "BACKGROUND", "term", "tty", "popup"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			_, err := parseOutput(bad)
			assert.Error(t, err, "output is a closed set compared as a string")
		})
	}
}

// TestParseOutput_MessagesNameEveryMode is a claim guard, not a behaviour one.
//
// These two messages are the ONLY place a user is told what `output` may be — there is
// no default to fall back on, by design. Both were written when `background` was the
// only mode and named it as the whole set, so a user adding `terminal` correctly would
// have been told, by the required-field message, that the value they wanted did not
// exist. A message that is a stale enumeration of a set that has grown is exactly the
// claim defect nothing else here can see.
func TestParseOutput_MessagesNameEveryMode(t *testing.T) {
	// Derived from the type, so a third mode joins this guard by existing.
	modes := []Output{OutputBackground, OutputTerminal}

	_, missing := parseOutput("")
	require.Error(t, missing)
	_, unknown := parseOutput("popup")
	require.Error(t, unknown)

	for _, m := range modes {
		assert.Containsf(t, missing.Error(), string(m),
			"the required-output message must name %q — it is the only place the legal "+
				"values appear", m)
		assert.Containsf(t, unknown.Error(), string(m),
			"the unknown-output message must name %q", m)
	}
}
