package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckRepoScripts_ReportsRefusedEntries(t *testing.T) {
	cfg := &config.Config{RepoScripts: []config.RepoScript{
		{Name: "web", SetupScript: "npm ci"},
		{Name: "broken", SetupScript: "npm ci {{.Session.Wortree}}"},
	}}

	problems := CheckRepoScripts(cfg)

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "broken")
}

func TestCheckRepoScripts_SaysNothingAboutAValidSection(t *testing.T) {
	cfg := &config.Config{RepoScripts: []config.RepoScript{{Name: "web", SetupScript: "npm ci"}}}

	assert.Empty(t, CheckRepoScripts(cfg))
	assert.Empty(t, CheckRepoScripts(&config.Config{}))
	assert.Empty(t, CheckRepoScripts(nil))
}

func TestRenderRepoScripts(t *testing.T) {
	problems := CheckRepoScripts(&config.Config{RepoScripts: []config.RepoScript{
		{Name: "broken", SetupScript: "{{.Session"},
	}})

	out := RenderRepoScripts(problems)

	assert.True(t, strings.HasPrefix(out, "Repo scripts:\n"), "got %q", out)
	assert.Contains(t, out, "⚠")
	assert.Contains(t, out, "broken")
	assert.Empty(t, RenderRepoScripts(nil))
}
