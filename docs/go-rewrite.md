# Quoto: rewrite in Go

Plan for moving Quoto from Python (aiogram, SQLAlchemy, APScheduler, Alembic) to
a Go service built like searchy, voicy and branchy. Base: `v0.11.1`
(`53ebc34`), branch `go`. Written 2026-10-22, before any Go code.

Status: approved by the owner on 2026-10-22. The decisions on section 7 are in
7.0 and override the "My pick" columns where they differ. Prod facts from the
read-only catalog snapshot of 2026-10-22 and from the key names of the host
`.env` are folded in below.

This is a new implementation of the same product, not a line-by-line port.
Section 6 lists what must not change; section 7 lists what changes.

## 1. Targets

| | Python today (ws04, 2026-10-22) | Go target |
|:--|:--|:--|
| Memory, idle | 640 MiB container, RSS of python 514 MB | below 60 MB |
| Memory, 50 MB video | not measured | below 200 MB |
| Image | 1.05 GB | about 220 MB, the size of voicy (alpine plus ffmpeg; the Go binary is a few MB) |

"200 MB" is counted for the container, so ffmpeg children are included. Section
2 says what that costs. The 259 behaviours found in the Python tests carry over
as Go tests.

Product boundaries stay as written in `AGENTS.md`: `/start` is the only command;
silent collection between two daily cutoffs; at most one quote per group per
day; fewer than ten messages is skipped without a notice; an unremarkable day
gets a boring-day notice and no fallback quote; 16 languages; identity, chats
and language only through `core.touch`, `core.set_language`,
`core.clear_language`, `core.effective_language`.

## 2. Where Python memory goes, and the Go rule

| Python (evidence) | Go rule |
|:--|:--|
| `ai.py` reads the media file with `read_bytes`, base64-encodes it, builds a data URI, and `httpx` serialises the body again on every attempt. Measured on a 20 MB file: peak 87 MB, about 4.3 times the file. | The file stays on disk. The request body is written through `io.Pipe` and `base64.NewEncoder`, reopened per attempt. `Content-Length` is computed from the file size (prefix + 4*ceil(n/3) + suffix) so the request is not chunked. |
| Pillow decodes a whole `image/*` document and keeps two copies. Measured: 48 MP JPEG, peak RSS 444 MB. | `image.DecodeConfig` first. More than 40 MP is refused as `failed` with the metadata description. One image decode at a time. A Telegram `photo` is at most 1280 px and never hits this. |
| No limit on concurrent media jobs. aiogram runs every update as its own task; Pillow and ffmpeg run in a pool of up to `min(32, cpu+4)` threads. | One bounded queue: 2 media jobs at once, 1 of them may be a video. The scheduler keeps its constant width of 8 groups; their payloads are text. |
| ffmpeg is a child process: 119 to 330 MB RSS, depending on threads (measured on macOS, 1080p H.264, 30 s). It counts against the container. | `-threads 1 -filter_threads 1` on input and `-threads 1` on output, one video at a time, `GOMEMLIMIT` set in the image. Not measured in the final image yet; phase 5 does it with a 50 MB file. |
| Phash loads every `media_cache` row of the kind, with descriptions, as ORM objects on every lookup. | Select `(id, phash)` only, compare Hamming distance in Go, fetch the row on a hit. |
| 11 loggers, each with a rotating file, plus stdout. | One JSON logger on stdout. |

Go runtime, the embedded locales (16 files, 10 to 15 KB each) and `time/tzdata`
(about 450 KB) are small next to the above. The sibling bots idle at 13 to 23 MiB.

## 3. Package layout

Module `github.com/FreshLabDev/quoto`, Go 1.26.10, `cmd/bot` plus `internal/`.

| Package | Does |
|:--|:--|
| `cmd/bot` | Wiring, signals, ordered shutdown with a `WaitGroup`. Only place with `os.Exit`. |
| `internal/buildinfo` | Version, commit, date (`-ldflags`), plus the constants the About card shows: repository, license (`Apache-2.0`), operator, contact. One source for `/healthz` and About. |
| `internal/config` | Reads env, applies defaults, fails the start on a bad value. Returns a plain struct, no globals. |
| `internal/log` | `slog` JSON logger and the single secret redactor, fed with every secret from config. |
| `internal/domain` | Types and string constants shared across packages: quote statuses, media statuses, content types, reason codes. No I/O. |
| `internal/db` | pgx pool, `search_path`, migration runner, and one store type per area (groups, messages, reactions, quotes, media, reports, stats, core). SQL sits next to the method. Consumers declare the narrow interface they need. |
| `internal/i18n` | `translations.json` via `go:embed`, `T(lang, key, pairs...)`, language normalisation and aliases, date formatting. |
| `internal/window` | Cutoff, closed window and "due" logic in a group's time zone, with DST rules. Pure functions. |
| `internal/scoring` | Picks the quote from AI scores: `#quoto` filter, truncation, context selection, stars. Pure functions. |
| `internal/claude` | Anthropic Messages API over `net/http`: streaming, `fallbacks: "default"`, beta header, usage by iteration, schema stripped of bounds. |
| `internal/openrouter` | Chat completions over `net/http`: provider block, headers, streamed media body. |
| `internal/ai` | Evaluation and media description on top of the two clients: prompts, payload, schemas, response parser, candidate chain, circuit breaker, media cooldown. |
| `internal/audit` | `ai_audit.jsonl`: append, seven-day prune, mode 0600. |
| `internal/media` | Source extraction from a message, download, normalisation (image, ffmpeg), perceptual hash, cache lookup, status machine, retry queue. |
| `internal/scheduler` | Minute tick, per-group daily run, publish, boring notice, stale recovery, media recovery job. |
| `internal/telegram` | Thin adapter over `*tg.Client` for what only Quoto needs: silent send, `copyMessage`, `pinChatMessage`, `getChat`, outcome classifier (definitely failed or unknown). |
| `internal/bot` | Update loop, handlers, deep links, rich-Markdown capability state with HTML fallback. |
| `internal/menu` | `callback_data` codec and panel builders. |
| `internal/agreement` | Document structure, version gate, Markdown and HTML renderers. |
| `internal/stats` | Group and user statistics (published quotes only) and the evaluation report snapshot written after each run. |
| `internal/health` | `/healthz`: database, Telegram polling freshness, scheduler freshness. |

