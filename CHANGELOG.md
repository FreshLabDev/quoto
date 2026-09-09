# Changelog

All notable Quoto changes are documented here.

Quoto uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

### Added
- Continuous integration. Quoto was the only bot in the family with no
  `.github/` at all: the suite in `tests/` ran only when somebody remembered,
  and nothing checked that a migration could be applied or undone before it
  reached the host. `ci.yml` runs the tests, compiles every source file, checks
  that each package in `requirements.in` is pinned in `requirements.txt`,
  applies the migrations against a real PostgreSQL 17 and steps one back down
  again, refuses a branched alembic history, and builds the image.
- `release.yml` publishes the image to GHCR and creates the GitHub Release from
  the matching `CHANGELOG.md` section on a `v*` tag, refusing a tag that is not
  an ancestor of `main` or that has no changelog section. That is steps 6-8 of
  `docs/releases.md`, which were done by hand.
- `deploy/ws04/compose.yaml`, the production stack, pulling the released image
  from GHCR. The stack on the host built its own image from a working copy, so
  what served users was not a published artifact and nothing on the host could
  say which commit it came from. `QUOTO_IMAGE` has no default: an unset one
  stops the stack rather than quietly starting something else.

### Fixed
- The Dockerfile no longer defaults `QUOTO_VERSION` to `0.10.2`. It stayed at
  that value through the whole of v0.10.3, so an image built without the
  argument labelled itself as a release it was not.
- A second `## Unreleased` section, left behind by the v0.10.2 release, sat
  between v0.10.2 and v0.10.1. Release preparation renames `## Unreleased` to
  the new version, and with two of them the wrong one could be renamed.

### Changed
- Media description prompts now cast the model as the eyes and ears of someone
  who can't see or hear the file: on-screen text and speech must be quoted
  verbatim (screenshots of chats keep every line with its sender), unclear
  audio is marked `[неразборчиво]` instead of guessed, and the length caps grow
  to 1200 characters when there is a lot of text or speech (the payload still
  clips descriptions at 1500). `MEDIA_CACHE_PROMPT_VERSION` is deliberately
  left at `v2`, so already-described files keep their old descriptions.

### Removed
- Legacy `OPENROUTER_MODEL` setting. The quote evaluation model is now read only
  from `OPENROUTER_EVAL_MODEL`, whose default is `poolside/laguna-s-2.1:free`;
  a blank value falls back to that default. Previously the two settings aliased
  each other at startup and whatever was written into `OPENROUTER_MODEL` was
  silently overwritten.

### Added
- `scripts/bench_web.py` + `scripts/bench_web_ui.html`: a local web hub for
  picking an eval model. Lists the days recorded in `logs/ai_audit.jsonl`, shows
  the day's messages and what production picked, runs any set of models against
  that day with a chosen reasoning effort, streams each model's reasoning and
  answer live, then compares the picked quotes with tokens, cost and timing.
  Runs are archived under `logs/bench_runs/`. Read-only, and it sends nothing
  until Run is pressed. Sampling temperature is never set, matching production.
  Each selected model can carry its own reasoning effort (or follow the global
  one), and the catalog sorts by release date, alphabetically or by price, with
  filters for structured-output support and free models.
  A second, fully separate tab benchmarks media description: upload an image,
  video or audio file, watch it go through the bot's own normalization pipeline
  (resize, re-encode), and compare how each model describes it and at what cost.
  Each tab keeps its own model selection, efforts, settings and results, since
  the two jobs need different models. The media tab narrows the catalog to the
  input modality the uploaded file needs; image-generating models (anything
  that answers with pictures) and `:batch` tier ids (refused by
  `/chat/completions`) are dropped from the catalog entirely. The effort picker
  offers only the efforts a model actually accepts, read from the catalog's
  `reasoning` block, and flags models where reasoning is mandatory or where the
  effort is ignored. When a model asks for context, the card shows the whole
  context block in the exact order production would publish it, reusing
  `scoring._valid_context_messages` so the two can't drift apart. The summary
  table sorts by price ascending out of the box, and every column header is a
  sort toggle; rows without a value (failures) always sink to the bottom.
- `scripts/compare_eval_models.py`: replays days saved in `logs/ai_audit.jsonl`
  against several eval models in parallel and prints, per model, the quote it
  picked, the day verdict, token usage and the dollar cost. Read-only — nothing
  is written to the database.
