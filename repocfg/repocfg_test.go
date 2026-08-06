package repocfg

import (
	"reflect"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProbePopulatesEveryContextLeaf is what keeps validation's one render honest.
// A template is refused only when rendering it against the probe fails, so a leaf
// the probe leaves empty turns "this placeholder does not exist" into "this session
// happens to have no branch" — and a real typo would sail through. Derived from the
// type by reflection so adding a leaf (a port, say) fails here rather than silently
// widening the hole.
func TestProbePopulatesEveryContextLeaf(t *testing.T) {
	outer := reflect.ValueOf(probe)
	for i := 0; i < outer.NumField(); i++ {
		group := outer.Field(i)
		require.Equalf(t, reflect.Struct, group.Kind(), "Ctx.%s must be a struct of string leaves", outer.Type().Field(i).Name)
		for j := 0; j < group.NumField(); j++ {
			leaf := group.Field(j)
			require.Equalf(t, reflect.String, leaf.Kind(), "Ctx.%s.%s must be a string",
				outer.Type().Field(i).Name, group.Type().Field(j).Name)
			assert.NotEmptyf(t, leaf.String(), "probe leaves Ctx.%s.%s empty",
				outer.Type().Field(i).Name, group.Type().Field(j).Name)
		}
	}
}

// ok is a minimal valid entry the rule tests mutate one field of at a time, so a
// failure names the rule under test rather than an unrelated omission.
func ok() config.RepoScript {
	return config.RepoScript{Name: "web", SetupScript: "npm ci"}
}

func TestValidate_AcceptsAWellFormedEntry(t *testing.T) {
	got, problems := Validate([]config.RepoScript{ok()})

	require.Empty(t, problems)
	require.Len(t, got, 1)
	assert.Equal(t, "web", got[0].Name)
	assert.True(t, got[0].HasSetupScript())
}

// An entry that configures nothing is not an error — it is dead weight, and saying
// so is the difference between "your script did not run" and silence.
func TestValidate_RejectsAnEntryThatConfiguresNothing(t *testing.T) {
	entry := ok()
	entry.SetupScript = ""

	got, problems := Validate([]config.RepoScript{entry})

	require.Empty(t, got)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "configures nothing")
}

func TestValidate_RejectsMalformedEntries(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*config.RepoScript)
		wantMsg string
	}{
		{"unparsable setup template", func(r *config.RepoScript) { r.SetupScript = "npm ci {{ .Session" }, "template"},
		{"unknown placeholder", func(r *config.RepoScript) { r.SetupScript = "npm ci {{.Session.Wortree}}" }, "Wortree"},
		{"unknown top-level field", func(r *config.RepoScript) { r.SetupScript = "npm ci {{.Worktree}}" }, "Worktree"},
		{"empty env name", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"": "x"} }, "environment variable name"},
		{"env name with a dash", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"GO-CACHE": "x"} }, "environment variable name"},
		{"env name starting with a digit", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"1CACHE": "x"} }, "environment variable name"},
		// Atrium's own names are refused rather than silently won or lost. A session_env
		// entry named ATRIUM_SESSION would fight the value tmux injects for the hook
		// plumbing, and which of the two a session ended up with would depend on how
		// tmux resolves a repeated -e.
		{"reserved atrium name", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"ATRIUM_SESSION": "x"} }, "reserved"},
		{"reserved atrium prefix", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"ATRIUM_ANYTHING": "x"} }, "reserved"},
		{"reserved claude config dir", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"CLAUDE_CONFIG_DIR": "x"} }, "reserved"},
		{"reserved gh config dir", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"GH_CONFIG_DIR": "x"} }, "reserved"},
		{"unparsable env template", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"CACHE": "{{ .Session"} }, "template"},
		{"unknown placeholder in env", func(r *config.RepoScript) { r.SessionEnv = map[string]string{"CACHE": "{{.Session.Wortree}}"} }, "Wortree"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := ok()
			tc.mutate(&entry)

			got, problems := Validate([]config.RepoScript{entry})

			require.Empty(t, got, "a rejected entry must be dropped, not applied")
			require.Len(t, problems, 1)
			assert.Contains(t, problems[0].Error(), tc.wantMsg)
		})
	}
}

// A config with one bad entry still yields every good one: Validate reports its two
// results independently, because LoadConfig cannot fail and a broken entry must not
// take the rest of the file down with it.
func TestValidate_KeepsGoodEntriesAlongsideBadOnes(t *testing.T) {
	bad := ok()
	bad.Name = "broken"
	bad.SetupScript = "{{ .Session"

	got, problems := Validate([]config.RepoScript{bad, ok()})

	require.Len(t, got, 1)
	assert.Equal(t, "web", got[0].Name)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "broken", "a problem must name the entry it refused")
}

