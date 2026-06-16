# Release notes

Curated, human-written release notes — one file per release. When a file exists
for the tag being released, it becomes the GitHub release body (and, in future,
the in-app "What's new" surface); otherwise GoReleaser falls back to an
auto-generated commit-list changelog.

## How it's wired

`.github/workflows/release.yml` ("Determine GoReleaser args" step) checks for
`.github/release-notes/<tag>.md` and, when present, runs
`goreleaser release --release-notes <file>`. The `<tag>` is taken verbatim from
the pushed git tag, so:

- The filename **must include the `v` prefix**: `v0.6.0.md`, not `0.6.0.md`.
- It must match the tag exactly: tag `v0.6.0` → `.github/release-notes/v0.6.0.md`.

Files that don't match a tag (this `README.md`, the `TEMPLATE.md`) are ignored by
the workflow and never published.

## Process

Author the notes **before tagging the release**, in the same PR or commit that
prepares the release:

1. Copy `TEMPLATE.md` to `v<X.Y.Z>.md` (with the `v`).
2. Write for a user reading them on launch — lead with what changed for *them*,
   not the commit log. Group into Highlights / Fixes; keep it skimmable.
3. Push the matching `v<X.Y.Z>` tag. The workflow picks the file up automatically.

Don't backfill already-published tags — their GitHub release bodies are frozen,
so a late file changes nothing.

## Style

- Audience is a user who just updated, not a contributor reading the diff.
- Lead with the change and its benefit; mention internals only when they affect
  the user.
- Short bullets, present tense ("adds", "fixes"), grouped under clear headings.
- `v0.1.0.md` is a good worked example.
