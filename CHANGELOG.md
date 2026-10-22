# Changelog

All notable Quoto changes are documented here.

The `## <tag>` section of this file *is* the GitHub Release body: the release
workflow copies it verbatim and refuses a tag that has no section. Write it
for whoever has to decide whether to upgrade.

See [`docs/versioning.md`](docs/versioning.md) for what the numbers mean and
[`docs/releases.md`](docs/releases.md) for how a release is published.

## Unreleased

## v1.0.0 - 2026-10-22

Quoto now runs on Go. Groups get the same daily quote; the service behind it is
a single binary that uses a fraction of the memory, and about twenty bugs found
while porting are fixed.

### In numbers

Measured on ws04 on 2026-10-22: the Python bot in production, the Go bot on the
test stack after a full day with photos, a sticker, a GIF and a Claude pick.

| | v0.11.1 (Python) | v1.0.0 (Go) |
|:--|--:|--:|
| Container memory | 428 to 640 MiB | 8 to 10 MiB |
| Image on disk | 1.05 GB | 223 MB (61 MB to download) |
| Dependencies | 13 packages, 51 pinned | 4 Go modules |
| Settings read from env | 65 | 23 |
| Test functions | 300 | 645 |
| Media model answering | 404 every hour, then the fallback | first try |

On the test stack the quote went out 10 seconds after the cutoff; a photo or a
sticker took about 5 seconds to describe, a GIF 2.3.

### What changed for groups

- **Anonymous reactions count.** Python read a field the reaction-count update
  does not have, so the totals of anonymous reactions were never stored.
- **Media of a group that has not accepted the agreement goes to no AI
  provider.** It is kept as file type, size and duration until an admin accepts.
- **Messages older than 72 hours are deleted**, and the message text copied into
  the evaluation report is blanked at the same age. Before, days that were never
  processed stayed forever.
- **A new Russian-speaking group starts in Europe/Kyiv**, not Europe/Moscow.
  Admins still pick any zone in the settings.
- **The welcome message shows the group's real cutoff** instead of 21:00, and the
  dashboard shows the real media-analysis state instead of always "on".
- **Voice messages are described in full.** Python's silence filter cut a voice
  note at its first pause.
- **Joins, leaves, pins, dice and locations are not stored** and no longer count
  towards the ten-message threshold.
- **The Details card drops the Reactions and Length bars.** Neither one affected
  the pick.

### What changed for operators

- New compose manifest, `DATABASE_URL` instead of `DB_URL`, `/healthz` instead
  of a heartbeat file. Rolling back to Python means its old compose file and its
  old digest together.
- Migrations are embedded and run at start; the Alembic database is adopted
  after its tables and columns are checked, and the bot refuses to start if they
  differ.
- The first night after the deploy re-describes media once (prompt `v2` to `v3`).
- License: Apache-2.0 from this release. Releases up to v0.11.1 stay GPL-3.0.

Every change, with the reason for it, is in the `v1.0.0-alpha.1` section below
and in [`docs/go-rewrite.md`](docs/go-rewrite.md).

## v1.0.0-alpha.1 - 2026-10-22

Quoto is rewritten in Go. For the people in the chats it does what it did, with
the changes listed below; what moves is the service around it: one static binary
and `ffmpeg` instead of a Python interpreter and its packages, and a license
that matches the rest of the family. The design, the compatibility contract and
every decision are in [`docs/go-rewrite.md`](docs/go-rewrite.md).

### Operators

Read this part before deploying.

- **The new image does not run under the old manifest, and the old image does not
  run under the new one.** `deploy/ws04/compose.yaml` renames the service from
  `app` to `bot` (the container is still `quoto-bot`), builds the database URL
  with a `postgres://` scheme, and checks `/healthz` instead of the heartbeat
  file. To go back to a Python release, deploy that release's `compose.yaml`
  together with its image digest; setting only `QUOTO_IMAGE` back fails the health
  check and `ws04 deploy` rolls back again. The database needs nothing for that.
