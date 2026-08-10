package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// install.sh is the script a new user runs, and until #656 nothing exercised it. The
// three checks it had are all blind to behaviour: TestShellScriptsParse asks bash whether
// it parses, the lint workflow's shellcheck job asks whether it smells, and
// internal/doctor.TestInstallScriptStatesTheTmuxFloor reads one assignment out of its
// text. None of them can see that a failed install used to print its diagnosis into a
// variable and carry on — which is the class of bug this file exists to catch.
//
// Not every case here is a #656 reproduction, and pretending otherwise would be the
// easiest way to write a guard that proves nothing. Measured against the pre-fix script
// (see "Verifying" below), the split is:
//
//	fail on the old script — api unreachable, no releases, unparseable response,
//	                         asset missing after resolving latest
//	pass on both           — asset missing for an explicit version, fresh install,
//	                         upgrade, explicit version, tmux -V unreadable
//
// The four that fail are the defects. The five that pass are stated as what they are:
// regression guards over paths the old script handled correctly, kept because the fix
// rewrote how main obtains a version, plus one negative control and one tripwire.
//
// # Hermeticity
//
// These runs need more than the usual HOME sandbox:
//
//   - BIN_DIR is a t.TempDir(). Left to itself the script installs into ~/.local/bin and
//     symlinks `atr` next to it, on the machine running the test.
//   - HOME is a t.TempDir() because setup_shell_and_path appends an `export PATH=` line
//     to $PROFILE — $HOME/.bashrc for SHELL=/bin/bash. Note it does that *before* the
//     version is resolved, so even the failing cases write there.
//   - The environment is built from nothing rather than inherited, so no ambient VERSION,
//     BIN_DIR or PATH entry can reach the script. (The root package has no TestMain
//     installing testutil.SandboxHomeMain, and CI runs these under -shuffle=on.)
//   - PATH is the stubs plus a fixed system path, never the caller's. Inheriting it made
//     the fresh-install case fail here for a real reason: a Go-installed `atrium` on PATH
//     put check_command_exists into upgrade mode, so the case meant to cover a machine
//     with no atrium covered the opposite, depending on who ran it.
//   - `sudo`, `brew` and the package managers are stubbed as recorders that refuse and
//     log. Every case asserts that log is empty. That is the tripwire for all of the
//     above: if the tmux or gh stub ever loses the PATH race,
//     check_and_install_dependencies runs `sudo apt-get install`, and this reports it
//     instead of a CI runner discovering it.
//
// The stubs are written from Go into a temp dir rather than committed as testdata, so
// they stay out of `git ls-files -- '*.sh'` — the list both TestShellScriptsParse and the
// shellcheck job are built from. That is deliberate (a fixture is not one of the scripts
// those two grade) and it costs the stubs their syntax check, so newInstallFixture runs
// `bash -n` over each one itself.
//
// # Verifying that these actually catch #656
//
// Run them against the pre-fix script. `go test` keys its cache on files the test process
// opens, and install.sh reaches bash as a path, so runInstallScript reads it purely to
// register it as an input — without that, editing only install.sh replays a cached PASS.
// Belt and braces, pass -count=1:
//
//	git checkout origin/main -- install.sh
//	go test -count=1 -run TestInstallScript -v .   # the four failures listed above
//	git checkout -- install.sh

// releasesJSON is a GitHub /releases payload shaped the way install.sh parses it: the
// first "tag_name" line wins for the version, and every "tag_name" line is listed by the
// available-versions tip. It has to be pretty-printed one field per line, because the sed
// that extracts the tag is greedy (`.*"([^"]+)".*`) and so captures the last quoted run on
// the line. That is a real fragility in install.sh:87, dormant only because the GitHub
// REST API pretty-prints; it is out of scope here, but the fixture must respect it.
const releasesJSON = `[
  {
    "upload_url": "https://uploads.github.com/repos/ZviBaratz/atrium/releases/1/assets{?name,label}",
    "tag_name": "v9.9.9"
  },
  {
    "upload_url": "https://uploads.github.com/repos/ZviBaratz/atrium/releases/2/assets{?name,label}",
    "tag_name": "v9.9.8"
  },
  {
    "upload_url": "https://uploads.github.com/repos/ZviBaratz/atrium/releases/3/assets{?name,label}",
    "tag_name": "v9.9.7"
  }
]
`