`app/core.py` (1 239 lines mixing group settings, messages, reactions, quotes
and statistics) becomes the store types in `internal/db`, one file each.

## 4. Environment

Rule: env holds secrets, addresses, and what differs between the test and the
production bot. Claude and OpenRouter model names stay configurable. Everything
nobody has touched becomes a constant next to the code that uses it.

Python has 65 settings in `Settings` plus a few read directly (`QUOTO_VERSION`,
`HEARTBEAT_FILE`, `TZ`). Go reads 23.

### 4.1 Stays in env

| Variable | Note |
|:--|:--|
| `BOT_TOKEN` | Secret. Also the HMAC key of deep links: rotating it invalidates signed links. Name unchanged so the host `.env` keeps working (decision E1). |
| `BOT_USERNAME` | Test vs prod. |
| `DATABASE_URL` | Was `DB_URL`. Secret. The value changes from `postgresql+asyncpg://` to `postgres://`; compose already builds it from `QUOTO_CORE_PASSWORD`, so the host `.env` is not touched. `search_path=quoto` is set in code. |
| `TELEGRAM_BOT_API_BASE_URL` | Address. Empty today, and stays empty until the local server's media directory is mounted. |
| `BOT_API_FILES_DIR` | New. Required only when the base URL is set (`tg.WithLocalFiles`). |
| `DEVELOPER_IDS`, `PREMIUM_CHAT_IDS` | Differ between test and prod. Accept a JSON array (the form pydantic needs, so the current `.env` parses) or a comma list. |
| `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY` | Secrets. |
| `ANTHROPIC_EVAL_MODEL`, `ANTHROPIC_EVAL_EFFORT`, `ANTHROPIC_MEDIA_MODEL`, `ANTHROPIC_MEDIA_EFFORT` | Model choice. |
| `OPENROUTER_EVAL_MODEL`, `OPENROUTER_EVAL_FALLBACK_MODEL`, `OPENROUTER_MEDIA_MODEL`, `OPENROUTER_MEDIA_FALLBACK_MODEL`, `OPENROUTER_EVAL_REASONING_EFFORT`, `OPENROUTER_MEDIA_REASONING_EFFORT` | Model choice; effort is tuned together with the model. |
| `MEDIA_ANALYSIS_ENABLED` | Cost switch; a per-group switch also exists in the database. |
| `AUDIT_LOG_DIR` | Was `LOGS_PATH`. Holds `ai_audit.jsonl` only. Set in compose, not in the host `.env`. |
| `HTTP_ADDR` | New, default `:8080`, serves `/healthz`. |
| `LOG_LEVEL` | New, default `info`. |

### 4.2 Becomes a constant

| Python setting | Value | Lives in | Why |
|:--|:--|:--|:--|
| `QUOTE_HOUR`, `QUOTE_MINUTE`, `QUOTE_MINUTE_JITTER` | 21, 0, 10 | `window` / `db` | Defaults for a new group. Per-group values are in the database. |
| `TIMEZONE` | `Europe/Kyiv` | `window` | Product default; every group has its own zone in the database. |
| `MIN_MESSAGES_FOR_AUTO_REVIEW` | 10 | `scoring` | Default for new groups; per group in the database. |
| `MAX_MESSAGES_PER_DAILY_EVAL` | 1500 | `scoring` | Cost cap. Premium chats are exempt. |
| `SCHEDULER_CONCURRENCY` | 8 | `scheduler` | Not in `.env.example`, never changed. |
| `LENGTH_OPTIMAL_MIN/MAX` | 20, 150 | gone with S1 | Only fed the informational Length bar. |
| `ANTHROPIC_EVAL_MAX_TOKENS`, `ANTHROPIC_MEDIA_MAX_TOKENS`, `OPENROUTER_EVAL_MAX_TOKENS` | 32000, 8000, 32000 | `claude`, `openrouter` | Safety ceilings. |
| `OPENROUTER_BASE_URL`, `OPENROUTER_APP_TITLE` | fixed | `openrouter` | No deployment overrides them. |
| `OPENROUTER_DATA_COLLECTION`, `OPENROUTER_ZDR` | `deny`, `true` | `openrouter` | The agreement promises exactly this. See E4. |
| `MEDIA_*` (18 settings) | defaults unchanged | `media`, `ai` | ffmpeg parameters, retry intervals, timeouts. Each is a package-level `var` so tests can shorten it. Defaults never changed since 2026-07. |
| `MEDIA_CACHE_PROMPT_VERSION` | `v2` → `v3` | `ai`, beside the prompts | It has to move with the prompt text (`AGENTS.md`); an env override defeats that. |

### 4.3 Removed

| Variable | Why |
|:--|:--|
| `COLORS`, `EMOJIS`, `WEIGHT_REACTIONS`, `WEIGHT_AI`, `WEIGHT_LENGTH` | Never read. `ScoreBreakdown.total` returns the AI score. The compose manifest sets `COLORS=False` and it has no effect. |
| `HEARTBEAT_FILE` | Set nowhere. `/healthz` replaces the heartbeat file. |
| `ENABLE_DEVELOPERS_NOTIFY` | Redundant with an empty `DEVELOPER_IDS`. See E3. |
| `OPENROUTER_MEDIA_IMAGE_MODEL`, `_VIDEO_MODEL`, `_AUDIO_MODEL` | Empty in `.env.example`, absent from README; only `try_media_model.py` set them. |
| `OPENROUTER_HTTP_REFERER` | Default hard-codes the prod bot. Derived from `BOT_USERNAME`. |
| `RENAMED_ENV_KEYS` warning | Transitional. |
| `QUOTO_VERSION`, `TZ`, `PYTHONIOENCODING`, `PYTHONUNBUFFERED`, `LANG`, `LC_ALL` | Python and build plumbing; version comes from `-ldflags`. |
| `MIGRATIONS_DIR`, `AUTO_MIGRATE` (voicy has them) | Not added. Migrations are embedded and always applied at start, as the Python entrypoint always ran `alembic upgrade head`. |