- **Renamed**: `DB_URL` is now `DATABASE_URL` and must be a `postgres://` URL
  (`postgresql+asyncpg://` is refused at start); `LOGS_PATH` is now
  `AUDIT_LOG_DIR`. The production manifest sets both, so the host `.env` is not
  touched. `BOT_TOKEN` and `BOT_USERNAME` keep their names.
- **New**: `BOT_API_FILES_DIR` (required only together with
  `TELEGRAM_BOT_API_BASE_URL`), `HTTP_ADDR` (`:8080`, serves `/healthz`) and
  `LOG_LEVEL` (`info`).
- **No longer read**: `QUOTE_HOUR`, `QUOTE_MINUTE`, `QUOTE_MINUTE_JITTER`,
  `TIMEZONE`, `MIN_MESSAGES_FOR_AUTO_REVIEW`, `MAX_MESSAGES_PER_DAILY_EVAL`,
  `SCHEDULER_CONCURRENCY`, `WEIGHT_*`, `COLORS`, `EMOJIS`, `LENGTH_OPTIMAL_*`,
  `ENABLE_DEVELOPERS_NOTIFY`, `HEARTBEAT_FILE`, the `MEDIA_*` tuning variables,
  `OPENROUTER_DATA_COLLECTION`, `OPENROUTER_ZDR`, `OPENROUTER_BASE_URL`,
  `OPENROUTER_APP_TITLE`, `OPENROUTER_HTTP_REFERER`, `OPENROUTER_MEDIA_{IMAGE,VIDEO,AUDIO}_MODEL`
  and the `*_MAX_TOKENS` ceilings. Their old defaults are constants now. A name
  that is still set is listed once at start (never its value) and does nothing.
  The production `.env` sets only values equal to those defaults, so nothing
  changes there. Alerts stay on while `DEVELOPER_IDS` is not empty.
- **Migrations are embedded in the binary and applied at every start**, under an
  advisory lock, and recorded in `schema_migrations`. A database that Alembic
  last touched (`20261018_01`) is adopted after its tables and columns are
  compared with what the Go version expects; if they differ, the bot refuses to
  start and names the table or column. `alembic_version` stays. `002_indexes`
  adds two indexes. No column is dropped in this release.
- **Core contract unchanged**: app key `quoto`, `core.touch`, `core.set_language`,
  `core.clear_language`, `core.effective_language`; no direct writes to `core.*`.
  A new database (not production) also needs `GRANT REFERENCES ON core.person,
  core.chat TO quoto_core`, which core grants to other bots' roles but not to this
  one yet.
- **The image runs as uid and gid 1000**, the owner of the host `logs/`
  directory. The manifest has no `user:` line.
- **The first night after the deploy costs more.** The media description prompt
  moves from `v2` to `v3`, so the cache misses once and images and videos are
  described again.
- **The first retention run deletes every message older than 72 hours**, which
  includes the messages of days that were never processed, and it cannot be
  undone by going back to Python. Take `pg_dump -n quoto` before the first start.
- **The first start needs more privileges than a restart.** It creates
  `schema_migrations` and the two indexes of `002`, so the role needs `CREATE` on
  schema `quoto` and ownership of `messages` and `message_ai_scores`. Python's
  Alembic changed those tables as the same role, so it very likely has both; if
  not, the start stops with `permission denied` and names the `GRANT`, and the
  data is untouched. Later starts need neither. The catalog queries that check it
  are in `docs/go-rewrite.md` section 5.5.
- **The manifest is hardened**: `cap_drop: [ALL]`, `no-new-privileges`,
  `pids_limit: 256` and `mem_limit: 512m`. The test stack publishes its port on
  loopback only.
