#!/usr/bin/env bash
#
# Vulnerability scan with a single, documented allowlist.
#
# govulncheck is symbol-level and low-noise: it fails only when a known-vulnerable
# function is actually reachable. It flags exactly one advisory here, GO-2026-5932:
# golang.org/x/crypto/openpgp is unmaintained/unsafe-by-design (Fixed in: N/A).
# See https://pkg.go.dev/vuln/GO-2026-5932.
#
# Why we accept this one advisory:
#   - Atrium pulls openpgp in only transitively, through
#     github.com/creativeprojects/go-selfupdate, whose package imports it for a
#     PGPValidator that Atrium never configures. The updater verifies releases with
#     a SHA256 ChecksumValidator over checksums.txt (fetched over HTTPS and
#     additionally cosign/Sigstore-signed; see .goreleaser.yaml). Every govulncheck
#     trace is an init() side-effect of that import chain, never a real openpgp
#     call, so the reachable attack surface is zero.
#   - Upstream cannot be bumped out of it: go-selfupdate's latest release and
#     current master both still import openpgp, with no fix in flight.
#   - Replacing the updater wholesale (hand-rolled release detection, download,
#     archive extraction, checksum verification, atomic binary swap) is
#     disproportionate to a non-exploitable "unmaintained package" flag.
#
# So we allowlist GO-2026-5932 and fail on ANYTHING else. To retire the allowlist,
# drop the entry once go-selfupdate stops importing x/crypto/openpgp.
#
# GO is overridable so this works where `go` isn't on PATH: `GO=/path/to/go`.
# Requires jq (preinstalled on GitHub's ubuntu-latest runners).
set -uo pipefail

GO="${GO:-go}"
ALLOW=(GO-2026-5932)

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# govulncheck exits 0 (no vulns) or 3 (vulns found) on a successful scan; any other
# code is a real tool/build error we must surface.
"$GO" run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... >"$tmp"
code=$?
if [ "$code" -ne 0 ] && [ "$code" -ne 3 ]; then
	echo "govulncheck failed to run (exit $code):" >&2
	cat "$tmp" >&2
	exit "$code"
fi

# A finding whose most-specific trace frame names a function is symbol-reachable —
# exactly what fails a build. Module/package-only findings are ignored, matching
# govulncheck's default text behavior.
reachable="$(jq -r 'select(.finding.trace[0].function != null) | .finding.osv' "$tmp" | sort -u)"

echo "Symbol-reachable advisories:"
echo "${reachable:-  (none)}"

# Strip blank lines and every allowlisted ID; whatever survives is unexpected.
# grep exits 1 when it filters everything out, so tolerate that with `|| true`.
allow_args=(-e '')
for id in "${ALLOW[@]}"; do
	allow_args+=(-e "$id")
done
unexpected="$(printf '%s\n' "$reachable" | grep -vxF "${allow_args[@]}" || true)"

if [ -n "$unexpected" ]; then
	echo "::error::Unexpected reachable vulnerabilities (not allowlisted):" >&2
	echo "$unexpected" >&2
	echo "If one is intentional, add it to ALLOW in scripts/govulncheck.sh with justification." >&2
	exit 1
fi

echo "OK: only the documented allowlist (${ALLOW[*]}) is reachable."
