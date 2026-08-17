package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// driveAgentHarness sources scripts/drive-agent.sh and stubs everything that would touch
// tmux or the filesystem outside t.TempDir(), so a verb's ARGUMENT handling can be run for
// real without a live server.
//
// load_run is stubbed to do what the real one does and nothing else — source meta.env —
// because the ordering between that read and the argument defaults is exactly what is under
// test. Stubbing it to a no-op with the globals pre-set would make this test pass under the
// bug it exists for: `fresh <width>` defaulted its new height from $HEIGHT while $HEIGHT was
// still the empty string the script declares at the top, and only load_run fills it in.
//
// Sourcing works because the script's `main "$@"` runs with no arguments here, which resolves
// to `help` — it prints and returns rather than exiting, leaving every function defined.
const driveAgentHarness = `
set -- ; source "$SCRIPT" >/dev/null 2>&1
RUN="$TMP"
load_run() { source "$RUN/meta.env"; }
reap() { :; }
new_workspace() { mkdir -p "$1"; }
start_session() { PANE=%9; WINDOW=@9; }
note() { :; }
assert_under_run_root() { :; }
`

func runDriveAgent(t *testing.T, script, tmp, snippet string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", driveAgentHarness+snippet)
	cmd.Env = append(os.Environ(), "SCRIPT="+script, "TMP="+tmp)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeMetaEnv seeds a run directory the way `up` leaves one.
func writeMetaEnv(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.env"), []byte(strings.Join([]string{
		"PROGRAM=gemini", "BIN=gemini", "VERSION=0.55.1", "CAPTURED=2026-08-17",
		"WIDTH=120", "HEIGHT=40", "PANE=%1", "WINDOW=@1", "",
	}, "\n")), 0o644))
}

// `fresh <width>` — the documented form, the one the cost-saver section tells you to reach
// for, and the ONLY way to capture a once-per-path gate (a trust dialog) at a second width.
//
// It regressed the moment `fresh` grew a height argument: `height="${2:-$HEIGHT}"` was
// evaluated sixteen lines above the load_run that populates $HEIGHT, so the default was the
// empty string and the verb died on its own numeric validation with "must be numbers, got:
// 40 " — for every invocation, including the bare `fresh`. `bash -n` and shellcheck both pass
// on it, which is why this runs the verb instead of parsing it.
func TestDriveAgentFreshDefaultsHeightFromTheRun(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	tmp := t.TempDir()
	writeMetaEnv(t, tmp)

	for _, tc := range []struct {
		name, call     string
		wantW, wantH   string
		wantWorkdirDir string
	}{
		{"width only inherits the run's height", `cmd_fresh 45`, "45", "40", "fresh45"},
		{"bare fresh inherits both", `cmd_fresh`, "120", "40", "fresh120"},
		{"explicit height at the run's own value keeps the bare name", `cmd_fresh 45 40`, "45", "40", "fresh45"},
		{"a differing height gets its own directory", `cmd_fresh 45 19`, "45", "19", "fresh45x19"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMetaEnv(t, dir)
			out, err := runDriveAgent(t, script, dir, tc.call+`
echo "W=$WIDTH H=$HEIGHT"
ls "$RUN" | tr '\n' ' '`)
			require.NoErrorf(t, err, "%s must succeed; output:\n%s", tc.call, out)
			require.Contains(t, out, "W="+tc.wantW+" H="+tc.wantH,
				"the globals must carry the geometry the session was actually started at:\n%s", out)
			require.Contains(t, out, tc.wantWorkdirDir,
				"the workdir name is what a capture path is labelled with:\n%s", out)

			// meta.env must agree with the globals, or every LATER verb aims at the old
			// session: `ladder` re-asserts -y "$HEIGHT" per rung and returns to -x "$WIDTH".
			meta, readErr := os.ReadFile(filepath.Join(dir, "meta.env"))
			require.NoError(t, readErr)
			require.Contains(t, string(meta), "WIDTH="+tc.wantW)
			require.Contains(t, string(meta), "HEIGHT="+tc.wantH)
		})
	}
}

// Leading zeros. `((dim > 0))` reads them as OCTAL and tmux reads the same string as decimal,
// so `fresh 010` used to validate as 8, name its workdir fresh010 and size the window at 10 —
// a capture labelled with a geometry it was not taken at, which is the one failure this script
// exists to prevent. `fresh 08` was worse: bash printed "value too great for base" and then
// died with the wrong message ("must be greater than zero", for 8).
func TestDriveAgentFreshCanonicalisesLeadingZeros(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	for _, tc := range []struct {
		name, call, wantW, wantH, wantDir string
	}{
		{"octal-looking width", `cmd_fresh 010`, "10", "40", "fresh10"},
		{"8 and 9 are not octal digits at all", `cmd_fresh 08`, "8", "40", "fresh8"},
		{"a padded height equal to the run's must not fork a directory", `cmd_fresh 45 040`, "45", "40", "fresh45"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMetaEnv(t, dir)
			out, err := runDriveAgent(t, script, dir, tc.call+`
echo "W=$WIDTH H=$HEIGHT"
ls "$RUN" | tr '\n' ' '`)
			require.NoErrorf(t, err, "%s must not die on base-8 arithmetic; output:\n%s", tc.call, out)
			require.Contains(t, out, "W="+tc.wantW+" H="+tc.wantH, "output:\n%s", out)
			require.Contains(t, out, tc.wantDir,
				"the workdir must be named for the DECIMAL geometry tmux will be given:\n%s", out)
			require.NotContains(t, out, "value too great for base", "output:\n%s", out)
		})
	}
}

// The validation still fails loudly, and still BEFORE the reap — a bad value must not cost a
// dialog that took an API turn to reach.
func TestDriveAgentFreshRejectsNonNumericGeometry(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	for _, tc := range []struct{ name, call, want string }{
		{"path traversal in the width", `cmd_fresh ../../etc`, "must be numbers"},
		{"path traversal in the height", `cmd_fresh 45 ../../etc`, "must be numbers"},
		{"zero width", `cmd_fresh 0`, "greater than zero"},
		{"zero height", `cmd_fresh 45 0`, "greater than zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMetaEnv(t, dir)
			out, err := runDriveAgent(t, script, dir, `reap() { echo REAPED; }
`+tc.call)
			require.Error(t, err, "a bad geometry must be fatal; output:\n%s", out)
			require.Contains(t, out, tc.want, "output:\n%s", out)
			require.NotContains(t, out, "REAPED",
				"validation must run BEFORE the session is reaped, or a typo throws away a "+
					"dialog that cost an API turn to reach")
		})
	}
}

// meta.env is rewritten by `sed`, and a substitution that matches nothing exits 0. A run
// directory written by an older build of the script is missing the key, the rewrite is a
// silent no-op, and every later verb aims at the old value.
func TestDriveAgentFreshFailsLoudlyOnAStaleMetaEnv(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	dir := t.TempDir()
	// Everything but HEIGHT — the field this change made load-bearing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.env"), []byte(strings.Join([]string{
		"PROGRAM=gemini", "WIDTH=120", "PANE=%1", "WINDOW=@1", "",
	}, "\n")), 0o644))

	out, err := runDriveAgent(t, script, dir, `cmd_fresh 45 19`)
	require.Error(t, err, "a meta.env the rewrite cannot land in must be fatal; output:\n%s", out)
	require.Contains(t, out, "meta.env did not take", "output:\n%s", out)
}
