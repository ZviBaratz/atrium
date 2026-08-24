package main

// cli_new_pins_test.go — the --model/--effort/--permission-mode pins: folded into
// the spooled program by agent.ComposeProgramFlags, the same composition the create
// form's submit runs, so what reaches the drain is a program string any running TUI
// already honors verbatim. What is asserted here is the CLI's own half: which
// members carry the pins, what is refused, and that the unpinned contract — spool
// "" and let the draining TUI choose — did not move.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPinsComposeIntoTheSpooledProgram: a pinned create with no --program or
// --profile resolves the configured default and spools the composed program — a pin
// cannot be folded into a choice deferred to the drain, so "" stops being an option
// the moment one is given.
func TestNewPinsComposeIntoTheSpooledProgram(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t),
		model: "opus", mode: "plan", effort: "xhigh",
	})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "claude --model opus --permission-mode plan --effort xhigh",
		entries[0].Request.Program)
}

// TestNewUnpinnedSpoolsTheEmptyProgram holds the pre-pin contract in place: with no
// pins and no --program, the spooled Program stays "", which is what makes a bare
// `atrium new` equivalent to pressing the new-session key on whatever program the
// draining TUI is configured with.
func TestNewUnpinnedSpoolsTheEmptyProgram(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].Request.Program)
}

// TestNewPinsRefuseANonClaudeSession: the create form renders no model/effort/mode
// fields for a non-claude profile, and the flag-shaped equivalent of "no field" is a
// refusal — not the silent unpin composing alone would produce. Nothing is spooled.
func TestNewPinsRefuseANonClaudeSession(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t),
		profile: "codex", model: "opus",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--model")
	assert.Contains(t, err.Error(), "codex --full-auto")
	assert.Empty(t, spooledCreates(t))
}

// TestNewPinsRideTheClaudeVariantsAlone mirrors the create form's mixed-batch
// contract: the pins land on the claude members and leave the codex member's
// program untouched, in spec order.
func TestNewPinsRideTheClaudeVariantsAlone(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	_, _, err := newSession(t, newRequest{
		title: "bake", path: repo,
		variants: "claude:1,codex:1", variantsSet: true,
		model: "opus", effort: "high",
	})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 2)
	assert.Equal(t, "claude --model opus --effort high", entries[0].Request.Program)
	assert.Equal(t, "codex --full-auto", entries[1].Request.Program)
}

// TestNewPinsRefuseAnAllNonClaudeFanOut: a batch none of whose members runs claude
// would carry the pins nowhere, so it is refused whole — before anything is spooled,
// like every other flag contradiction this command answers.
func TestNewPinsRefuseAnAllNonClaudeFanOut(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	_, _, err := newSession(t, newRequest{
		title: "bake", path: repo,
		variants: "codex:2", variantsSet: true,
		model: "opus",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--model")
	assert.Contains(t, err.Error(), "runs claude")
	assert.Empty(t, spooledCreates(t))
}

// TestNewPinsRejectAnInvalidValueBeforeSpooling: value validation is
// agent.ComposeProgramFlags' (tested exhaustively beside it); what this pins is the
// CLI consequence — the refusal happens before the spool, so a typo costs nothing.
func TestNewPinsRejectAnInvalidValueBeforeSpooling(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), effort: "ultracode",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid effort level")
	assert.Empty(t, spooledCreates(t))
}
