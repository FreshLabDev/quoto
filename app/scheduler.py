import logging
from datetime import timedelta

from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.cron import CronTrigger
from aiogram import Bot
from sqlalchemy import select

from . import core, scoring
from .config import settings, setup_logging
from .db import SessionLocal
from .models import Group, Quote
from .quote_status import (
    MANUAL_PUBLISHABLE_STATUSES,
    STATUS_BORING_NOTICE_FAILED,
    STATUS_BORING_NOTICE_UNKNOWN,
    STATUS_NOTIFYING_BORING,
    STATUS_PUBLISHED,
    STATUS_PUBLISH_FAILED,
    STATUS_PUBLISH_UNKNOWN,
    STATUS_PUBLISHING,
    STATUS_SKIPPED_BORING,
)
from .windows import QuoteWindow, get_closed_window, utc_now
from .windows import quote_timezone

log = setup_logging(logging.getLogger(__name__))

_STALE_QUOTE_AFTER = timedelta(minutes=15)


async def quote_of_the_day_job(bot: Bot) -> None:
    window = get_closed_window()
    log.info(
        f"⏰ Запуск выбора цитаты дня за окно "
        f"{window.start_local.isoformat()} -> {window.end_local.isoformat()}"
    )

    async with SessionLocal() as session:
        result = await session.execute(select(Group))
        groups = result.scalars().all()

    if not groups:
        log.info("📭 Нет групп для обработки")
        return

    for group in groups:
        try:
            await _process_group(bot, group, window)
        except Exception as e:
            log.error(f"❌ Ошибка при обработке группы {group.chat_id}: {e}")


async def _process_group(bot: Bot, group: Group, window: QuoteWindow | None = None) -> None:
    window = window or get_closed_window()
    log.debug(f"{group.chat_id} | ⏳ Обработка окна {window.quote_day} для группы {group.name}")

    existing = await core.get_quote_for_day(group.id, window.quote_day)
    if existing:
        if await _recover_stale_quote(existing):
            existing = await core.get_quote_for_day(group.id, window.quote_day)

        if existing and existing.decision_status in MANUAL_PUBLISHABLE_STATUSES:
            log.info(f"{group.chat_id} | ⏭️ Окно {window.quote_day} ждёт ручного решения ({existing.decision_status})")
            return
        if existing:
            log.info(f"{group.chat_id} | ⏭️ Окно {window.quote_day} уже обработано ({existing.decision_status})")
            return

    evaluation = await scoring.pick_best_quote(
        group.chat_id,
        window,
        include_day_verdict=True,
        day_verdict_min_messages=settings.MIN_MESSAGES_FOR_AUTO_REVIEW,
    )

    if evaluation.message_count == 0:
        log.info(f"{group.chat_id} | 📭 Окно {window.quote_day} пустое")
        return

    if evaluation.message_count < settings.MIN_MESSAGES_FOR_AUTO_REVIEW:
        deleted = await core.clear_window_messages(group.chat_id, window)
        log.info(
            f"{group.chat_id} | 🤫 Окно {window.quote_day} пропущено: "
            f"сообщений {evaluation.message_count} < {settings.MIN_MESSAGES_FOR_AUTO_REVIEW}. "
            f"Очищено {deleted} сообщений."
        )
        return

    if not evaluation.best_message:
        log.warning(f"{group.chat_id} | ⚠️ Не удалось выбрать лидера окна {window.quote_day}")
        return

    if evaluation.day_verdict_error:
        quote, created = await core.create_quote_record(
            group=group,
            best_message=evaluation.best_message,
            breakdown=evaluation.breakdown,
            window=window,
            decision_status=STATUS_PUBLISH_FAILED,
            operation_error=evaluation.day_verdict_error,
        )
        if created:
            log.error(
                f"{group.chat_id} | ⚠️ AI verdict для окна {window.quote_day} невалиден: "
                f"{evaluation.day_verdict_error}"
            )
        else:
            log.info(f"{group.chat_id} | ⏭️ Окно {window.quote_day} уже забрал другой воркер")
        return

    author_name = evaluation.best_message.author.name if evaluation.best_message.author else "Аноним"

    if evaluation.should_publish:
        quote, created = await core.create_quote_record(
            group=group,
            best_message=evaluation.best_message,
            breakdown=evaluation.breakdown,
            window=window,
            decision_status=STATUS_PUBLISHING,
            decision_reason=evaluation.day_reason_text or None,
        )
        if not created:
            log.info(f"{group.chat_id} | ⏭️ Окно {window.quote_day} уже забрал другой воркер")
            return
        await _publish_quote_message(
            bot=bot,
            group=group,
            quote=quote,
            author_name=author_name,
            breakdown=evaluation.breakdown,
            forced_by_admin=False,
            clear_window_after=True,
        )
        return

    quote, created = await core.create_quote_record(
        group=group,
        best_message=evaluation.best_message,
        breakdown=evaluation.breakdown,
        window=window,
        decision_status=STATUS_NOTIFYING_BORING,
        decision_reason=evaluation.day_reason_text or None,
    )
    if not created:
        log.info(f"{group.chat_id} | ⏭️ Окно {window.quote_day} уже забрал другой воркер")
        return
    await _send_boring_notice(bot=bot, group=group, quote=quote, clear_window_after=True)


