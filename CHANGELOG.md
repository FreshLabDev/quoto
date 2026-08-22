# Changelog

All notable Quoto changes are documented here.

Quoto uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

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

## Unreleased

Use this section for changes that are merged but not released yet.

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
