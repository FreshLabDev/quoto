import hashlib
import hmac
import logging
import re
import time
from html import escape
from aiogram import Bot
from aiogram.enums import ParseMode
from aiogram.client.default import DefaultBotProperties

from .config import settings, setup_logging


bot = Bot(
    token=settings.BOT_TOKEN,
    default=DefaultBotProperties(parse_mode=ParseMode.HTML)
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


def _scrub_secrets(text: str) -> str:
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

    safe = escape(_scrub_secrets(message))
    for dev_id in settings.DEVELOPER_IDS:
        try:
            await bot.send_message(dev_id, safe)
        except Exception as e:
            log.error(f"Ошибка при отправке сообщения разработчику {dev_id}: {e}")