// stubCurlScript replays the outcome the test staged in $CTL_DIR and records every call,
// so a test can assert on calls that did *not* happen. Two call shapes reach it, both
// from install.sh:
//
//	curl -sS <api-url>
//	curl -sS -L -f -w '%{http_code}' <asset-url> -o <path> 2>&1
//
// It scans the argv for the URL and the -o target rather than indexing it, so a change to
// either call's flag order cannot quietly turn this into a stub that answers nothing. The
// match is on the scheme: `%{http_code}` could otherwise be mistaken for a URL.
const stubCurlScript = `#!/bin/sh
printf 'CURLCALL %s\n' "$(printf '%s ' "$@" | tr '\n' ' ')" >> "$CTL_DIR/curl.log"

url=
out=
prev=
for arg in "$@"; do
    [ "$prev" = "-o" ] && out=$arg
    case $arg in
        http://*|https://*) [ -n "$url" ] || url=$arg ;;
    esac
    prev=$arg
done

case $url in
    *api.github.com*)
        code=$(cat "$CTL_DIR/api_exit")
        if [ "$code" -ne 0 ]; then
            echo "curl: ($code) Could not resolve host: api.github.com" >&2
            exit "$code"
        fi
        cat "$CTL_DIR/api_body"
        ;;
    *)
        code=$(cat "$CTL_DIR/dl_exit")
        printf '200'
        if [ "$code" -ne 0 ]; then
            echo "curl: ($code) The requested URL returned error: 404" >&2
            exit "$code"
        fi
        cp "$(cat "$CTL_DIR/dl_payload")" "$out"
        ;;
esac
`

// stubTmuxScript answers the two things install.sh asks of tmux: that it exists, and what
// version it is. The default is far above MIN_TMUX_VERSION rather than just above it, so
// raising the floor cannot start splicing a too-old warning into the output every other
// case reads.
const stubTmuxScript = `#!/bin/sh
code=$(cat "$CTL_DIR/tmux_exit")
[ "$1" = "-V" ] && [ "$code" -eq 0 ] && cat "$CTL_DIR/tmux_version"
exit "$code"
`

const stubGhScript = `#!/bin/sh
exit 0
`

// stubForbiddenScript stands in for the package managers and the privilege escalation
// install.sh reaches for when a dependency is missing. Reaching one means the stub PATH
// failed, so it records and refuses rather than doing anything.
const stubForbiddenScript = `#!/bin/sh
printf 'FORBIDDEN %s %s\n' "$(basename "$0")" "$*" >> "$CTL_DIR/forbidden.log"
exit 99
`

// installedBinary is the payload of the fixture archive: extract_and_install moves it into
// BIN_DIR and then runs it with `version`, treating a non-zero status as a failed install.
// A shell script satisfies that as well as a real binary would.
const installedBinary = `#!/bin/sh
echo "atrium version 9.9.9 (test fixture)"
`

// systemPathDirs is the PATH install.sh gets below the stubs: enough for the coreutils it
// shells out to (uname, mktemp, tar, grep, sed, mv, ln, chmod, rm, which) and nothing
// else. Deliberately not the caller's PATH — see this file's header.
var systemPathDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// forbiddenCommands are stubbed as recorders. `sudo` covers the Linux branches and `brew`
// the macOS ones; the managers themselves are listed so a `command -v` probe cannot pick
// a real one off the system path.
var forbiddenCommands = []string{"sudo", "brew", "apt-get", "dnf", "yum", "pacman"}

type installResult struct {
	stdout    string
	stderr    string
	exitCode  int
	curlCalls []string
	forbidden string
}

