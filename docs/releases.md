# Release Process

This document explains how Quoto uses `CHANGELOG.md` and GitHub Releases.

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
  - `Removed`
  - `Security`
  - `Breaking`
  - `Known Limitations`
- Keep entries short and concrete.
- Mention required environment variable, shared `core` schema/role, Alembic
  migration, or deployment changes explicitly.

Development happens on `dev`; releases are published from `main`
(see [`versioning.md`](versioning.md)).

## Preparing A Release

1. On `dev`, finish code and documentation changes.
2. Run the verification commands: `pytest`, `docker build -t quoto .`, and
   `docker compose config`.
3. Run a real smoke test (the `/start` panel and a daily quote run against a live
   group) for `beta`, `rc`, and stable releases.
4. On `dev`, move relevant `Unreleased` entries into a version section, and keep
   a fresh empty `## Unreleased` above it:

   ```text
   ## v0.10.2 - 2026-08-09
   ```

5. Merge `dev` into `main`: `git checkout main && git merge --no-ff dev`.
6. Write release notes from the version section.
7. Create an annotated git tag on `main`.
8. Create a GitHub Release.

## GitHub Release Notes

Use this shape for release notes:

```text
Quoto v0.10.0

Summary:
- Short release purpose.

Highlights:
- Important shipped behavior.

Operations:
- Required env or deployment notes.
- Shared core schema/role or Alembic migration notes.

Verification:
- pytest
- docker build
- docker compose config
- smoke test status

Known limitations:
- What is intentionally not done yet.
```

For `alpha`, `beta`, and `rc` versions, mark the GitHub Release as pre-release.
For a stable tag (`v0.10.0`, and eventually `v1.0.0`), publish a normal GitHub
Release.

## Commands

Apply migrations to the target database first, so the release runs against the
current schema:

```sh
alembic upgrade head
```

Create a pre-release:

```sh
git tag -a v0.10.0-rc.1 -m "v0.10.0-rc.1"
git push origin main
git push origin v0.10.0-rc.1
gh release create v0.10.0-rc.1 \
  --prerelease \
  --title "Quoto v0.10.0-rc.1" \
  --notes-file /tmp/quoto-release-notes.md
```

Create a stable release:

```sh
git tag -a v0.10.2 -m "v0.10.2"
git push origin main
git push origin v0.10.2
gh release create v0.10.2 \
  --title "Quoto v0.10.2" \
  --notes-file /tmp/quoto-release-notes.md
```

Do not publish a release before the release notes, tag, and verification status
all match.
