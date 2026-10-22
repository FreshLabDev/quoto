# AGENTS.md

This file is for coding agents working on Quoto. Keep it minimal, private by
default, and production-minded.

## Project Shape

- Quoto is one Go service (`cmd/bot`, packages under `internal/`). The layout and
  the reasons for it are in [`docs/go-rewrite.md`](docs/go-rewrite.md), section 3.
- PostgreSQL is the only durable store. Domain tables live in the `quoto`
  schema on the shared `core-postgres`, reached as `quoto_core`; the bot sets
  `search_path=quoto` on its connections and writes `core.` explicitly.
  Telegram identity, chats and language are delegated to shared `core` through
  `core.touch` / `core.set_language` / `core.clear_language` /
  `core.effective_language` -- never by writing `core.*` tables directly.
- Quoto's tables reference `core.person` and `core.chat` by the Telegram natural
  keys. There is no per-bot users or groups table.
- Migrations are embedded SQL files in `migrations/`, applied by `internal/db`
  at start under an advisory lock and recorded in `schema_migrations`. A
  database that Alembic last touched (revision `20261018_01`) is adopted after a
  catalog check; `alembic_version` is left alone until a stable Go release has
  run. Column drops only in a late migration, never in the release that
  introduces Go.
- The Telegram client is `github.com/FreshLabDev/tg`. What only Quoto needs
  (silent send, pin, outcome classification) lives in `internal/telegram`.
- AI scoring goes to Claude through the Anthropic Messages API (`internal/claude`,
  plain `net/http`), then down the OpenRouter chain when Claude fails. Claude
  also describes photos, images and stickers; video and audio go only through
  OpenRouter, because Claude takes neither. Every eval request body and raw
  response, from either provider, is written to `logs/ai_audit.jsonl` and kept
  for seven days.
- Every Claude request carries `fallbacks: "default"` (beta
  `server-side-fallback-2026-07-01`): a safety decline is re-run in the same
  call on the model Anthropic recommends, and `actual_model` names the one that
  answered. A cyber decline goes from Opus 5.5 to Claude Opus 4.8 and from
  Sonnet 5.5 to Claude Sonnet 5, both covered by the agreement; check where the
  fallback goes before switching either model.
- A group's data reaches Claude or OpenRouter only once the group accepted
  agreement 1.2 or later: `agreement.MIN_ACCEPTED_VERSION`, checked against
  `group_settings.agreement_version`. Until then media is described from
  metadata, as when analysis is off, and a day is not published. Raise the
  version only for a change that needs fresh consent, because every group then
  publishes nothing until an admin accepts again.
- The agreement says Anthropic does not train on API data and deletes it
  within 30 days, longer only for what its safety systems flag. Changing the
  Claude setup in a way that breaks that (the Batch or Files API, a Fable or
  Mythos model) means changing the agreement first.
- Structured outputs on the Anthropic API reject numeric and length bounds and
  `maxItems`. `internal/claude` strips them from Claude's copy of each schema;
  the parsers in `internal/ai` clamp the values themselves.
- Every OpenRouter request carries `provider: {data_collection: "deny", zdr:
  true}`; both are constants in `internal/openrouter` because the user agreement
  promises exactly that. A new model must have a ZDR endpoint on OpenRouter
  (`/api/v1/endpoints/zdr`). `google/gemini-2.5-flash-lite` has none, which was
  the hourly 404 before the rewrite.
- `TELEGRAM_BOT_API_BASE_URL` points the bot at our own Bot API server. Empty
  means api.telegram.org, and then the Bot API 10.3 methods do not exist: the
  user agreement drops from rich Markdown to HTML. `internal/bot` probes
  `sendRichMessage` at startup and says which one it got.

## Product Boundaries

- `/start` is the only command. Everything else is inline buttons.
- The bot collects messages silently between two daily cutoffs and publishes at
  most one quote per group per day.
- Fewer than ten messages in a day is skipped silently, never announced.
- A day the AI judges unremarkable gets a boring-day notice, not a weak quote.
  Do not add a fallback that forces a quote out of a flat day.
- Interface languages are the sixteen in `internal/i18n`. A group with no saved
  language gets one chosen once by the daily AI run.