// installFixture stages one run of install.sh.
type installFixture struct {
	home       string
	binDir     string
	stubDir    string
	ctlDir     string
	tmpDir     string
	version    string   // VERSION env var; empty leaves it unset, i.e. "latest"
	extraEnv   []string // appended verbatim to the child environment
	pathPrefix []string // prepended to systemPathDirs
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is a bash script, and so is every stub here")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	// Say so plainly on a host that keeps its coreutils elsewhere (Nix, Guix) rather than
	// failing later inside the script, where it would read as a script bug.
	for _, tool := range []string{"uname", "mktemp", "tar"} {
		if !inSystemPath(tool) {
			t.Skipf("%s is not under %v, which is the PATH these runs get", tool, systemPathDirs)
		}
	}

	f := &installFixture{
		home:    t.TempDir(),
		binDir:  t.TempDir(),
		stubDir: t.TempDir(),
		ctlDir:  t.TempDir(),
		tmpDir:  t.TempDir(),
	}
	f.pathPrefix = []string{f.stubDir}

	f.writeStub(t, "curl", stubCurlScript)
	f.writeStub(t, "tmux", stubTmuxScript)
	f.writeStub(t, "gh", stubGhScript)
	for _, name := range forbiddenCommands {
		f.writeStub(t, name, stubForbiddenScript)
	}

	// extract_and_install chmods the installed file and then runs it, so a temp dir on a
	// noexec mount breaks these runs. exec.LookPath only reads mode bits and cannot see
	// that, so probe by actually executing something.
	probe := filepath.Join(f.stubDir, "noexec-probe")
	require.NoError(t, os.WriteFile(probe, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	if err := exec.CommandContext(t.Context(), probe).Run(); err != nil {
		t.Skipf("temp dir does not support executing files (noexec?): %v", err)
	}

	// Defaults: the API answers, the download succeeds, tmux is comfortably new. Each
	// case overrides only what it is about.
	f.control(t, "api_exit", "0")
	f.control(t, "api_body", releasesJSON)
	f.control(t, "dl_exit", "0")
	f.control(t, "dl_payload", tarballWithAtrium(t, f.ctlDir))
	f.control(t, "tmux_exit", "0")
	f.control(t, "tmux_version", "tmux 9.9\n")
	return f
}

// writeStub installs one fake command and checks it parses. These files are invisible to
// TestShellScriptsParse and to the shellcheck job by design, and that same design removes
// the only thing that would have caught a typo in the Go string literal above.
func (f *installFixture) writeStub(t *testing.T, name, body string) {
	t.Helper()
	path := filepath.Join(f.stubDir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	out, err := exec.CommandContext(t.Context(), "bash", "-n", path).CombinedOutput()
	require.NoError(t, err, "stub %s does not parse: %s", name, out)
}

func (f *installFixture) control(t *testing.T, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.ctlDir, name), []byte(body), 0o644))
}

func (f *installFixture) run(t *testing.T) installResult {
	t.Helper()

	env := []string{
		"HOME=" + f.home,
		"SHELL=/bin/bash",
		"BIN_DIR=" + f.binDir,
		"TMPDIR=" + f.tmpDir,
		"PATH=" + strings.Join(append(append([]string{}, f.pathPrefix...), systemPathDirs...), string(os.PathListSeparator)),
		"CTL_DIR=" + f.ctlDir,
	}
	if f.version != "" {
		env = append(env, "VERSION="+f.version)
	}
	env = append(env, f.extraEnv...)

	script := filepath.Join(moduleRoot(t), "install.sh")
	// Read it and discard: `go test` caches on the files a test process opens, and this
	// one hands install.sh to bash as a path, so without this a run where only install.sh
	// changed replays a cached PASS. The subject here is not a Go source file, so nothing
	// else registers it.
	src, err := os.ReadFile(script)
	require.NoError(t, err)
	require.NotEmpty(t, src, "install.sh is empty; these runs would prove nothing")

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Env = env
	cmd.Dir = f.tmpDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	res := installResult{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running %s: %v", script, err)
		}
		res.exitCode = exit.ExitCode()
	}
	res.stdout, res.stderr = stdout.String(), stderr.String()
	res.curlCalls = f.readLog(t, "curl.log")
	res.forbidden = strings.Join(f.readLog(t, "forbidden.log"), "\n")

	// Nine subprocess cases are close to undebuggable from an assertion message alone.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("install.sh exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s\n--- curl ---\n%s",
				res.exitCode, res.stdout, res.stderr, strings.Join(res.curlCalls, "\n"))
		}
	})

	// Asserted here rather than per-case: no run has any business reaching a package
	// manager, and a case that forgot to check would be the one that needed it.
	require.Empty(t, res.forbidden, "install.sh escaped the stubs and reached for a package manager")
	return res
}

// readLog returns one entry per recorded line. It counts sentinel-prefixed records rather
// than lines because with the #656 bug present install.sh builds a URL containing
// newlines, which would inflate a naive line count.
func (f *installFixture) readLog(t *testing.T, name string) []string {
	t.Helper()
	// A missing log means the command was never called, which is an outcome to assert on
	// rather than an error to report.
	body, err := os.ReadFile(filepath.Join(f.ctlDir, name))
	if err != nil {
		return nil
	}
	var entries []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "CURLCALL ") || strings.HasPrefix(line, "FORBIDDEN ") {
			entries = append(entries, line)
		}
	}
	return entries
}

// apiCalls counts the calls that went to the releases API rather than to an asset
// download. "How often did it reach for the API" and "did it try to download anything"
// are the two questions the failure-path cases turn on.
func (r installResult) apiCalls() int {
	n := 0
	for _, c := range r.curlCalls {
		if strings.Contains(c, "api.github.com") {
			n++
		}
	}
	return n
}

