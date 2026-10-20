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
- AI scoring goes to Claude through the Anthropic API (the `anthropic` SDK),
  then down the OpenRouter chain when Claude fails. Claude also describes
  photos, images and stickers; video and audio go only through OpenRouter,
  because Claude takes neither. Every eval request body and raw response, from
  either provider, is written to `logs/ai_audit.jsonl` and kept for seven days.
- Every Claude request goes through `ai._claude_message()` with
  `fallbacks: "default"` (beta `server-side-fallback-2026-07-01`): a safety
  decline is re-run in the same call on the model Anthropic recommends, and
  `actual_model` names the one that answered. A cyber decline goes from
  Opus 5.5 to Claude Opus 4.8 and from Sonnet 5.5 to Claude Sonnet 5, both
  covered by the agreement; check where the fallback goes before switching
  either model.
- A group's data reaches Claude only once the group accepted agreement 1.2 or
  later: `agreement.MIN_ACCEPTED_VERSION`, checked against
  `group_settings.agreement_version`. Raise it only for a change that needs
  fresh consent, because every group then publishes nothing until an admin
  accepts again.
- The agreement says Anthropic does not train on API data and deletes it
  within 30 days, longer only for what its safety systems flag. Changing the
  Claude setup in a way that breaks that (the Batch or Files API, a Fable or
  Mythos model) means changing the agreement first.
- Structured outputs on the Anthropic API reject numeric and length bounds and
  `maxItems`. `ai._claude_schema()` strips them from Claude's copy of each
  schema; the parsers clamp the values themselves.
- Every OpenRouter request carries `provider` routing built by
  `ai._openrouter_provider()`: `data_collection: deny` and `zdr: true` by default
  (`OPENROUTER_DATA_COLLECTION`, `OPENROUTER_ZDR`), so chat text only reaches
  endpoints that neither keep it nor train on it. The user agreement promises
  exactly that. A new model must have a ZDR endpoint on OpenRouter
  (`/api/v1/endpoints/zdr`), and a new request, the bench scripts included,
  must carry the same field.
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
- Interface languages are the sixteen in `app/i18n.py` `SUPPORTED_LANGUAGES`.
  A group with no saved language gets one chosen once by the daily AI run.

## Data And Security

- Never log `BOT_TOKEN`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, or a database URL with its
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
- Statistics and language switching still render in all sixteen locales.
- The audit log shows `"provider": {"data_collection": "deny", "zdr": true}` in
  every OpenRouter evaluation request body, and a Claude record with
  `"stop_reason": "end_turn"` for the day.
- A group that accepted an older agreement version gets the "agreement
  changed" notice, publishes nothing, and works again right after an admin
  accepts the current version.

## License

GPL-3.0, unlike the rest of the family, which is Apache-2.0. See
[LICENSE](LICENSE). `app/version.py` holds the name the About card and the
agreement show.

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