- **Memory**, measured on macOS arm64 against the real binary and pipeline (the
  Alpine image and a container were not measured; no Docker was available):
  idle 8 to 20 MB, a day of 1500 messages 29 MB, a 50 MB video 135 to 145 MB for
  the bot and ffmpeg together (the bot 20 to 26 MB), no growth over 30 videos in a
  row. Python used 640 MiB in the container. A 16-megapixel picture sent as a
  file takes 125 to 145 MB, a progressive 4:4:4 JPEG of that size up to 300 MB.

### Changed

- **Media of a group that has not accepted the agreement goes nowhere.** Until an
  admin accepts version 1.2, a photo, video or voice message reaches neither
  Claude nor OpenRouter; it is described from its metadata, as when media
  analysis is off. Before, only the Claude path was gated and OpenRouter still
  received the media. Media that arrived before acceptance is not analysed
  afterwards.
- **The media model default is `google/gemini-3.5-flash-lite`**, with
  `google/gemini-3.1-flash-lite` behind it. `google/gemini-2.5-flash-lite` has no
  zero-data-retention endpoint on OpenRouter, so every request carrying
  `zdr: true` was refused with `404` once an hour and fell through to the
  fallback.
- **The Details card drops the Reactions and Length bars.** Neither affected the
  pick (the score is the AI's); the heart count stays. The columns stay and hold 0.
- **Media**: video is never scaled up (a 320x240 clip stayed 960x720 before) and
  has one duration cap of an hour; audio is trimmed only at the start, where a
  pause in the middle used to cut a voice message to its first phrase; a file
  over 100 MiB is not downloaded and keeps its metadata description; a network
  error while downloading is retried like other temporary errors.
- **The per-message copies in the evaluation report are blanked after 72 hours.**
  `message_ai_scores` kept the author name, text, caption and media description
  of every scored message, and `ai_evaluation_runs` the model's reason text,
  indefinitely, although the messages themselves are deleted. Scores, ranks, token
  counts and media metadata stay.
- **Oversized files are refused before they are decoded.** An image above 16
  megapixels or a video with frames above 2560x1440 keeps its metadata
  description instead of being decoded, and a file that makes the process crash
  is given up after five tries instead of being claimed again every ten minutes.
  ffmpeg runs with a minimal environment, may open files only and refuses
  oversized frames itself. A large picture and a video are never processed at
  the same time.
- **A message with no text, caption or file is not stored.** A join, a leave, a
  pin, a poll, a dice or a location was stored as `<type>: сообщение без текста`,
  counted towards the day's minimum and sent to the model as noise, so a quiet
  day with a few joins cost an AI call. The group is still recorded. Rows already
  stored go with the 72-hour retention.
- **A day that cannot be closed waits 15 minutes before the next try** instead of
  trying and paying for the AI every minute for two hours. A day with no eligible
  message is closed without another call. A run that is still in progress is not
  started a second time by the catch-up after the agreement is accepted, and
  that catch-up only runs once the day's cutoff has passed.
- **A day verdict that does not parse sends the day to the next model** instead of
  publishing nothing for that run.
- **Messages are deleted after 72 hours** even when their day was never processed
  (a group that did not accept the agreement, an outage, an `*_unknown` day).
- **Secrets are scrubbed in more forms.** The redactor also removes
  percent-encoded passwords and whole `postgres://` URLs, the Claude and
  OpenRouter clients no longer follow redirects (the key and the day's text would
  go to the new address), and the audit log is pruned at start as well as on
  write.
- **Logs are JSON on stdout.** The per-module files and the emoji console format
  are gone; `logs/ai_audit.jsonl` stays, with its seven days and mode 0600.
- **About shows "Scoring: Claude, OpenRouter"** instead of the stale
  "OpenRouter".
- **License: Apache-2.0** from this release, like the other Asterfield bots.
  `v0.11.1` and earlier stay GPL-3.0. The user agreement and its version are
  unchanged; the license appears in About only.

### Fixed

- **Every group runs in its own time zone.** The zone database is embedded in the
  binary, because the image has none; without it the cutoff and the day window
  would have been computed in UTC. The bot checks the zones it names itself at
  start.
- **Anonymous reaction totals are counted.** The handler read a field the update
  does not have, so every `message_reaction_count` update failed.
- **Unblocking the bot in a private chat no longer creates a group.** It created a
  `group_settings` row for the user id, and the scheduler then posted agreement
  reminders into that chat.
- **The `sendRichMessage` probe survives a network error, `429`, `5xx` and `409`**
  by staying on HTML, and rich Markdown is switched off only when the method is
  really gone, not by a deleted panel or a message that cannot be edited.
- **A failing `getChatMember` answers the button with an alert** instead of
  leaving it spinning.
- **The agreement reminder is marked done only after it was sent**, as the
  `v0.10.2` changelog said.
- **The welcome message shows the group's real cutoff** (it printed 21:00 for a
  group whose minute is between 21:00 and 21:10), and the group dashboard shows
  the real media-analysis state.