func inSystemPath(tool string) bool {
	for _, dir := range systemPathDirs {
		if _, err := os.Stat(filepath.Join(dir, tool)); err == nil {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}

// tarballWithAtrium builds the archive the fake curl serves. install.sh validates the
// download with `tar tzf` before unpacking it, so this has to be a real gzipped tar.
func tarballWithAtrium(t *testing.T, dir string) string {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "atrium", // extract_and_install moves ${tmp_dir}/atrium by that literal name
		Mode:     0o755,
		Size:     int64(len(installedBinary)),
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write([]byte(installedBinary))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	path := filepath.Join(dir, "atrium.tar.gz")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// TestInstallScriptFailurePaths is the guard for #656.
//
// Each assertion here is chosen to fail against the pre-fix script rather than merely to
// describe the new one, which is easy to get wrong: asserting that the combined output
// contains "Failed to connect to GitHub API" would have passed on the old script too,
// because it printed that text inside "Expected asset name: atrium_Failed to connect to
// GitHub API_linux_amd64.tar.gz". What the old script cannot do is put the message on
// stderr (it went to stdout, into a command substitution) or stop before the download
// (nothing aborted, so it went on to request a URL built from the error text).
func TestInstallScriptFailurePaths(t *testing.T) {
	t.Run("api unreachable", func(t *testing.T) {
		f := newInstallFixture(t)
		f.control(t, "api_exit", "6") // curl's "could not resolve host"

		res := f.run(t)

		require.Equal(t, 1, res.exitCode, "an install that cannot reach the API must fail")
		require.Contains(t, res.stderr, "Failed to connect to GitHub API",
			"the diagnosis has to be on stderr; on stdout a command substitution eats it")
		require.Len(t, res.curlCalls, 1,
			"the run must stop at the API failure, not go on to download a URL built from the error text")
		require.NotContains(t, res.stdout, "Expected asset name",
			"reaching the download diagnostics means the failed lookup did not stop the run")
		require.NoFileExists(t, filepath.Join(f.binDir, "atrium"))
	})

	t.Run("no releases in the repository", func(t *testing.T) {
		f := newInstallFixture(t)
		f.control(t, "api_body", `{"message": "Not Found"}`)

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		require.Contains(t, res.stderr, "No releases found in the repository")
		require.Len(t, res.curlCalls, 1, "no download should be attempted once the lookup failed")
		require.NoFileExists(t, filepath.Join(f.binDir, "atrium"))
	})

	t.Run("unparseable api response", func(t *testing.T) {
		f := newInstallFixture(t)
		f.control(t, "api_body", `{
  "message": "API rate limit exceeded for 203.0.113.7.",
  "upload_url": "https://uploads.github.com/SHOULD-BE-FILTERED"
}`)

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		require.Contains(t, res.stderr, "Failed to parse a version from the GitHub API response")
		require.Contains(t, res.stderr, "API rate limit exceeded for 203.0.113.7.",
			"the body is the evidence a bug report needs, so it belongs with the verdict on stderr")
		require.Len(t, res.curlCalls, 1)
		// A formatting guard for install.sh's upload_url filter, not a #656 pin: the old
		// script's stderr was empty, so this passes there vacuously.
		require.NotContains(t, res.stderr, "SHOULD-BE-FILTERED")
	})

	t.Run("asset missing after resolving latest", func(t *testing.T) {
		f := newInstallFixture(t)
		f.control(t, "dl_exit", "22") // curl -f on an HTTP error

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		// Both halves of this were broken: API_RESPONSE never left get_latest_version's
		// subshell, and the tip was gated on a $version main had already resolved away
		// from "latest", so the block could not run even with a response to print.
		require.Contains(t, res.stdout, "Tip: Try installing a specific version instead of 'latest'")
		require.Contains(t, res.stdout, "Available versions:")
		// The older tags are what discriminate: they can only come from API_RESPONSE
		// crossing the subshell boundary. 9.9.9 alone would not — it also appears in the
		// "Expected asset name" line above the tip.
		require.Contains(t, res.stdout, "9.9.8")
		require.Contains(t, res.stdout, "9.9.7")
	})

	t.Run("asset missing for an explicitly requested version", func(t *testing.T) {
		// A negative control for the new gate rather than a #656 reproduction: the old
		// script printed no tip on any path, so this passes either way. It is here so
		// that "gate on what was requested" cannot be satisfied by a gate that is always
		// on, and API_RESPONSE is injected from the environment so it cannot be satisfied
		// by gating on that being non-empty either.
		f := newInstallFixture(t)
		f.version = "1.2.3"
		f.extraEnv = append(f.extraEnv, "API_RESPONSE="+releasesJSON)
		f.control(t, "dl_exit", "22")

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		require.NotContains(t, res.stdout, "Available versions:",
			"the tip is for someone who asked for 'latest'; this user named a version")
		require.NotContains(t, res.stdout, "Tip: Try installing a specific version")
		require.Contains(t, res.stdout, "/v1.2.3/", "the attempted URL should name the requested version")
		require.Equal(t, 0, res.apiCalls(), "an explicit VERSION should not consult the releases API")
	})
}

// TestInstallScriptInstallsAndUpgrades covers paths that already worked, because the fix
// rewrote how main obtains the version. These pass against the old script too; they are
// here so the restructure cannot quietly break the install everyone actually performs.
func TestInstallScriptInstallsAndUpgrades(t *testing.T) {
	t.Run("fresh install of latest", func(t *testing.T) {
		f := newInstallFixture(t)

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		require.Contains(t, res.stdout, "Installed as 'atrium':")
		require.Contains(t, res.stdout, "atrium version 9.9.9 (test fixture)",
			"the banner quotes the installed binary, so one that cannot run is reported as a failure")
		require.NotContains(t, res.stdout, "Successfully upgraded")
		require.NotContains(t, res.stdout, "Found existing installation",
			"a stray atrium on PATH would make this case cover upgrade instead of fresh install")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))

		// The version resolved from the API has to reach the download URL. This is the
		// wire #656 corrupted — it carried "Failed to connect to GitHub API" on the
		// failure path, and it is the same wire here.
		require.Len(t, res.curlCalls, 2)
		require.Contains(t, res.curlCalls[1], "/v9.9.9/atrium_9.9.9_")

		link := filepath.Join(f.binDir, "atr")
		info, err := os.Lstat(link)
		require.NoError(t, err, "the default install links atr -> atrium")
		require.NotZero(t, info.Mode()&os.ModeSymlink, "atr should be a symlink, not a copy")
		target, err := os.Readlink(link)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(f.binDir, "atrium"), target)

		// setup_shell_and_path put BIN_DIR on the PATH of the sandbox shell profile —
		// which doubles as proof that HOME really is the sandbox.
		profile, err := os.ReadFile(filepath.Join(f.home, ".bashrc"))
		require.NoError(t, err)
		require.Contains(t, string(profile), `export PATH="$PATH:`+f.binDir+`"`)
	})

	t.Run("upgrade over an existing install", func(t *testing.T) {
		f := newInstallFixture(t)
		// The real upgrade shape: an older atrium already in BIN_DIR, with BIN_DIR on
		// PATH, which is what check_command_exists looks for.
		writeExecutable(t, filepath.Join(f.binDir, "atrium"), "#!/bin/sh\necho \"atrium version 0.0.1\"\n")
		f.pathPrefix = append(f.pathPrefix, f.binDir)

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		require.Contains(t, res.stdout, "Found existing installation of 'atrium'")
		require.Contains(t, res.stdout, "Removing previous installation from")
		require.Contains(t, res.stdout, "Successfully upgraded 'atrium' to:")
		require.Contains(t, res.stdout, "atrium version 9.9.9 (test fixture)")
		require.NotContains(t, res.stdout, "atrium version 0.0.1", "the old binary should be gone")
	})

	t.Run("explicit version skips the api", func(t *testing.T) {
		f := newInstallFixture(t)
		f.version = "1.2.3"

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		require.Equal(t, 0, res.apiCalls(),
			"VERSION is a request to install exactly that, without asking GitHub what is newest")
		require.Len(t, res.curlCalls, 1)
		require.Contains(t, res.curlCalls[0], "/v1.2.3/atrium_1.2.3_")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
	})

	t.Run("unreadable tmux version does not fail the install", func(t *testing.T) {
		// A tripwire for the decision recorded at the bottom of install.sh: this PR left
		// `main "$@" || exit 1` in place, so errexit stays suppressed. check_tmux_version
		// is warning-only and reads `raw="$(tmux -V)"` unguarded, which is one of the
		// sites that would become fatal if errexit were restored. Whoever restores it
		// finds this case red rather than a paragraph in an old PR description.
		f := newInstallFixture(t)
		f.control(t, "tmux_exit", "1")

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "a tmux that cannot report its version must not fail the install: %s", res.stderr)
		require.NotContains(t, res.stdout, "WARNING:", "an unreadable version is not evidence of an old one")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
	})
}
