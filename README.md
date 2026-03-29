<h1 align="center">🏆 Quoto — Quote of the Day</h1>
<div align="center">

English | [Русский](./README.ru.md)

A Telegram bot that tracks chat windows, previews quote candidates, and only publishes a **quote of the day** when the conversation was actually worth quoting.

[![GitHub Stars](https://img.shields.io/github/stars/FreshLabDev/quoto?style=for-the-badge&labelColor=1c1917&color=f59e0b&logo=data:image/svg%2bxml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0IiBmaWxsPSIjZjU5ZTBiIiBzdHJva2U9Im5vbmUiPjxwb2x5Z29uIHBvaW50cz0iMTIgMiAxNS4wOSA4LjI2IDIyIDkuMjcgMTcgMTQuMTQgMTguMTggMjEuMDIgMTIgMTcuNzcgNS44MiAyMS4wMiA3IDE0LjE0IDIgOS4yNyA4LjkxIDguMjYgMTIgMiIvPjwvc3ZnPg==)](https://github.com/FreshLabDev/quoto/stargazers)
![GitHub Repo Size](https://img.shields.io/github/repo-size/FreshLabDev/quoto?style=for-the-badge&labelColor=1c1917&color=a6da95&logo=data:image/svg%2bxml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0IiBmaWxsPSJub25lIiBzdHJva2U9IiM3ODcxNmMiIHN0cm9rZS13aWR0aD0iMiI+PHBhdGggZD0iTTIyIDE5YTIgMiAwIDAgMS0yIDJINGEyIDIgMCAwIDEtMi0yVjVhMiAyIDAgMCAxIDItMmg1bDIgM2g5YTIgMiAwIDAgMSAyIDJ6Ii8+PC9zdmc+)
[![GitHub License](https://img.shields.io/github/license/FreshLabDev/quoto?style=for-the-badge&labelColor=1c1917&color=7dc4e4&logo=data:image/svg%2bxml;base64,PD94bWwgdmVyc2lvbj0iMS4wIiBlbmNvZGluZz0idXRmLTgiPz48IS0tIFVwbG9hZGVkIHRvOiBTVkcgUmVwbywgd3d3LnN2Z3JlcG8uY29tLCBHZW5lcmF0b3I6IFNWRyBSZXBvIE1peGVyIFRvb2xzIC0tPg0KPHN2ZyB3aWR0aD0iODAwcHgiIGhlaWdodD0iODAwcHgiIHZpZXdCb3g9IjAgMCAyNCAyNCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj4NCjxwYXRoIGQ9Ik0xOSAzSDlWM0M3LjExNDM4IDMgNi4xNzE1NyAzIDUuNTg1NzkgMy41ODU3OUM1IDQuMTcxNTcgNSA1LjExNDM4IDUgN1YxMC41VjE3IiBzdHJva2U9IiMwMDAwMDAiIHN0cm9rZS13aWR0aD0iMiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+DQo8cGF0aCBkPSJNMTQgMTdWMTlDMTQgMjAuMTA0NiAxNC44OTU0IDIxIDE2IDIxVjIxQzE3LjEwNDYgMjEgMTggMjAuMTA0NiAxOCAxOVY5VjQuNUMxOCAzLjY3MTU3IDE4LjY3MTYgMyAxOS41IDNWM0MyMC4zMjg0IDMgMjEgMy42NzE1NyAyMSA0LjVWNC41QzIxIDUuMzI4NDMgMjAuMzI4NCA2IDE5LjUgNkgxOC41IiBzdHJva2U9IiMwMDAwMDAiIHN0cm9rZS13aWR0aD0iMiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+DQo8cGF0aCBkPSJNMTYgMjFINUMzLjg5NTQzIDIxIDMgMjAuMTA0NiAzIDE5VjE5QzMgMTcuODk1NCAzLjg5NTQzIDE3IDUgMTdIMTQiIHN0cm9rZT0iIzAwMDAwMCIgc3Ryb2tlLXdpZHRoPSIyIiBzdHJva2UtbGluZWNhcD0icm91bmQiIHN0cm9rZS1saW5lam9pbj0icm91bmQiLz4NCjxwYXRoIGQ9Ik01IDdIMTQiIHN0cm9rZT0iIzAwMDAwMCIgc3Ryb2tlLXdpZHRoPSIyIiBzdHJva2UtbGluZWNhcD0icm91bmQiIHN0cm9rZS1saW5lam9pbj0icm91bmQiLz4NCjxwYXRoIGQ9Ik05IDExSDE0IiBzdHJva2U9IiMwMDAwMDAiIHN0cm9rZS13aWR0aD0iMiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+DQo8L3N2Zz4=)](LICENSE)

</div>

## ✨ Features

- 🏆 **Windowed Quote Flow** — scores messages between two daily cutoff points
- 🤖 **AI Scoring** — evaluates messages for humor, wit, depth, and memorability via OpenRouter API
- 😴 **Boring-Day Detection** — if the day feels flat, the bot says so instead of forcing a weak quote
- ❤️ **Reaction Tracking** — accounts for emoji reactions from chat members
- 📏 **Text Analysis** — smart scoring based on message length and quality
- 🔎 **Admin AI Preview** — `/quote` shows the current leader for chat admins without publishing or clearing data
- 📌 **Auto-Pin** — pins the winning quote in the chat
- 📊 **Statistics** — chat stats, personal stats, top authors, and rating breakdown
- ⏰ **Scheduler** — configurable daily time for quote selection
- 🐳 **Docker Support** — easy deployment with Docker Compose

## ⚙️ How It Works

1. Add the bot to your Telegram group and grant admin rights
2. Members chat as usual — the bot silently collects messages and reactions
3. The bot collects messages inside a rolling window from the previous cutoff to the next one
4. At the scheduled time (default `21:00`), the bot evaluates the closed window
5. If there are fewer than `10` messages, the window is skipped silently
6. If there are `10+` messages, AI both scores messages and decides whether the whole day is quote-worthy
7. The best message is selected based on a **weighted scoring formula**:

| Component     | Weight | Description                                    |
| :------------ | :----- | :--------------------------------------------- |
| **Reactions** | 20%    | Normalized count of emoji reactions            |
| **AI Score**  | 70%    | LLM-based evaluation (humor, wit, quotability) |
| **Length**    | 10%    | Optimal message length bonus                   |

8. If the day is boring, the bot posts a boring-day notice with a `Details` link instead of a weak quote

## 📌 Commands

| Command    | Description                      |
| :--------- | :------------------------------- |
| `/start`   | Bot info and help                |
| `/quote`   | AI preview of the current open window for chat admins |
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
OPENROUTER_MODEL=google/gemma-3-1b-it:free
DEVELOPER_IDS=[1234567890]
QUOTE_HOUR=21
QUOTE_MINUTE=0
TIMEZONE=Europe/Berlin
MIN_MESSAGES_FOR_AUTO_REVIEW=10
```

### Running

`/quote` is an admin-only AI preview. `/publish_quote` is reserved for admins when the automatic run marked the window as boring or failed.

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
