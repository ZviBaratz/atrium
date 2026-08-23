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
fresh_preflight() { :; }
new_workspace() { mkdir -p "$1"; }
start_session() { PANE=%9; WINDOW=@9; }
note() { :; }
assert_under_run_root() { :; }
`

func runDriveAgent(t *testing.T, script, tmp, snippet string) (string, error) {
	t.Helper()
	return runDriveAgentWith(t, script, tmp, driveAgentHarness, snippet)
}

// runDriveAgentWith is runDriveAgent over an explicit harness, for the one test that needs a
// stub REMOVED. Everything the harness fakes is faked because it would touch tmux or a real
// filesystem; fresh_preflight is faked because `command -v` resolves against the runner's
// PATH and CI has no agent binaries installed — so the test that covers it has to put it back.
func runDriveAgentWith(t *testing.T, script, tmp, harness, snippet string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", harness+snippet)
	cmd.Env = append(os.Environ(), "SCRIPT="+script, "TMP="+tmp)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeMetaEnv makes dir look like a run directory `up` created — which means BOTH files.
// cap-env is written here rather than per-test because cmd_fresh validates it before it
// reaps, so every verb that goes through `fresh` now needs it, and a test that omits it dies
// on the wrong message. TestDriveAgentCapEnvMissingFileIsFatal deletes it back out to model a
// run directory that predates the feature.
func writeMetaEnv(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.env"), []byte(strings.Join([]string{
		"PROGRAM=gemini", "BIN=gemini", "VERSION=0.55.1", "CAPTURED=2026-08-17",
		"WIDTH=120", "HEIGHT=40", "PANE=%1", "WINDOW=@1", "",
	}, "\n")), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"), []byte("GEMINI_CLI_HOME=/iso\n"), 0o600))
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
	// A cap-env file so the run gets PAST cmd_fresh's pre-reap validation, which is a
	// different failure with a different message. Without it this test passes on the wrong
	// die and stops saying anything about the meta.env rewrite.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"), []byte("GEMINI_CLI_HOME=/iso\n"), 0o600))

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
fresh_preflight() { :; }
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
	// Removed so the existence assertion below MEANS something. writeMetaEnv creates cap-env,
	// so leaving it in place made "must be written even when ATR_CAP_ENV is unset" a check on
	// the fixture rather than on write_cap_env; only the emptiness assertion was live.
	require.NoError(t, os.Remove(filepath.Join(dir, "cap-env")))

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
	// The premise, made explicit: a run directory from before ATR_CAP_ENV existed.
	require.NoError(t, os.Remove(filepath.Join(dir, "cap-env")))

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
		// The pair rule's guard used to be keyed on the NAME, so the empty spelling
		// satisfied it — while gemini's homedir() returns GEMINI_CLI_HOME only
		// `if (envHome)`, which makes an empty value identical to unset and re-aims
		// migrateFromFileStorage's rm at the real ~/.gemini/oauth_creds.json. Same
		// destination as the missing half, reached through a check that passed (#765).
		{"empty gemini cli home", "GEMINI_CLI_HOME=\nGEMINI_FORCE_FILE_STORAGE=true", "EMPTY GEMINI_CLI_HOME"},
	} {
		// Both entry points, every entry. `up` validates the VARIABLE; every native rung of
		// a ladder reaches new-session through load_cap_env, which validates the FILE. One
		// table over both is what holds them to the same list — a refusal that lived on only
		// one path is the shape this guard exists to prevent.
		for _, path := range []struct{ name, snippet string }{
			{"variable", `validate_cap_env <<<"$ATR_CAP_ENV"`},
			{"file", `printf '%s\n' "$ATR_CAP_ENV" >"$RUN/cap-env"; load_cap_env`},
		} {
			t.Run(tc.name+"/"+path.name, func(t *testing.T) {
				dir := t.TempDir()
				out, _, err := runDriveAgentTmux(t, script, dir, tc.env, path.snippet)
				require.Error(t, err, "output:\n%s", out)
				require.Contains(t, out, tc.want, "output:\n%s", out)
			})
		}
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
	//
	// Asserted as what new-session RECEIVES, positively. `NotContains(argv, "[env]")` was the
	// spelling here and it cannot fail: tmux_boot logs one [%s] line per argv ELEMENT and
	// start_session passes the whole program string as ONE element, so an env-wrapped program
	// records `[env GEMINI_CLI_HOME=/iso gemini]` and never the token `[env]`. The element
	// being exactly the agent binary is the property, and it is the one an env prefix breaks.
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	out, argv, err := runDriveAgentTmux(t, script, dir, "GEMINI_CLI_HOME=/iso",
		`program=gemini; printf 'BIN=%s\n' "${program%% *}"; write_cap_env; load_cap_env
		 printf 'BARE=%s\n' "${CAP_ENV_BARE[0]}"
		 start_session "$TMP/repo" 45 19 "$program"`)
	require.NoError(t, err, "output:\n%s", out)
	require.Contains(t, out, "BIN=gemini", "output:\n%s", out)
	require.Contains(t, out, "BARE=GEMINI_CLI_HOME=/iso",
		"probe_version needs the bare form, or the recorded version is a differently-configured binary's")
	require.NotEmpty(t, argv, "the premise: start_session recorded an argv for the next assertion to read")
	require.Contains(t, argv, "[-e]\n[GEMINI_CLI_HOME=/iso]\n",
		"the variable reaches new-session as its own pair; argv:\n%s", argv)
	require.Contains(t, argv, "[gemini]\n",
		"and the program element is the bare binary, not an env-prefixed string; argv:\n%s", argv)
}

// The refusal list and the argv builder must be one code path. validate_cap_env runs at `up`
// over $ATR_CAP_ENV, but every native rung of a ladder reaches new-session through `fresh` ->
// start_session -> load_cap_env, which reads $RUN/cap-env — a file `help` names, so editing it
// is an invited operation. TMUX_TMPDIR is the entry that matters: inside the pane it points a
// nested tmux at the live Atrium fleet, the accident every -S in that script exists to prevent
// (#547/#581). Asserted on the FILE, which is the path `up`'s validation never sees.
func TestDriveAgentCapEnvIsRevalidatedOnLoad(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
		[]byte("GEMINI_CLI_HOME=/iso\nTMUX_TMPDIR=/tmp\n"), 0o600))

	out, argv, err := runDriveAgentTmux(t, script, dir, "",
		`start_session "$TMP/repo" 45 19 gemini`)
	require.Error(t, err, "a hand-edited TMUX_TMPDIR must stop the run; output:\n%s", out)
	require.Contains(t, out, "belongs to the harness")
	// Empty, not "does not contain TMUX_TMPDIR". The refusal happens inside start_session
	// before tmux_boot is ever called, so argv.log does not exist and argv is "" — a
	// NotContains against it passes for any needle and states nothing about the ordering.
	require.Empty(t, argv, "the refusal must land before new-session, not after; argv:\n%s", argv)
}

// Three readers, one file, one answer. validate_cap_env skips '#' lines, load_cap_env used to
// skip only blanks, and cap_env_names (awk) skipped neither — so a '#' line passed validation
// by being ignored, was forwarded to new-session verbatim as `-e '#NAME=VALUE'` (which tmux
// accepts, exit 0), and was still stamped into the fixture header as an applied name.
//
// The damage is not the stray argument. `help` names $RUN/cap-env by path and the file invites
// hand-editing, and the recipe it prints is a PAIR: commenting out GEMINI_CLI_HOME leaves
// GEMINI_FORCE_FILE_STORAGE applied, which drive-agent.sh's own help warns makes gemini read
// the developer's real oauth_creds.json and delete it — under a header claiming the sandbox.
//
// Asserted on all three readers, because agreeing with one of the other two is not agreement.
func TestDriveAgentCapEnvCommentsAreSkippedByEveryReader(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	// The commented-out half used to be GEMINI_CLI_HOME with GEMINI_FORCE_FILE_STORAGE left
	// live below it, and this test then asserted that surviving entry was correctly APPLIED —
	// pinning as expected behaviour the exact state that makes gemini delete the developer's
	// real oauth_creds.json. validate_cap_env refuses that pair now, and
	// TestDriveAgentCapEnvRefusesForceFileStorageWithoutCliHome owns it. What THIS test is
	// about is comment skipping, so its fixture uses a variable with no blast radius.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
		[]byte("# the sandbox home, commented out by hand\n#GEMINI_CLI_HOME=/iso\nGEMINI_SANDBOX=false\n"), 0o600))

	out, argv, err := runDriveAgentTmux(t, script, dir, "",
		`start_session "$TMP/repo" 45 19 gemini
		 printf 'APPLIED=%s\n' "${CAP_ENV_BARE[*]}"
		 printf 'REPORTED='; cap_env_names`)
	require.NoError(t, err, "a commented line must not be an error, just absent; output:\n%s", out)

	require.Contains(t, out, "APPLIED=GEMINI_SANDBOX=false\n",
		"load_cap_env must apply the uncommented entry and only that; output:\n%s", out)
	require.Contains(t, out, "REPORTED=GEMINI_SANDBOX\n",
		"and emit must stamp exactly what was applied — a header naming a commented-out "+
			"variable claims an isolation the pane never had; output:\n%s", out)
	require.NotContains(t, argv, "#GEMINI_CLI_HOME=/iso",
		"and nothing '#'-prefixed may reach new-session; argv:\n%s", argv)
	require.Contains(t, argv, "[-e]\n[GEMINI_SANDBOX=false]\n", "argv:\n%s", argv)
}

// cmd_fresh validates the cap-env file BEFORE it reaps. start_session re-validates too, but
// start_session runs after `reap` has taken the live session and `rm -rf` has taken the
// workspace — so the refusal for the one case the re-validation exists for, a hand-edited
// file, arrived with the dialog it was protecting already gone. That dialog costs an API turn
// to reach and a once-per-path gate cannot be re-reached at all.
//
// It is the ordering that is asserted, by making the stub reap announce itself. Asserting only
// that the run dies would pass with the check in either place, which is how this shipped:
// TestDriveAgentCapEnvIsRevalidatedOnLoad calls start_session directly and structurally cannot
// see the difference.
func TestDriveAgentFreshValidatesCapEnvBeforeItReaps(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
		[]byte("GEMINI_CLI_HOME=/iso\nTMUX_TMPDIR=/tmp\n"), 0o600))

	out, _, err := runDriveAgentTmux(t, script, dir, "",
		`reap() { printf 'REAPED\n'; }
		 new_workspace() { printf 'WORKSPACE-REBUILT\n'; mkdir -p "$1"; }
		 cmd_fresh 45 19`)
	require.Error(t, err, "a hand-edited TMUX_TMPDIR must stop the run; output:\n%s", out)
	require.Contains(t, out, "belongs to the harness", "output:\n%s", out)
	require.NotContains(t, out, "REAPED",
		"the refusal must land before the session is reaped; output:\n%s", out)
	require.NotContains(t, out, "WORKSPACE-REBUILT",
		"and before the workspace is rebuilt; output:\n%s", out)
}

// A cap-env whose LAST line has no trailing newline. `read` returns non-zero at EOF even
// though it assigned the line, so a bare `while IFS= read -r line` drops it — in BOTH loops
// that walk this file. The two halves of the damage fail in opposite directions, so each is
// asserted on its own:
//
//   - The refusal is skipped. TMUX_TMPDIR is the entry validate_cap_env exists to refuse, and
//     a check that never sees the line cannot refuse it.
//
//   - The applied set and the REPORTED set disagree. cap_env_names is awk, which does read a
//     final unterminated line, so emit stamps a name into the fixture's doc comment that the
//     session was never started with.
//
//     WHICH line is dropped is the last one, and for the recipe `help` actually prints —
//     GEMINI_CLI_HOME first, GEMINI_FORCE_FILE_STORAGE second — that drops the file-storage
//     half and leaves the isolation applied, which is the harmless direction. An earlier draft
//     of this comment claimed the opposite and its fixture below was written in the reverse
//     order to make the sentence come out true. The fixture keeps that order, now labelled as
//     the adversarial case it is: it is the ordering that costs the most, and a guard should
//     hold the worst arrangement of a hazard rather than the shipped one.
//
// The second has no upper bound on its damage, so it is asserted as an equality between the
// two readers rather than as a property of either one.
func TestDriveAgentCapEnvReadsAFinalLineWithNoNewline(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	t.Run("the refusal still fires", func(t *testing.T) {
		dir := t.TempDir()
		writeMetaEnv(t, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
			[]byte("GEMINI_CLI_HOME=/iso\nTMUX_TMPDIR=/tmp"), 0o600)) // no trailing newline

		out, argv, err := runDriveAgentTmux(t, script, dir, "",
			`start_session "$TMP/repo" 45 19 gemini`)
		require.Error(t, err, "an unterminated TMUX_TMPDIR must stop the run; output:\n%s", out)
		require.Contains(t, out, "belongs to the harness")
		require.Empty(t, argv, "nothing reached new-session; argv:\n%s", argv)
	})

	t.Run("what emit reports is what load applies", func(t *testing.T) {
		dir := t.TempDir()
		// Deliberately the REVERSE of the recipe `help` prints, so the entry an unterminated
		// final line would drop is GEMINI_CLI_HOME — the isolation itself, the arrangement in
		// which the bug costs the most rather than the one it ships in.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
			[]byte("GEMINI_FORCE_FILE_STORAGE=true\nGEMINI_CLI_HOME=/iso"), 0o600)) // no trailing newline

		out, _, err := runDriveAgentTmux(t, script, dir, "",
			`load_cap_env; printf 'APPLIED=%s\n' "${CAP_ENV_BARE[*]}"; printf 'REPORTED='; cap_env_names`)
		require.NoError(t, err, "output:\n%s", out)
		require.Contains(t, out, "APPLIED=GEMINI_FORCE_FILE_STORAGE=true GEMINI_CLI_HOME=/iso",
			"the last entry must survive the read; output:\n%s", out)
		require.Contains(t, out, "REPORTED=GEMINI_FORCE_FILE_STORAGE, GEMINI_CLI_HOME",
			"emit's names must match what was applied; output:\n%s", out)
	})
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

// The seam exists to be stubbed on CI, so the one test that exercises it takes the harness
// back apart. Without that, the fix is unguarded: every other cmd_fresh test stubs
// fresh_preflight away, so deleting the call from cmd_fresh outright leaves them all green.
//
// What is asserted is the ORDER, not the message. `fresh` reaping a session and rm -rf'ing a
// workspace before discovering the agent is unreachable throws away a dialog that cost an API
// turn — the same cost the load_cap_env hoist beside it exists to prevent, which is why the
// two checks sit together. Each verb is its own process, so `fresh` does not inherit `up`'s
// PATH and this is reachable without anyone editing anything.
func TestDriveAgentFreshChecksTheProgramBeforeItReaps(t *testing.T) {
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.env"), []byte(strings.Join([]string{
		"PROGRAM=atrium-no-such-agent-736", "BIN=atrium-no-such-agent-736", "VERSION=0",
		"CAPTURED=2026-08-17", "WIDTH=120", "HEIGHT=40", "PANE=%1", "WINDOW=@1", "",
	}, "\n")), 0o644))

	unstubbed := strings.Replace(driveAgentHarness, "fresh_preflight() { :; }\n", "", 1)
	require.NotEqual(t, driveAgentHarness, unstubbed, "the premise: the stub was actually removed")

	out, err := runDriveAgentWith(t, script, dir, unstubbed,
		"reap() { echo REAPED; }\ncmd_fresh 45 19")
	require.Error(t, err, "an unresolvable program must stop the verb; output:\n%s", out)
	require.Contains(t, out, "not on PATH: atrium-no-such-agent-736")
	require.NotContains(t, out, "REAPED",
		"and it must stop BEFORE the reap, or the check costs the capture it exists to save; output:\n%s", out)
}

// The third reader of cap-env shares the other two's refusal, asserted on the entry that
// proved a matching skip predicate is not enough on its own. " #NAME=VALUE" — one leading
// space — is not a '#'-prefixed line to bash, so validate_cap_env dies on it as a
// non-identifier; awk's /^#/ did not match it either, and printed " #NAME". emit and status
// reach cap_env_names without going through load_cap_env, so that name was stamped into a
// committed fixture header as an environment the session never carried.
func TestDriveAgentCapEnvNamesRefuseWhatTheBashReadersRefuse(t *testing.T) {
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cap-env"),
		[]byte(" #GEMINI_CLI_HOME=/iso\n"), 0o600))

	// stderr discarded, so what is left is exactly what emit would consume. The refusal names
	// the offending line on stderr — that is how you find it — and asserting against the
	// combined streams would have been asserting the diagnostic away.
	out, err := runDriveAgent(t, script, dir, "load_run; cap_env_names 2>/dev/null")
	require.Error(t, err, "a line the bash readers refuse must not be reportable; output:\n%s", out)
	require.Empty(t, strings.TrimSpace(out),
		"nothing may reach stdout, where emit would stamp it into a fixture header; got:\n%s", out)
}

// start_session's OWN missing-file refusal, which stopped being covered the moment cmd_fresh
// grew one of its own. TestDriveAgentCapEnvMissingFileIsFatal goes through cmd_fresh, so the
// new pre-reap load_cap_env now fires first and that test passes without start_session ever
// being reached — mutation-verified during review: deleting load_cap_env from start_session
// left it green. start_session is reachable directly, so the refusal is asserted there.
//
// It matters because start_session is what `up` calls. A run whose cap-env has been deleted
// by hand would otherwise start a session carrying no isolation at all, and every capture
// taken in it would claim one in its header.
func TestDriveAgentStartSessionRefusesAMissingCapEnv(t *testing.T) {
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	dir := t.TempDir()
	writeMetaEnv(t, dir)
	require.NoError(t, os.Remove(filepath.Join(dir, "cap-env")))

	out, argv, err := runDriveAgentTmux(t, script, dir, "",
		`start_session "$TMP/repo" 45 19 gemini`)
	require.Error(t, err, "a run directory with no cap-env must not start a session; output:\n%s", out)
	require.Contains(t, out, "predates ATR_CAP_ENV")
	require.Empty(t, argv,
		"and the refusal must land before new-session, not after; argv:\n%s", argv)
}

// The one refusal about a PAIR rather than a line, and the only one whose failure destroys
// something outside the run directory. GEMINI_FORCE_FILE_STORAGE routes credentials through
// the file store, and gemini's migrateFromFileStorage reads homedir()/.gemini/oauth_creds.json
// and then deletes it — where homedir() falls back to the real $HOME whenever GEMINI_CLI_HOME
// is unset. `help` prints the two together as one recipe and writes them to a file it invites
// you to hand-edit, so commenting out one line is the whole distance to deleting your own
// refresh token.
//
// Both directions are asserted. Refusing the half-set pair proves nothing on its own if the
// harness would refuse the full pair too — that would be a script nobody can isolate with.
func TestDriveAgentCapEnvRefusesForceFileStorageWithoutCliHome(t *testing.T) {
	script := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")

	out, err := runDriveAgent(t, script, t.TempDir(),
		`validate_cap_env <<<"GEMINI_FORCE_FILE_STORAGE=true"`)
	require.Error(t, err, "the half-set pair must be refused; output:\n%s", out)
	require.Contains(t, out, "without GEMINI_CLI_HOME")

	// The premise: the pair the recipe actually prints still passes.
	out, err = runDriveAgent(t, script, t.TempDir(),
		`validate_cap_env <<<$'GEMINI_CLI_HOME=/iso\nGEMINI_FORCE_FILE_STORAGE=true'`)
	require.NoError(t, err, "the documented pair must still be accepted; output:\n%s", out)

	// And the other half alone is fine — isolation without forcing the file store is the
	// weaker but non-destructive configuration.
	out, err = runDriveAgent(t, script, t.TempDir(),
		`validate_cap_env <<<"GEMINI_CLI_HOME=/iso"`)
	require.NoError(t, err, "output:\n%s", out)
}
