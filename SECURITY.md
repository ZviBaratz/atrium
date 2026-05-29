# Security Policy

## Supported versions

Atrium is pre-1.0 and released from `main`. Security fixes land on the latest
release; please reproduce on the most recent version before reporting.

| Version | Supported          |
| ------- | ------------------ |
| latest  | :white_check_mark: |
| < latest | :x:               |

## Reporting a vulnerability

**Please do not open public issues for security problems.**

Use GitHub's private vulnerability reporting:

1. Go to the [Security tab](https://github.com/ZviBaratz/atrium/security/advisories)
   of this repository.
2. Click **Report a vulnerability**.

If you cannot use that channel, email **z.baratz@gmail.com** with details and,
ideally, a minimal reproduction. You can expect an initial response within a few
days. Please allow reasonable time for a fix before any public disclosure.

When reporting, include the Atrium version (`atrium version`), your OS, and the
steps to reproduce.

## Verifying releases

Every release is built by GitHub Actions with a pinned, auditable workflow.
Release artifacts can be verified two complementary ways. Both anchor to
`checksums.txt`, which lists the digest of every archive.

### 1. Build provenance (SLSA, GitHub-native)

Each artifact carries a signed provenance attestation tying it to the exact
workflow run that produced it. Verify with the GitHub CLI:

```bash
gh attestation verify atrium_<version>_<os>_<arch>.tar.gz --repo ZviBaratz/atrium
```

### 2. Keyless signature (Sigstore / cosign)

`checksums.txt` is signed with [cosign](https://github.com/sigstore/cosign)
keyless signing. The signature (`checksums.txt.sig`) and the ephemeral signing
certificate (`checksums.txt.pem`) are attached to each release.

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/ZviBaratz/atrium/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# Then confirm your downloaded artifact matches the verified checksums:
sha256sum --check --ignore-missing checksums.txt
```

A Software Bill of Materials (SBOM) is published alongside each archive for
dependency auditing.