Compose-only variables stay: `QUOTO_IMAGE`, `QUOTO_CORE_PASSWORD`.

Startup validation: a bad value stops the process with a non-zero exit (Python
exited 0 on a fatal start error, so `restart: unless-stopped` looped silently).
Variables that were removed or became constants and are still present in the
environment produce one startup warning that lists their names (never values).
The warning covers only names Quoto used to read: it does not mention
`POSTGRES_*`, `QUOTO_*` or `TZ`.

## 5. Schema plan

### 5.1 Facts

- Alembic head is `20261018_01`; table `quoto.alembic_version`. Five revisions.
  The first one runs `Base.metadata.create_all` from the **current** models, so
  it is not a frozen DDL. A fresh database at head equals `models.py` today.
- Eight tables: `group_settings`, `messages`, `reactions`, `media_cache`,
  `message_media`, `quotes`, `ai_evaluation_runs`, `message_ai_scores`. Foreign
  keys into `core.person` and `core.chat`. No enums, no CHECK constraints; all
  statuses are `VARCHAR`.
- Defaults declared in Python (`default=`) are not in the DDL. Only
  `group_settings.is_active` and `message_media.retry_count` have database
  defaults. Go must supply every other `NOT NULL` value in each `INSERT`.
- The prod catalog (2026-10-22) matches `models.py` and the migrations: same
  columns, types, nullability, defaults, constraints and indexes. Names to copy:
  the quote day index is `ix_quoto_quotes_quote_day`, foreign keys are
  `<table>_<column>_fkey`, `alembic_version` is `20261018_01`. Column order is
  the order in which migrations added them; Go never uses `SELECT *` or
  positional scans.
- Prod size: two groups (one with `agreement_version` NULL, one with `1.2`), 141
  quotes, about 1.6k messages, 4.8k `media_cache` rows (including 33 rows of
  prompt `v1`), 6k `message_ai_scores` rows. Everything is small, so no
  retention work is needed for the report tables.
- Not found in the repo or in `core/migrations`: who creates schema `quoto` and
  who owns it. `alembic/env.py` creates the schema only if missing, and
  `quoto_core` has no `CREATE` on the database. The test stack therefore
  creates the schema in its stub (section 11).

### 5.2 Plan

1. `migrations/001_baseline.sql` (the directory is embedded by `migrations/migrations.go`): plain DDL of Alembic head, written by hand from
   `models.py` and the four later migrations. No `CREATE SCHEMA`.
