#!/usr/bin/env bash

set -e

setup_shell_and_path() {
    local path_line profile_dir
    BIN_DIR=${BIN_DIR:-$HOME/.local/bin}

    # The line to append is per-shell because the profile's language is. Default to the
    # POSIX-ish form and let a branch below override it.
    path_line="export PATH=\"\$PATH:$BIN_DIR\""

    case $SHELL in
        */zsh)
            PROFILE=$HOME/.zshrc
            ;;
        */bash)
            PROFILE=$HOME/.bashrc
            ;;
        */fish)
            PROFILE=$HOME/.config/fish/config.fish
            # fish has no `export` builtin, and its $PATH is a list rather than a
            # colon-joined string, so the default line above cannot put BIN_DIR on a fish
            # PATH — it fails at every startup instead, which is the same "installed but
            # not reachable" outcome as an append that never landed (#662). `set -gx` is
            # the fish spelling and is valid on every fish; fish_add_path would be
            # idempotent, but it needs fish 3.2 or newer.
            path_line="set -gx PATH \$PATH \"$BIN_DIR\""
            ;;
        */ash)
            PROFILE=$HOME/.profile
            ;;
        *)
            echo "could not detect shell, manually add ${BIN_DIR} to your PATH."
            exit 1
    esac

    if [[ ":$PATH:" != *":${BIN_DIR}:"* ]]; then
        # The profile's directory can be missing: fish creates ~/.config/fish on its
        # first run, and a `curl … | bash` install can come first (#662). Created only
        # when there is something to write, and skipped when the name has no directory
        # part at all — $HOME unset leaves PROFILE=/.bashrc, where `mkdir -p ''` would
        # report a failure about the wrong thing.
        profile_dir=${PROFILE%/*}
        if [ -n "$profile_dir" ] && [ ! -d "$profile_dir" ]; then
            ensure mkdir -p "$profile_dir"
        fi

        # One printf rather than the two echos this replaces, and deliberately NOT a
        # `{ …; } >> "$PROFILE"` group: bash reports a failed redirection on a compound
        # command and sets $? to 1, but negating that still yields 1 — measured on bash
        # 5.3.9 — so `if ! { …; } >> file` takes the *success* branch and swallows exactly
        # the failure this guards. A simple command's status negates correctly.
        #
        # Guarded because errexit alone would abort at the call site in main: this `if`
        # is the last command in the function, so its status is the function's, and the
        # message a user sees has to name the profile and the line rather than
        # "setup_shell_and_path".
        if ! printf '\n%s\n' "$path_line" >> "$PROFILE"; then
            echo "Failed to add ${BIN_DIR} to your PATH: could not append to $PROFILE" >&2
            err "Add this line to your shell profile by hand, or set BIN_DIR to a directory already on your PATH, and re-run: $path_line"
        fi
    fi
}

detect_platform_and_arch() {
    PLATFORM="$(uname | tr '[:upper:]' '[:lower:]')"
    if [[ "$PLATFORM" == mingw*_nt* ]]; then
        PLATFORM="windows"
    fi

    ARCHITECTURE="$(uname -m)"
    if [ "${ARCHITECTURE}" = "x86_64" ]; then
        # Redirect stderr to /dev/null to avoid printing errors if non Rosetta.
        if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = "1" ]; then
            ARCHITECTURE="arm64" # Rosetta.
        else
            ARCHITECTURE="amd64" # Intel.
        fi
    elif [ "${ARCHITECTURE}" = "arm64" ] || [ "${ARCHITECTURE}" = "aarch64" ]; then
        ARCHITECTURE="arm64" # Arm.
    else
        ARCHITECTURE="amd64" # Amd.
    fi

    if [[ "$PLATFORM" == "windows" ]]; then
        ARCHIVE_EXT=".zip"
        EXTENSION=".exe"
    else
        ARCHIVE_EXT=".tar.gz"
        EXTENSION=""
    fi
}

# release_tags prints every tag in API_RESPONSE, one per line, newest release first, with
# the leading "v" stripped. Both places that need a tag go through here so the parse has
# one home: the version lookup takes the first line, the "Available versions" tip below
# lists them.
#
# Unlike get_latest_version below, this one IS safe in a command substitution — it only
# reads the global and prints, and never calls err — which is why the callers can pipe it.
#
# `grep -o` rather than a `sed` over the whole line, because a leading `.*` is greedy and
# so captures the LAST match on the line, not the first (#663). The old pattern
# (`.*"([^"]+)".*`) took the last quoted run of any kind, which on a compact body is an
# upload_url; merely anchoring the key still takes the last *tag* when a body puts several
# on one line, i.e. the oldest release. Extracting matches makes the order explicit, and
# GitHub's pretty-printing stops being load-bearing.
#
# grep exits 1 when it selects nothing, but the pipeline's status is sed's, so a body with
# no tags prints nothing and still returns 0. Reporting that is the caller's job: the
# empty-LATEST_VERSION check below is what has the response body to show.
release_tags() {
    echo "$API_RESPONSE" | grep -o '"tag_name": *"[^"]*"' | sed -E 's/^"tag_name": *"(.*)"$/\1/' | sed 's/^v//'
}

# get_latest_version resolves the newest release, prereleases included, and answers
# through two globals rather than through stdout: LATEST_VERSION (the tag, leading "v"
# stripped) and API_RESPONSE (the raw body, which download_release quotes when an asset
# is missing).
#
# Answering through globals is the fix for #656, not a style choice, and it is why this
# function MUST BE CALLED DIRECTLY — never as `VERSION=$(get_latest_version)`, in a
# pipeline, or in any other subshell. That is what the caller used to do, and a command
# substitution is a subshell: every message below landed in VERSION instead of the
# terminal, the `exit 1` ended only the subshell, and the install carried on with the
# error text as the version string, asking GitHub for
# "atrium_Failed to connect to GitHub API_linux_amd64.tar.gz". API_RESPONSE never
# reached the parent either, so the "Available versions" tip had nothing to print.
#
# Failures leave through err(), so they land on stderr and stop the script. Both halves
# matter: stderr is what survives a caller that captures stdout, and the stopping is the
# direct call's doing, since `exit` in a normally-called function leaves the whole script
# — which is why it reports the same way whether or not errexit is in force.
# On success it prints nothing and returns 0 — a trailing `if` with a false condition and
# no else branch returns 0, so appending to this function needs care.
#
# TestInstallScriptFailurePaths drives every failure below.
#
# Testing the curl directly rather than via a following `$?` keeps that branch reachable
# on its own terms: a bare `VAR=$(curl …)` is a simple command, which errexit would abort
# before any `$?` check ran.
#
# stderr is deliberately not merged into the capture: -sS already prints curl's error to
# the terminal, and this value is parsed for "tag_name" below.
get_latest_version() {
    if ! API_RESPONSE=$(curl -sS "https://api.github.com/repos/ZviBaratz/atrium/releases"); then
        err "Failed to connect to GitHub API"
    fi

    # Match the error envelope, not the whole payload: a bare "Not Found" also matches a
    # release note that happens to mention it ("fix: handle Not Found from the GitHub
    # API" is an entirely plausible line in this repo's own history), and the releases
    # JSON carries every release's body. That false positive now aborts the install
    # rather than merely garbling it, so it is worth being exact about.
    if echo "$API_RESPONSE" | grep -q '"message": *"Not Found"'; then
        err "No releases found in the repository"
    fi

    # Get the first release (latest) from the array
    LATEST_VERSION=$(release_tags | head -n 1)
    if [ -z "$LATEST_VERSION" ]; then
        # The body is the evidence a bug report needs, so it goes out before the verdict
        # — err prints one line and exits, so nothing after it would run. Both halves go
        # to stderr so they stay together. `|| true` because grep exits 1 when it selects
        # nothing (an empty body, or one whose every line matches), which errexit — live
        # again since #662 — would otherwise turn into an exit before the verdict.
        echo "GitHub API response was:" >&2
        echo "$API_RESPONSE" | grep -v "upload_url" >&2 || true # Drop the long upload_url line
        err "Failed to parse a version from the GitHub API response above"
    fi
}

download_release() {
    local version=$1
    local binary_url=$2
    local archive_name=$3
    local tmp_dir=$4
    # What the caller ASKED for, which is not what $version holds: main resolves "latest"
    # to a concrete number before calling. Gating the tip below on $version is what made
    # that whole block unreachable on every path (#656). API_RESPONSE stays a global
    # because it is bulk data produced by get_latest_version, not a per-call input.
    local requested_version=${5:-}
    local download_output

    echo "Downloading binary from $binary_url"
    # As in get_latest_version, the curl is tested directly rather than via a following
    # `$?`. The capture is the point here: -w writes the HTTP status to stdout and -sS
    # writes curl's message to stderr, so `2>&1` collects both — and until now nothing
    # read them, so every download failure reported the guesses below without the one
    # fact that settles it. The old `HTTP_CODE=$?` was curl's exit status, not the HTTP
    # code, which is why it could not be printed as one.
    if ! download_output=$(curl -sS -L -f -w '%{http_code}' "$binary_url" -o "${tmp_dir}/${archive_name}" 2>&1); then
        echo "Error: Failed to download release asset"
        echo "curl reported: ${download_output}"
        echo "This could be because:"
        echo "1. The release ${version} doesn't have assets uploaded yet"
        echo "2. The asset for ${PLATFORM}_${ARCHITECTURE} wasn't built"
        echo "3. The asset name format has changed"
        echo ""
        echo "Expected asset name: ${archive_name}"
        echo "URL attempted: ${binary_url}"
        if [ "$requested_version" == "latest" ]; then
            echo ""
            echo "Tip: Try installing a specific version instead of 'latest'"
            # Capped, and labelled with the cap: the API returns up to 30 releases per
            # page, and this prints under the whole diagnosis above (#664). Labelled
            # because an unlabelled truncated list reads as everything that exists.
            echo "Available versions (newest 10):"
            release_tags | head -n 10
        fi
        rm -rf "$tmp_dir"
        exit 1
    fi
}

extract_and_install() {
    local tmp_dir=$1
    local archive_name=$2
    local bin_dir=$3
    local extension=$4

    if [[ "$PLATFORM" == "windows" ]]; then
        if ! unzip -t "${tmp_dir}/${archive_name}" > /dev/null 2>&1; then
            echo "Error: Downloaded file is not a valid zip archive"
            rm -rf "$tmp_dir"
            exit 1
        fi
        ensure unzip "${tmp_dir}/${archive_name}" -d "$tmp_dir"
    else
        if ! tar tzf "${tmp_dir}/${archive_name}" > /dev/null 2>&1; then
            echo "Error: Downloaded file is not a valid tar.gz archive"
            rm -rf "$tmp_dir"
            exit 1
        fi
        ensure tar xzf "${tmp_dir}/${archive_name}" -C "$tmp_dir"
    fi

    if [ ! -d "$bin_dir" ]; then
        ensure mkdir -p "$bin_dir"
    fi

    # Remove existing binary if upgrading
    if [ "$UPGRADE_MODE" = true ] && [ -f "$bin_dir/$INSTALL_NAME${extension}" ]; then
        echo "Removing previous installation from $bin_dir/$INSTALL_NAME${extension}"
        rm -f "$bin_dir/$INSTALL_NAME${extension}"
    fi

    # Install binary with desired name. Through ensure, which names both paths: errexit
    # would abort here too, but with nothing on the terminal except mv's own message. The
    # `[ ! -f … ]` check that used to follow is gone with it — it could only ever fire
    # because a failed mv was ignored, so under a guarded mv it is unreachable, and a
    # check that cannot run reads as one that does.
    ensure mv "${tmp_dir}/atrium${extension}" "$bin_dir/$INSTALL_NAME${extension}"
    rm -rf "$tmp_dir"

    ensure chmod +x "$bin_dir/$INSTALL_NAME${extension}"

    # Provide the short `atr` alias for the default install (skipped on Windows and
    # when a custom --name is used, to avoid clobbering an unrelated `atr`).
    #
    # Reported, never fatal, and that is the whole point of the branch: BIN_DIR can sit on
    # a filesystem with no symlinks (an exFAT stick, WSL's DrvFs), where an unguarded `ln`
    # under errexit aborts *after* a working binary is in place — so a complete install
    # exits 1 with ln's message and no version banner, which is #662 with the sign
    # flipped. The old code was worse than either: it printed "Linked" unconditionally,
    # claiming a link it had just failed to make.
    if [ "$INSTALL_NAME" = "atrium" ] && [ "$PLATFORM" != "windows" ]; then
        if ln -sf "$bin_dir/atrium" "$bin_dir/atr"; then
            echo "Linked 'atr' -> 'atrium'."
        else
            echo "Could not link 'atr' -> 'atrium' — a filesystem without symlinks? Use '$INSTALL_NAME' directly."
        fi
    fi

    # Ask the binary for its version before announcing success, so the two agree. The
    # old `echo "$(… version)"` reported echo's exit status, which left a binary that
    # could not run — wrong arch, truncated asset — printing a success banner and a
    # blank line, then exiting 0. Testing it here reports that as the failure it is.
    # stderr is deliberately not merged in: this value is printed as the version line
    # below, so a loader warning from a binary that runs-but-complains would be spliced
    # into it. Left on the terminal, it still reaches the user in both outcomes.
    local installed_version
    if ! installed_version=$("$bin_dir/$INSTALL_NAME${extension}" version); then
        echo "Installed to $bin_dir/$INSTALL_NAME${extension}, but it could not run."
        if [ "$UPGRADE_MODE" = true ]; then
            # The old binary was removed above and `atr` already repointed, so there is
            # nothing left to fall back to — say so, because that changes the fix.
            echo "The previous '$INSTALL_NAME' has already been replaced; reinstall a known-good version to recover."
        fi
        exit 1
    fi

    echo ""
    if [ "$UPGRADE_MODE" = true ]; then
        echo "Successfully upgraded '$INSTALL_NAME' to:"
    else
        echo "Installed as '$INSTALL_NAME':"
    fi
    echo "$installed_version"
}

# check_command_exists decides between a fresh install and an upgrade.
#
# `command -v`, not `which`: which is not POSIX and is absent from minimal images, where it
# returned nothing and left the banner below reading "at " with a blank after it — and
# under errexit its non-zero status would abort the install outright (#662). Taking the
# path from the `if` condition also makes it one lookup instead of two, and an assignment
# in a condition is exempt from errexit whether or not it finds anything.
check_command_exists() {
    if EXISTING_PATH=$(command -v "$INSTALL_NAME"); then
        echo "Found existing installation of '$INSTALL_NAME' at $EXISTING_PATH"
        echo "Will upgrade to the latest version"
        UPGRADE_MODE=true
    else
        UPGRADE_MODE=false
    fi
}

# The oldest tmux Atrium can start a session on. Kept in step with
# session/tmux.MinVersion by TestInstallScriptStatesTheTmuxFloor.
MIN_TMUX_VERSION="3.2"

# check_tmux_version warns when the tmux on PATH is older than MIN_TMUX_VERSION.
# Never fatal, and never blocks on an unreadable version: tmux reports "3.2a" (a patch
# release), "next-3.4" (a prerelease) and, on OpenBSD, "openbsd-7.4" — an OS release
# rather than a tmux version, which must not be read as a number and compared.
check_tmux_version() {
    command -v tmux > /dev/null 2>&1 || return 0

    local raw found major minor want_major want_minor
    # Derive the threshold from MIN_TMUX_VERSION rather than repeating its digits in the
    # comparison below: a hardcoded "3"/"2" here would keep testing the old floor after
    # MIN_TMUX_VERSION was raised, while the warning text claimed the new one.
    want_major="${MIN_TMUX_VERSION%%.*}"
    want_minor="${MIN_TMUX_VERSION#*.}"

    # `|| raw=""` keeps this warning-only under errexit: a bare assignment from a command
    # substitution is a simple command, so a tmux that cannot report its version would
    # abort the whole install (#662). An empty raw falls out of the case below with a
    # silent return 0, which is the right answer — an unreadable version is not evidence
    # of an old one. TestInstallScriptInstallsAndUpgrades pins that.
    raw="$(tmux -V 2>/dev/null)" || raw=""
    # Strip the "tmux " prefix and an optional "next-", then keep MAJOR.MINOR only. Keep
    # the un-stripped token for the warning so it is not a second `tmux -V` — a second
    # exec could report a different binary than the one this verdict was computed from.
    found="${raw#tmux }"
    raw="${raw#tmux }"
    raw="${raw#next-}"
    case "$raw" in
        [0-9]*.[0-9]*) ;;
        *) return 0 ;;  # not a version we can compare — say nothing rather than guess
    esac
    major="${raw%%.*}"
    minor="${raw#*.}"
    minor="${minor%%[!0-9]*}"   # drop a trailing letter: 3.2a is a patch release of 3.2
    [ -n "$minor" ] || return 0

    if [ "$major" -lt "$want_major" ] || { [ "$major" -eq "$want_major" ] && [ "$minor" -lt "$want_minor" ]; }; then
        echo ""
        echo "WARNING: tmux $found is too old for Atrium."
        echo "         Atrium needs tmux $MIN_TMUX_VERSION or newer — every session is started with"
        echo "         'tmux new-session -e', which older versions reject, so no session will start."
        echo "         Your distribution's package is likely too old; see"
        echo "         https://github.com/tmux/tmux/wiki/Installing"
        echo ""
    fi
}

check_and_install_dependencies() {
    echo "Checking for required dependencies..."

    # Check for tmux
    if ! command -v tmux &> /dev/null; then
        echo "tmux is not installed. Installing tmux..."

        if [[ "$PLATFORM" == "darwin" ]]; then
            # macOS
            if command -v brew &> /dev/null; then
                ensure brew install tmux
            else
                echo "Homebrew is not installed. Please install Homebrew first to install tmux."
                echo "Visit https://brew.sh for installation instructions."
                exit 1
            fi
        elif [[ "$PLATFORM" == "linux" ]]; then
            # Linux
            if command -v apt-get &> /dev/null; then
                ensure sudo apt-get update
                ensure sudo apt-get install -y tmux
            elif command -v dnf &> /dev/null; then
                ensure sudo dnf install -y tmux
            elif command -v yum &> /dev/null; then
                ensure sudo yum install -y tmux
            elif command -v pacman &> /dev/null; then
                ensure sudo pacman -S --noconfirm tmux
            else
                echo "Could not determine package manager. Please install tmux manually."
                exit 1
            fi
        elif [[ "$PLATFORM" == "windows" ]]; then
            echo "For Windows, please install tmux via WSL or another method."
            exit 1
        fi

        echo "tmux installed successfully."
    else
        echo "tmux is already installed."
    fi

    # Atrium starts every session with `tmux new-session -e`, which landed in tmux 3.2.
    # Below that no session can start at all, and the failure is a bare "timed out
    # waiting for tmux session" that names neither tmux nor its version.
    #
    # Checked after the branch above, not inside it, because the common case is a host
    # that ALREADY has an old tmux: that path installs nothing and would otherwise report
    # success. The distro package is exactly what ships an old one — RHEL/CentOS 7's yum
    # gives tmux 1.8, and Debian 11's apt gives 3.1c, the newest release without -e.
    #
    # A warning, not a failure: Atrium itself installs fine, and the user can upgrade
    # tmux afterwards.
    check_tmux_version
    # Check for GitHub CLI (gh)
    if ! command -v gh &> /dev/null; then
        echo "GitHub CLI (gh) is not installed. Installing GitHub CLI..."

        if [[ "$PLATFORM" == "darwin" ]]; then
            # macOS
            if command -v brew &> /dev/null; then
                ensure brew install gh
            else
                echo "Homebrew is not installed. Please install Homebrew first to install GitHub CLI."
                echo "Visit https://brew.sh for installation instructions."
                exit 1
            fi
        elif [[ "$PLATFORM" == "linux" ]]; then
            # Linux
            if command -v apt-get &> /dev/null; then
                echo "Installing GitHub CLI on Debian/Ubuntu..."
                ensure curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg
                ensure sudo chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg
                ensure echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
                ensure sudo apt-get update
                ensure sudo apt-get install -y gh
            elif command -v dnf &> /dev/null; then
                ensure sudo dnf install -y 'dnf-command(config-manager)'
                ensure sudo dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo
                ensure sudo dnf install -y gh
            elif command -v yum &> /dev/null; then
                ensure sudo yum install -y yum-utils
                ensure sudo yum-config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo
                ensure sudo yum install -y gh
            elif command -v pacman &> /dev/null; then
                ensure sudo pacman -S --noconfirm github-cli
            else
                echo "Could not determine package manager. Please install GitHub CLI manually."
                echo "Visit https://github.com/cli/cli#installation for installation instructions."
                exit 1
            fi
        elif [[ "$PLATFORM" == "windows" ]]; then
            echo "For Windows, please install GitHub CLI manually."
            echo "Visit https://github.com/cli/cli#installation for installation instructions."
            exit 1
        fi

        echo "GitHub CLI (gh) installed successfully."
    else
        echo "GitHub CLI (gh) is already installed."
    fi

    echo "All dependencies are installed."
}

main() {
    # Parse command line arguments
    INSTALL_NAME="atrium"
    UPGRADE_MODE=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            --name)
                # A bare `--name` used to hang forever: `shift 2` with one argument left
                # shifts nothing and succeeds, so $# never reached 0 and this loop spun.
                # On the documented `curl … | bash -s -- --name <n>` that is a terminal
                # sitting silent with no output and no exit — the worst of the failure
                # paths #656 is about.
                [ $# -ge 2 ] || err "--name needs a value. Usage: install.sh [--name <n>]"
                INSTALL_NAME="$2"
                shift 2
                ;;
            *)
                echo "Unknown option: $1"
                echo "Usage: install.sh [--name <n>]"
                exit 1
                ;;
        esac
    done

    check_command_exists
    detect_platform_and_arch

    check_and_install_dependencies

    setup_shell_and_path

    # Keep the request as well as the answer. download_release's "Available versions"
    # tip is for someone who asked for "latest", and after this block VERSION holds a
    # concrete number — so comparing *it* to "latest" down there can never be true, which
    # is exactly why that block had never run (#656).
    REQUESTED_VERSION=${VERSION:-"latest"}
    VERSION="$REQUESTED_VERSION"
    if [[ "$VERSION" == "latest" ]]; then
        # Called directly, not as `VERSION=$(get_latest_version)`: a command substitution
        # is a subshell, and this function reports through globals and exits through err.
        # See the note on the function.
        get_latest_version
        VERSION="$LATEST_VERSION"
    fi

    RELEASE_URL="https://github.com/ZviBaratz/atrium/releases/download/v${VERSION}"
    ARCHIVE_NAME="atrium_${VERSION}_${PLATFORM}_${ARCHITECTURE}${ARCHIVE_EXT}"
    BINARY_URL="${RELEASE_URL}/${ARCHIVE_NAME}"
    TMP_DIR=$(mktemp -d)

    download_release "$VERSION" "$BINARY_URL" "$ARCHIVE_NAME" "$TMP_DIR" "$REQUESTED_VERSION"
    extract_and_install "$TMP_DIR" "$ARCHIVE_NAME" "$BIN_DIR" "$EXTENSION"
}

# Run a command that should never fail. If the command fails execution
# will immediately terminate with an error showing the failing
# command.
ensure() {
    if ! "$@"; then err "command failed: $*"; fi
}

err() {
    echo "$1" >&2
    exit 1
}

# Called bare, so the `set -e` at the top of this file is in force for everything under
# main. Never `main "$@" || exit 1`: bash exempts both sides of a `&&`/`||` list from
# errexit and the exemption propagates into the functions called there, which is what let
# the profile append fail, print bash's error, and install anyway — reporting success with
# BIN_DIR off the user's PATH (#662).
#
# What that costs: a command under main that is ALLOWED to fail has to say so — `|| true`,
# `|| var=""`, or an `if`/`&&`/`||` context — and a command that must not fail silently
# still wants `ensure` or `err`, because errexit exits without explaining itself.
main "$@"
