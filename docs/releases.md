# Release Process

Every Asterfield repository releases the same way. This document is identical in
all of them; only the verification section is specific to Quoto.

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

## Deploying

The host is WS04. Every stack lives in `/opt/stacks/<stack>` and is driven by the
`ws04` CLI, which exists on the operator's machine and reaches the host over the
LAN. Nothing here is built on the host any more: a stack pulls the image the
release workflow published and runs that. If you find a `build:` section in a
production manifest, that is a bug, not a shortcut.

### One deploy

```sh
ws04 deploy quoto --dry-run --yes    # prints what it would do, changes nothing
ws04 deploy quoto --yes
```

`deploy` snapshots the stack's compose, env and image ids into
`/opt/stacks/.ws04/deploy-snapshots/quoto/<timestamp>`, pulls, brings the stack
up, waits up to ninety seconds for the container to report healthy, and **rolls
back on its own** if it does not. The snapshot is kept either way.

### Pointing the stack at a version

The image is chosen by one variable in the stack's env file on the host, not by
anything in this repository:

```sh
QUOTO_IMAGE=ghcr.io/freshlabdev/quoto@sha256:<digest>
```

Pin the **digest**, not the tag. A tag can be moved; a digest names one build
that was tested, so a rollback is one line with nothing to rebuild, and
`docker inspect` on the running container answers which commit it came from. The
digest of a release is in its GitHub Release notes, or:

```sh
gh api /orgs/FreshLabDev/packages/container/quoto/versions \
  --jq '.[] | select(.metadata.container.tags[]? == "<tag>") | .name'
```

The variable has no default. An unset one stops the stack with a message naming
it, rather than quietly starting something else.

### Rolling back

Set `QUOTO_IMAGE` to the previous digest and deploy again. That is the whole
rollback — the images are still on the host, and nothing is rebuilt. Then publish
a patch that fixes what went wrong; never retag or delete the bad release.

### What this stack needs to exist

| | |
|:--|:--|
| Stack | `quoto` — `/opt/stacks/quoto` |
| Manifest | [`deploy/ws04/compose.yaml`](deploy/ws04/compose.yaml) in this repository |
| Env file | `.app.env` on the host, never in git |
| Networks | `core_net` (core-postgres), `telegram_bot_api_net` (the self-hosted Bot API server) |

Quoto reads the self-hosted Bot API server for **downloads**, not for rich text:
Telegram's own endpoint always carries the newest Bot API, but caps `getFile` at
20 MB, and quoto analyses video up to `MEDIA_VIDEO_MAX_SECONDS`. Without
`TELEGRAM_BOT_API_BASE_URL` anything larger is unreadable and the day's media is
skipped with nobody told.

The stack directory on the host still splits its configuration in two: `.env`
for the Compose variables and `.app.env` for the application. This manifest
expects one `.env` holding both, the way every other stack in the family does.
Merge them at the first deploy from GHCR — the keys do not collide — and retire
`.app.env` in the same step, since a manifest naming a file that is gone stops
the stack.

The retired local `db` service still exists in the stack directory as a rollback
anchor for the 0.9.0 core consolidation. It is not part of the deployment and
must not be started by it.

### Checking what is running

```sh
ws04 container list                    # health of everything
ws04 logs quoto-bot --since 1h
ws04 container inspect quoto-bot     # includes the image digest
```

The bot also reports its own version — from the About card in Telegram, and from
its health endpoint where it has one. Those two and `docker inspect` should
agree; if they do not, something was deployed by hand.

## Verification

```sh
python -m pytest -q tests
docker build -t quoto:ci .
docker compose config
```

CI additionally applies the migrations against a real PostgreSQL 17, steps one
back down and up again, and refuses a branched alembic history.

For `beta`, `rc`, and stable: the `/start` panel and one full daily quote run
against a live group, including the boring-day path.