2. The runner is voicy's `Migrate` (ledger `schema_migrations(version, name,
   applied_at)` in the bot's schema, one transaction per file, advisory lock
   `4242424501`) with one addition, `adoptAlembic`. If the ledger is empty and
   `quoto.alembic_version` holds `20261018_01`, record version 1 without running
   it, after checking through `information_schema` that every table and column of
   the baseline exists with the expected type; a mismatch stops the start with an
   error naming the table and column. Any other revision also stops the start.
   On an empty database `001` runs as a normal migration, so it is tested. The
   expected-column list is a Go table, and a test asserts it equals the catalog
   after `001` on a fresh database, so the two cannot drift.
3. `migrations/002_indexes.sql`: additive only while Python remains a rollback
   option. Index `messages(chat_id, created_at)` (every window query filters on
   it, none exists) and index `message_ai_scores(message_db_id)` (every message
   delete checks it for `ON DELETE SET NULL`). At 1.6k and 6k rows the lock is
   instantaneous. It also gives the CI "up, down, up" step something to roll back.
4. `quoto.alembic_version` is not touched or dropped until a stable Go release.
5. Column drops go in one late migration, after Go has run on prod. Candidate
   list is R3.
6. Down migrations: the family has none (`core/docs/releases.md`). For the CI step
   "up, down, up" there is a `migrations/down/NNN_*.sql` per migration from `002`
   on; the runner's `Glob` does not enter subdirectories. Never run on prod.

### 5.3 Checks

- **Equivalence.** Check out `v0.11.1`, run `alembic upgrade head` on a throwaway
  PostgreSQL 17, `pg_dump --schema-only -n quoto`; run `001` on another; diff
  after normalising order. Needs no production access.
- **Adoption.** Four cases against PostgreSQL 17: empty database; database at
  head (hash of the `quoto` catalog before and after must match); other
  revision (refuses to start); two processes starting at once.
- **Prod drift.** Done once by hand on 2026-10-22 (read-only catalog snapshot,
  no row contents): no drift against `models.py`. The equivalence diff above
  uses the prod index and constraint names.
- **Core stub.** `deploy/core-init.sql` is a copy of core's `001` and lacks 010 to
  013. Integration tests use a smaller stub, as voicy does: schemas, `core.person`,
  `core.chat`, and the four functions.
- pgx needs explicit casts for core's enum parameters: `$n::core.pref_scope`,
  `$n::core.lang_source`.

### 5.4 Rollback compatibility

While the Python image is a possible rollback, Go writes every column Python
reads, in a format Python reads. That includes the two JSON blobs in `quotes`
and `ai_evaluation_runs.context_message_ids`, `request_id` as 32 hex characters,
and `chat_id` equal to `group_id` in the two report tables. The JSON is compact
(no space after `,` and `:`); Python only ever parses it with `json.loads`, so
the separators do not matter. Go does not read `quotes.context_message_ids` (no
reader in Python either) but still writes it.

A fresh database (not production) needs `GRANT REFERENCES ON core.person,
core.chat TO quoto_core` before `001` can create its foreign keys. Core grants it
to `makeitmd_core` (003) and `voicy_core` (011) but not to `quoto_core`, whose
tables predate the grant. Production never runs `001`, so this only matters for
a new test stack; it goes in the final report as a follow-up for core.

## 6. Compatibility contract

Every line below gets a test before the Go version is allowed near prod.

| Area | What stays | Test |
|:--|:--|:--|
| `callback_data` | `menu:{owner}:{scope}:{action}[:{payload}]` parsed with `split(":", 4)`; `agree:{action}:{lang}[:scope:owner]` with 3 or 5 parts; same scopes and action letters, payload formats (`timeadj` ±60/±15, `minadj` ±5/±1, `toggle` keys, `settz`, `setlang`). Unknown or stale data answers with an empty `answerCallbackQuery`, never an error. | Strings taken from the Python tests and round-tripped; stale names from history get an empty ack. |
| Deep links | `quote_{id}_{first 16 hex of HMAC-SHA256(BOT_TOKEN, "quote:{id}")}`; legacy `quote_{id}` shown only after a membership check (`_can_view_legacy_quote`); same "not found" answer for a missing and an inaccessible quote; only published quotes. ASCII digits only, so a Unicode digit is "not found", not a crash. | Vectors generated by the Python code. |
| Agreement | `DOCUMENT_VERSION` and `MIN_ACCEPTED_VERSION` stay `1.2`; integer-tuple comparison; a NULL `agreement_version` does not count; acceptance writes four columns together. | Python test cases ported (13). |
| Media cache | `prompt_version` rises to `v3`. `sha256` of the normalised file cannot match (encoder and ffmpeg thread count change the bytes); perceptual hashes are reproducible (see below). Expect one round of cache misses and paid re-descriptions; old rows stay in the table. | 13 golden phash vectors; lookup order test. |
| Strings in the database | Quote statuses (8), media statuses (7), `worthy`/`boring_day`, content types, `phash` as 16 hex or up to three joined by `:`, `phash_algo` `dct64`, `sha256` as 64 hex. | Constants asserted. |
| Lookup order | `file_unique_id`, `file_id`, `sha256`, `phash` (distance ≤ 5), all within `prompt_version` and kind. | Table test. |
| Queue claim | `processing` lease of 600 s, same select conditions, 5 attempts, delays 300/600/1200/2400 s capped at 3600, cooldown does not count as an attempt. | Table test with a fake clock. |
| Windows | Half-open `[start, end)` in UTC; `quote_day` is the local date of the closing cutoff; start is the previous cutoff by wall clock, not minus 24 h; catch-up window 2 h; DST gap shifts back, DST overlap takes the first occurrence. | Python's DST cases plus `Europe/Kyiv` transitions. |
| Python semantics | Stars use banker's rounding (`math.RoundToEven`); `#quoto` filter with Unicode word boundaries by hand (RE2 has no lookaround and `\w` is ASCII-only); `html.escape` output (`&#x27;`, `&quot;`); lengths and truncation in runes, entity lengths in UTF-16; JSON key order in prompts, schemas and payloads (structs or raw strings, never a map). | One test per item. |
| Prompts | Byte-for-byte, including the missing space in `_DAY_PROMPT` (`...}]}.Keep reason_text...`) and the leading spaces of `_INJECTION_GUARD` and `_LANGUAGE_DETECTION_PROMPT`. Language names in the detection prompt come from the locale files. | Golden text files extracted from Python. |
| Claude | `fallbacks: "default"`, beta `server-side-fallback-2026-07-01`, adaptive thinking, `output_config.effort`, structured output with bounds stripped from the schema (parser clamps), no Batch or Files API, no Fable or Mythos, `actual_model` logged, cost by `usage.iterations`. Photos, images and stickers only; video and audio go to OpenRouter. | `httptest` body assertions. |
| OpenRouter | `provider: {data_collection: "deny", zdr: true}` in every request including media; headers `HTTP-Referer`, `X-OpenRouter-Title`. | Every request path asserted. |
| Audit log | `ai_audit.jsonl`, same record keys, 7 days, operator only. | Record shape test; file mode test. |
| Agreement delivery | Empty `TELEGRAM_BOT_API_BASE_URL`: HTML, not rich Markdown; the log says which one the bot got. | Both paths tested. |
| Core contract | App key `quoto`; `core.touch` argument order and trimming; only normalised codes go to `core.set_language`; no direct writes to `core.*`. | Fake store and an integration test. |
| Supergroup migration | `migrate_group_chat_id` moves every table that carries the chat id, in one transaction; `core.rekey_chat` is not called (revoked for bots). | Integration test. |

Perceptual hash. The DCT stage (32×32 grey, 8×8 DCT, median of the 63 AC
coefficients, 64 bits) matched Pillow bit for bit on 13 of 13 test images when
fed the same grey grid. A Go decode plus a Pillow-like bicubic resize gave the
same hash on 9 of 9 real JPEGs, while the grids differed in 40 to 553 of 1 024
cells. Any smoothing resample stayed within distance 2 of 5. Nearest-neighbour
reached 18, so it is not allowed. Measured on darwin/arm64 with Go 1.27; linux
is checked in CI.

## 7. Cleanup list

### 7.0 Owner decisions (2026-10-22)

Yes to 7.1 in full and to every item in 7.2 to 7.5 as marked "My pick", with
these exceptions and additions:

- **R1** yes, 72 hours.
- **R2** dropped: the report tables hold 6k rows. **R3** stays a list only; no
  migration in this release. **R5** and 7.8 stay as they are.
- **R4** changes. Media of a group that has not accepted agreement
  `MIN_ACCEPTED_VERSION` goes neither to Claude nor to OpenRouter. Until the
  agreement is accepted the description comes from metadata, as when analysis is
  switched off (status `disabled`). Media that arrived before acceptance is not
  analysed afterwards. `CHANGELOG` records it as a behaviour change. With the
  gate on the whole path, the separate "may this group use Claude" check goes
  away: if analysis runs, Claude is allowed.
- **E6** changes. The image itself runs as uid and gid 1000, and compose has no
  `user:` line. The host `logs/` directory and `ai_audit.jsonl` already belong to
  uid 1000.
