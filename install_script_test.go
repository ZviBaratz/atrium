package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
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
// Not every case here reproduces a defect, and pretending otherwise would be the easiest
// way to write a guard that proves nothing. Each group below was measured against the
// script it is about rather than assumed (see "Verifying" below).
//
// Against the pre-#656 script:
//
//	fail — api unreachable, no releases, unparseable response,
//	       asset missing after resolving latest, --name without a value
//	pass — asset missing for an explicit version, fresh install, upgrade,
//	       explicit version, release note mentioning "Not Found", tmux -V unreadable
//
// Those five failures are #656. The six that pass are stated as what they are: regression
// guards over paths the old script handled correctly, kept because the fix rewrote how
// main obtains a version, plus one negative control and one tripwire.
//
// Against the script as of #660 — which is to say the five cases added for #662/#663/#664,
// all of which fail there:
//
//	unwritable profile             — installed and exited 0 with BIN_DIR off PATH (#662)
//	fish syntax in a missing dir   — appended nothing, or a line fish cannot use (#662)
//	no symlinks on the filesystem  — claimed a link ln had failed to make (#662)
//	compact release body           — resolved an upload_url as the version (#663)
//	available-versions tip capped  — printed every release the page carried (#664)
//
// TestInstallScriptRestoresErrexit belongs to the same change and guards the one-line
// decision none of the above can see.
//
// Two cases that predate #662 also go red against #660, and both are guards doing their
// job rather than surprises: "upgrade over an existing install" fails because `which` is
// now a recorder stub and the old check_command_exists called it, and "asset missing after
// resolving latest" fails on the tip's new "(newest 10)" label.
//
// One of them earns its place a level down. "a release note mentioning Not Found" passes
// against the old script — which greps the whole payload, matches, and then carries on
// and installs anyway, being #656 — but fails against this PR's own first commit, where
// making that path abort turned a garbled install into a refused one. It guards a
// regression introduced and fixed inside this branch, which is the only reason a case
// that passes on main is worth keeping.
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
// they stay out of `git ls-files -- '*.sh'` — the list the guards that grade the scripts
// as a *set* are built from, whether they parse them, lint them or read their prose. (A
// guard aimed at one named script reaches it by path and never consults that list, which
// is how internal/doctor grades install.sh.) That is deliberate (a fixture is not one of
// the scripts those guards grade) and it costs
// the stubs their syntax check, so newInstallFixture runs `bash -n` over each one
// itself.
//
// # Verifying that these actually catch what they claim
//
// Run them against the script the case is measured against — for the #656 cases the
// version before that fix landed, for the four newer ones the version before #662. Fetch
// it by PR rather than by branch commit (a squash-merged branch's SHAs never reach main):
//
//	git fetch origin refs/pull/660/head && git checkout FETCH_HEAD -- install.sh
//	go test -count=1 -run TestInstallScript -v .   # the four #662/#663/#664 failures
//	git checkout HEAD -- install.sh
//
// `go test` keys its cache on files the test process opens, and install.sh reaches bash as
// a path, so installFixture.run reads it purely to register it as an input — without that,
// editing only install.sh replays a cached PASS. Belt and braces, pass -count=1.
//
// Note the restore is `git checkout HEAD --`, not `git checkout --`: the first form also
// writes the index, so the plain undo would restore the pre-fix file from there.

// releasesJSON is a GitHub /releases payload: the first tag is the version install.sh
// resolves, and all of them feed the available-versions tip. Pretty-printed one field per
// line because that is what the REST API sends — not because the parse needs it. It used
// to need it: the tag came out of a greedy `sed -E 's/.*"([^"]+)".*/\1/'`, which captures
// the last quoted run on the line, so a compact body resolved an upload_url as the version
// (#663). releasesJSONCompact is the guard that the shape is no longer load-bearing, and
// it is why this constant is free to stay pretty.
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

