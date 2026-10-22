<h1 align="center">Quoto</h1>

<p align="center"><strong>The quote of the day, out of whatever the chat wrote.</strong><br/>A Telegram group bot that reads the day and publishes one line worth keeping, or says the day had none.</p>

<p align="center">
  <a href="https://github.com/FreshLabDev/quoto/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/quoto?include_prereleases&sort=semver&style=for-the-badge&label=latest&labelColor=0f172a&color=3F3160" alt="latest version"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.26-00ADD8?style=for-the-badge&logo=go&logoColor=white&labelColor=0f172a" alt="go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
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
| Stable | `v0.11.1` | Python. Claude scores the day, OpenRouter is the fallback |
| Next | `Unreleased` | The Go rewrite: same product, one binary, a fraction of the memory |

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

Quoto is one Go service. It stores messages and reactions as they arrive,
and once a minute checks whether any group's day has closed.

```text
messages + reactions -> day window (cutoff to cutoff, group time zone)
  -> images described by Claude, video and audio by OpenRouter, cached by perceptual hash
  -> one evaluation call to Claude: score every message, judge the whole day
  -> publish and pin the quote, or post the boring-day notice, or stay silent
  -> delete the day's messages
```

The quote is chosen by the AI score. Reactions are sent along as context, and
message length is kept only for the "Details" view. A group with no saved
language gets one picked once by the same evaluation call, from the language
the chat actually writes in. Messages that contain `#quoto`, Quoto's own posts
among them, are left out of the day.

Claude's safety classifiers now and then decline a day of ordinary banter, so
every request opts into Anthropic's server-side fallback: the API re-runs a
declined day in the same call on the model Anthropic picks for that kind of
refusal. For a cyber decline on Opus 5.5 that is Claude Opus 4.8. If that model
refuses too, Claude runs out of credit, is down or answers with something that
does not parse, the same day goes to OpenRouter's evaluation model and then to
its fallback. If the whole chain fails, Quoto retries every minute for two
hours after the cutoff and then gives up on that day.

---

## Privacy

Quoto reads a whole group chat, so this part matters most.

- Message text, captions, media descriptions and author names for the day go
  to Claude through the Anthropic API to pick the quote, and photos, images
  and stickers go there to be described. Anthropic does not train on API data
  and deletes it within 30 days; it keeps longer only what its safety systems
  flag as a policy violation.
