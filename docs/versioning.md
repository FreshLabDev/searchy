# Versioning

Every Asterfield repository versions and releases the same way. This document is
identical in all of them; only the last two sections — this repository's own
line, and the surface where a change here breaks something — are specific to
Searchy.

## The number

A released version is three numbers:

```text
1.2.3
│ │ └── patch   the ordinary update
│ └──── minor   something new, or a large rewrite
└────── major   a different product
```

**Patch — the last number.** The one published most. Something was broken and
now works, a message reads better, a limit was tuned, a small convenience
appeared. Nobody has to do anything differently after it lands, and nothing
anyone relied on has moved.

**Minor — the middle number.** Someone can now do something they could not do
before, or the rewrite behind an unchanged surface was large enough that calling
it a fix would be dishonest. A new command, a new setting, a new supported
source, a new panel. Existing installs keep working untouched.

**Major — the first number.** The thing is meaningfully not what it was, or an
existing install cannot carry over as it stands. Spend this number rarely: a
major is a claim, and using one for a big-but-compatible change spends a number
that cannot be got back.

What separates minor from major is not how much code moved. It is whether
somebody running the previous version has to *do* something — edit an `.env`,
run a step by hand, accept that a behaviour they depended on is gone. If yes it
is major, however small the diff. If no it is minor, however large.

## The pre-release suffix

```text
1.2.3-alpha.4
  │     │   └── the fourth cut of this stage
  │     └────── the stage: alpha, then beta, then rc
  └──────────── the version being aimed at
```

`1.2.3-alpha.4` is **not** version 1.2.3. It is the fourth attempt at getting
there. That last number counts cuts, and a cut is whatever had to be published
to keep moving: one slice of the work, or a fix for what the previous cut got
wrong. Both are the same kind of event — a build that went somewhere real and
was looked at — so both advance it. There is no need to distinguish them in the
number; the changelog says which it was.

The `1.2.3` in front is a statement of intent, not a commitment. If the work
grows past what `1.2.3` was meant to be, the next cut is `1.3.0-alpha.1`, not
`1.2.3-alpha.9`. Re-aiming is normal and costs nothing.

### alpha, beta, rc

**alpha** — in progress. Parts are unproven, or not written yet. This is where
most pre-releases live, and there is no shame in a long alpha line.

**beta** — the substance is done and it seems fine. Not *verified*, not *nothing
left to fix* — seems fine. That is the actual bar, and it is worth saying
plainly because pretending the bar is higher is what keeps things in alpha
forever. Beta means the shape has settled and what remains is whatever real use
turns up.

**rc** — this is the release unless something blocks it. After an rc, only fixes
land. Anything that *adds* sends the line back to beta, because a release
candidate that grew a feature was not a candidate.

Those three are the ordinary path, not a mandatory one. Two shortcuts are
legitimate, and both should be taken deliberately rather than by drift:

- **Jump straight to rc.** When something has to ship now, an alpha that has
  been exercised enough can be tagged `rc.1` without a beta in between. The
  claim being made is that only blockers remain. Make it knowingly.
- **Run a pre-release in production.** Point the production bot at an alpha
  without promoting it. Sometimes the only way to find out whether something
  works is to let it work on real traffic. What is not allowed is leaving it
  there quietly: it either earns a stable tag or it gets rolled back.

Neither shortcut renames anything. An alpha serving production is still an
alpha, and the version it reports still says `-alpha.N`. The number describes
how proven the code is, never where it happens to be running.

## Branches

Two long-lived branches, and a bot attached to each.

| Branch | Holds | Tagged with | Bot |
|:--|:--|:--|:--|
| `dev` | all work | every pre-release: `-alpha.N`, `-beta.N`, `-rc.N` | the test bot |
| `main` | what is published | stable versions only | the production bot |

Everything lands on `dev` first. Pre-releases are tagged on `dev`, because a
pre-release is by definition not a release — it is a cut of work in progress,
and work in progress lives on `dev`.

When a version is ready to be the real thing, `dev` merges into `main` and the
stable tag goes on that merge commit. So `main` answers exactly one question,
and answers it without ambiguity: what is in production right now. Nothing is
committed to `main` directly, ever.

### The test bot

A pre-release should run somewhere before it is promoted, and that somewhere is a
second bot: its own token from BotFather, its own stack on the host, its own
`.env`, its own row in the shared `core` database. Vido has a standing one —
`vido-test`, running the `dev` image alongside production.

Not every repository needs one permanently. When a change is big enough that
reading the diff is not enough confidence, ask for a test bot to be created and
set it up properly. Pointing the production bot at a branch is not the same
thing and defeats the purpose: the point is that a mistake stays inside the test
bot, where it costs nothing.

The test bot runs `dev`. The production bot runs `main`. Nothing else should
ever be true of either.

## Rules

- All work lands on `dev`. Never commit to `main` directly.
- Pre-releases (`-alpha.N`, `-beta.N`, `-rc.N`) are tagged on `dev`.
- Stable versions are tagged on `main`, on the merge commit from `dev`.
- Every pre-release is marked as a pre-release on GitHub. The release workflow
  does this from the shape of the tag.
- A published version is never reused, moved, or retagged. If it was wrong,
  publish the next number.
- Every tag has a matching `## <tag>` section in `CHANGELOG.md`. The release
  workflow refuses a tag without one, and builds the release notes from it.
- The GitHub Release title is exactly the tag: no project name, no description.
- A breaking change gets a `Breaking` section in the changelog whatever the
  numbers say, so it is visible to somebody skimming.


## Searchy today

Searchy is pre-`v1.0.0`.

```text
v0.1.0          public MVP release
v0.2.0-alpha.N  relevance, discovery pools, dead-result and cover-card hardening
v1.0.0          after real production history and a stable behavior contract
```

Pre-releases up to `v0.2.0-alpha.4` were tagged on `main`, under the earlier
rule. From now on they are tagged on `dev`.

Searchy has no test bot of its own, but the Vido bridge means a change here can
fail inside Vido. A release touching delivery should be exercised against
`vido-test` before it reaches the production pair.

## Breaking-sensitive surface

A change is breaking — and needs a `Breaking` changelog entry — when it affects:

- required environment variables
- PostgreSQL analytics schema or migration requirements
- SearXNG query, engine, or JSON-API assumptions
- shared `core` behavior (functions, role, `search_path`) and saved-language
  resolution
- the Vido bridge contract: intents, delivery plans, and ACK semantics
- Docker Compose or deployment assumptions
