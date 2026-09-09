# AGENTS.md

This file is for coding agents working on Quoto. Keep it minimal, private by
default, and production-minded.

## Project Shape

- Quoto is one Python service (aiogram, SQLAlchemy, APScheduler).
- PostgreSQL is the only durable store. Domain tables live in the `quoto`
  schema on the shared `core-postgres`, reached as `quoto_core` with the role's
  default `search_path=quoto,core`. Telegram identity, chats and language are
  delegated to shared `core` through `core.touch` / `core.set_language` /
  `core.effective_language` -- never by writing `core.*` tables directly.
- Quoto's tables reference `core.person` and `core.chat` by the Telegram natural
  keys. There is no per-bot users or groups table.
- Migrations are Alembic, under `alembic/versions/`. `alembic upgrade head` must
  apply cleanly on the shared database before the bot boots.
- AI scoring goes through OpenRouter. Every request body and raw response is
  written to `logs/ai_audit.jsonl` and kept for seven days.
- `TELEGRAM_BOT_API_BASE_URL` points the bot at our own Bot API server. Empty
  means api.telegram.org, and then the Bot API 10.3 methods do not exist: the
  user agreement drops from rich Markdown to HTML. `app/richmd.py` probes
  `sendRichMessage` at startup and says which one it got.

## Product Boundaries

- `/start` is the only command. Everything else is inline buttons.
- The bot collects messages silently between two daily cutoffs and publishes at
  most one quote per group per day.
- Fewer than ten messages in a day is skipped silently, never announced.
- A day the AI judges unremarkable gets a boring-day notice, not a weak quote.
  Do not add a fallback that forces a quote out of a flat day.
- Interface languages are `ru`, `uk`, `en`, `de`. A group with no saved language
  gets one chosen once by the daily AI run.

## Data And Security

- Never log `BOT_TOKEN`, `OPENROUTER_API_KEY`, or a database URL with its
  password. Alerts redact the password before sending.
- Quote deep links are signed; only published quote details are exposed, and
  legacy links require Telegram membership verification before they resolve.
- The AI audit log holds real message text. It is operator-only and expires.
- Media descriptions are cached by perceptual hash plus
  `MEDIA_CACHE_PROMPT_VERSION`. Changing the prompt without bumping that version
  silently reuses descriptions written by the old prompt.

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
python -m pytest -q tests
docker build -t quoto:ci .
docker compose config
```

The suite mocks the database; `tests/__init__.py` supplies the settings the app
validates at import time. CI additionally applies the migrations against a real
PostgreSQL 17, steps one back down and up again, refuses a branched alembic
history, and checks that every package in `requirements.in` is pinned into
`requirements.txt`.

## Release Checklist

- `alembic upgrade head` applies cleanly against a copy of the shared database.
- The `/start` panel opens in DM and in a group, and both reach the agreement
  and About tabs.
- The user agreement renders as rich Markdown against our Bot API server, and
  as HTML with `TELEGRAM_BOT_API_BASE_URL` unset.
- One full daily quote run against a live group, including the boring-day path.
- Media description works for an image, a video and an audio file, and the
  fallback chain is exercised at least once.
- Statistics and language switching still render in all four locales.

## License

Apache License 2.0. See [LICENSE](LICENSE).
