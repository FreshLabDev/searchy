# Release Process

This document explains how Searchy uses `CHANGELOG.md` and GitHub Releases.

## Changelog Rules

- Keep `CHANGELOG.md` as the source of truth for human-readable release history.
- Put unreleased user-visible, operational, security, schema, or behavior changes
  under `## Unreleased`.
- Do not record every small refactor. Record changes that matter to users,
  operators, contributors, or future release decisions.
- Use these sections when relevant:
  - `Added`
  - `Changed`
  - `Fixed`
  - `Security`
  - `Breaking`
  - `Known Limitations`
- Keep entries short and concrete.
- Mention required environment variable, SearXNG requirement, analytics schema,
  or deployment changes explicitly.

Development happens on `dev`; releases are published from `main`
(see [`versioning.md`](versioning.md)).

## Preparing A Release

1. On `dev`, finish code and documentation changes.
2. Run the verification commands from `AGENTS.md`.
3. Run a real smoke test (an inline search and a DM/group search) for `beta`,
   `rc`, and public releases.
4. On `dev`, move relevant `Unreleased` entries into a version section, and keep
   a fresh empty `## Unreleased` above it:

   ```text
   ## v0.1.0-alpha.1 - 2026-07-04
   ```

5. Merge `dev` into `main`: `git checkout main && git merge --no-ff dev`.
6. Write release notes from the version section.
7. Create an annotated git tag on `main`.
8. Create a GitHub Release.

## GitHub Release Notes

Use this shape for release notes:

```text
v0.1.0-alpha.1

Summary:
- Short release purpose.

Highlights:
- Important shipped behavior.

Operations:
- Required env or deployment notes.
- SearXNG or schema notes.

Verification:
- go test ./...
- go vet ./...
- docker build
- smoke test status

Known limitations:
- What is intentionally not done yet.
```

For `alpha`, `beta`, and `rc` versions, mark the GitHub Release as pre-release.
For the public MVP tag `v0.1.0`, publish a normal GitHub Release.

## Commands

Create a pre-release:

```sh
git tag -a v0.1.0-alpha.1 -m "v0.1.0-alpha.1"
git push origin main
git push origin v0.1.0-alpha.1
gh release create v0.1.0-alpha.1 \
  --prerelease \
  --title "v0.1.0-alpha.1" \
  --notes-file /tmp/searchy-release-notes.md
```

Create the public MVP release:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin main
git push origin v0.1.0
gh release create v0.1.0 \
  --title "v0.1.0" \
  --notes-file /tmp/searchy-release-notes.md
```

Pushing the tag also drives the release workflow (`.github/workflows/release.yml`),
which builds a version-stamped image, pushes it to GHCR, and publishes a GitHub
Release from the matching changelog section (pre-release tags are marked as
pre-releases). Do not publish a release before the release notes, tag, and
verification status all match.

The visible GitHub Release title must equal the tag exactly. Do not prefix it
with the project name or append descriptive text.
