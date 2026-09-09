# Release Process

Every Asterfield repository releases the same way. This document is identical in
all of them; only the verification section is specific to Searchy.

See [`versioning.md`](versioning.md) for what the numbers mean and why
pre-releases are tagged on `dev` and stable versions on `main`.

## The changelog is the release notes

`CHANGELOG.md` is the source of truth for history, and the release workflow reads
it directly — the GitHub Release body is the `## <tag>` section, copied verbatim.
There is no second place to write release notes, and no step where the two can
disagree.

Which means the changelog has to be written for somebody else to read:

- Put unreleased changes under `## Unreleased`, in the section that fits:
  `Added`, `Changed`, `Fixed`, `Removed`, `Security`, `Breaking`,
  `Known Limitations`.
- Record what matters to a user, an operator, or the next person deciding
  whether to upgrade. Not every refactor.
- Say what changed and why it mattered, concretely. "Fixed a bug" tells nobody
  anything.
- Call out anything an operator must act on — a new or renamed environment
  variable, a migration, a changed deployment assumption — explicitly, in its
  own entry.
- Exactly one `## Unreleased` section, always at the top. Two of them means the
  next release renames the wrong one.

## Publishing a pre-release

A pre-release is tagged on `dev`. Nothing merges anywhere.

1. Finish the work on `dev` and run the verification below.
2. Rename `## Unreleased` to the version, and open a fresh empty `## Unreleased`
   above it:

   ```text
   ## Unreleased

   ## v1.2.3-alpha.4 - 2026-09-09
   ```

3. Commit that on `dev` and push it.
4. Tag the pushed commit and push the tag:

   ```sh
   git tag -a v1.2.3-alpha.4 -m "v1.2.3-alpha.4"
   git push origin dev
   git push origin v1.2.3-alpha.4
   ```

The tag push runs `.github/workflows/release.yml`, which re-runs the checks,
refuses the tag if it is not on `dev` or has no changelog section, builds and
publishes the image, and creates the GitHub Release marked as a pre-release.

Then point the test bot at it. A pre-release nobody ran is a pre-release that
proved nothing.

## Publishing a stable release

A stable version is tagged on `main`, on the merge commit.

1. The version being promoted should already have been through at least one
   pre-release that actually ran somewhere. If it has not, say why in the
   changelog.
2. On `dev`, rename `## Unreleased` to the stable version and push.
3. Merge into `main` with a merge commit, so the tag has something to sit on:

   ```sh
   git checkout main
   git merge --no-ff dev
   git push origin main
   ```

4. Tag the merge commit and push the tag:

   ```sh
   git tag -a v1.2.3 -m "v1.2.3"
   git push origin v1.2.3
   ```

5. Deploy it, and check the running version says what it should.

## Rolling back

Do not retag and do not delete a published release. Roll back by deploying the
previous version — the images are pinned by digest, so the previous digest is
the whole rollback — and then publish a new patch that fixes what went wrong.

A version that was published is a fact about what existed. Rewriting it makes
every other record of it wrong.

## Verification

The local machine may not have `go` in `PATH`; Docker is the reliable route:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go test ./...
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go vet ./...
```

CI additionally runs `go mod verify`, `go test -race ./...`, `govulncheck`, the
Docker build, and Compose validation.

For `beta`, `rc`, and stable, a real smoke test:

- inline image and video cards plus pagination;
- DM and group numbered grids;
- selector Download delivery from Searchy in the same chat/topic;
- another group member's Download handoff to a private Vido job;
- inline Download handoff to Vido DM;
- audio follow-up, terminal downloader error, and cached `file_id` reuse;
- a newly delivered production bridge row with `target_bot=searchy`.