// The index is in the problem even for an unnamed entry, which is the only handle a
// user has on a nameless row in their config.
func TestProblem_NamesTheEntryByIndexWhenItHasNoName(t *testing.T) {
	bad := ok()
	bad.Name = ""
	bad.SetupScript = "{{ .Session"

	_, problems := Validate([]config.RepoScript{ok(), bad})

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "repo_scripts[1]")
}

func TestScript_RendersTheSetupScriptAgainstTheSession(t *testing.T) {
	entry := ok()
	entry.SetupScript = "install {{.Session.Worktree}}"
	got, problems := Validate([]config.RepoScript{entry})
	require.Empty(t, problems)
	require.Len(t, got, 1)

	script, err := got[0].RenderSetup(Ctx{Session: SessionCtx{Worktree: "/wt"}})

	require.NoError(t, err)
	assert.Equal(t, "install /wt", script)
}

func TestScript_RendersSessionEnvAsNameEqualsValue(t *testing.T) {
	entry := ok()
	entry.SessionEnv = map[string]string{"CACHE": "/tmp/{{.Session.Name}}"}
	got, problems := Validate([]config.RepoScript{entry})
	require.Empty(t, problems)
	require.Len(t, got, 1)

	env, err := got[0].RenderEnv(Ctx{Session: SessionCtx{Name: "web-1"}})

	require.NoError(t, err)
	assert.Equal(t, []string{"CACHE=/tmp/web-1"}, env)
}

// Rendered env is sorted by name, so the environment a session gets and the
// `new-session -e` argv it is launched with are the same on every run of one config
// rather than following Go's map iteration order.
func TestScript_RendersSessionEnvInAStableOrder(t *testing.T) {
	entry := ok()
	entry.SessionEnv = map[string]string{"B": "2", "A": "1", "C": "3"}
	got, problems := Validate([]config.RepoScript{entry})
	require.Empty(t, problems)
	require.Len(t, got, 1)

	env, err := got[0].RenderEnv(Ctx{})

	require.NoError(t, err)
	assert.Equal(t, []string{"A=1", "B=2", "C=3"}, env)
}

// The `quote` helper the README points a user at, which is also the only escaping this
// section has: {{.Session.Name}} is the freely-mutable display name, so a session
// renamed to `x; rm -rf ~` is a shell injection into the user's own setup script
// wherever it is interpolated bare. Without the FuncMap the template does not even
// parse, and the entry is dropped for a reason the user is told to write.
func TestScript_QuoteEscapesAValueForTheShell(t *testing.T) {
	entry := ok()
	entry.SetupScript = "mkdir -p /tmp/c-{{ quote .Session.Name }}"

	got, problems := Validate([]config.RepoScript{entry})
	require.Empty(t, problems, "the function the docs name must exist")
	require.Len(t, got, 1)

	script, err := got[0].RenderSetup(Ctx{Session: SessionCtx{Name: "x; rm -rf ~"}})

	require.NoError(t, err)
	assert.Equal(t, `mkdir -p /tmp/c-'x; rm -rf ~'`, script)
}

// session_env values render through the same helper set, so a value carrying a quote is
// escapable there too.
func TestScript_QuoteIsAvailableInSessionEnv(t *testing.T) {
	entry := ok()
	entry.SessionEnv = map[string]string{"LABEL": "{{ quote .Session.Name }}"}

	got, problems := Validate([]config.RepoScript{entry})
	require.Empty(t, problems)
	require.Len(t, got, 1)

	env, err := got[0].RenderEnv(Ctx{Session: SessionCtx{Name: "it's fine"}})

	require.NoError(t, err)
	assert.Equal(t, []string{`LABEL='it'"'"'s fine'`}, env)
}

// ValidateOne carries the entry's real position, which is the only handle a message
// about an unnamed entry has. Validating a one-element slice instead reported every
// problem as repo_scripts[0] and pointed the user at whichever entry came first.
func TestValidateOne_ReportsTheEntrysRealPosition(t *testing.T) {
	broken := config.RepoScript{SetupScript: "npm ci {{.Session.Wortree}}"}

	script, problem := ValidateOne(3, broken)

	require.NotNil(t, problem)
	assert.False(t, script.HasSetupScript())
	assert.Contains(t, problem.Error(), "repo_scripts[3]")
}

// And nothing to report for a good entry, whatever its position.
func TestValidateOne_ReportsNothingForAValidEntry(t *testing.T) {
	script, problem := ValidateOne(2, ok())

	assert.Nil(t, problem)
	assert.True(t, script.HasSetupScript())
}
