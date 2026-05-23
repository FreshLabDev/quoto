<h1 align="center">🏆 Quoto — Quote of the Day</h1>
<div align="center">

English | [Русский](./README.ru.md)

A Telegram bot that tracks quote days, previews quote candidates, and only publishes a **quote of the day** when the conversation was actually worth quoting.

[![GitHub Stars](https://img.shields.io/github/stars/FreshLabDev/quoto?style=for-the-badge&labelColor=1c1917&color=f59e0b&logo=data:image/svg%2bxml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0IiBmaWxsPSIjZjU5ZTBiIiBzdHJva2U9Im5vbmUiPjxwb2x5Z29uIHBvaW50cz0iMTIgMiAxNS4wOSA4LjI2IDIyIDkuMjcgMTcgMTQuMTQgMTguMTggMjEuMDIgMTIgMTcuNzcgNS44MiAyMS4wMiA3IDE0LjE0IDIgOS4yNyA4LjkxIDguMjYgMTIgMiIvPjwvc3ZnPg==)](https://github.com/FreshLabDev/quoto/stargazers)
![GitHub Repo Size](https://img.shields.io/github/repo-size/FreshLabDev/quoto?style=for-the-badge&labelColor=1c1917&color=a6da95&logo=data:image/svg%2bxml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0IiBmaWxsPSJub25lIiBzdHJva2U9IiM3ODcxNmMiIHN0cm9rZS13aWR0aD0iMiI+PHBhdGggZD0iTTIyIDE5YTIgMiAwIDAgMS0yIDJINGEyIDIgMCAwIDEtMi0yVjVhMiAyIDAgMCAxIDItMmg1bDIgM2g5YTIgMiAwIDAgMSAyIDJ6Ii8+PC9zdmc+)
[![GitHub License](https://img.shields.io/github/license/FreshLabDev/quoto?style=for-the-badge&labelColor=1c1917&color=7dc4e4&logo=data:image/svg%2bxml;base64,PD94bWwgdmVyc2lvbj0iMS4wIiBlbmNvZGluZz0idXRmLTgiPz48IS0tIFVwbG9hZGVkIHRvOiBTVkcgUmVwbywgd3d3LnN2Z3JlcG8uY29tLCBHZW5lcmF0b3I6IFNWRyBSZXBvIE1peGVyIFRvb2xzIC0tPg0KPHN2ZyB3aWR0aD0iODAwcHgiIGhlaWdodD0iODAwcHgiIHZpZXdCb3g9IjAgMCAyNCAyNCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj4NCjxwYXRoIGQ9Ik0xOSAzSDlWM0M3LjExNDM4IDMgNi4xNzE1NyAzIDUuNTg1NzkgMy41ODU3OUM1IDQuMTcxNTcgNSA1LjExNDM4IDUgN1YxMC41VjE3IiBzdHJva2U9IiMwMDAwMDAiIHN0cm9rZS13aWR0aD0iMiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+DQo8cGF0aCBkPSJNMTQgMTdWMTlDMTQgMjAuMTA0NiAxNC44OTU0IDIxIDE2IDIxVjIxQzE3LjEwNDYgMjEgMTggMjAuMTA0NiAxOCAxOVY5VjQuNUMxOCAzLjY3MTU3IDE4LjY3MTYgMyAxOS41IDNWM0MyMC4zMjg0IDMgMjEgMy42NzE1NyAyMSA0LjVWNC41QzIxIDUuMzI4NDMgMjAuMzI4NCA2IDE5LjUgNkgxOC41IiBzdHJva2U9IiMwMDAwMDAiIHN0cm9rZS13aWR0aD0iMiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+DQo8cGF0aCBkPSJNMTYgMjFINUMzLjg5NTQzIDIxIDMgMjAuMTA0NiAzIDE5VjE5QzMgMTcuODk1NCAzLjg5NTQzIDE3IDUgMTdIMTQiIHN0cm9rZT0iIzAwMDAwMCIgc3Ryb2tlLXdpZHRoPSIyIiBzdHJva2UtbGluZWNhcD0icm91bmQiIHN0cm9rZS1saW5lam9pbj0icm91bmQiLz4NCjxwYXRoIGQ9Ik01IDdIMTQiIHN0cm9rZT0iIzAwMDAwMCIgc3Ryb2tlLXdpZHRoPSIyIiBzdHJva2UtbGluZWNhcD0icm91bmQiIHN0cm9rZS1saW5lam9pbj0icm91bmQiLz4NCjxwYXRoIGQ9Ik05IDExSDE0IiBzdHJva2U9IiMwMDAwMDAiIHN0cm9rZS13aWR0aD0iMiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+DQo8L3N2Zz4=)](LICENSE)

</div>

## ✨ Features

- 🏆 **Quote of the Day Flow** — scores messages between two daily cutoff points
- 🤖 **AI Scoring** — evaluates messages for humor, wit, depth, and memorability via OpenRouter API
- 🧵 **Optional Quote Context** — adds up to 5 consecutive or reply-linked messages only when needed
- 😴 **Boring-Day Detection** — if the day feels flat, the bot says so instead of forcing a weak quote
- ❤️ **Reaction Context** — sends emoji reactions to AI as context for each message
- 🧾 **AI Audit Log** — writes the exact OpenRouter request body and raw response to `logs/ai_audit.jsonl` for 7 days
- 📏 **Text Context** — stores message length signals for transparent details
- 🔎 **Admin AI Preview** — `/quote` shows the current leader for chat admins without publishing or clearing data
- 📌 **Auto-Pin** — pins the winning quote in the chat
- 📊 **Statistics** — chat stats, personal stats, top authors, and rating breakdown
- ⏰ **Scheduler** — configurable daily time for quote selection
- 🐳 **Docker Support** — easy deployment with Docker Compose

## ⚙️ How It Works

1. Add the bot to your Telegram group and grant admin rights
2. Members chat as usual — the bot silently collects messages and reactions
3. The bot collects messages for the day from the previous cutoff to the next one
4. At the scheduled time (default `21:00`), the bot evaluates the closed day
5. If there are fewer than `10` messages, the day is skipped silently
6. If there are `10+` messages, AI both scores messages and decides whether the whole day is quote-worthy
7. The best message is selected by the **AI score**; reactions and length are only context:

| Component     | Weight | Description                                    |
| :------------ | :----- | :--------------------------------------------- |
| **AI Score**  | 100%   | LLM-based evaluation with reaction context     |
| **Reactions** | Context | Emoji reactions sent to AI when present       |
| **Length**    | Context | Stored for transparent quote details          |

8. If the quote needs setup, AI may attach a validated consecutive/reply-linked context block of up to `5` messages
9. If the day is boring, the bot posts a boring-day notice with a `Details` link instead of a weak quote

## 📌 Commands

| Command    | Description                      |
| :--------- | :------------------------------- |
| `/start`   | Bot info and help                |
| `/quote`   | AI preview of the current day for chat admins |
| `/publish_quote` | Admin-only override for boring-day / failed runs |
| `/stats`   | Chat statistics and top authors  |
| `/mystats` | Your personal statistics         |

## 🚀 Quick Start

### Prerequisites

- Python 3.10+
- PostgreSQL
- Docker (optional)

### Local Installation

```bash
# 1. Clone the repository
git clone https://github.com/FreshLabDev/quoto.git
cd quoto

# 2. Create virtual environment
python -m venv venv
source venv/bin/activate  # Linux/Mac
. venv\Scripts\activate  # Windows

# 3. Install dependencies
pip install -r requirements.txt

# 4. Apply database migrations
alembic upgrade head
```

### Configuration

Create a `.env` file in the root directory (see `.env.example`):

```env
BOT_TOKEN=your_telegram_bot_token
BOT_USERNAME=your_bot_username
DB_URL=postgresql+asyncpg://user:password@localhost:5432/dbname
OPENROUTER_API_KEY=sk-or-v1-your-key-here
OPENROUTER_EVAL_MODEL=google/gemini-3.5-flash
OPENROUTER_EVAL_REASONING_EFFORT=medium
OPENROUTER_EVAL_MAX_TOKENS=32000
OPENROUTER_MEDIA_MODEL=google/gemini-3.1-flash-lite
OPENROUTER_MEDIA_REASONING_EFFORT=medium
MEDIA_ANALYSIS_ENABLED=True
MEDIA_PHASH_DISTANCE=5
MEDIA_CACHE_PROMPT_VERSION=v1
MEDIA_IMAGE_MAX_SIDE=1280
MEDIA_IMAGE_QUALITY=82
MEDIA_VIDEO_MAX_SECONDS=3600
MEDIA_VIDEO_LOW_RES_MAX_SECONDS=10800
MEDIA_VIDEO_MAX_HEIGHT=720
MEDIA_VIDEO_CRF=30
MEDIA_VIDEO_FPS=12
MEDIA_AUDIO_BITRATE=64k
MEDIA_AUDIO_SAMPLE_RATE=24000
MEDIA_COMMAND_TIMEOUT_SECONDS=300
DEVELOPER_IDS=[1234567890]
QUOTE_HOUR=21
QUOTE_MINUTE=0
TIMEZONE=Europe/Kyiv
MIN_MESSAGES_FOR_AUTO_REVIEW=10
WEIGHT_REACTIONS=0.0
WEIGHT_AI=1.0
WEIGHT_LENGTH=0.0
```

### Running

`/quote` is an admin-only AI preview. `/publish_quote` is reserved for admins when the automatic run marked the day as boring or failed.

```bash
python main.py
```

## 🐳 Docker Support

You can easily run the bot using Docker Compose:

```bash
docker-compose up -d --build
```

## 🛠️ Tech Stack

| Layer           | Technology                      |
| :-------------- | :------------------------------ |
| **Framework**   | Aiogram 3                       |
| **Database**    | PostgreSQL + SQLAlchemy (Async) |
| **Validation**  | Pydantic                        |
| **AI**          | OpenRouter API (any LLM)        |
| **Scheduler**   | APScheduler                     |
| **HTTP Client** | HTTPX                           |

## 📂 Project Structure

```
quoto/
├── app/
│   ├── ai.py           # OpenRouter AI integration & message evaluation
│   ├── config.py       # Settings, logging, environment variables
│   ├── core.py         # Core business logic (users, groups, messages)
│   ├── db.py           # Database session & initialization
│   ├── handlers.py     # Telegram bot handlers & commands
│   ├── models.py       # SQLAlchemy models (User, Group, Message, Quote)
│   ├── scheduler.py    # APScheduler jobs & quote of the day pipeline
│   ├── scoring.py      # Scoring engine & best quote selection
│   └── utils.py        # Utility functions
├── docker-compose.yml
├── Dockerfile
├── main.py             # Entry point
├── requirements.in
├── requirements.txt
└── .env.example
```

## 🤝 Contributing

Contributions are welcome! Feel free to:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## ©️ License

GNUv3 License — see [LICENSE](./LICENSE) file for details

<div align="center">

## 👤 Author

Created with ❤️ by [FreshLabDev](https://github.com/FreshLabDev)

<b>⭐ Add a star to my project!</b> <br>
![star](https://github.com/user-attachments/assets/cc66e612-3b0f-4232-9467-e246d2d30f90)<br>

</div>
