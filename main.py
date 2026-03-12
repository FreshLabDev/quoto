import logging
import asyncio
from aiogram import Bot, Dispatcher
from aiogram.enums import ParseMode
from aiogram.client.default import DefaultBotProperties

from app import config, utils, db, handlers, scheduler

bot = Bot(
    token=config.settings.BOT_TOKEN,
    default=DefaultBotProperties(parse_mode=ParseMode.HTML, disable_notification=True, link_preview_is_disabled=True)
)

log = config.setup_logging(logging.getLogger(__name__))

async def main():
    dp = Dispatcher()
    dp.include_router(handlers.router)

    await db.init_db()

    # Инициализация планировщика
    sched = scheduler.setup_scheduler(bot)
    sched.start()

    try:
        log.info("🟢 Бот запущен!")
        await dp.start_polling(bot)
    finally:
        sched.shutdown(wait=False)
        await bot.session.close()
        log.info("🔴 Бот остановлен!")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        log.info("🔴 Скрипт остановлен")
    except Exception as e:
        log.critical(f"Критическая ошибка: {e}")
        asyncio.run(utils.notify_developers(f"❌ Критическая ошибка в main.py: {e}"))