import hashlib
import hmac
import logging
import re
import time
from html import escape
from aiogram import Bot
from aiogram.enums import ParseMode
from aiogram.client.default import DefaultBotProperties
from aiogram.client.session.aiohttp import AiohttpSession
from aiogram.client.telegram import TelegramAPIServer

from .config import settings, setup_logging


def build_session() -> AiohttpSession | None:
    """Point a bot at the self-hosted Bot API server when one is configured.

    None means aiogram's default, api.telegram.org. The base URL is what
    decides whether Bot API 10.3 methods exist at all, so every Bot instance
    in the process has to be built through here.
    """
    base = settings.TELEGRAM_BOT_API_BASE_URL
    if not base:
        return None
    # is_local matters and is not the default. Our server runs with --local, so
    # getFile answers with an absolute path on the server's own disk and the
    # /file/bot<token>/... route returns 404 by design. Left at aiogram's
    # default, download_file would treat that path as a URL suffix and fetch a
    # 404 for every photo, video, circle and voice note -- which is not an
    # error quoto retries or skips, it is a failed analysis stored as such, and
    # the only sign would be a duller quote a day later.
    return AiohttpSession(api=TelegramAPIServer.from_base(base, is_local=True))


bot = Bot(
    token=settings.BOT_TOKEN,
    session=build_session(),
    default=DefaultBotProperties(parse_mode=ParseMode.HTML),
)

log = setup_logging(logging.getLogger(__name__))

# Same error within this window notifies developers only once (avoids alert
# storms — e.g. a DNS outage firing every poll).
_NOTIFY_COOLDOWN_SECONDS = 600
_last_notified: dict[str, float] = {}

_BEARER_RE = re.compile(r"(?i)bearer\s+[A-Za-z0-9._\-]+")
_OPENROUTER_KEY_RE = re.compile(r"sk-or-[A-Za-z0-9._\-]+")
_DB_URL_PASSWORD_RE = re.compile(r"(?i)(://[^:/\s]+:)([^@\s]+)(@)")


def quote_start_payload(quote_id: int) -> str:
    """Create a short tamper-resistant Telegram deep-link payload."""
    body = str(int(quote_id))
    signature = hmac.new(
        settings.BOT_TOKEN.encode("utf-8"),
        f"quote:{body}".encode("utf-8"),
        hashlib.sha256,
    ).hexdigest()[:16]
    return f"quote_{body}_{signature}"


def parse_quote_start_payload(payload: str) -> int | None:
    parts = payload.split("_")
    if len(parts) != 3 or parts[0] != "quote" or not parts[1].isdigit():
        return None
    expected = quote_start_payload(int(parts[1])).split("_")[-1]
    if not hmac.compare_digest(parts[2], expected):
        return None
    return int(parts[1])


def parse_legacy_quote_start_payload(payload: str) -> int | None:
    """Parse pre-0.10.2 links so they can be checked with chat membership."""
    parts = payload.split("_")
    if len(parts) != 2 or parts[0] != "quote" or not parts[1].isdigit():
        return None
    return int(parts[1])


def scrub_secrets(text: str) -> str:
    """Remove the bot token, API keys and database passwords from a string.

    Used wherever an exception's text reaches a log line or the database: a
    transport error carries the request URL, and a Bot API URL carries the
    token.
    """
    cleaned = text
    for secret in (settings.BOT_TOKEN, settings.OPENROUTER_API_KEY):
        if secret:
            cleaned = cleaned.replace(secret, "***")
    cleaned = _BEARER_RE.sub("Bearer ***", cleaned)
    cleaned = _OPENROUTER_KEY_RE.sub("sk-or-***", cleaned)
    cleaned = _DB_URL_PASSWORD_RE.sub(r"\1***\3", cleaned)
    return cleaned


async def notify_developers(message: str, *, dedupe_key: str | None = None) -> None:
    if not settings.ENABLE_DEVELOPERS_NOTIFY:
        return

    key = dedupe_key or message
    now = time.monotonic()
    last = _last_notified.get(key)
    if last is not None and now - last < _NOTIFY_COOLDOWN_SECONDS:
        return
    _last_notified[key] = now
    if len(_last_notified) > 512:
        for stale_key, seen_at in list(_last_notified.items()):
            if now - seen_at > _NOTIFY_COOLDOWN_SECONDS:
                _last_notified.pop(stale_key, None)

    safe = escape(scrub_secrets(message))
    for dev_id in settings.DEVELOPER_IDS:
        try:
            await bot.send_message(dev_id, safe)
        except Exception as e:
            log.error(f"Ошибка при отправке сообщения разработчику {dev_id}: {e}")
