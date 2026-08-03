package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCustomCommands_SilentWhenEveryEntryIsValid(t *testing.T) {
	cfg := &config.Config{CustomCommands: []config.CustomCommand{
		{Key: "g", Description: "lazygit here", Command: "lazygit", Output: "background"},
	}}

	assert.Empty(t, CheckCustomCommands(cfg))
	assert.Equal(t, "", RenderCustomCommands(nil))
}

// TestCheckCustomCommands_ReportsEachRejectedEntry is the CLI half of "rejected at
// load with a message naming both parties": doctor is where a user checks why the
// command they configured is not in the menu.
func TestCheckCustomCommands_ReportsEachRejectedEntry(t *testing.T) {
	cfg := &config.Config{CustomCommands: []config.CustomCommand{
		{Key: "g", Description: "lazygit here", Command: "lazygit", Output: "background"},
		{Key: "g", Description: "just ci", Command: "just ci", Output: "background"},
		{Key: "t", Description: "typo", Command: "echo {{.Session.Wortree}}", Output: "background"},
		{Key: "n", Description: "no mode", Command: "echo hi"},
	}}

	warns := CheckCustomCommands(cfg)

	require.Len(t, warns, 3)
	out := RenderCustomCommands(warns)
	assert.Contains(t, out, "Custom commands:")
	assert.Contains(t, out, "just ci", "the duplicate names the rejected entry")
	assert.Contains(t, out, "lazygit here", "and the entry it collides with")
	assert.Contains(t, out, "Wortree", "the template typo is quoted back")
	assert.Contains(t, out, "output is required")
	assert.Equal(t, 3, strings.Count(out, "⚠"), "one warning glyph per rejected entry")
}

// TestRenderCustomCommands_MatchesThePoolsSectionShape keeps doctor's output one
// voice: every section is a title line then two-space-indented ⚠ rows.
func TestRenderCustomCommands_MatchesThePoolsSectionShape(t *testing.T) {
	out := RenderCustomCommands(CheckCustomCommands(&config.Config{
		CustomCommands: []config.CustomCommand{{Key: "g", Description: "d", Command: "c"}},
	}))

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "Custom commands:", lines[0])
	assert.True(t, strings.HasPrefix(lines[1], "  ⚠ "), "got %q", lines[1])
}