- **E2 and E3** are safe. The host `.env` overrides only values equal to the
  defaults (`QUOTE_HOUR=21`, `QUOTE_MINUTE=0`, `TIMEZONE=Europe/Kyiv`,
  `MIN_MESSAGES_FOR_AUTO_REVIEW=10`, `WEIGHT_*`, `MEDIA_CACHE_PROMPT_VERSION=v2`,
  `OPENROUTER_EVAL_REASONING_EFFORT=low`, `OPENROUTER_APP_TITLE=Quoto`).
  `ENABLE_DEVELOPERS_NOTIFY=True` with `DEVELOPER_IDS=[...]`, so alerts stay on.
  `OPENROUTER_MEDIA_MODEL` is not set, so the new default in P1 takes effect.
  Plus the startup warning for leftover names (4.3).
- **7.7** delete all four scripts and `tests/test_bench_web.py`.
- **License**: About only; the agreement text and `DOCUMENT_VERSION` stay; the
  wrong comment in `version.py` about the agreement is not carried over.
- **`adoptAlembic`** checks tables and columns (5.2).

### 7.1 Removed or merged with no change in behaviour

Not ported. Say if one of them should stay.

- `ai._parse_scores` (no caller). The `include_day_verdict=False` mode and its
  prompt (`_SCORE_PROMPT`): the only production call passes `True`.
  `require_language` (always `False`). `_eval_max_tokens` (deletes both
  arguments). Branches for audio formats other than mp3 and image types other
  than JPEG (the normaliser only produces those).
- `windows.get_open_window`, `legacy_window_from_created_at`,
  `localize_legacy_datetime`, `cutoff_time()`. `quote_status.FINAL_STATUSES`.
  `scoring.create_bar` styles other than the one used.
- `media._ensure_message_media_item` (`save_message` already inserts the row).
  `_mark_orphan_pending_messages_failed` (orphans are no longer created). The
  second id lookup in `_find_cache`. Unused arguments of `message_media_from_source`.
- Handlers: the `or_f(and_f(private, CommandStart()), private)` filter (equal to
  `private`); the `settings` argument branch; the `operation_error` block and
  five of six `status.*` labels in Details (the card only shows published quotes).
- Duplicate calls: `_chat_language_already_claimed` twice around the AI call;
  `core.touch` twice per group message (the group touch already upserts the
  person, so one call with user and chat replaces two); `is_active = true` plus a
  commit on every message (written only when the stored value is `false`).
- The second `Bot` instance used for developer alerts. Alerts still go out with
  sound, the main bot's messages stay silent.
- The unbounded `_PANEL_COMMAND_MESSAGES` map: a bounded map with expiry.
- Redaction: one redactor knows every secret. Today `scrub_secrets` misses
  `ANTHROPIC_API_KEY` and `_redact` misses `BOT_TOKEN`.
- Audit writer: errors are logged instead of swallowed; the file is pruned at
  most once an hour via temp file and rename instead of being rewritten on every
  write.
- Stats queries get tie-breakers, so equal-win authors come out in a stable order.
- Typed response parsing removes the `TypeError` hole in the Claude path.

### 7.2 Behaviour fixes (yes or no each)

Default if unanswered: keep the Python behaviour.

