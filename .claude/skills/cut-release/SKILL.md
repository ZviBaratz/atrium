---
name: cut-release
description: Use when cutting, tagging, or publishing a new Atrium release — a new vX.Y.Z version, a GitHub release, a GoReleaser build, or recovering a bad release tag or missing release notes.
---

# Cut an Atrium release

## Overview

A release is triggered by **pushing a `vX.Y.Z` tag**; the `release.yml` workflow
then runs GoReleaser to build artifacts and create a **draft** GitHub release. A
human publishes the draft — that is the real go-live.

Two invariants make or break it:

1. **Curated notes must be in the tagged commit's tree.** The workflow reads
   `.github/release-notes/<tag>.md` (the **leading `v` is load-bearing** — `0.7.0.md`
   is silently ignored and you ship the auto-changelog instead). So the notes must
   merge to `main` *before* you tag, and the tag must point at that commit. Order is
   **notes → merge → tag**, never tag-first/backfill-later (published bodies are frozen).
2. **The release is `draft: true`** (set in `.goreleaser.yaml`). Nothing is public
   until a human reviews the draft and clicks Publish. Forgetting this is the most
   likely miss.

## Procedure

1. **Confirm readiness.** Clean tree, `just build` + `just test` green, CI green on
   `main` HEAD. Pick the version (0.x → minor bump for features).
2. **Write notes on a branch.** `cp .github/release-notes/TEMPLATE.md
   .github/release-notes/vX.Y.Z.md` (keep the `v`). Lead with what changed for the
   *user*; group Highlights / Fixes; drop pure test/chore commits. `v0.6.0.md` /
   `v0.7.0.md` are the models. Update the `@vX.Y.Z` line in the Install block.
3. **PR → CI green → squash-merge to `main`.** Conventional lowercase title
   (`docs: add vX.Y.Z release notes`).
4. **Tag the merged commit and push.** `git checkout main && git pull`, confirm HEAD
   is the merge commit, then `just release X.Y.Z` (tags `vX.Y.Z` + pushes). This
   triggers the workflow.
5. **Watch the build**, then verify the draft body is the curated notes and assets
   attached (6 archives + SBOMs + `checksums.txt`/`.sig`/`.pem`).
6. **Hand the draft URL to the maintainer to publish.** Do not auto-publish.

## Quick reference

| Step | Command |
|------|---------|
| Build + test | `just build` && `just test` |
| go / hooks PATH | `export PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH"` |
| gh token (mutations) | `export GH_TOKEN="$(gh auth token --user ZviBaratz)"` |
| Tag + push (trigger) | `just release X.Y.Z` |
| Inspect draft | `gh api repos/ZviBaratz/atrium/releases/tags/vX.Y.Z --jq '{draft,body,assets:[.assets[].name]}'` |

## Recovering from a bad tag or notes filename

Only possible **while the release is still a draft** (published bodies are frozen).
GoReleaser is configured with `replace_existing_draft: true` (in `.goreleaser.yaml`),
so moving the tag rebuilds and replaces the draft. Fix the cause on `main` first
(rename `0.8.0.md` → `v0.8.0.md`, or add the missing notes), merge it, then move the
tag to that commit:

```bash
git push origin :refs/tags/vX.Y.Z   # delete the remote tag
git tag -d vX.Y.Z                    # delete locally
just release X.Y.Z                   # re-tag main's fixed HEAD + push → rebuilds the draft
```

If the release was already **published**, don't backfill — its body is frozen; ship
the correction in the next version.

## Gotchas

- **Auth:** all `gh` on this repo is the **ZviBaratz** account; `gh auth token`
  *without* `--user` returns the wrong (shared) keyring entry. Mutations need
  `GH_TOKEN` exported.
- **PATH for the pre-push hook:** the tag push runs golangci-lint + `go mod tidy`;
  without the mise go bin + `~/go/bin` on PATH they fail "executable not found".
- **Stale golangci cache across worktrees:** a lint failure citing a *different/deleted*
  worktree path is stale shared cache, not your change — `golangci-lint cache clean`,
  don't edit code.
- **`just release` tags current HEAD:** run it from `main`, not a stale feature
  worktree, or you tag the wrong commit.
- **Polling scripts:** `cd` is broken in this shell (zoxide); `status` is a read-only
  zsh var — use another name in loops.

## Why notes are authored locally, not generated in CI

Considered and rejected: a CI step that calls the Anthropic API to draft notes.
It's the wrong fit — the notes are a **git-tracked file that must exist in the
tagged commit**, authored *before* tagging, but a tag-triggered CI job runs too
late to put a file in that commit; it could only patch the draft body post-hoc,
discarding the version-controlled, PR-reviewable artifact. And the draft-publish
gate already provides human review, so a key + network nondeterminism + cost buy
nothing. Author notes locally (this skill), keep them in git.