- **Editing a media message keeps its stored description.**
- **A person without a name is shown as "Anonymous" in the viewer's language**
  instead of a hard-coded Russian word.
- **Equal-win authors in the statistics come out in a stable order.**

### Removed

- `scripts/bench_web.py`, `bench_web_ui.html`, `compare_eval_models.py`,
  `try_media_model.py` and their tests: they imported the Python modules. They stay
  in git history and at tag `v0.11.1`.
- The Python service: `app/`, `alembic/`, `requirements*`, `tests/`,
  `healthcheck.py`, `docker-entrypoint.sh`, `main.py`.

## v0.11.1 - 2026-10-20

Claude Opus 5.5 picks the quote of the day and Claude Sonnet 5.5 describes
images, with OpenRouter behind both; the user agreement is 1.2, and every group
accepts it again before Quoto publishes there.

This is `v0.11.1-alpha.2` promoted unchanged. Both alphas ran on the production
bot from 18 October: the day was scored by Claude every evening, including
20 October, and the report fix in alpha.2 held. The OpenRouter media fallback
answered `402 Payment Required` on 18 October until its balance was topped
up; Claude's own path was not affected. Operators coming from `v0.11.0`: read
the Operators section of `v0.11.1-alpha.1` for the new variables, the
migration and the new dependency.

## v0.11.1-alpha.2 - 2026-10-18

One fix found on the first real days of alpha.1. Nothing changes for
operators: no new variables, no migration.

### Fixed

- **A day recomputed after a failed publish keeps the report of the run that
  succeeded.** `ai_evaluation_runs` holds one row per chat and day. When the
  first attempt ended as `ai_failed` and a later one picked the quote, the
  insert hit `uq_ai_evaluation_run_group_day`, the error was logged as "already
  exists", and the table kept the failed row: 17 October's row said
  OpenRouter had failed while Claude Opus 5.5 had published the quote, and the
  scores, model and cost of the real run were lost. The failed row is now
  replaced together with its `message_ai_scores` in one transaction. A report
  that is already `parsed` is still left alone.

## v0.11.1-alpha.1 - 2026-10-18

Claude now picks the quote and describes images, and the user agreement moves
to 1.2 so that every group agrees to that first.

Quoto has no test bot, so this alpha runs on the production bot. It either
becomes 0.11.1 after a few real days or gets rolled back to 0.11.0.

### Changed

- **Claude Opus 5.5 scores the day** through the Anthropic API, at `medium`
  effort with adaptive thinking and a structured answer. OpenRouter's
  `openai/gpt-6-luna` and `google/gemini-3.1-flash-lite` stay behind it: a
  refusal, an empty balance, an outage or an answer that does not parse moves
  the day to the next model. Replaying 10–17 October, Opus at `medium` and
  `high` picked the same quote on six days of seven, while Sonnet 5.5's pick
  changed from run to run; Opus costs about $0.11 a day there, Sonnet 5.5
  about $0.055. The API reports tokens, not dollars, so Claude's cost is
  computed from list prices ($4 / $20 per million tokens for Opus 5.5) and
  stored with the run like OpenRouter's.
