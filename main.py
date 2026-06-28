import logging
import asyncio
from aiogram import Bot, Dispatcher, types
from aiogram.enums import ParseMode
from aiogram.client.default import DefaultBotProperties

from app import config, utils, db, handlers, scheduler

bot = Bot(
    token=config.settings.BOT_TOKEN,
    default=DefaultBotProperties(parse_mode=ParseMode.HTML, disable_notification=True, link_preview_is_disabled=True)
)

log = config.setup_logging(logging.getLogger(__name__))


async def on_update_error(event: types.ErrorEvent) -> bool:
    """Catch any unhandled error so a single bad update can't kill polling."""
    exc = event.exception
    log.error(f"❌ Необработанная ошибка обновления: {exc}", exc_info=exc)
    await utils.notify_developers(
        f"❌ Update error: {type(exc).__name__}: {exc}",
        dedupe_key=f"update:{type(exc).__name__}",
    )
    return True


async def main():
    config.validate_runtime()

    dp = Dispatcher()
    dp.include_router(handlers.router)
    dp.errors.register(on_update_error)

    await db.init_db()
    await bot.set_my_commands(
        [
            types.BotCommand(command="start", description="Open Quoto menu"),
            types.BotCommand(command="privacy", description="User agreement & privacy"),
        ]
    )

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
