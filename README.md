<h1 align="center">Quoto</h1>

<p align="center"><strong>The quote of the day, out of whatever the chat wrote.</strong><br/>A Telegram group bot that reads the day and publishes one line worth keeping, or says the day had none.</p>

<p align="center">
  <a href="https://github.com/FreshLabDev/quoto/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/quoto?include_prereleases&sort=semver&style=for-the-badge&label=latest&labelColor=0f172a&color=3F3160" alt="latest version"></a>
  <a href="Dockerfile"><img src="https://img.shields.io/badge/python-3.12-3776AB?style=for-the-badge&logo=python&logoColor=white&labelColor=0f172a" alt="python version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL--3.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
  <a href="https://t.me/quototbot"><img src="https://img.shields.io/badge/telegram-%40quototbot-26A5E4?style=for-the-badge&logo=telegram&logoColor=white&labelColor=0f172a" alt="telegram bot"></a>
</p>

<p align="center">
  English | <a href="README.ru.md">Русский</a>
</p>

<p align="center">
  <a href="#using-quoto">Using Quoto</a> ·
  <a href="#how-it-works">How It Works</a> ·
  <a href="#privacy">Privacy</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#running-it">Running It</a> ·
  <a href="#docs">Docs</a>
</p>

---

## What It Does

A group chat says something memorable a few times a week, and by the next
morning it is buried under everything else. Quoto reads the day, picks the one
message worth keeping, posts it as the quote of the day and pins it.

Quoto is deliberately narrow:

| Need | Quoto approach |
|:--|:--|
| One result a day | At most one quote per group per day, at a time the group sets |
| No forced quotes | A flat day gets a short "boring day" notice instead of a weak quote |
| Quiet days stay quiet | Fewer than 10 messages and the day is skipped without a word |
| Context when it matters | Up to five surrounding messages when the quote needs setup |
| Photos, videos, voice | Media is described for the AI, so a voice note or a video can win too |
| One door | `/start` is the only command; everything else is inline buttons |

---

## Status

| Channel | Version | Meaning |
|:--|:--|:--|
| Stable | `v0.11.0` | Chat text reaches only zero-retention model endpoints |
| Next | `Unreleased` | Compatible fixes and improvements land on `dev` |

