# Changelog

All notable Quoto changes are documented here.

The `## <tag>` section of this file *is* the GitHub Release body: the release
workflow copies it verbatim and refuses a tag that has no section. Write it
for whoever has to decide whether to upgrade.

See [`docs/versioning.md`](docs/versioning.md) for what the numbers mean and
[`docs/releases.md`](docs/releases.md) for how a release is published.

## Unreleased

Use this section for changes that are merged but not released yet.

### Added

- **The user agreement is sent as rich Markdown.** It is the one document in
  Quoto that was faking structure — seven `<b>1. …</b>` items inside a
  blockquote — and it now goes out through `sendRichMessage`
  (`rich_message.markdown`, Bot API 10.3), so Telegram renders real headings, a
  real list of what gets processed, and the AI-processing caution as a real
  quote. The document is authored once as structure in the locales and rendered
  twice, so the HTML fallback says exactly the same things.
- **The agreement is signed.** The italic `doc_footer` glued to the bottom is
  replaced by a signature section that is part of the document: agreement
  version (`1.0`), effective date (`2026-09-09`, ISO 8601 so it reads the same
  in every locale), operator (`FreshLabDev`) and contact (`@amtiyo`).
- `TELEGRAM_BOT_API_BASE_URL`, **an operator-facing setting**: the self-hosted
  Bot API server to run against. Empty keeps api.telegram.org, which has no
  `sendRichMessage` and drops the agreement to HTML. Quoto had no way to be
  pointed at our own server at all, so the 10.3 methods were unreachable.
- A startup preflight for `sendRichMessage`, the family's Preflight pattern.
  The method is asked for with an empty body — an existing method rejects that
  on its parameters, a missing one answers `404 method not found` — and the
  answer decides which rendering the agreement uses. Unlike makeitMD, where the
  method is the whole product and its absence is fatal, here it is a degraded
  mode: loud in the log and in a developer notification, never silent. An
  inconclusive probe counts as "no", because HTML is the answer that always
  works.
- An **About** tab in the `/start` panel, in both scopes: name and version, one
  line of purpose, then scoring provider, repository (a link in the text, with
  no button duplicating it) and admin contact as `key · value` rows.
- The user agreement is now a tab of the `/start` panel — in a group it is where
  an admin accepts it, in a private chat it is read-only — instead of a command.
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

### Changed

- The production stack joins `telegram_bot_api_net` as well as `core_net`.
  Quoto never had a `TELEGRAM_BOT_API_BASE_URL` setting at all, so it has always
  talked to api.telegram.org and Bot API 10.3 was unreachable. Adding the
  variable is not enough on its own: the container also has to sit on the
  network the server lives on, or the URL resolves to nothing and the new
  rich-markdown agreement falls back to HTML on every send. The operator still
  has to set the variable in `.app.env` on the host.

- **Commands are registered per scope and per language.** One global list with
  hardcoded English descriptions advertised `/start` and `/privacy` to
  everybody; `/privacy` led nowhere in a private chat, and `/start` opens a
  different screen in a group than it does in a DM. `/start` is now published
  through `BotCommandScopeAllPrivateChats` and `BotCommandScopeAllGroupChats`
  with a description written for each, localized into all four interface
  languages via `language_code`. It is the only registered command in either
  scope: `/privacy` was a document view, so it became a tab and the command was
  removed.
- Navigation matches the rest of the family: `Back` lost its `‹`, and `Close`
  appears only in group panels — a private panel has nothing to close.
- The trophy is a state marker again, not decoration. `🏆` is gone from the
  panel headers, the group greeting and the private hello, and stays only on the
  published quote, where it means the line that won the day. The agreement
  buttons lost their `✅` and `📄` icons.
- One versioning and release document for the whole family. `docs/versioning.md`
  and `docs/releases.md` are now byte-identical across every Asterfield
  repository apart from two clearly marked sections: this repository's own
  version line, and the surface where a change here breaks something. They spell
  out what each of the three numbers means, what the `-alpha.N` suffix counts,
  when alpha becomes beta and when it is legitimate to skip to rc or run a
  pre-release in production.
- **Pre-releases are now tagged on `dev`, not `main`.** Only stable versions are
  tagged on `main`, on the merge commit from `dev`, so `main` answers exactly one
  question: what is in production. The test bot runs `dev`, the production bot
  runs `main`. `release.yml` enforces this and refuses a tag on the wrong branch.
  Earlier pre-releases in this repository were tagged on `main` under the
  previous rule; they are left as they are.
- `AGENTS.md`, which quoto was the only repository in the family to lack.

- Media description prompts now cast the model as the eyes and ears of someone
  who can't see or hear the file: on-screen text and speech must be quoted
  verbatim (screenshots of chats keep every line with its sender), unclear
  audio is marked `[неразборчиво]` instead of guessed, and the length caps grow
  to 1200 characters when there is a lot of text or speech (the payload still
  clips descriptions at 1500). `MEDIA_CACHE_PROMPT_VERSION` is deliberately
  left at `v2`, so already-described files keep their old descriptions.

### Fixed

- The operator contact in the user agreement was `@amti_yo`, which is not a
  Telegram account. It is `@amtiyo`, corrected in all four locales, and the
  document now takes it from one constant instead of repeating it.
- The Dockerfile no longer defaults `QUOTO_VERSION` to `0.10.2`. It stayed at
  that value through the whole of v0.10.3, so an image built without the
  argument labelled itself as a release it was not.
- A second `## Unreleased` section, left behind by the v0.10.2 release, sat
  between v0.10.2 and v0.10.1. Release preparation renames `## Unreleased` to
  the new version, and with two of them the wrong one could be renamed.

### Removed

- Legacy `OPENROUTER_MODEL` setting. The quote evaluation model is now read only
  from `OPENROUTER_EVAL_MODEL`, whose default is `poolside/laguna-s-2.1:free`;
  a blank value falls back to that default. Previously the two settings aliased
  each other at startup and whatever was written into `OPENROUTER_MODEL` was
  silently overwritten.

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