// releasesJSONCompact is releasesJSON with the whitespace taken out — the body a proxy, a
// cache or a `jq -c` wrapper would hand the installer. Every tag is on one line, and each
// release puts upload_url BEFORE tag_name, which is what makes this discriminating:
//
//	greedy `.*"([^"]+)".*`          -> https://uploads.github.com/…/3/assets{?name,label}
//	anchored `.*"tag_name": *"…".*` -> 9.9.7, the OLDEST release, because .* is still greedy
//	first match of `"tag_name": *"[^"]*"` -> 9.9.9
//
// Only the third is the newest release, so a case using this body pins the parse rather
// than merely rejecting a URL.
const releasesJSONCompact = `[{"upload_url":"https://uploads.github.com/repos/ZviBaratz/atrium/releases/1/assets{?name,label}","tag_name":"v9.9.9"},` +
	`{"upload_url":"https://uploads.github.com/repos/ZviBaratz/atrium/releases/2/assets{?name,label}","tag_name":"v9.9.8"},` +
	`{"upload_url":"https://uploads.github.com/repos/ZviBaratz/atrium/releases/3/assets{?name,label}","tag_name":"v9.9.7"}]
`

// releasesJSONPage builds a pretty-printed body of n releases, newest first, tagged
// v8.0.<n-1> down to v8.0.0. The available-versions tip caps its output, and a fixture with
// more releases than the cap is the only way to see the cap (#664).
func releasesJSONPage(n int) string {
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "  {\n    \"tag_name\": \"v8.0.%d\"\n  }", n-1-i)
	}
	b.WriteString("\n]\n")
	return b.String()
}

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
//
// The download branch writes the staged HTTP code to stdout before it decides whether to
// fail, which is what real curl does under `-w '%{http_code}'`: the code goes to stdout
// and `-sS`'s message to stderr, and install.sh merges the two with 2>&1. Staging it
// matters — a stub that hardcoded 200 for a simulated 404 would put a false value in the
// one field the "curl reported:" diagnostic exists to surface.
const stubCurlScript = `#!/usr/bin/env bash
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
        http=$(cat "$CTL_DIR/dl_http")
        printf '%s' "$http"
        if [ "$code" -ne 0 ]; then
            echo "curl: ($code) The requested URL returned error: $http" >&2
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
const stubTmuxScript = `#!/usr/bin/env bash
code=$(cat "$CTL_DIR/tmux_exit")
[ "$1" = "-V" ] && [ "$code" -eq 0 ] && cat "$CTL_DIR/tmux_version"
exit "$code"
`

const stubGhScript = `#!/usr/bin/env bash
exit 0
`

// stubForbiddenScript stands in for the package managers and the privilege escalation
// install.sh reaches for when a dependency is missing. Reaching one means the stub PATH
// failed, so it records and refuses rather than doing anything.
const stubForbiddenScript = `#!/usr/bin/env bash
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
// shells out to (uname, tr, mktemp, tar, grep, sed, head, mkdir, mv, ln, chmod, rm) and
// nothing else. Deliberately not the caller's PATH — see this file's header.
var systemPathDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// requiredTools are the ones a missing copy of which has to surface as a skip rather than
// as a failure inside the script — see the probe in newInstallFixture. They are the tools
// on the path every run takes: `tr` lowercases uname's output, and grep/sed/head are what
// resolve a version out of the API response, so a host that keeps its coreutils elsewhere
// (Nix, Guix) would otherwise fail these cases with an unparseable-response verdict.
var requiredTools = []string{"uname", "tr", "mktemp", "tar", "grep", "sed", "head"}

// forbiddenCommands are stubbed as recorders, and every case asserts nothing reached one.
// `sudo` covers the Linux dependency branches and `brew` the macOS ones; the managers
// themselves are listed so a `command -v` probe cannot pick a real one off the system path.
//
// `which` is here for a different reason: check_command_exists used it to report where an
// existing install lives, and it is absent from minimal images, so #662 replaced it with
// `command -v` (a builtin, which no stub can intercept). Listing it turns every case in
// this file into a guard against it coming back.
var forbiddenCommands = []string{"sudo", "brew", "apt-get", "dnf", "yum", "pacman", "which"}

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
	shell      string   // SHELL env var, which picks $PROFILE and its syntax
	args       []string // command-line arguments for install.sh
	extraEnv   []string // appended verbatim to the child environment
	pathPrefix []string // prepended to systemPathDirs
	timeout    time.Duration
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
	for _, tool := range requiredTools {
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
		shell:   "/bin/bash",
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
	f.control(t, "dl_http", "200")
	f.control(t, "dl_payload", tarballWithAtrium(t, f.ctlDir))
	f.control(t, "tmux_exit", "0")
	f.control(t, "tmux_version", "tmux 9.9\n")
	return f
}