- Token usage and cost of the winning eval call are now requested from
  OpenRouter (`usage.include`) and stored on `ai_evaluation_runs`
  (`prompt_tokens`, `completion_tokens`, `reasoning_tokens`, `total_tokens`,
  `cost_usd`); migration `20260907_01`.
- Startup warning for renamed env keys (`OPENROUTER_MODEL`,
  `OPENROUTER_REASONING_EFFORT`). `extra="ignore"` used to swallow them, so a
  stale `.env` kept settings that had stopped applying.

## v0.10.3 - 2026-08-22

### Added
- Per-kind media models: `OPENROUTER_MEDIA_IMAGE_MODEL`, `OPENROUTER_MEDIA_VIDEO_MODEL`,
  and `OPENROUTER_MEDIA_AUDIO_MODEL` select dedicated primary models for image,
  video, and audio analysis. Chain per kind: specific model (if set) ->
  `OPENROUTER_MEDIA_MODEL` -> `OPENROUTER_MEDIA_FALLBACK_MODEL`. An empty value
  keeps previous behavior (common model first).
- Dev tool `scripts/try_media_model.py`: test any OpenRouter model against a
  local media file through the production normalization pipeline.

## v0.10.2 - 2026-08-09

### Fixed
- Sign quote deep links and expose only published quote details. Legacy links are
  accepted only after Telegram membership verification.
- Respect per-group media-analysis settings during both immediate processing and
  pending recovery, with a database lease preventing duplicate AI work.
- Retry transient message-save failures and alert after the final attempt instead
  of silently dropping updates.
- Make Telegram delivery failures with an unknown outcome terminal, preventing a
  timeout from causing a duplicate publication or fallback message.
- Complete Telegram group migration by touching the new chat identity before
  re-keying Quoto history and settings.
- Retry agreement reminders after failed delivery, serialize reaction updates,
  redact database URL passwords in alerts, and rotate regular log files.

### Operations
- Runtime logs now include the `0.10.2` application version.
- AI audit retention is intentionally unchanged and remains available for debug
  and quality verification.

## v0.10.1 - 2026-07-21

### Changed
- Default quote evaluation models: `OPENROUTER_EVAL_MODEL` now uses
  `poolside/laguna-s-2.1:free`, with `OPENROUTER_EVAL_FALLBACK_MODEL` set to
  `poolside/laguna-s-2.1`.

## v0.10.0 - 2026-07-12

### Changed
- Clarified the agreement's storage language for operational records and AI
  processing results. Existing group acceptances must be reset after deployment
  so admins review the revised terms.

### Fixed
- Commit the outer SQLAlchemy transaction used by Alembic so PostgreSQL keeps
  successful schema upgrades instead of rolling them back on connection close.
- Suspend groups when the bot is removed (or Telegram reports `chat not found`),
  and stop reprocessing agreement-gated groups on every minute of the catch-up
  window.
- Keep transient media-analysis failures pending with bounded exponential retry,
  and apply an account-wide cooldown when every configured media model returns
  HTTP 402 instead of permanently losing the description immediately.

### Operations
- Adds Alembic revisions `20260712_01` and `20260712_02` for active-group
  lifecycle state and bounded media retry state.

## v0.9.1 - 2026-07-05

### Added
- Automatic fallback for the quote-of-the-day eval model: if `OPENROUTER_EVAL_MODEL`
  errors out after its retries (or returns an empty/unparsable response),
  evaluation now retries against `OPENROUTER_EVAL_FALLBACK_MODEL` before giving up
  and returning neutral scores. Mirrors the existing media-model fallback.

### Changed
- Default eval models: `OPENROUTER_EVAL_MODEL` is now `poolside/laguna-xs-2.1:free`
  with `OPENROUTER_EVAL_FALLBACK_MODEL` defaulting to `poolside/laguna-xs-2.1`.

## v0.9.0 - 2026-07-03

First release under Semantic Versioning and this changelog. Earlier builds used
CalVer date tags (`vYYYY.MM.DD`) with no changelog.

### Changed
- Quoto now runs on the shared **`core` PostgreSQL** database. Identity, chat and
  language state live in schema `core`; quoto's own tables live in schema `quoto`
  and reference `core.person` / `core.chat` by the Telegram natural keys (user id,
  chat id). Per-user and per-chat language is resolved through `core` and shared
  across all bots on the host.

### Removed
- Surrogate `users` / `groups` id primary keys. Everything keys on the Telegram
  ids now, matching vido and branchy. Group settings moved to a `GroupSettings`
  table keyed by `chat_id`.
