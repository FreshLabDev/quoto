import logging
import asyncio
from aiogram import Bot, Dispatcher, types
from aiogram.enums import ParseMode
from aiogram.client.default import DefaultBotProperties

from app import config, utils, db, handlers, i18n, richmd, scheduler
from app.version import VERSION

bot = Bot(
    token=config.settings.BOT_TOKEN,
    session=utils.build_session(),
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


async def register_commands(bot: Bot) -> None:
    """Publish the command menu per chat type and per interface language.

    `/start` is the only registered command in either scope -- the agreement,
    stats, settings and About are tabs inside the panel it opens -- but it
    opens a different screen in a private chat than in a group, so the two
    scopes describe it differently. Telegram picks the list matching the
    client's language; the language-less lists are the English fallback for
    every other client.
    """
    languages: list[str | None] = [None, *i18n.SUPPORTED_LANGUAGES]
    for language in languages:
        text = language or i18n.DEFAULT_LANGUAGE
        private = [
            types.BotCommand(command="start", description=i18n.t(text, "command.start_private"))
        ]
        group = [
            types.BotCommand(command="start", description=i18n.t(text, "command.start_group"))
        ]
        try:
            if language is None:
                # Also the default scope, so the pre-scope command list (which
                # still advertises /privacy) is overwritten rather than left
                # behind for any chat type the scoped lists do not cover.
                await bot.set_my_commands(private, scope=types.BotCommandScopeDefault())
            await bot.set_my_commands(
                private,
                scope=types.BotCommandScopeAllPrivateChats(),
                language_code=language,
            )
            await bot.set_my_commands(
                group,
                scope=types.BotCommandScopeAllGroupChats(),
                language_code=language,
            )
        except Exception as exc:
            log.warning(f"⚠️ Не удалось зарегистрировать команды ({language or 'default'}): {exc}")


async def main():
    config.validate_runtime()
    log.info(f"📦 Версия Quoto: {VERSION}")

    dp = Dispatcher()
    dp.include_router(handlers.router)
    dp.errors.register(on_update_error)

    await db.init_db()
    await richmd.preflight(bot)
    await register_commands(bot)

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