// writeStub installs one fake command and checks it parses. These files are invisible to
// every guard built on the committed-script list, by design, and that same design removes
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
		"SHELL=" + f.shell,
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

	// A deadline, not a formality: install.sh has had an argument-parsing loop that spun
	// forever, and a hang has to surface as a failed case rather than as a suite that
	// never returns.
	timeout := f.timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", append([]string{script}, f.args...)...)
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
	// manager (or `which`), and a case that forgot to check would be the one that needed
	// it. See forbiddenCommands for what is on the list and why.
	require.Empty(t, res.forbidden, "install.sh escaped the stubs and reached for a forbidden command")
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

// TestInstallScriptRestoresErrexit reads the one line #662 was about.
//
// A text assertion because nothing behavioural can see this: `main "$@" || exit 1` exempts
// both sides of the list from errexit and the exemption reaches into every function called
// there, so putting it back would silently re-inert the `set -e` at the top of the file
// while every case in this file still passed — each of them reaches its verdict through
// `err` or an explicit `exit`, never through errexit. The guards for the three sites that
// needed the exemption live in the two tests below; this one guards the switch itself.
func TestInstallScriptRestoresErrexit(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(moduleRoot(t), "install.sh"))
	require.NoError(t, err)

	// Comments are skipped rather than searched: the note above the call quotes the form it
	// forbids, so a whole-file substring check reports the documentation as the defect.
	var setE bool
	var calls []string
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if line == "set -e" {
			setE = true
		}
		if strings.Contains(line, `main "$@"`) {
			calls = append(calls, line)
		}
	}

	require.True(t, setE, "errexit has to be set for calling main bare to mean anything")
	require.Equal(t, []string{`main "$@"`}, calls,
		"main must be called bare: anything after it — `|| exit 1`, a pipe — exempts the "+
			"whole install from errexit, which is what let a failed step report success (#662)")
}

