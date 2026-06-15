<!--
Copy this file to v<X.Y.Z>.md (keep the leading v; match the git tag exactly).
Delete this comment and any sections you don't need. Write for a user who just
updated — lead with what changed for them, not the commit log. See README.md.
-->

One-line summary of what this release is about.

## Highlights

- User-facing feature or improvement, and why it matters.

## Fixes

- Notable bug fix, described by the symptom it resolves.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/ZviBaratz/atrium/main/install.sh | bash
```

Or with Go — note this builds from source and reports a `dev` version (the
script installs the version-stamped, self-updating release binary):

```bash
go install github.com/ZviBaratz/atrium@v<X.Y.Z>
```

This is a 0.x release — APIs and behavior may change.