- **A safety refusal no longer costs the day.** Every Claude request opts into
  Anthropic's server-side fallback (`fallbacks: "default"`): a day the
  classifiers decline is answered in the same call by the model Anthropic
  recommends for that refusal category. Replaying 16 October, the cyber
  classifier declined a whole day of banter on Opus 5.5 and Sonnet 5.5 at every
  effort level; with the fallback, Claude Opus 4.8 picked the quote. The
  declined attempt is counted in the stored cost, so such a day costs about
  twice as much as an ordinary day of the same size.
- **Claude Sonnet 5.5 describes photos, images and stickers.** Video, video messages,
  voice messages and audio stay on OpenRouter, because Claude takes none of
  them. A failed Claude call falls back to the OpenRouter media chain, and
  OpenRouter's cooldown after a 402 no longer holds images back from Claude.
- **The user agreement is 1.2, and every group accepts it again.** The AI
  section now says that text, images and stickers go to Claude, that Anthropic
  does not train on them and deletes them within 30 days, keeping longer only
  what its safety systems flag, and that video and audio go through OpenRouter
  to providers that keep nothing. 1.1 promised only the latter, so a group that
  accepted it publishes nothing until an admin accepts 1.2; it gets a daily
  notice saying what changed, and its images keep going to OpenRouter only.
- `scripts/compare_eval_models.py` replays audited days on Claude as well, and
  reads records written by either provider. `claude-sonnet-5-5@low,claude-sonnet-5-5@high`
  compares effort levels in one run. Claude replays send the same fallback as
  the bot; `--no-fallbacks` shows the bare model's refusals.

### Operators

- **New variables**: `ANTHROPIC_API_KEY`; `ANTHROPIC_EVAL_MODEL`
  (`claude-opus-5-5`) and `ANTHROPIC_MEDIA_MODEL` (`claude-sonnet-5-5`);
  `ANTHROPIC_EVAL_EFFORT` and `ANTHROPIC_MEDIA_EFFORT`, both `medium`;
  `ANTHROPIC_EVAL_MAX_TOKENS` (32000) and `ANTHROPIC_MEDIA_MAX_TOKENS` (8000).
  Without `ANTHROPIC_API_KEY` the bot sends everything to OpenRouter as 0.11
  did. `OPENROUTER_API_KEY` is no longer required on its own: one of the two
  keys is.
- **Migration `20261018_01`** adds `quoto.group_settings.agreement_version`.
  Every group that accepted before has NULL there, which the bot reads as a
  version older than 1.2.
- **New dependency**: `anthropic` 1.15.0, which brings `httpx2`, `jiter`,
  `docstring-parser`, `opentelemetry-api`, `sniffio` and `truststore`.
  `pip-audit` reports nothing.

## v0.11.0 - 2026-10-16

A group's messages now reach only model endpoints that keep nothing and train
on nothing.

Until now production scored the day with a discounted Meta tier whose prompts
and outputs may be used to improve Meta's products, and the built-in defaults
were free endpoints. Every request now asks OpenRouter for zero-retention
endpoints only, the default models are ones that have them, and the user
agreement says so. The dependency lock moves too: 87 known advisories in ten
packages, most of them in aiohttp and pillow, are down to none.

There was no pre-release. Every day this waited on an alpha was another day of
chat text going to a provider allowed to keep it. The request changes by one
field, covered by tests, and each new model was checked against OpenRouter's
zero-retention list before the tag.

### Security

- **Chat text only reaches endpoints that keep nothing and train on nothing.**
  Quoto sends a whole day of a group's messages, with author names, to a model.
  The built-in defaults were free endpoints, and production scored the day with
  `meta/muse-spark-1.3-contributor`, a discounted tier whose prompts and outputs
  may be used to improve Meta's products. Every OpenRouter request now carries
  `provider: {"data_collection": "deny", "zdr": true}`, so OpenRouter routes it
  only to endpoints with a zero-data-retention policy. A model without such an
  endpoint fails over to the fallback model instead of reaching one that keeps
  the data. The bench scripts, which replay real audited days, send the same.