- Video, video messages, voice messages and audio go to
  [OpenRouter](https://openrouter.ai), which Claude cannot take, and so does
  anything Claude fails on. Every OpenRouter request carries
  `data_collection: "deny"` and `zdr: true`, so OpenRouter sends it only to
  endpoints with a zero-data-retention policy that do not train on it. A model
  with no such endpoint fails over to the fallback model instead of reaching
  one that keeps the data.
- Nothing of a group reaches Anthropic or OpenRouter before an admin accepts
  version 1.2 of the agreement, the first that names them. Until then Quoto
  publishes nothing, and a photo, video or voice message is described from its
  metadata only. Media that arrived before the
  agreement was accepted is not analysed afterwards.
- After a day is processed its messages are deleted. Messages of a day that was
  never processed (a group that did not accept the agreement, a long outage) are
  deleted once they are 72 hours old. The published quote, its context and the
  scores stay, because the stats and the "Details" view are built from them. The
  copies of author names, text, captions and media descriptions that the
  evaluation report keeps next to the scores are blanked at the same age.
- Every evaluation request and raw response is written to
  `logs/ai_audit.jsonl` on the server for seven days, for debugging. It holds
  real message text and is readable only by the operator.
- Bot token, Anthropic and OpenRouter keys and database password are never logged.

The user agreement shown in the bot says the same in all sixteen languages.

---

## Configuration

The full list with comments is in [`.env.example`](.env.example). The bot reads
23 variables; everything nobody tunes (the quote time default, the media
pipeline limits, the OpenRouter privacy settings) is a constant in the code.
Variables from the Python version that no longer do anything are listed by name
in a startup warning. The process stops at start on a value it cannot use.

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `BOT_TOKEN` | yes | — | Bot token from BotFather. Also signs the quote deep links: changing it invalidates links already posted. |
| `BOT_USERNAME` | yes | — | The bot's public username, used in the links the bot builds |
| `DATABASE_URL` | yes | — | `postgres://` URL; in production the shared `core` database as `quoto_core`. The Python form `postgresql+asyncpg://` is rejected. |
| `TELEGRAM_BOT_API_BASE_URL` | no | empty | Self-hosted Bot API server; empty means api.telegram.org, where the agreement renders as HTML instead of rich Markdown |
| `BOT_API_FILES_DIR` | with the line above | empty | The Bot API server's data directory as this process sees it |
| `ANTHROPIC_API_KEY` | one of the two keys | empty | Anthropic API key; empty sends scoring and images to OpenRouter only |
| `ANTHROPIC_EVAL_MODEL` | no | `claude-opus-5-5` | Model that scores the day; set to an empty string to skip Claude |
| `ANTHROPIC_EVAL_EFFORT` | no | `medium` | `low` to `max`: how much the model thinks before it answers |
| `ANTHROPIC_MEDIA_MODEL` | no | `claude-sonnet-5-5` | Model that describes photos, images and stickers; empty skips Claude |
| `ANTHROPIC_MEDIA_EFFORT` | no | `medium` | The same, for image descriptions |
| `OPENROUTER_API_KEY` | one of the two keys | — | OpenRouter key: video, audio, and the fallback for everything else |
| `OPENROUTER_EVAL_MODEL` | no | `openai/gpt-6-luna` | Scores the day when Claude fails |
| `OPENROUTER_EVAL_FALLBACK_MODEL` | no | `google/gemini-3.1-flash-lite` | Used when the evaluation model fails |
| `OPENROUTER_EVAL_REASONING_EFFORT` | no | `low` | `none` to `xhigh` |
| `OPENROUTER_MEDIA_MODEL` | no | `google/gemini-3.5-flash-lite` | Describes video and audio, and images when Claude fails. Needs a zero-data-retention endpoint on OpenRouter. |
| `OPENROUTER_MEDIA_FALLBACK_MODEL` | no | `google/gemini-3.1-flash-lite` | Used when the media model fails |
| `OPENROUTER_MEDIA_REASONING_EFFORT` | no | `medium` | `none` to `xhigh` |
| `MEDIA_ANALYSIS_ENABLED` | no | `true` | Global switch for media description |
| `DEVELOPER_IDS` | no | empty | Telegram ids that receive operational alerts; empty turns alerts off. JSON list or comma list. |
| `PREMIUM_CHAT_IDS` | no | empty | Groups exempt from the daily cap of 1 500 messages |
| `AUDIT_LOG_DIR` | no | `logs` | Where `ai_audit.jsonl` goes |
| `HTTP_ADDR` | no | `:8080` | Where `/healthz` listens |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` |

---

## Running It

The test stack brings its own PostgreSQL, seeded with a copy of the shared `core`
schema so the foreign keys into `core.person` and `core.chat` resolve. It needs a
separate bot token and API keys, and shares nothing with production:

```sh
cp .env.example .env
$EDITOR .env
docker compose -f deploy/ws04/compose.test.yaml up --build
```

The service applies its migrations at start. Without Docker, with Go 1.26 or
newer and a database that already has the `core` schema and a `quoto_core` role
(`internal/db/dbtest/core_stub.sql` is a copy for development):

```sh
go run ./cmd/bot
```

Production runs from `/opt/stacks/quoto` against the shared `core-postgres`.
Nothing is built on the host; see the Deploying section of
[`docs/releases.md`](docs/releases.md).

---

## Testing

```sh
gofmt -l .
go vet ./...
go test -race ./...
docker build -t quoto:ci .
```

The database tests carry the `integration` tag and need a PostgreSQL 17 they may
wipe. Each test run drops and recreates schema `quoto` in it, so point the
variable at a throwaway database and run the packages one at a time:

```sh
QUOTO_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:5432/quoto_test \
  go test -tags=integration -p 1 ./...
```

CI runs the same against `postgres:17-alpine`, scans for known vulnerabilities
with `govulncheck`, builds the image and validates both compose files.

---

## Docs

| Document | Purpose |
|:--|:--|
| [Versioning](docs/versioning.md) | What the version numbers mean |
| [Release process](docs/releases.md) | Changelog, GitHub Release, deploying and rolling back |
| [Rewrite in Go](docs/go-rewrite.md) | Why the service was rewritten, what stayed compatible, what was removed |
| [Changelog](CHANGELOG.md) | What changed in each release |

---

<p align="center">
  <a href="https://github.com/FreshLabDev/quoto/releases">Releases</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="LICENSE">Apache-2.0</a>
</p>

<p align="center">
  Quoto is open source software by Asterfield.
</p>
