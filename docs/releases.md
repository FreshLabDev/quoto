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

The tag push runs `.github/workflows/release.yml`. It runs CI (`ci.yml`) on
the tagged commit first and publishes nothing unless it passed; then it
refuses the tag if it is not on `dev` or has no changelog section, builds and
publishes the image, adds its digest to the notes, and creates the GitHub
Release marked as a pre-release.

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

## Publishing without CI

A pushed tag publishes only after CI passed on the tagged commit. When CI could
not run (an Actions outage, a spending limit) or failed for a reason that is not
the code, and the release cannot wait, publish the existing tag by hand:

```sh
gh workflow run release.yml -f tag=v1.2.3 -f reason="why CI is skipped"
```

That run skips CI and puts the reason at the top of the GitHub Release, so the
release itself says it was not checked.

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
digest of a release is in its GitHub Release notes. To read it off the host that
will run it, pull the tag once and ask the daemon:

```sh
docker pull ghcr.io/freshlabdev/quoto:<tag>
docker inspect --format '{{index .RepoDigests 0}}' ghcr.io/freshlabdev/quoto:<tag>
```

That pull used to be mandatory, and it is worth saying why it no longer is,
because the reason was never the digest. Deploying straight to
`ghcr.io/freshlabdev/quoto@sha256:<digest>` answered `403` on a blob, and
pulling the tag first always cleared it.

The cause was the account doing the deploying. `ws04 deploy` runs as root, and
root's stored GHCR credential could not read the private package; the
pull-by-tag only worked because it was run as the login user, whose credential
can — which left the image in the shared local store for `up` to find. The
registry says which is which: asked for a manifest digest as if it were a blob,
an account that can read the package answers `404`, one that cannot answers
`403`. A `403` here is an access problem wearing a not-found costume.

So the fix is on the host, not in the recipe: make sure the account the deploy
runs as can read the package (`sudo docker login ghcr.io`), and pinning a digest
works on its own. Pulling the tag first is still a fine way to warm the layers.

Without a shell on the host, the API answers the same question:

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

Rolling back **across the Go rewrite** takes the old compose file as well. The Go
manifest sets `DATABASE_URL` with a `postgres://` scheme and a `/healthz` check;
the Python image understands neither, so setting only `QUOTO_IMAGE` back would
fail the health check and `ws04 deploy` would roll back again. Keep the last
Python `deploy/ws04/compose.yaml` (tag `v0.11.1`) next to the digest of its image.
The database needs nothing: the Go release only adds `schema_migrations` and two
indexes, and leaves `alembic_version` and every column the Python version uses in
place.

### First deploy of the Go release

- `QUOTO_CORE_PASSWORD` is still the only secret the manifest interpolates. The
  database URL is built from it as before, now with the `postgres://` scheme.
- The host `.env` keeps working. Variables the Go version no longer reads
  (`QUOTE_HOUR`, `TIMEZONE`, `WEIGHT_*` and the like) are listed by name once at
  start in a warning; remove them when convenient. A value the new version
  cannot use stops the start with a message naming the variable.
- The first start checks the tables and columns against what the Python version
  left, then records `001_baseline` as applied without running it and creates two
  indexes (`002_indexes`). If the schema differs it refuses to start and says
  which table or column.
- The first night after the deploy the media cache misses once (the description
  prompt version moved from `v2` to `v3`), so images and videos are described
  again at OpenRouter's and Anthropic's price.
- The first retention run deletes every message older than 72 hours, including
  those of days that were never processed. Going back to Python does not bring
  them back, so take `pg_dump -n quoto` before the first start.
- The first start needs `CREATE` on schema `quoto` (for `schema_migrations`) and
  ownership of `messages` and `message_ai_scores` (for the two indexes). Check
  with the catalog queries in `docs/go-rewrite.md` section 5.5. If the role lacks
  one, the start stops with `permission denied` and names the `GRANT`; nothing is
  changed. Later starts need neither.

### What this stack needs to exist

| | |
|:--|:--|
| Stack | `quoto` — `/opt/stacks/quoto` |
| Manifest | [`deploy/ws04/compose.yaml`](deploy/ws04/compose.yaml) in this repository |
| Env file | `.env` on the host, never in git |
| Networks | `core_net` (core-postgres), `telegram_bot_api_net` (the self-hosted Bot API server) |

**Leave `TELEGRAM_BOT_API_BASE_URL` empty for now.** Quoto would benefit from the
self-hosted server for downloads — Telegram's own endpoint caps `getFile` at
20 MB and quoto analyses video far longer than that — but three things have to
be true before it can be switched on, and none of them is done:

- The server runs with `--local`, so `getFile` answers with a path on the
  server's own disk and the `/file/bot<token>/…` route returns 404. Quoto sets
  `is_local=True` for that, which means the file is *read*, not fetched — so the
  server's media directory has to be mounted into this container. Mount only
  quoto's own token subdirectory, the way voicy does: the parent holds one per
  bot, each named after that bot's full token.
- The server writes as uid 101; quoto's image runs as 1000, which also owns the
  host `logs/` directory. Reading the server's files means choosing a uid for
  the container or a group both can share, and `logs/` has to stay writable.
- A token is logged in on exactly one server at a time. Moving quoto means
  `logOut` on api.telegram.org first, and that is a one-way step.

Switched on without the mount, every photo, video, circle and voice note fails
analysis and the day's quote quietly gets worse — visible a day later, if at all.

The manifest reads one `.env` in the stack directory, which holds both the
Compose variables (`QUOTO_IMAGE`, `QUOTO_CORE_PASSWORD`) and the application's
settings, the way every other stack in the family does.

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
`/healthz` (`ws04 container inspect` shows the health status; the endpoint is
on the container's port 8080). Those and `docker inspect` should agree; if they
do not, something was deployed by hand.

## Verification

```sh
gofmt -l .
go vet ./...
go test -race ./...
docker build -t quoto:ci .
docker compose -f deploy/ws04/compose.yaml config
```

CI additionally runs the database tests against a real PostgreSQL 17 (the
migrations up, one step down and up again, adoption of an Alembic database),
scans the dependencies with `govulncheck`, and refuses a `replace` directive in
`go.mod`.

For `beta`, `rc`, and stable: the `/start` panel and one full daily quote run
against a live group, including the boring-day path.
