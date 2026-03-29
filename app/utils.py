import logging
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

async def notify_developers(message: str):
    if not settings.ENABLE_DEVELOPERS_NOTIFY:
        return
    
    for dev_id in settings.DEVELOPER_IDS:
        try:
            await bot.send_message(dev_id, escape(message))
        except Exception as e:
            log.error(f"Ошибка при отправке сообщения разработчику {dev_id}: {e}")