The bot is public as [@quototbot](https://t.me/quototbot). Until `v1.0.0` the
operator contract (environment, schema, `core` contract) may still change
between minor versions; see [`docs/versioning.md`](docs/versioning.md).

---

## Using Quoto

1. Add [@quototbot](https://t.me/quototbot) to a group.
2. A group admin opens the user agreement from the welcome message and accepts
   it. Until then Quoto publishes nothing.
3. Make Quoto an admin with the right to pin messages. Telegram only shows a
   bot every message in a group once it is an admin.
4. Chat as usual. At the end of the day the quote arrives on its own.

`/start` in the group opens the panel. Everyone gets chat and personal stats
and the top authors. Admins also get the settings: group language, quote time,
time zone, minimum message count, and the publishing switches for pinning,
context, the boring-day notice and media.

---

## How It Works

Quoto is one Python service. It stores messages and reactions as they arrive,
and once a minute checks whether any group's day has closed.

```text
messages + reactions -> day window (cutoff to cutoff, group time zone)
  -> media described by a vision model, cached by perceptual hash
  -> one evaluation call: score every message, judge the whole day
  -> publish and pin the quote, or post the boring-day notice, or stay silent
  -> delete the day's messages
```

The quote is chosen by the AI score. Reactions are sent along as context, and
message length is kept only for the "Details" view. A group with no saved
language gets one picked once by the same evaluation call, from the language
the chat actually writes in. Messages that contain `#quoto`, Quoto's own posts
among them, are left out of the day.

If the evaluation call fails, Quoto retries every minute for two hours after
the cutoff and then gives up on that day.

---

## Privacy

Quoto reads a whole group chat, so this part matters most.

- Message text, captions, media and author names for the day are sent to
  [OpenRouter](https://openrouter.ai) to pick the quote. Every request carries
  `data_collection: "deny"` and `zdr: true`, so OpenRouter sends it only to
  endpoints with a zero-data-retention policy that do not train on it. A model
  with no such endpoint fails over to the fallback model instead of reaching
  one that keeps the data.
- After a day is processed its messages are deleted. The published quote, its
  context and the scores stay, because the stats and the "Details" view are
  built from them.
- Every evaluation request and raw response is written to
  `logs/ai_audit.jsonl` on the server for seven days, for debugging. It holds
  real message text and is readable only by the operator.
- Bot token, OpenRouter key and database password are never logged.

The user agreement shown in the bot says the same in all sixteen languages.

---

## Configuration

The full list with comments is in [`.env.example`](.env.example). The ones you
will touch:

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `BOT_TOKEN` | yes | — | Bot token from BotFather |
| `BOT_USERNAME` | yes | — | The bot's public username, used in the links the bot builds |
| `DB_URL` | yes | — | `postgresql+asyncpg://` URL; in production the shared `core` database as `quoto_core` |
| `TELEGRAM_BOT_API_BASE_URL` | no | empty | Self-hosted Bot API server; empty means api.telegram.org, where the agreement renders as HTML instead of rich Markdown |
| `OPENROUTER_API_KEY` | yes | — | OpenRouter key |
| `OPENROUTER_EVAL_MODEL` | no | `openai/gpt-6-luna` | Model that scores the day |
| `OPENROUTER_EVAL_FALLBACK_MODEL` | no | `google/gemini-3.1-flash-lite` | Used when the evaluation model fails |
| `OPENROUTER_MEDIA_MODEL` | no | `google/gemini-2.5-flash-lite` | Model that describes photos, video and audio |
| `OPENROUTER_MEDIA_FALLBACK_MODEL` | no | `google/gemini-3.1-flash-lite` | Used when the media model fails |
| `OPENROUTER_DATA_COLLECTION` | no | `deny` | `deny` keeps every request away from providers that train on it |
| `OPENROUTER_ZDR` | no | `true` | Only endpoints with zero data retention; turning this off or setting `allow` above breaks the promise in the agreement and logs a warning at startup |
| `MEDIA_ANALYSIS_ENABLED` | no | `true` | Global switch for media description |
| `QUOTE_HOUR`, `QUOTE_MINUTE` | no | `21`, `0` | Default cutoff for a new group |
| `TIMEZONE` | no | `Europe/Kyiv` | Default time zone for a new group |
| `MIN_MESSAGES_FOR_AUTO_REVIEW` | no | `10` | Default minimum messages for a day to be judged |
| `MAX_MESSAGES_PER_DAILY_EVAL` | no | `1500` | Newest messages sent to the AI per day |
| `DEVELOPER_IDS`, `ENABLE_DEVELOPERS_NOTIFY` | no | `[]`, `false` | Telegram ids that receive operational alerts, and the switch that sends them |

---

## Running It

Local development with Docker starts a bundled PostgreSQL seeded with a copy of
the shared `core` schema (`deploy/core-init.sql`), so the foreign keys into
`core.person` and `core.chat` resolve:

```sh
cp .env.example .env
$EDITOR .env
docker compose up --build
```

The container applies `alembic upgrade head` before starting the bot. Without
Docker, use Python 3.12:

```sh
python -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/alembic upgrade head
.venv/bin/python main.py
```

Production runs from `/opt/stacks/quoto` against the shared `core-postgres`.
Nothing is built on the host; see the Deploying section of
[`docs/releases.md`](docs/releases.md).

---

## Testing

```sh
python -m pytest -q tests
docker build -t quoto:ci .
docker compose config
```

The suite mocks the database. CI also applies the migrations to a real
PostgreSQL 17, audits `requirements.txt` with `pip-audit`, and checks that every
package in `requirements.in` is pinned.

---

## Docs

| Document | Purpose |
|:--|:--|
| [Versioning](docs/versioning.md) | What the version numbers mean |
| [Release process](docs/releases.md) | Changelog, GitHub Release, deploying and rolling back |
| [Changelog](CHANGELOG.md) | What changed in each release |

---

<p align="center">
  <a href="https://github.com/FreshLabDev/quoto/releases">Releases</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="LICENSE">GPL-3.0</a>
</p>

<p align="center">
  Quoto is open source software by Asterfield.
</p>
