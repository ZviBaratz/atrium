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

// driveAgentTmuxHarness lets start_session RUN, and records the arguments tmux_boot was
// called with. driveAgentHarness above stubs start_session wholesale, which is right for the
// argument-defaulting tests it serves and useless here: the question ATR_CAP_ENV raises is
// what reaches `new-session` argv, and a stub that never builds argv cannot answer it.
//
// `pane` must echo something non-empty or start_session's paint-wait loop spins for 30s.
const driveAgentTmuxHarness = `
set -- ; source "$SCRIPT" >/dev/null 2>&1
RUN="$TMP"
SOCK="$TMP/tmux/sock"
SESSION=cap
load_run() { source "$RUN/meta.env"; }
tmux_boot() { for a in "$@"; do printf '[%s]\n' "$a"; done >>"$TMP/argv.log"; }
t() { :; }
assert_render_conf_applied() { :; }
resolve_ids() { PANE=%9; WINDOW=@9; }
assert_geometry() { :; }
settle() { :; }
pane() { printf 'painted\n'; }
reap() { :; }
new_workspace() { mkdir -p "$1"; }
note() { :; }
die() { printf '%s\n' "$*" >&2; exit 1; }
assert_under_run_root() { :; }
`

func runDriveAgentTmux(t *testing.T, script, tmp, capEnv, snippet string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", driveAgentTmuxHarness+snippet)
	cmd.Env = append(os.Environ(), "SCRIPT="+script, "TMP="+tmp)
	if capEnv != "" {
		cmd.Env = append(cmd.Env, "ATR_CAP_ENV="+capEnv)
	}
	out, err := cmd.CombinedOutput()
	argv, _ := os.ReadFile(filepath.Join(tmp, "argv.log"))
	return string(out), string(argv), err
}

// The load-bearing property: each NAME=VALUE reaches `new-session` as its own -e pair, and a
// value containing a space stays ONE argument. Asserted on the recorded argv element by
// element ([%s] per element) rather than on a flattened string, because a value that split
// into two arguments would still be a substring match of the joined form.
func TestDriveAgentCapEnvBuildsNewSessionArgs(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)

	out, argv, err := runDriveAgentTmux(t, script, dir, "A=1\nB=/x y",
		`write_cap_env; start_session "$TMP/repo" 45 19 gemini`)
	require.NoError(t, err, "output:\n%s", out)

	require.Contains(t, argv, "[-e]\n[A=1]\n", "argv:\n%s", argv)
	require.Contains(t, argv, "[-e]\n[B=/x y]\n",
		"a value with a space must stay one argument; argv:\n%s", argv)
}

// The negative control for the whole feature. A passthrough that fires when nothing asked for
// it changes every OTHER agent's captures silently — the fixtures would be taken under an
// environment their headers do not mention. cap-env must still EXIST (empty), because
// load_cap_env uses its absence to mean "this run predates the feature".
func TestDriveAgentCapEnvIsInertWhenUnset(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)

	out, argv, err := runDriveAgentTmux(t, script, dir, "",
		`write_cap_env; start_session "$TMP/repo" 45 19 gemini`)
	require.NoError(t, err, "output:\n%s", out)
	require.NotContains(t, argv, "[-e]", "argv:\n%s", argv)

	body, readErr := os.ReadFile(filepath.Join(dir, "cap-env"))
	require.NoError(t, readErr, "cap-env must be written even when ATR_CAP_ENV is unset")
	require.Empty(t, strings.TrimSpace(string(body)))
}

// `fresh` is the verb every native rung in a gemini ladder goes through, and it calls
// start_session again. A fresh that dropped the environment would write fixtures taken
// against the real config dir while their headers claim the isolated one — the exact failure
// the mechanism exists to prevent, and invisible in the resulting bytes.
func TestDriveAgentFreshReappliesTheCapEnv(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
		[]byte("GEMINI_CLI_HOME=/iso\n"), 0o600))

	out, argv, err := runDriveAgentTmux(t, script, dir, "", `cmd_fresh 45 19`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, argv, "[-e]\n[GEMINI_CLI_HOME=/iso]\n", "argv:\n%s", argv)
}

// A run directory written before this existed has no cap-env. Starting a session in it would
// silently carry no environment, so it must be fatal rather than empty — the same move
// cmd_fresh makes for a meta.env its rewrite cannot land in.
func TestDriveAgentCapEnvMissingFileIsFatal(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)

	out, _, err := runDriveAgentTmux(t, script, dir, "", `cmd_fresh 45 19`)
	require.Error(t, err, "output:\n%s", out)
	require.Contains(t, out, "predates ATR_CAP_ENV", "output:\n%s", out)
}