| ID | Change | Why | My pick |
|:--|:--|:--|:--|
| F1 | Count `message_reaction_count` updates using `total_count`. | The handler reads `reaction.count`; the type has `total_count`, so every such update raises `AttributeError` (found by reading the handler and aiogram's type definitions; not run). Anonymous reaction totals have never been counted. | yes |
| F2 | `my_chat_member` acts only in groups and supergroups. | No chat-type filter today: unblocking the bot in a private chat creates a `group_settings` row for the user id and the scheduler then posts agreement reminders into that chat. | yes |
| F3 | The `sendRichMessage` probe treats network errors, 429, 5xx and 409 as "unknown, stay on HTML". Needs the tg fix in section 8. | The code comment says so; for network errors the code does the opposite, because `TelegramNetworkError` falls into `except TelegramAPIError` (class hierarchy read from aiogram's source, not run). | yes |
| F4 | Rich mode is switched off only when the method is really gone (404), not on any `400` other than "message is not modified". | A deleted panel or "message can't be edited" currently disables rich Markdown for everyone until restart. | yes |
| F5 | A failing `getChatMember` answers the callback with an alert. | Today the update is dropped and the button spins. | yes |
| F6 | A day with no eligible message is marked done in memory; a failing group run waits 15 minutes before the next try. | Today both retry every minute for 2 hours, and each try pays for an AI call (up to about 120 per group). | yes |
| F7 | A per-group lock between the minute tick and the catch-up after accepting the agreement; the catch-up runs only if today's cutoff has passed, as its comment says. | The quote row is created after the AI call, so both can pay for it. The catch-up has no cutoff check: accepting before the cutoff publishes yesterday's window, late. | yes |
| F8 | The agreement reminder counts as done only after it was sent. | `CHANGELOG` v0.10.2 says failed reminders are retried; the code marks the day done right after the call. | yes |
| F9 | The "bot added" welcome shows the group's real cutoff. | It prints 21:00 while the row gets a minute between 21:00 and 21:10. | yes |
| F10 | The group HOME dashboard shows the real media-analysis state. | `build_group_panel` does not pass it, so the flag always reads on. | yes |
| F11 | Editing a media message keeps its stored description. | `update_message` overwrites `messages.text` with the placeholder. | yes |
| F12 | A person without a name shows `common.anonymous` in the viewer's language. Snapshots use the group language. | Hard-coded Russian `Аноним` shows in all locales. AI payloads keep `Unknown`. | yes |
| F13 | About shows "Scoring: Claude, OpenRouter as fallback". | `EVALUATOR = "OpenRouter"` has been stale since v0.11.1. | yes |
| C1 | Config checks: start fails if both AI keys are empty. | Today it logs critical and runs a bot that cannot publish. | yes |

### 7.3 Media (yes or no each)

| ID | Change | Why | My pick |
|:--|:--|:--|:--|
| M1 | A network error while downloading a file is retried like other temporary errors. | Today `TelegramNetworkError` and flood waits end as `failed` immediately, though the comment calls them temporary. | yes |
| M2 | Audio: trim only leading silence. | `silenceremove` with `stop_periods=1` cut a 5 s clip (2 s tone, 1 s pause, 2 s tone) to 2.3 s (ffmpeg 9.0.1). Any voice message with a pause is described from its first phrase only. | yes |
| M3 | Video: do not upscale below 720p; one duration cap of 3 600 s. | `scale=-2:720` turns 320×240 into 960×720 (measured). Of the two caps (3 600 and 10 800) only 10 800 applies to known durations; with the 20 MB cloud limit neither is reachable. | yes |
| M4 | Download cap of 100 MiB; larger files get status `failed` with the metadata description and are not retried. | `media.py` has no limit; `tg.DownloadToFile` needs one. The number is a guess. | yes, tell me another number if you have one |
| P1 | Media model default: `google/gemini-3.5-flash-lite`, fallback `google/gemini-3.1-flash-lite`. | See 7.6. | yes |

### 7.4 Retention and data (yes or no each)

| ID | Change | Why | My pick |
|:--|:--|:--|:--|
| R1 | Delete `messages` (and by cascade reactions and media rows) older than 72 hours. | Messages of days that never got processed stay forever: groups that never accepted the agreement, downtime over 2 h, `*_unknown` days. A window is at most 25 hours and the catch-up 2 hours, so nothing older than about 27 hours can still be used. Privacy-relevant: it is chat text. The first run on prod deletes everything stale. | yes |
| R2 | Retention for `ai_evaluation_runs` and `message_ai_scores`. | Nothing in the app reads them and nothing prunes them. | dropped: 51 and 6 008 rows on prod, not worth a job |
| R3 | A late migration that drops: `quotes.forced_by_admin` (never read or written), `quotes.context_message_ids` (written, never read), `group_settings.is_premium` (read, never written; `PREMIUM_CHAT_IDS` covers it, to be checked on prod first), `chat_id` in the two report tables (equal to `group_id`), `message_ai_scores.ai_score_raw` (always `ai_score * 10`), `media_cache.phash_algo` (written, never read). | Dead weight. Only after Go has run on prod. | list now, run later |
| R4 | Media of a group without the accepted agreement goes nowhere: metadata description only (see 7.0). | Today only the Claude path is gated by the agreement; OpenRouter still receives the media. | yes (owner changed this from "keep") |
| R5 | `media_cache` is never pruned. | Same as today. | keep |

### 7.5 Interface and operations (yes or no each)

| ID | Change | Why | My pick |
|:--|:--|:--|:--|
| S1 | Details panel drops the Reactions and Length bars; the ❤️ count stays. | Neither bar affects the pick (`total` is the AI score), yet they look like score components. `reaction_score` and `length_score` columns stay (Go writes 0). | yes |
| S2 | Drop locale keys `private.hello` and `start.group` from all 16 files. | Unused in code and tests apart from one "no trophy emoji" assertion. | yes |
| E1 | Keep `BOT_TOKEN`, `BOT_USERNAME`; rename `DB_URL` → `DATABASE_URL` and `LOGS_PATH` → `AUDIT_LOG_DIR`. | Siblings use `TELEGRAM_BOT_TOKEN`, but the host `.env` (which I may not read) has `BOT_TOKEN`. Both renamed variables are set in compose, not in the host file. | yes |
| E2 | Everything in 4.2 becomes a constant. | If the host `.env` overrides any of them (not visible to me), the override is silently ignored after the switch. | yes, once you confirm the host `.env` has no such keys |
| E3 | Drop `ENABLE_DEVELOPERS_NOTIFY`; alerts are on if `DEVELOPER_IDS` is not empty. | If the host has ids set and the flag `False` on purpose, alerts turn on. | yes, same host check |
| E4 | OpenRouter `deny` and ZDR are hard-coded. | The only effect of changing them is breaking the agreement's promise; startup already warned about it. | yes |
| E5 | Logs: JSON on stdout only. Per-module files in `/opt/stacks/quoto/logs` and the emoji/colour console format are gone; `ai_audit.jsonl` stays. | `ws04 logs quoto-bot` is the operator's tool already. | yes |
| E6 | Container: the image runs as uid and gid 1000, no `user:` in compose; `healthcheck` via `wget /healthz` in compose; `stop_grace_period: 30s`. | The host `logs/` directory and the existing `ai_audit.jsonl` belong to uid 1000. `ws04 deploy` waits for "healthy" for 90 s, so the check must exist in compose. | yes (owner: uid 1000 in the image) |
| E7 | Migrations embedded in the binary. | Siblings copy `migrations/` into the image and read `MIGRATIONS_DIR`. Embedding removes a variable and a path mismatch. A small deviation from the family. | yes |

### 7.6 The OpenRouter 404

Every OpenRouter request carries `provider.zdr = true`. OpenRouter publishes the
list of ZDR endpoints (`/api/v1/endpoints/zdr`, 1 053 endpoints on 2026-10-22).
`google/gemini-2.5-flash-lite` has none in it. With `zdr: true` OpenRouter has
nothing it may route to, which fits an hourly 404 that triggers the fallback.
`google/gemini-3.5-flash-lite` (5 endpoints) and `google/gemini-3.1-flash-lite`
(the current fallback) are in the list.

This is inferred from the list. I did not read the 404 body in the logs, and the
host `.env` may set `OPENROUTER_MEDIA_MODEL` itself, in which case changing the
default changes nothing. Whether the new model accepts video and audio in the
form Quoto sends it is checked in the test stack before release.

### 7.7 Scripts

`scripts/bench_web.py`, `bench_web_ui.html` (2 100 lines together, with
`tests/test_bench_web.py`), `compare_eval_models.py`, `try_media_model.py` import
`app.ai`, `app.media` and `app.config`, so they cannot run against Go.

Proposal: delete all four with the rest of the Python. They stay in git history
and at tag `v0.11.1`. If model comparisons on saved audit days are wanted again,
write `cmd/evalbench` later as its own change on top of `internal/ai`; it would
need its own tests and I would not carry it in this rewrite. Alternative: port
only `compare_eval_models.py` now as `cmd/evalbench`. You decide.

### 7.8 Product behaviour kept, flagged for a conscious yes

- Automatic time zone for a group by language: `ru` Moscow, `uk` Kyiv, `en` UTC,
  `de` Berlin, the other twelve Kyiv. Looks arbitrary, kept.
- Windows are not merged when a group changes its cutoff time.
- `QUOTE_MINUTE_JITTER` shifts only the minute, never the hour.

## 8. What goes into tg, and why it is protocol

Criterion from `tg/AGENTS.md`: transport, errors, types, methods belong in tg;
what a bot sends and why belongs in the bot. If two bots would want it to behave
differently, it is not tg. A method one bot needs goes through `tg.Call`.

Branch `quoto-needs`, changelog under `## Unreleased`, no version or tag from me.

| Change | Why it is protocol |
|:--|:--|
| `Update.EditedMessage`, `Update.MessageReaction`, `Update.MessageReactionCount`; types `MessageReactionUpdated`, `MessageReactionCountUpdated`, `ReactionCount`, `ReactionType` | Bot API shapes. Without them `tg` decodes these updates into an `Update` that has only `UpdateID`, silently. Asking for them with `WithAllowedUpdates` already works. |
| `Message.Sticker`, `Message.Animation`; types `Sticker`, `Animation` | Bot API shapes. |
| `Message.MigrateToChatID`, `MigrateFromChatID`, `IsTopicMessage`; `ChatMember.IsMember` | Bot API fields. `IsTopicMessage` tells a forum topic from a plain reply thread; sending `message_thread_id` in the latter fails with 400. |
| `EditMessageRichMarkdown` | Same infrastructure as `EditMessageRichHTML`; makeitMD already sends Markdown through `SendRichMarkdown`, and `limitations.md` names the asymmetry. |
| `Probe` stops counting 409, 429 and 5xx as "method exists", with a test | A transport-level bug: a busy server at startup reads as "present" and latches the rich path on. Affects every bot using `Needs.Methods`. |

Optional and cheap: a comment on `CallbackQuery.Message` that `Date == 0` marks
an inaccessible message; `GetFile` wrapping "file is too big" in `ErrFileTooLarge`
(makeitMD already checks that error); correcting `limitations.md` on `nil`
keyboards and on `allowed_updates` persisting between calls.

Stays in the bot via `tg.Call` (only Quoto uses them): `copyMessage`,
`pinChatMessage`, `getChat`, silent sending (`disable_notification`),
`cache_time` on `answerCallbackQuery` (dropped, no visible effect). Silent
sending goes into `internal/telegram` as one function. If a second bot wants it,
tg gets a client option (`WithSilent`), not variadic options on `SendMessage`,
which would change the method signatures.

Stays in the bot without tg: the `GetUpdates` loop with offset and concurrency,
`content_type` from `Raw`, UTF-16 length of link entities, the "outcome unknown"
classifier, `Preflight` with `Files: IsLocalServer()`, and a plain
`Probe("sendRichMessage")` outside `Needs.Methods` (a server without the method
is a degraded mode, not a dead bot).

## 9. Dependencies

Allowed without comment: `github.com/jackc/pgx/v5` (v5.10.0), `golang.org/x/image`,
`golang.org/x/sync`. Plus `github.com/FreshLabDev/tg` (v0.1.0 now; `replace`
below).

| Module | Used for |
|:--|:--|
| `pgx/v5` | The database driver, like siblings. |
| `x/image` | `webp` decoder (stickers arrive as WebP; the standard library cannot read them) and `draw` for the bicubic resample. |
| `x/sync` | `singleflight`: two messages with the same file at once cost one AI call, and the semaphore for the media queue. |

Nothing else. No Anthropic SDK (it may not know `fallbacks` or the beta header,
and the prompt asks for plain `net/http`), no scheduler library (a minute ticker
is enough), no JSON-schema, config or logging library. Time zones come from
`import _ "time/tzdata"`, not from the image. Tests use the standard `testing`
package with hand-written fakes.

`go.mod` carries `replace github.com/FreshLabDev/tg => ../tg-quoto` until tg is
released. **Replace it with a tag before review.** The replace breaks
`docker build` and GitHub Actions, which have no sibling directory; until the
tag exists the image builds locally with
`docker build --build-context tg=../tg-quoto` and CI for `go` cannot pass.

## 10. License

Go version: Apache-2.0, like the rest of the family. megatocha, the author of the
initial commit, agreed (owner, 2026-10-22); the owner adds the written
confirmation link, I do not invent one.

- `LICENSE` replaced with the Apache-2.0 text (identical in the sibling repos),
  `NOTICE` as in searchy: `Quoto` / `Copyright 2026 Asterfield`.
- `// SPDX-License-Identifier: Apache-2.0` in every `.go` file and `go.mod`
  (and in `.sql`, YAML and `Dockerfile`, as voicy does).
- GPL removed from README, README.ru (badge and footer) and the License section
  of `AGENTS.md`, including the sentence that Quoto is the exception in the
  family.
- The value comes from `internal/buildinfo` only.
- `CHANGELOG`: versions before the Go rewrite stay GPL-3.0, from the rewrite on
  the code is Apache-2.0.
- `MIN_ACCEPTED_VERSION` stays `1.2`; data handling does not change.

**A mismatch with the brief.** The brief says the license appears in the
agreement text and in About. In the code it appears only in the About card:
`LICENSE = "GPL-3.0"` in `app/version.py`, printed at `menu.py:203`. The
agreement imports `CONTACT` and `OPERATOR` only, and none of the 16 locale files
contains "GPL" or "licen". So the license change touches About alone, and the
agreement text and `DOCUMENT_VERSION` stay as they are. If you want a license line
in the agreement, that is a text change in 16 languages and a version bump
(without requiring re-acceptance); tell me.

## 11. Packaging, CI, deploy, rollback

- **Dockerfile.** `golang:1.26.10-alpine` builds with `CGO_ENABLED=0 -trimpath
  -ldflags "-s -w -X ..."`; runtime `alpine:3.24` + `ffmpeg` + `ca-certificates`;
  user and group 1000 (the owner of the host `logs/` directory); labels
  `org.opencontainers.image.{version,revision,source}`;
  `GOMEMLIMIT` set; no `HEALTHCHECK` (it lives in compose, as in the siblings).
  `ffprobe` comes with the `ffmpeg` package.
- **CI.** Voicy's: test (`go vet`, `go test -race`, `gofmt -l`), `govulncheck`
  pinned, a `database` job on `postgres:17-alpine` for the integration tag
  (adoption, up/down/up, equivalence), image build, `compose config` for both
  manifests. Dependabot switches `pip` to `gomod`. `release.yml` stays, with
  provenance and SBOM from voicy.
- **Compose.** `deploy/ws04/compose.yaml`: service `bot`, `container_name:
  quoto-bot`, no `build:`, image by digest, `env_file: .env`, networks `core_net`
  and `telegram_bot_api_net`, `DATABASE_URL` built as today, `./logs:/app/logs`,
  no `user:` line, wget healthcheck, `stop_grace_period: 30s`, json-file 10m×3.
  `compose.test.yaml` for the test bot: its own PostgreSQL with the core stub
  (like voicy's local compose), own token and names, so it cannot touch prod
  data. Needs a test bot token and, for Claude and OpenRouter, test keys.
- **Shutdown.** SIGTERM stops polling and new group runs. An AI call in flight
  is cancelled (its result is lost and the next start repeats it inside the 2 h
  window, same as a Python restart). A publish step already begun is allowed to
  finish, so a message is not sent and then left unfinalised (`publish_unknown`).
- **Rollback to Python is the old compose plus the old digest.** The new
  manifest sets a `/healthz` check, `DATABASE_URL` and a `postgres://` scheme,
  none of which the Python image understands; setting only `QUOTO_IMAGE` back
  would fail the health check and `ws04 deploy` would roll back again. Keep the
  last Python compose file at a git tag.
- Docs: README, README.ru, `AGENTS.md`, `docs/releases.md` rewritten for Go
  (the `.app.env` vs `.env` mismatch and "health endpoint where it has one" are
  fixed on the way). Python goes entirely: `app/`, `alembic*`, `requirements*`,
  `tests/`, `scripts/`, `healthcheck.py`, `docker-entrypoint.sh`, `main.py`,
  `__pycache__`.

## 12. Risks and how each is checked

| Risk | Check |
|:--|:--|
| Go schema differs from prod | Equivalence diff (5.3); catalog read on prod once allowed. |
| Adoption misfires, or two instances race | The four adoption cases; advisory lock. |
| Python semantics lost in translation (rounding, regexp, escaping, runes, UTF-16, key order, DST) | One named test each (section 6). |
| Old panels and links break | Golden `callback_data` strings and link vectors. |
| Streamed request body rejected by OpenRouter | Content-Length is computed; verified live in the test stack, not offline. |
| Claude streaming event shapes with the `fallbacks` beta differ from my reading | Unit tests from the documented SSE format; one live request with a real key in the test stack before release. This is the largest unknown in the port. |
| Peak memory over 200 MB (ffmpeg child included) | 50 MB video in the final image, RSS of bot plus ffmpeg sampled; `-threads` tuned from the result. |
| Idle memory over 60 MB | Same run, idle for ten minutes. |
| Permissions on `logs/` after the rewrite | The image runs as 1000:1000 (E6), the owner of the host directory. |
| `go.mod` replace leaks into a release | Replace removed before review; CI fails on a `replace` line. |
| Restart repeats a paid AI call | Same as Python; accepted. |
| Host `.env` overrides a variable that became a constant | E2 and E3 need your check. |
| Two instances during a deploy overlap | Same guard as today: unique `(group_id, quote_day)`. |
| A hidden behaviour in the Python tests I did not carry | 259 behaviours listed; every Go test names the Python one it replaces. |

Final checks, run in `golang:1.26-alpine` as well as on the local Go 1.27:
`gofmt -l .`, `go vet ./...`, `go test -race ./...`, `go build ./...`,
`docker build -t quoto:go .`, `docker compose -f deploy/ws04/compose.yaml
config`, migrations on PostgreSQL 17, memory run.

## 13. Not verified

- **Production schema.** Read by the parent session on 2026-10-22, with the
  owner's permission; I did not run any statement against prod myself. What I
  know about prod comes from that snapshot (catalog and counts, no row contents).
  Not covered: who owns schema `quoto`, and the grants of `quoto_core`.
- **Host `.env`.** I did not read it. The parent session passed me the key names
  and the non-secret values (7.0).
- **The 404 body** in `ws04 logs` (7.6).
- **aiogram runtime behaviour.** aiogram is not installed locally; its behaviour
  is read from its source and type definitions, not run.
- **Container memory of ffmpeg** (section 2): measured on macOS only.
- **pgx** `Exec` without arguments: read in v5.11.0, not in v5.9.2.
- **Docker.** Not installed on this machine, so `docker build` and `docker compose
  config` cannot be run here; they run in CI.

## 14. Phases after approval

- Phase 2, tg in `tg-quoto`: the section 8 changes with tests (`go test -race`,
  `go vet`, `gofmt`), changelog under `## Unreleased`.
- Phase 3, skeleton: `go.mod`, `cmd/bot`, `config`, `buildinfo`, `log`, `db`
  (pool, runner, interfaces), `i18n`, `domain`.
- Phase 4, port by package: `claude`+`openrouter`+`audit`; `media`;
  `scheduler`+`scoring`+`window`; `bot`+`menu`+`agreement`; `stats`.
- Phase 5, four reviews: behaviour, privacy, data and migrations, memory.
- Phase 6, packaging and documents; Python removed.

Needs the owner, outside the decisions above: a tg release (tag) before CI on
`go` can pass; a test bot token and test API keys; any deploy.
