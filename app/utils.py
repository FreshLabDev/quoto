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


def _scrub_secrets(text: str) -> str:
    cleaned = text
    for secret in (settings.BOT_TOKEN, settings.OPENROUTER_API_KEY):
        if secret:
            cleaned = cleaned.replace(secret, "***")
    cleaned = _BEARER_RE.sub("Bearer ***", cleaned)
    cleaned = _OPENROUTER_KEY_RE.sub("sk-or-***", cleaned)
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