func TestDriveAgentCapEnvRejectsBadEntries(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	for _, tc := range []struct{ name, env, want string }{
		{"not an assignment", "FOO", "not an assignment"},
		{"leading digit", "1BAD=x", "not a shell identifier"},
		{"space in name", "A B=1", "not a shell identifier"},
		// TMUX_TMPDIR is the file's one safety invariant: set inside the pane it points a
		// nested tmux at the live fleet, which every -S in the script exists to prevent.
		{"tmux tmpdir", "TMUX_TMPDIR=/tmp", "belongs to the harness"},
		{"tmux", "TMUX=x", "belongs to the harness"},
		{"tmux pane", "TMUX_PANE=%1", "belongs to the harness"},
		{"duplicate", "A=1\nA=2", "sets A twice"},
		{"carriage return", "A=1\r", "carriage return"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			out, _, err := runDriveAgentTmux(t, script, dir, tc.env, `validate_cap_env`)
			require.Error(t, err, "output:\n%s", out)
			require.Contains(t, out, tc.want, "output:\n%s", out)
		})
	}
}

// The wart this flag exists to avoid, pinned so nobody "simplifies" back to it. Passing the
// variables as an `env FOO=bar gemini` PROGRAM STRING works — and cmd_up then derives BIN from
// the first token and stamps `env --version` into meta.env, which emit prints as every
// fixture's provenance. #737 drove that way and had to hand-write its fixture headers.
func TestDriveAgentCapEnvKeepsProvenanceOffTheEnvWrapper(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	// The bug, reproduced: the first token IS the recorded binary.
	out, _, err := runDriveAgentTmux(t, script, t.TempDir(), "",
		`program="env GEMINI_CLI_HOME=/iso gemini"; printf 'BIN=%s\n' "${program%% *}"`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, out, "BIN=env",
		"if this stops holding the wart is gone and this guard should be retired, not deleted")

	// And the fix: the program string is untouched, so the same derivation yields the agent.
	out, argv, err := runDriveAgentTmux(t, script, t.TempDir(), "GEMINI_CLI_HOME=/iso",
		`program=gemini; printf 'BIN=%s\n' "${program%% *}"; write_cap_env; load_cap_env
		 printf 'BARE=%s\n' "${CAP_ENV_BARE[0]}"`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, out, "BIN=gemini", "output:\n%s", out)
	require.Contains(t, out, "BARE=GEMINI_CLI_HOME=/iso",
		"probe_version needs the bare form, or the recorded version is a differently-configured binary's")
	require.NotContains(t, argv, "env ", "argv:\n%s", argv)
}

// emit stamps the fixture header, and a capture taken under a non-default agent config is not
// the same artifact as one taken under the default. NAMES only — a value here is a path into
// someone's home, and one of them points at a credentials directory.
func TestDriveAgentCapEnvDisclosesNamesNotValues(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
		[]byte("GEMINI_CLI_HOME=/very/secret/path\nGEMINI_FORCE_FILE_STORAGE=true\n"), 0o600))

	out, _, err := runDriveAgentTmux(t, script, dir, "", `cap_env_names`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, out, "GEMINI_CLI_HOME, GEMINI_FORCE_FILE_STORAGE")
	require.NotContains(t, out, "/very/secret/path", "values must never reach a fixture header")
}

// #735's fixture is an UNSUBMITTED composer taller than the pane, and neither `send` nor
// `paste` could reach it — both end in Enter. The refusal is the other half: send-keys -l
// delivers bytes raw and 0x0A is C-j, which sends_enter already counts as a submit, so
// dropping guard_enter is only sound while a newline cannot get through.
func TestDriveAgentNoEnterDeliversWithoutSubmitting(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)

	const recordKeys = `
require_live() { :; }
guard_enter() { printf 'GUARD RAN\n'; }
t() { printf '[t %s]\n' "$*"; }
`
	out, _, err := runDriveAgentTmux(t, script, dir, "",
		recordKeys+`NOENTER=1 cmd_send 'hello'`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, out, "send-keys -t %1 -l -- hello", "output:\n%s", out)
	require.NotContains(t, out, "send-keys -t %1 Enter",
		"NOENTER must not submit; output:\n%s", out)
	require.NotContains(t, out, "GUARD RAN",
		"there is no submit to guard, and the newline refusal is what makes that sound")

	// The control: without NOENTER the same call still submits, so the test above is
	// measuring the flag rather than a broken cmd_send.
	out, _, err = runDriveAgentTmux(t, script, dir, "", recordKeys+`cmd_send 'hello'`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, out, "send-keys -t %1 Enter", "output:\n%s", out)

	out, _, err = runDriveAgentTmux(t, script, dir, "",
		recordKeys+`NOENTER=1 cmd_send "$(printf 'a\nb')"`)
	require.Error(t, err, "a newline under NOENTER submits anyway; output:\n%s", out)
	require.Contains(t, out, "refuses a text containing a newline", "output:\n%s", out)
}
