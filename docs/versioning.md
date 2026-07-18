# Versioning

Searchy uses SemVer-style versions with pre-release tags while the bot is still
before `v1.0.0`.

## Branches

Searchy uses two long-lived branches:

- **`dev`** — development. Day-to-day work lands here, and the `## Unreleased`
  section of [`CHANGELOG.md`](../CHANGELOG.md) tracks what has merged but is not
  yet published.
- **`main`** — publication. Every released version — pre-release (`alpha` /
  `beta` / `rc`) and stable — is merged from `dev` into `main` and tagged there.
  `main` always reflects the latest published release.

Releasing renames `## Unreleased` to the new version, merges `dev` → `main`, and
tags the version on `main`. See [`releases.md`](releases.md).

## Version Line

The first release line moves through these tags:

```text
v0.1.0-alpha.1  initial open-source MVP code
v0.1.0-alpha.N  further alpha hardening
v0.1.0-beta.1   first real Telegram/SearXNG beta
v0.1.0-beta.2   personal Vido DM handoff from shared group cards
v0.1.0-beta.3   About-panel version display fix
v0.1.0-rc.1     joint Searchy × Vido candidate after the full delivery smoke
v0.1.0          public MVP release
```

After the public MVP release:

```text
v0.1.1          bug fixes without new behavior
v0.2.0          notable UX, operations, or MVP-compatible feature improvements
v1.0.0          stable production contract after real production usage
```

## Rules

- Use `alpha` while core flows are not proven with real Telegram credentials and
  a live SearXNG instance.
- Use `beta` after the bot works end to end, but only for limited users.
- Use `rc` when the release is intended to become public and only fixes are
  expected.
- Use patch versions for fixes that do not change product behavior or runtime
  assumptions.
- Use minor versions for visible UX improvements, operational improvements, or
  search/delivery changes that remain in MVP scope.
- Do not use `v1.0.0` until the bot has real production history, stable
  deployment practices, and a clear behavior contract.

## Breaking Changes Before v1.0.0

Before `v1.0.0`, Searchy can still change faster than a mature product, but
breaking changes must be explicit when they affect:

- required environment variables
- PostgreSQL analytics schema or migration requirements
- SearXNG query, engine, or JSON-API assumptions
- shared `core` behavior (functions, role, `search_path`) and saved-language
  resolution
- Docker Compose or deployment assumptions