async def manual_publish_latest(bot: Bot, chat_id: int) -> bool | None:
    quote, previous_status = await core.claim_latest_manual_publish_candidate(chat_id)
    if not quote or not quote.group:
        return None

    author_name = quote.author.name if quote.author else "Аноним"
    breakdown = scoring.ScoreBreakdown(
        reaction=quote.reaction_score or 0.0,
        ai=quote.ai_score or 0.0,
        length=quote.length_score or 0.0,
        reaction_count=quote.reaction_count or 0,
        ai_model=quote.ai_model or "",
        ai_best_text=quote.ai_best_text,
    )

    clear_window_after = _manual_publish_clears_window(previous_status)

    return await _publish_quote_message(
        bot=bot,
        group=quote.group,
        quote=quote,
        author_name=author_name,
        breakdown=breakdown,
        forced_by_admin=True,
        clear_window_after=clear_window_after,
    )


def _manual_publish_clears_window(previous_status: str | None) -> bool:
    return previous_status != STATUS_SKIPPED_BORING


async def _publish_quote_message(
    bot: Bot,
    group: Group,
    quote: Quote,
    author_name: str,
    breakdown: scoring.ScoreBreakdown,
    forced_by_admin: bool,
    clear_window_after: bool,
) -> bool:
    info_parts = [breakdown.stars]
    if breakdown.reaction_count:
        info_parts.append(f"{breakdown.reaction_count}❤️")
    info_parts.append(_format_quote_day(quote.quote_day))
    info_line = " · ".join(info_parts)

    msg_link = _message_link(group.chat_id, quote.message_id)
    details_link = f"https://t.me/{settings.BOT_USERNAME}?start=quote_{quote.id}"
    forced_line = "\n⚙️ <i>Опубликовано администратором вручную.</i>" if forced_by_admin else ""

    text = (
        "🏆 <b>Цитата окна</b>\n\n"
        f"💬 <i>«{quote.text}»</i>\n"
        f"— <b>{author_name}</b>\n\n"
        f"{info_line}\n\n"
        f"<a href='{msg_link}'>Оригинал</a> · <a href='{details_link}'>Подробнее</a> · #quoto"
        f"{forced_line}"
    )

    try:
        sent = await bot.send_message(chat_id=group.chat_id, text=text)
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_PUBLISH_FAILED,
            operation_error=str(e)[:250],
        )
        log.error(f"❌ Ошибка при отправке цитаты в {group.chat_id}: {e}")
        return False

    try:
        await core.update_quote_publication(
            quote_id=quote.id,
            bot_message_id=sent.message_id,
            forced_by_admin=forced_by_admin,
            decision_reason=quote.decision_reason,
        )
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_PUBLISH_UNKNOWN,
            operation_error=f"Telegram message was sent, but DB finalization failed: {str(e)[:200]}",
        )
        log.error(f"❌ Цитата отправлена, но не удалось зафиксировать статус в БД для {group.chat_id}: {e}")
        return False

    try:
        await bot.pin_chat_message(
            chat_id=group.chat_id,
            message_id=sent.message_id,
            disable_notification=True,
        )
    except Exception as e:
        await core.append_quote_operation_error(quote.id, f"Pin failed: {str(e)[:200]}")
        log.warning(f"⚠️ Не удалось закрепить сообщение в {group.chat_id}: {e}")

    if clear_window_after:
        try:
            deleted = await core.clear_window_messages(group.chat_id, _window_from_quote(quote))
            log.debug(f"{group.chat_id} | 🗑️ Очищено {deleted} сообщений после публикации")
        except Exception as e:
            await core.append_quote_operation_error(quote.id, f"Cleanup failed: {str(e)[:200]}")
            log.warning(f"⚠️ Не удалось очистить окно после публикации в {group.chat_id}: {e}")

    log.info(f"✅ Цитата окна {quote.quote_day} отправлена в {group.name} ({group.chat_id})")
    return True


