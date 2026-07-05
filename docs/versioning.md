# Versioning

Quoto uses SemVer-style versions with pre-release tags while the bot is still
before `v1.0.0`.

## Branches

Quoto uses two long-lived branches:

- **`dev`** — development. Day-to-day work lands here, and the `## Unreleased`
  section of [`CHANGELOG.md`](../CHANGELOG.md) tracks what has merged but is not
  yet published.
- **`main`** — publication. Every released version — pre-release (`alpha` /
  `beta` / `rc`) and stable — is merged from `dev` into `main` and tagged there.
  `main` always reflects the latest published release.

Releasing renames `## Unreleased` to the new version, merges `dev` → `main`, and
tags the version on `main`. See [`releases.md`](releases.md).

## Version Line

Quoto is still before `v1.0.0`. The line moves through these versions:

```text
v0.9.0     current pre-1.0 release: SemVer and this changelog adopted, running on
           the shared core Postgres
v0.9.x     bug fixes without new behavior
v0.10.0    notable UX, operations, or compatible feature improvements
v1.0.0     stable production contract after real production usage
```

Any cut that needs a soak before it is published can carry a pre-release suffix:

```text
vX.Y.Z-alpha.N  internal hardening of the cut
vX.Y.Z-beta.1   first limited-group build
vX.Y.Z-rc.1     release candidate, only fixes expected
```

## Rules

- Use patch versions (`v0.9.x`) for fixes that do not change product behavior,
  scoring, or runtime assumptions.
- Use minor versions (`v0.10.0`, `v0.11.0`, …) for visible UX improvements,
  operational improvements, or scoring / quote-pipeline changes that stay
  compatible.
- Add a pre-release suffix only when a cut needs a soak before it is published:
  `alpha` while it is unproven against real Telegram credentials, OpenRouter, and
  a live core Postgres; `beta` once it works end to end for limited groups; `rc`
  when the cut is intended to become the release and only fixes are expected.
- Do not use `v1.0.0` until the bot has real production history, stable
  deployment practices, and a clear behavior contract.

## Breaking Changes Before v1.0.0

Before `v1.0.0`, Quoto can still change faster than a mature product, but
breaking changes must be explicit when they affect:

- required environment variables (`BOT_TOKEN`, `DB_URL`, `OPENROUTER_API_KEY`,
  the `OPENROUTER_*` model settings, and the scheduler / limit settings)
- the shared `core-postgres` contract: the `quoto` schema, the `quoto_core` role,
  `search_path=quoto,core`, and the `core.person` / `core.chat` tables and
  `core.touch` / `core.set_language` functions quoto depends on
- Alembic migration requirements — `alembic upgrade head` must apply cleanly on
  the shared database before the bot boots
- Docker Compose or deployment assumptions (the bundled local Postgres seeded
  from `deploy/core-init.sql`, the container healthcheck, and container env)