- aiogram 3.25.0 → 3.31.0, which lets aiohttp go from 3.13.3 to 3.14.5, and
  pillow 12.2.0 → 12.3.0, with anyio, click, idna, mako, multidict,
  pydantic-settings, pygments, pytest and python-dotenv moved to fixed
  releases. `pip-audit` went from 87 known advisories in ten packages to none.
  aiohttp carries all of Quoto's Telegram traffic, and Pillow decodes the
  images people send to the chat.

### Changed

- **New default models**, each with zero-retention endpoints on OpenRouter.
  Evaluation: `openai/gpt-6-luna`, falling back to
  `google/gemini-3.1-flash-lite`. Media: `google/gemini-2.5-flash-lite`,
  falling back to `google/gemini-3.1-flash-lite`. At last month's volume the
  evaluation costs about $0.003 a day per group, the same as before.
- **Operators: two new variables, one env change.** `OPENROUTER_DATA_COLLECTION`
  (default `deny`) and `OPENROUTER_ZDR` (default `true`); the bot warns at
  startup when either is relaxed. A deployment that pins
  `OPENROUTER_EVAL_MODEL` or the media models to a model without a
  zero-retention endpoint keeps working through the fallback, but every
  request first fails against that model: remove the override or point it at
  a model with a zero-retention endpoint.
- The user agreement says that OpenRouter passes messages only to providers
  that neither store them nor train models on them, in all sixteen languages.
  Its version is 1.1; groups that accepted 1.0 are not asked again.
- The language prompt listed four interface codes while the response schema
  allowed sixteen. It now lists all sixteen.
- README and README.ru are rewritten in the family's shape: what Quoto does,
  how to use it, a privacy section, and the sixteen languages and GPL-3.0
  licence it actually has. AGENTS.md said Apache-2.0 and four languages; fixed.
- **A release waits for CI.** `release.yml` runs `ci.yml` on the tagged commit
  and publishes nothing unless it passed. When CI cannot run, `release.yml` can
  be started by hand with the tag and a reason; that run skips CI and prints the
  reason at the top of the release notes. See `docs/releases.md`.
- The release notes end with the image digest to pin, which `docs/releases.md`
  already said they did.
- CI audits `requirements.txt` against known advisories. It reports on every run
  and fails only the weekly one, so an advisory does not hold a release of
  unrelated code. When it was added it found 87 advisories in ten packages,
  most of them in aiohttp and pillow; the Security entries above clear them.
- Dependabot opens grouped version updates against `dev`.

## v0.10.7 - 2026-09-10

Quoto speaks sixteen languages instead of four, and each of them writes a date
the way that language writes one.

Twelve new locales landed, and they broke a date that four languages had hidden:
Spanish needed a preposition that was baked into the month name, Chinese and
Japanese write the year first, German and Czech want a period after the day.
The order now lives in the locale beside the month names.

Every screen is also built by one helper, so the group panel stops printing the
bot's name above its own title, and no screen repeats in the message what its
buttons already say.

### Changed

- `docs/releases.md` names the real cause of the `403` a digest pull used to
  answer: the account running the deploy could not read the private package,
  not anything about the digest. Pulling the tag first was a workaround for
  that, and is no longer required.

## v0.10.7-alpha.1 - 2026-09-09

### Changed

- The panel now follows the Asterfield family contract, so a person who learns
  one of the bots reads all of them. Every screen — both homes, language,
  quote day, timezone, publishing, stats, About and the user agreement — is
  built by one helper as title, hint, quote, and no screen repeats the bot's
  name above its own title any more.
- The language picker shows flag and native name in the family order
  (`en, ru, uk, de`), two to a row, in the panel and in the agreement alike.
  The list and its labels live in `app/i18n.py`; quoto renders the languages it
  has, so a new locale is a JSON file plus a code and its button appears in the
  right place by itself.