## Data And Security

- Never log `BOT_TOKEN`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, or a database
  URL with its password. `internal/log` has one redactor fed from
  `config.Secrets()`; a new secret goes there.
- Quote deep links are signed (`internal/deeplink`); only published quote details
  are exposed, and legacy links require Telegram membership verification before
  they resolve.
- The AI audit log holds real message text. It is operator-only (mode 0600) and
  expires.
- Media descriptions are cached by perceptual hash plus the prompt version
  constant in `internal/ai`. Changing a prompt without bumping that version
  silently reuses descriptions written by the old prompt.
- `callback_data` formats and the deep link format are a contract with panels
  and links that are already in chats. Do not change them without keeping the
  old form working.

## Versioning

- Work on `dev`. Pre-releases (`-alpha.N`, `-beta.N`, `-rc.N`) are tagged on
  `dev`; stable versions are tagged on `main`, on the merge commit from `dev`.
  The test bot runs `dev`, the production bot runs `main`.
- Follow `docs/versioning.md` and `docs/releases.md`.
- Tags shaped `v2026.07.01` are historical and predate the current scheme.

## Changelog And Releases

- Keep notable changes under `## Unreleased` in `CHANGELOG.md` until release
  preparation. Exactly one `## Unreleased`, always at the top.
- The GitHub Release body is the `## <tag>` changelog section, copied verbatim
  by the release workflow. There is no second place to write release notes.
- Call out env var, `core` contract, and migration changes explicitly.

## Verification

```sh
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
docker build -t quoto:ci .
docker compose -f deploy/ws04/compose.yaml config
```

The unit tests need no database or network. Tests that need PostgreSQL carry the
`integration` build tag and read `QUOTO_TEST_DATABASE_URL`; they drop and
recreate schema `quoto` in that database, so run them with `-p 1`:

```sh
QUOTO_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:5432/quoto_test \
  go test -tags=integration -race -p 1 ./...
```

CI also runs `govulncheck`, refuses a `replace` directive in `go.mod`, and
builds the image.

A test comment such as `tests/test_scheduler.py::test_name` names the Python
test, at tag `v0.11.1`, whose behaviour the Go test carries over. The Python
sources are gone from the tree; `git show v0.11.1:tests/test_scheduler.py` reads
them.

## Release Checklist

- The migrations apply cleanly on a copy of the shared database, and again on a
  database with the Alembic tables of the previous release (adoption).
- The `/start` panel opens in DM and in a group, and both reach the agreement
  and About tabs.
- The user agreement renders as rich Markdown against our Bot API server, and
  as HTML with `TELEGRAM_BOT_API_BASE_URL` unset.
- One full daily quote run against a live group, including the boring-day path.
- Media description works for an image, a video and an audio file, and the
  fallback chain is exercised at least once.
- Statistics and language switching still render in all sixteen locales.
- The audit log shows `"provider": {"data_collection": "deny", "zdr": true}` in
  every OpenRouter evaluation request body, and a Claude record with
  `"stop_reason": "end_turn"` for the day.
- A group that accepted an older agreement version gets the "agreement
  changed" notice, publishes nothing, and works again right after an admin
  accepts the current version.
- Idle memory stays under 60 MB and a 50 MB video peaks under 200 MB, ffmpeg
  included.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Every `.go`, `.sql`,
YAML file, `go.mod` and the `Dockerfile` starts with
`SPDX-License-Identifier: Apache-2.0`. The name the About card shows comes from
`internal/buildinfo`. Releases before the Go rewrite (up to `v0.11.1`) were
published under GPL-3.0 and stay so.

## Deploying

Do not invent a deploy. [`docs/releases.md`](docs/releases.md) has a **Deploying**
section describing this stack exactly: which host directory it lives in, which
env file names the image, which networks it needs, and how to roll back. Read it
before touching anything on the host.

Two rules that hold everywhere and are easy to get wrong:

- **Nothing is built on the host.** A production stack pulls the image the
  release workflow published. A `build:` section in a production manifest is a
  bug.
- **Pin the digest, not the tag.** A tag moves; a digest names one build that was
  tested, and a rollback becomes one line with nothing to rebuild.
