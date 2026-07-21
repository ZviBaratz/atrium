package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inst(title, path string) session.InstanceData {
	return session.InstanceData{Title: title, Path: path, Program: "claude"}
}

// TestResolveSessionExactTitle is the common case: the selector names a session.
func TestResolveSessionExactTitle(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{
		inst("fix-auth", "/repo/web"),
		inst("add-cache", "/repo/web"),
	}, "add-cache", "")
	require.NoError(t, err)
	assert.Equal(t, "add-cache", got.Title)
}

// TestResolveSessionExactBeatsSubstring pins the tiering. With sessions "api"
// and "api-v2" present, the selector "api" is an exact name, not an ambiguous
// prefix — collapsing the tiers into one substring pass would make it ambiguous
// and leave the user unable to address "api" at all.
func TestResolveSessionExactBeatsSubstring(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{
		inst("api", "/repo/svc"),
		inst("api-v2", "/repo/svc"),
	}, "api", "")
	require.NoError(t, err)
	assert.Equal(t, "api", got.Title)
}

// TestResolveSessionExactCaseWinsAcrossRepos is why the exact tier is
// case-sensitive and separate from the case-insensitive one. Titles that differ
// only in case cannot coexist in one repo group (DerivedNamesCollide folds case)
// but can across two, and a case-insensitive-only match would call that
// ambiguous. The property this protects: a title copied verbatim out of
// `ls --json` always resolves, which is what a script fanning out relies on.
func TestResolveSessionExactCaseWinsAcrossRepos(t *testing.T) {
	instances := []session.InstanceData{
		inst("API", "/repo/web"),
		inst("api", "/repo/svc"),
	}
	got, err := resolveSession(instances, "api", "")
	require.NoError(t, err)
	assert.Equal(t, "/repo/svc", got.Path)

	got, err = resolveSession(instances, "API", "")
	require.NoError(t, err)
	assert.Equal(t, "/repo/web", got.Path)
}

// TestResolveSessionCaseInsensitiveExactBeatsSubstring is what makes the
// case-insensitive tier load-bearing. With "api" and "api-v2" present, selector
// "API" matches "api" exactly bar case; without that tier it would fall through
// to the substring pass, match both, and be reported ambiguous.
func TestResolveSessionCaseInsensitiveExactBeatsSubstring(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{
		inst("api", "/repo/svc"),
		inst("api-v2", "/repo/svc"),
	}, "API", "")
	require.NoError(t, err)
	assert.Equal(t, "api", got.Title)
}

// TestResolveSessionUniqueSubstring keeps the convenience of a short selector
// when it is unambiguous.
func TestResolveSessionUniqueSubstring(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{
		inst("fix-auth", "/repo/web"),
		inst("add-cache", "/repo/web"),
	}, "cach", "")
	require.NoError(t, err)
	assert.Equal(t, "add-cache", got.Title)
}

// TestResolveSessionCaseInsensitive: selectors typed by hand should not have to
// match the stored capitalization.
func TestResolveSessionCaseInsensitive(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{inst("Fix-Auth", "/repo/web")}, "fix-auth", "")
	require.NoError(t, err)
	assert.Equal(t, "Fix-Auth", got.Title)
}

// TestResolveSessionByDisplayName: the list shows the cosmetic label, so that is
// what a user reads off the screen and types back.
func TestResolveSessionByDisplayName(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.DisplayName = "the auth fix"
	got, err := resolveSession([]session.InstanceData{d, inst("other", "/repo/web")}, "the auth fix", "")
	require.NoError(t, err)
	assert.Equal(t, "fix-auth", got.Title)
}

// TestResolveSessionByTmuxName lets scripts address the globally unique handle
// that `ls --json` hands them.
func TestResolveSessionByTmuxName(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	got, err := resolveSession([]session.InstanceData{d, inst("other", "/repo/svc")}, "atrium_web_fix-auth", "")
	require.NoError(t, err)
	assert.Equal(t, "fix-auth", got.Title)
}

// TestResolveSessionAmbiguousAcrossRepos is the case that forces (Title, Path)
// identity: titles are unique only within a repo group, so the same title in two
// repos must be reported rather than silently resolved to whichever came first.
func TestResolveSessionAmbiguousAcrossRepos(t *testing.T) {
	_, err := resolveSession([]session.InstanceData{
		inst("api", "/repo/web"),
		inst("api", "/repo/svc"),
	}, "api", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/repo/web", "the error must name every candidate's path")
	assert.Contains(t, err.Error(), "/repo/svc")
	assert.Contains(t, err.Error(), "--path", "and point at the way to disambiguate")
}

// TestResolveSessionPathDisambiguates is the other half of that contract.
func TestResolveSessionPathDisambiguates(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{
		inst("api", "/repo/web"),
		inst("api", "/repo/svc"),
	}, "api", "/repo/svc")
	require.NoError(t, err)
	assert.Equal(t, "/repo/svc", got.Path)
}

