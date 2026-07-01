package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/require"
)

// TestAssembleHomeWiring drives the pure assembler directly — something the old
// newHome could not support, because it loaded config/state/storage and
// os.Exit'd on failure. It asserts the model is wired from the injected inputs
// (no disk IO): scalar fields, the UI components, listRatio from state, and that
// loaded instances land in the list with autoYes propagated.
func TestAssembleHomeWiring(t *testing.T) {
	cfg := config.DefaultConfig()
	st := config.DefaultState()

	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   "wired",
		Path:    t.TempDir(),
		Program: "claude",
	})
	require.NoError(t, err)

	h := assembleHome(context.Background(), "claude", true, "v1.2.3", "atr", cfg, st, storage, []*session.Instance{inst})

	require.NotNil(t, h)
	require.Equal(t, stateDefault, h.state)
	require.Same(t, storage, h.storage)
	require.Same(t, cfg, h.appConfig)
	require.Equal(t, "v1.2.3", h.version)
	require.Equal(t, "atr", h.binName)
	require.True(t, h.autoYes)
	require.NotNil(t, h.list)
	require.NotNil(t, h.menu)
	require.NotNil(t, h.tabbedWindow)
	require.NotNil(t, h.errBox)
	require.Equal(t, st.GetListRatio(), h.listRatio)
	require.Len(t, h.list.GetInstances(), 1, "the injected instance should be added to the list")
	require.True(t, inst.AutoYes, "autoYes should propagate to loaded instances")
}

// TestNewHomeReturnsErrorInsteadOfExiting proves the loader surfaces a load
// failure as an error rather than the os.Exit(1) it used to call — which would
// have aborted the test binary, so this behavior was untestable before the
// split. HOME points at a fresh config dir holding a state.json that parses as
// State but whose instances payload is a JSON object (not a []InstanceData), so
// Storage.LoadInstances fails to unmarshal it.
func TestNewHomeReturnsErrorInsteadOfExiting(t *testing.T) {
	// newHome mutates the process-global theme; restore it after the test.
	defer theme.Set(config.DefaultConfig().Theme)()

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, config.StateFileName),
		[]byte(`{"instances": {"not":"an array"}}`), 0o644))

	_, err = newHome(context.Background(), "claude", false, "v", "atr")
	require.Error(t, err)
}
