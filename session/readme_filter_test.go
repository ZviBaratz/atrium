package session

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/require"
)

// TestReadmeFilterExamples runs the README Filtering section's worked examples
// verbatim through the real parser (#382). Keep these query strings identical to
// the README; if the parser's syntax changes, this fails and the docs must move
// with it.
func TestReadmeFilterExamples(t *testing.T) {
	needsDirty := newFilterInstance(t, "needs-and-dirty", "feat/x")
	needsDirty.SetStatus(NeedsInput)
	needsDirty.SetDiffStats(&git.DiffStats{Dirty: true})

	readyClean := newFilterInstance(t, "ready-clean", "feat/y")
	readyClean.SetStatus(Ready)
	readyClean.SetDiffStats(&git.DiffStats{Dirty: false})

	behindOpen := newFilterInstance(t, "behind-open", "feat/z")
	behindOpen.SetDiffStats(&git.DiffStats{Behind: 2})
	behindOpen.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN"})

	workRelease := newFilterInstance(t, "prep", "feat/w")
	workRelease.SetClaudeAccount("work", "", false)
	workRelease.SetNote("release checklist")

	authNamed := newFilterInstance(t, "auth flow", "feat/login")

	// `status:need dirty` — needs input AND uncommitted changes.
	require.True(t, ParseFilter("status:need dirty").Matches(needsDirty))
	require.False(t, ParseFilter("status:need dirty").Matches(readyClean))

	// `behind:>0 pr:open` — behind base AND an open PR.
	require.True(t, ParseFilter("behind:>0 pr:open").Matches(behindOpen))
	require.False(t, ParseFilter("behind:>0 pr:open").Matches(readyClean))

	// `account:work note:release` — work account AND note prefixed "release".
	require.True(t, ParseFilter("account:work note:release").Matches(workRelease))
	require.False(t, ParseFilter("account:work note:release").Matches(needsDirty))

	// `effort:max dirty` — max effort AND uncommitted changes.
	maxDirty := newFilterInstance(t, "hard-refactor", "feat/v")
	maxDirty.SetEffortMeta("max")
	maxDirty.SetDiffStats(&git.DiffStats{Dirty: true})
	require.True(t, ParseFilter("effort:max dirty").Matches(maxDirty))
	require.False(t, ParseFilter("effort:max dirty").Matches(readyClean))

	// `model:opus behind` — opus family AND behind base.
	opusBehind, err := NewInstance(InstanceOptions{Title: "opus-behind", Path: "/tmp/repoR", Program: "claude"})
	require.NoError(t, err)
	opusBehind.modelID = "claude-opus-4-8"
	opusBehind.SetDiffStats(&git.DiffStats{Behind: 1})
	require.True(t, ParseFilter("model:opus behind").Matches(opusBehind))
	require.False(t, ParseFilter("model:opus behind").Matches(readyClean))

	// `mode:plan status:need` — planning sessions AND waiting on you.
	planNeeds := newFilterInstance(t, "spec-work", "feat/u")
	planNeeds.SetModeMeta("plan")
	planNeeds.SetStatus(NeedsInput)
	require.True(t, ParseFilter("mode:plan status:need").Matches(planNeeds))
	require.False(t, ParseFilter("mode:plan status:need").Matches(readyClean))

	// `auth` — plain substring in name/branch/note.
	require.True(t, ParseFilter("auth").Matches(authNamed))
	require.False(t, ParseFilter("auth").Matches(readyClean))
}