// TestResolveSessionPathTolerantOfTrailingSlash: a path pasted from a shell
// completion carries a trailing separator.
func TestResolveSessionPathTolerantOfTrailingSlash(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{
		inst("api", "/repo/web"),
		inst("api", "/repo/svc"),
	}, "api", "/repo/svc/")
	require.NoError(t, err)
	assert.Equal(t, "/repo/svc", got.Path)
}

// TestResolveSessionEmptyPathIsNotCwd guards a filepath.Clean trap: Clean("")
// returns ".", so cleaning an unset --path would filter every session against
// the literal path "." and match nothing.
func TestResolveSessionEmptyPathIsNotCwd(t *testing.T) {
	got, err := resolveSession([]session.InstanceData{inst("only", "/repo/web")}, "only", "")
	require.NoError(t, err)
	assert.Equal(t, "only", got.Title)
}

// TestResolveSessionUnknown names the selector back so a typo is obvious.
func TestResolveSessionUnknown(t *testing.T) {
	_, err := resolveSession([]session.InstanceData{inst("fix-auth", "/repo/web")}, "nope", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nope"`)
}

// TestResolveSessionUnknownPath reports the path filter rather than pretending
// the session does not exist at all.
func TestResolveSessionUnknownPath(t *testing.T) {
	_, err := resolveSession([]session.InstanceData{inst("api", "/repo/web")}, "api", "/repo/nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/repo/nope")
}

// TestResolveSessionNoInstances: an empty store deserves a different message
// than a bad selector.
func TestResolveSessionNoInstances(t *testing.T) {
	_, err := resolveSession(nil, "anything", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sessions")
}

// TestResolveSessionEmptySelector rejects the empty string rather than letting
// it substring-match every session and report an ambiguity.
func TestResolveSessionEmptySelector(t *testing.T) {
	_, err := resolveSession([]session.InstanceData{inst("only", "/repo/web")}, "", "")
	require.Error(t, err)
}

// TestLoadStoredInstancesEmptyState: a data dir with no state.json yet is not an
// error, it is a fleet with no sessions.
func TestLoadStoredInstancesEmptyState(t *testing.T) {
	sandboxDataDir(t)
	got, err := loadStoredInstances()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestLoadStoredInstancesReadsState covers the headless read path: it must not
// go through Storage.LoadInstances, which reattaches to live tmux.
func TestLoadStoredInstancesReadsState(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"), inst("add-cache", "/repo/svc"))

	got, err := loadStoredInstances()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "fix-auth", got[0].Title)
}

// TestLoadStoredInstancesCreatesNothing is the read-only guarantee at its
// weakest point. config.LoadState() would create state.json from defaults here;
// a headless command must leave a data dir it has only read from untouched.
func TestLoadStoredInstancesCreatesNothing(t *testing.T) {
	dir := sandboxDataDir(t)

	got, err := loadStoredInstances()
	require.NoError(t, err)
	assert.Empty(t, got)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "reading an empty data dir must not write to it")
}

// TestLoadStoredInstancesLeavesInFlightTempFiles is the sharper half of the same
// rule. config.LoadState() sweeps orphaned "<file>.tmp-*" files on every load —
// but an atomic write in progress is indistinguishable from an orphan, so
// sweeping deletes a live TUI's temp and makes its rename fail, silently losing
// that save. `atrium ls` in a watch loop would do this routinely.
func TestLoadStoredInstancesLeavesInFlightTempFiles(t *testing.T) {
	dir := sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	// Stand in for another process mid-writeFileAtomic.
	inFlight := filepath.Join(dir, "."+config.StateFileName+".tmp-123456")
	require.NoError(t, os.WriteFile(inFlight, []byte(`{"instances":[]}`), 0o600))

	_, err := loadStoredInstances()
	require.NoError(t, err)

	assert.FileExists(t, inFlight, "another process's in-flight atomic write must survive our read")
}

// TestLoadStoredInstancesDoesNotQuarantineCorruptState: config.LoadState()
// renames an unparseable file to <file>.corrupt. A headless reader must report
// the problem, not move the user's state aside.
func TestLoadStoredInstancesDoesNotQuarantineCorruptState(t *testing.T) {
	dir := sandboxDataDir(t)
	statePath := filepath.Join(dir, config.StateFileName)
	require.NoError(t, os.WriteFile(statePath, []byte("{not json"), 0o644))

	_, err := loadStoredInstances()
	require.Error(t, err)

	assert.FileExists(t, statePath, "the state file must stay where it is")
	assert.NoFileExists(t, statePath+".corrupt", "a reader must not quarantine")
}

// TestLoadStoredInstancesIgnoresUnknownStateFields: state.json also carries UI
// state a headless command has no business parsing, and must not fail on.
func TestLoadStoredInstancesIgnoresUnknownStateFields(t *testing.T) {
	dir := sandboxDataDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, config.StateFileName),
		[]byte(`{"instances":[{"title":"a","path":"/p"}],"some_future_field":{"nested":1}}`), 0o644))

	got, err := loadStoredInstances()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Title)
}