async def _send_boring_notice(
    bot: Bot,
    group: Group,
    quote: Quote,
    clear_window_after: bool,
) -> None:
    details_link = f"https://t.me/{settings.BOT_USERNAME}?start=quote_{quote.id}"
    reason_line = f"\n💭 {quote.decision_reason}" if quote.decision_reason else ""
    text = (
        "😴 <b>Сегодня окно вышло скучным</b>\n\n"
        "Бот не нашёл достаточно сильную цитату для публикации."
        f"{reason_line}\n\n"
        f"<a href='{details_link}'>Подробнее</a> · #quoto"
    )

    try:
        sent = await bot.send_message(chat_id=group.chat_id, text=text)
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_BORING_NOTICE_FAILED,
            operation_error=str(e)[:250],
        )
        log.error(f"❌ Ошибка при отправке boring-day уведомления в {group.chat_id}: {e}")
        return

    try:
        await core.update_quote_notice(quote.id, sent.message_id)
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_BORING_NOTICE_UNKNOWN,
            operation_error=f"Telegram notice was sent, but DB finalization failed: {str(e)[:200]}",
        )
        log.error(
            f"❌ Boring-day уведомление отправлено, но не удалось зафиксировать статус в БД для "
            f"{group.chat_id}: {e}"
        )
        return

    if clear_window_after:
        try:
            deleted = await core.clear_window_messages(group.chat_id, _window_from_quote(quote))
            log.debug(f"{group.chat_id} | 🗑️ Очищено {deleted} сообщений после скучного окна")
        except Exception as e:
            await core.append_quote_operation_error(quote.id, f"Cleanup failed: {str(e)[:200]}")
            log.warning(f"⚠️ Не удалось очистить скучное окно в {group.chat_id}: {e}")

    log.info(f"😴 Окно {quote.quote_day} помечено как скучное в {group.name} ({group.chat_id})")


async def _recover_stale_quote(quote: Quote) -> bool:
    if quote.decision_status not in {STATUS_PUBLISHING, STATUS_NOTIFYING_BORING}:
        return False

    if utc_now() - quote.created_at < _STALE_QUOTE_AFTER:
        return False

    fallback_status = (
        STATUS_PUBLISH_UNKNOWN
        if quote.decision_status == STATUS_PUBLISHING
        else STATUS_BORING_NOTICE_UNKNOWN
    )
    await core.mark_quote_status(
        quote.id,
        fallback_status,
        operation_error="In-progress quote timed out before final status confirmation.",
    )
    log.warning(f"⚠️ Quote #{quote.id} помечен как {fallback_status} после таймаута")
    return True


def _window_from_quote(quote: Quote) -> QuoteWindow:
    tz = quote_timezone()
    return QuoteWindow(
        quote_day=quote.quote_day,
        start_local=quote.window_start_at.astimezone(tz),
        end_local=quote.window_end_at.astimezone(tz),
        start_utc=quote.window_start_at,
        end_utc=quote.window_end_at,
    )


def _message_link(chat_id: int, message_id: int | None) -> str:
    link_chat_id = str(chat_id).replace("-100", "", 1) if str(chat_id).startswith("-100") else str(chat_id)
    return f"https://t.me/c/{link_chat_id}/{message_id}"


def _format_quote_day(quote_day) -> str:
    months = [
        "",
        "Января",
        "Февраля",
        "Марта",
        "Апреля",
        "Мая",
        "Июня",
        "Июля",
        "Августа",
        "Сентября",
        "Октября",
        "Ноября",
        "Декабря",
    ]
    return f"{quote_day.day} {months[quote_day.month]}"


def setup_scheduler(bot: Bot) -> AsyncIOScheduler:
    scheduler = AsyncIOScheduler(timezone=settings.TIMEZONE)
    scheduler.add_job(
        quote_of_the_day_job,
        trigger=CronTrigger(
            hour=settings.QUOTE_HOUR,
            minute=settings.QUOTE_MINUTE,
            timezone=settings.TIMEZONE,
        ),
        args=[bot],
        id="quote_of_the_day",
        name="Цитата дня",
        replace_existing=True,
    )

    log.info(
        f"📅 Планировщик настроен: цитата дня в "
        f"{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d} ({settings.TIMEZONE})"
    )
    return scheduler
