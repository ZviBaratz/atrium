# Release notes

Curated, human-written release notes — one file per release. When a file exists
for the tag being released, it becomes the GitHub release body; otherwise
GoReleaser falls back to an auto-generated commit-list changelog.

That body has **two** readers, and the second one constrains how you write it:
Atrium fetches the published release back and shows it in-app as the "What's new"
modal after an update. See [Style](#style) before reaching for a table.

## How it's wired

`.github/workflows/release.yml` ("Determine GoReleaser args" step) checks for
`.github/release-notes/<tag>.md` and, when present, runs
`goreleaser release --release-notes <file>`. The `<tag>` is taken verbatim from
the pushed git tag, so:

- The filename **must include the `v` prefix**: `v0.6.0.md`, not `0.6.0.md`.
- It must match the tag exactly: tag `v0.6.0` → `.github/release-notes/v0.6.0.md`.

Files that don't match a tag (this `README.md`, the `TEMPLATE.md`) are ignored by
the workflow and never published.

`--release-notes` replaces the **whole** body, so GoReleaser's auto-generated
"Full Changelog" line is not appended — add a
`https://github.com/ZviBaratz/atrium/compare/<prev>...<tag>` link by hand. GitHub
renders a bare compare URL as its own `<prev>...<tag>` chip.

## Process

1. Copy `TEMPLATE.md` to `v<X.Y.Z>.md` (with the `v`).
2. Write for a user reading them on launch — lead with what changed for *them*,
   not the commit log. Group into Highlights / Fixes; keep it skimmable.
3. **Reconcile immediately before tagging** — see below.
4. Push the matching `v<X.Y.Z>` tag. The workflow picks the file up automatically.

Step 1 may happen whenever the release has a shape; the file is inert until a
matching tag exists, so a draft can sit on `main` and grow with the work. That is
the normal case for a long cycle, and it is why step 3 is not optional: a file
written a hundred merges ago describes a release that no longer exists.

**Reconciling.** Walk `git log <prev-tag>..main --grep '^feat'` and strike each
commit off the file; whatever is left over is a gap. Then diff the feature list
against the **previous release's notes**, not just the commit range — a predicate
or a flag that already shipped reads as new when the list is built from
`git log` alone, and the commit range cannot see what the last release already
claimed.

Don't backfill already-published tags — their GitHub release bodies are frozen,
so a late file changes nothing.

## Style

- Audience is a user who just updated, not a contributor reading the diff.
- Lead with the change and its benefit; mention internals only when they affect
  the user.
- Short bullets, present tense ("adds", "fixes"), grouped under clear headings.
  A big release earns `###` subgroups inside Highlights so it skims by theme; a
  small one doesn't. Compare `v0.9.0.md` (eight subgroups) with `v0.7.0.md`
  (three flat bullets).
- **Headings and bullets only — no tables, no HTML, no `<details>`.** The
  published body is shown in-app as the "What's new" modal, which renders it as
  *raw text*: `app/app_releasenotes.go` writes it straight into a `TextOverlay`,
  and nothing in that path parses markdown. Headings, bullets, `**bold**` and
  backticks degrade readably; a table or a tag ships as literal noise. Check both
  surfaces before merging — `fold -s -w 76 <file>` for the modal, and
  `gh api /markdown --input <(jq -Rs '{text:.,mode:"gfm"}' <file>)` for the
  release page.
- `v0.1.0.md` is a good worked example.