- Buttons carry colour where Telegram can show it: Close is destructive, the
  current language, the chosen timezone and the open stats tab are marked as
  the state you are in, and each screen with one obvious next step has exactly
  one highlighted button — "Add to group" in a direct chat, "Stats" in a group,
  "Accept" on the agreement. Screens that lead nowhere have none.
- The timezone, language and publishing screens no longer list in the message
  what their own buttons already say. The state lives on the buttons; the text
  says only what a button cannot — where a language came from, and what
  "context" means.

### Fixed

- The quote-day time nudges said "ч" and "м" on every locale's panel, Russian
  units on the English and German ones included. They now read `−1:00` and
  `+0:15`, which is the same in every language and needs no translation.

## v0.10.6 - 2026-09-09

Quoto is GPL-3.0. The About card and the agreement signature said Apache-2.0.

### Fixed

- The licence named in the About card and under the user agreement is GPL-3.0,
  which is what `LICENSE` has always said. Apache-2.0 was written in by
  assumption from the rest of the family, where it is correct — and it went
  into two places a reader is meant to be able to rely on, one of them a
  document people accept.


## v0.10.5 - 2026-09-09

A one-line fix for something v0.10.4 shipped: it reported the wrong version.

### Fixed

- The version is read from the image rather than written in the source.
  `Dockerfile` already exported `QUOTO_VERSION` from the release workflow's tag,
  but `app/version.py` held a literal that nobody bumps — so v0.10.4 started up
  announcing 0.10.3, and the About card added in that same release would have
  told every group the same thing. An unstamped build now says `dev` instead of
  naming a release it is not.


## v0.10.4 - 2026-09-09

The user agreement is a real document now, rendered natively rather than
imitated with bold text, and signed. Quoto also gets the continuous integration
it was the only bot in the family to lack.

### Added

- `docs/releases.md` gained a **Deploying** section, and `AGENTS.md` points at it.
  Releasing was documented; deploying was not, in any repository in the family —
  the process stopped at "deploy it" and never said how. That gap mattered more
  after the stacks moved from building on the host to pulling a published image,
  because the procedure changed on the same day. The section names this stack's
  host directory, its env file, the variable that selects the image, the networks
  it needs, and what a rollback actually is.

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

- The production manifest expects one `.env` rather than the stack's current
  `.env` plus `.app.env` split, matching every other stack in the family. The
  keys do not collide, so the two merge at the first deploy from GHCR.
- Corrected why quoto joins the Bot API network: it is `getFile`, not rich text.
  Telegram's own endpoint carries the newest Bot API — makeitMD renders rich
  messages against it today — but caps a download at 20 MB, and quoto analyses
  video far longer than that.

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

- The Bot API session is built with `is_local=True`. Our server runs with
  `--local`, where `getFile` answers with a path on the server's own disk and
  the `/file/bot<token>/…` route returns 404 by design. Left at aiogram's
  default, pointing quoto at that server would have failed the download of every
  photo, video, circle and voice note — not as a retry or a skip, but as a
  stored failed analysis, whose only symptom is a duller quote a day later.
- A transport error is scrubbed before it is logged and before it is stored. Its
  text carries the request URL, and a Bot API URL carries the bot token, so the
  media failure path was writing the token into `logs/` and into
  `message_media.error`.
- The rich-Markdown fallback also catches a `400`. It caught only `404`, but a
  server that does not understand `rich_message` ignores the field and complains
  the text is empty — which is a 400, and would have left the agreement tab dead
  rather than falling back to the HTML the document is authored twice for.
- The startup probe no longer reads flood control, a `5xx` or a webhook conflict
  as proof that `sendRichMessage` exists. All three are `TelegramAPIError`
  subclasses, so a server that was briefly busy at startup would have latched
  the rich path on for the life of the process.

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