// TestInstallScriptFailurePaths is the guard for #656, and for the failure paths #662 added.
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
		f.control(t, "dl_http", "404")

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		require.Contains(t, res.stdout, "curl reported: 404",
			"the HTTP code is the one fact that diagnostic exists to surface")
		// Both halves of this were broken: API_RESPONSE never left get_latest_version's
		// subshell, and the tip was gated on a $version main had already resolved away
		// from "latest", so the block could not run even with a response to print.
		require.Contains(t, res.stdout, "Tip: Try installing a specific version instead of 'latest'")
		require.Contains(t, res.stdout, "Available versions (newest 10):")
		// The older tags are what discriminate: they can only come from API_RESPONSE
		// crossing the subshell boundary. 9.9.9 alone would not — it also appears in the
		// "Expected asset name" line above the tip.
		require.Contains(t, res.stdout, "9.9.8")
		require.Contains(t, res.stdout, "9.9.7")
	})

	t.Run("an unwritable profile installs but exits non-zero", func(t *testing.T) {
		// The #662 reproduction. setup_shell_and_path appended the PATH line to $PROFILE
		// with no guard, so an unwritable one printed bash's raw redirection error and the
		// install carried on to a success banner and exit 0 — with BIN_DIR never on PATH,
		// which is the one thing that append exists to do.
		//
		// The fix is not "abort": the append runs before the download, and on an
		// immutable-dotfiles setup (Nix home-manager's read-only ~/.zshrc, chezmoi, stow)
		// BIN_DIR is writable and the binary works, so refusing to install throws away a
		// working install over a file the installer does not need. It installs, says what
		// did not happen, and exits non-zero.
		//
		// The exit code is what discriminates against the old script, which printed a
		// message naming this same path (bash's own) and exited 0 — so an assertion on the
		// path alone would pass against it. Code plus our text is the pair that cannot.
		if os.Geteuid() == 0 {
			t.Skip("root ignores the mode bits this case turns on")
		}
		f := newInstallFixture(t)
		profile := filepath.Join(f.home, ".bashrc")
		require.NoError(t, os.WriteFile(profile, []byte("# pre-existing\n"), 0o444))

		res := f.run(t)

		require.Equal(t, 1, res.exitCode, "PATH setup did not happen, so this is not a clean install")
		require.Contains(t, res.stderr, "Could not append to "+profile,
			"the immediate note has to name the profile and be ours, not bash's redirection error")

		// Installed, and said so: the binary is the half that worked.
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
		require.Contains(t, res.stdout, "Installed as 'atrium':")
		require.Len(t, res.curlCalls, 2, "the install itself must run to completion")

		// …and the warning is the last thing on screen, after the banner it qualifies.
		require.Contains(t, res.stdout, "WARNING: 'atrium' is installed, but "+f.binDir+" is not on your PATH.")
		require.Contains(t, res.stdout, profile+" could not be appended to")
		require.Contains(t, res.stdout, `export PATH="$PATH:`+f.binDir+`"`,
			"a user who has to do this by hand needs the line quoted for them")
		require.Contains(t, res.stdout, "run it by full path: "+filepath.Join(f.binDir, "atrium"))
		require.Greater(t, strings.Index(res.stdout, "WARNING:"), strings.Index(res.stdout, "Installed as"),
			"a warning above the success banner is one the banner scrolls off")

		// The profile is left exactly as it was: a failed append must not half-write.
		body, err := os.ReadFile(profile)
		require.NoError(t, err)
		require.Equal(t, "# pre-existing\n", string(body))
	})

	t.Run("the available versions tip is capped at ten", func(t *testing.T) {
		// #664: the tip prints under the whole download diagnosis, and the API returns up
		// to 30 releases per page. Twelve here so the cap is visible in both directions.
		f := newInstallFixture(t)
		f.control(t, "api_body", releasesJSONPage(12))
		f.control(t, "dl_exit", "22")
		f.control(t, "dl_http", "404")

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		require.Contains(t, res.stdout, "Available versions (newest 10):",
			"the label has to state the cap, or the list reads as everything that exists")
		require.Contains(t, res.stdout, "8.0.11", "the newest release is 8.0.11")
		require.Contains(t, res.stdout, "8.0.2", "the tenth-newest is still inside the cap")
		require.NotContains(t, res.stdout, "8.0.1\n", "the eleventh release is past the cap")
		require.NotContains(t, res.stdout, "8.0.0", "the twelfth release is past the cap")
	})

	t.Run("--name without a value fails instead of hanging", func(t *testing.T) {
		// `shift 2` with one argument left shifts nothing and succeeds, so `while [[ $#
		// -gt 0 ]]` spun forever. On the documented `curl … | bash -s -- --name <n>`
		// that is a silent terminal that never returns — no output, no exit, nothing to
		// report. The short deadline is the assertion: a regression here reports a
		// killed process (-1) rather than wedging the suite for a minute.
		f := newInstallFixture(t)
		f.args = []string{"--name"}
		f.timeout = 15 * time.Second

		res := f.run(t)

		require.Equal(t, 1, res.exitCode, "a flag missing its value must fail fast")
		require.Contains(t, res.stderr, "--name needs a value")
		require.Empty(t, res.curlCalls, "argument parsing failed, so nothing should have been fetched")
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
		f.control(t, "dl_http", "404")

		res := f.run(t)

		require.Equal(t, 1, res.exitCode)
		// No colon: the label carries the cap now ("Available versions (newest 10):"), and
		// an assertion on the old exact string would pass even if the whole tip printed.
		require.NotContains(t, res.stdout, "Available versions",
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
		// Injected from the environment for the same reason API_RESPONSE is in the
		// explicit-version case below: main initializes PATH_SETUP_FAILED itself, and this
		// is what proves it, rather than trusting that an unset global reads as false. A
		// stray one in the caller's environment must not fail an install whose profile was
		// written fine.
		f.extraEnv = append(f.extraEnv, "PATH_SETUP_FAILED=true")

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		require.NotContains(t, res.stdout, "WARNING",
			"nothing here warrants a warning: the profile was written and tmux is new")
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

	t.Run("a compact release body resolves the newest tag", func(t *testing.T) {
		// #663. Against the greedy sed this run does not merely pick the wrong release —
		// it asks GitHub for atrium_https://uploads.github.com/…_linux_amd64.tar.gz, which
		// is a -o path with slashes in it, so the download fails and the install exits 1.
		// Anchoring alone would land on 9.9.7 here; see releasesJSONCompact.
		f := newInstallFixture(t)
		f.control(t, "api_body", releasesJSONCompact)

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		require.Len(t, res.curlCalls, 2)
		require.Contains(t, res.curlCalls[1], "/v9.9.9/atrium_9.9.9_",
			"the newest tag is the first one in the body, whatever the whitespace")
		require.Contains(t, res.stdout, "Installed as 'atrium':")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
	})

	t.Run("a fish profile gets fish syntax in a directory that did not exist", func(t *testing.T) {
		// Two defects in one line, both #662: ~/.config/fish does not exist until fish
		// first writes there, so the append failed outright on a fresh install — and what
		// it appended was `export PATH=…`, which fish has no `export` for, so even where
		// the directory existed the line could not put BIN_DIR on PATH.
		f := newInstallFixture(t)
		f.shell = "/usr/bin/fish"

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		profile, err := os.ReadFile(filepath.Join(f.home, ".config", "fish", "config.fish"))
		require.NoError(t, err, "the fish config dir has to be created, not assumed")
		require.Contains(t, string(profile), `set -gx PATH $PATH "`+f.binDir+`"`)
		require.NotContains(t, string(profile), "export PATH",
			"fish has no export builtin, so that line cannot put BIN_DIR on a fish PATH")
	})

	t.Run("a filesystem without symlinks still installs", func(t *testing.T) {
		// The `atr` alias is a convenience — extract_and_install already skips it for a
		// custom --name and on Windows — so a filesystem that cannot make symlinks (an
		// exFAT stick, WSL's DrvFs) must not fail the install. Under restored errexit an
		// unguarded `ln` aborts here, *after* the binary is in place: the run exits 1 with
		// ln's message and no version banner, reporting a working install as a total
		// failure. Which is #662 with the sign flipped, so it belongs to the same fix.
		f := newInstallFixture(t)
		f.writeStub(t, "ln", "#!/usr/bin/env bash\necho \"ln: failed to create symbolic link: Operation not supported\" >&2\nexit 1\n")

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "the alias is optional; stderr was: %s", res.stderr)
		require.Contains(t, res.stdout, "Installed as 'atrium':", "the install itself succeeded")
		require.Contains(t, res.stdout, "atrium version 9.9.9 (test fixture)")
		require.Contains(t, res.stdout, "Could not link 'atr' -> 'atrium'",
			"a link that was not made has to be reported as not made")
		require.NotContains(t, res.stdout, "Linked 'atr'",
			"the old code claimed the link unconditionally, ln having just failed")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
		require.NoFileExists(t, filepath.Join(f.binDir, "atr"))
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

	t.Run("a release note mentioning Not Found still installs", func(t *testing.T) {
		// The "no releases" check greps the whole payload, and the releases JSON carries
		// every release's body — so a bare `grep -q "Not Found"` rejects a perfectly good
		// response over a release note. That is not hypothetical for this repo: the line
		// below is the shape of its own commit titles. Matching the error envelope
		// instead is what makes this pass.
		f := newInstallFixture(t)
		f.control(t, "api_body", `[
  {
    "body": "fix: handle Not Found from the GitHub API",
    "tag_name": "v9.9.9"
  }
]`)

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "stderr was: %s", res.stderr)
		require.NotContains(t, res.stderr, "No releases found in the repository")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
	})

	t.Run("unreadable tmux version does not fail the install", func(t *testing.T) {
		// errexit is live under main (#662), and `raw="$(tmux -V 2>/dev/null)"` is a simple
		// command: without the `|| raw=""` beside it, a tmux that cannot report its version
		// aborts the whole install. check_tmux_version is warning-only by design — Atrium
		// installs fine against an old or unreadable tmux — so this case is what holds the
		// guard in place. Verified red by deleting that `|| raw=""`.
		f := newInstallFixture(t)
		f.control(t, "tmux_exit", "1")

		res := f.run(t)

		require.Equal(t, 0, res.exitCode, "a tmux that cannot report its version must not fail the install: %s", res.stderr)
		require.NotContains(t, res.stdout, "WARNING:", "an unreadable version is not evidence of an old one")
		require.FileExists(t, filepath.Join(f.binDir, "atrium"))
	})
}
