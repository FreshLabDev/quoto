import logging
import zoneinfo
from datetime import datetime, timezone as tz
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.cron import CronTrigger
from aiogram import Bot
from sqlalchemy import select, update, func, delete

from .config import settings, setup_logging
from .db import SessionLocal
from .models import Group, Quote, Message
from . import scoring

log = setup_logging(logging.getLogger(__name__))


async def quote_of_the_day_job(bot: Bot) -> None:
    """Ежедневный джоб: выбор и отправка цитаты дня для каждой группы."""
    log.info("⏰ Запуск выбора цитаты дня...")

    async with SessionLocal() as session:
        result = await session.execute(select(Group))
        groups = result.scalars().all()

    if not groups:
        log.info("📭 Нет групп для обработки")
        return

    for group in groups:
        try:
            await _process_group(bot, group)
        except Exception as e:
            log.error(f"❌ Ошибка при обработке группы {group.chat_id}: {e}")


async def _process_group(bot: Bot, group: Group) -> None:
    """Обработка одной группы: скоринг → отправка → сохранение → очистка."""
    log.debug(f"{group.chat_id} | ⏳ Выбор цитаты для группы {group.name}")
    best_msg, breakdown = await scoring.pick_best_quote(group.chat_id)

    if not best_msg:
        log.debug(f"{group.chat_id} | 📭 Нет цитаты для группы {group.name}")
        return

    author_name = best_msg.author.name if best_msg.author else "Аноним"

    months = [
        "", "Января", "Февраля", "Марта", "Апреля", "Мая", "Июня",
        "Июля", "Августа", "Сентября", "Октября", "Ноября", "Декабря",
    ]
    now = datetime.now(zoneinfo.ZoneInfo(settings.TIMEZONE))
    date_str = f"{now.day} {months[now.month]}"

    reactions_str = f"{breakdown.reaction_count}❤️" if breakdown.reaction_count else ""

    # ⭐⭐⭐⭐☆ · 2❤️ · 21 Февраля
    info_parts = [breakdown.stars]
    if reactions_str:
        info_parts.append(reactions_str)
    info_parts.append(date_str)
    info_line = " · ".join(info_parts)

    link_chat_id = str(group.chat_id).replace("-100", "", 1) if str(group.chat_id).startswith("-100") else str(group.chat_id)
    msg_link = f"https://t.me/c/{link_chat_id}/{best_msg.message_id}"

    text = (
        f"🏆 <b>Цитата дня</b>\n\n"
        f"💬 <i>«{best_msg.text}»</i>\n"
        f"— <b>{author_name}</b>\n\n"
        f"{info_line}\n\n"
    )

    try:
        async with SessionLocal() as session:
            quote = Quote(
                group_id=group.id,
                author_id=best_msg.user_id,
                text=best_msg.text,
                score=breakdown.total,
                reaction_score=breakdown.reaction,
                ai_score=breakdown.ai,
                length_score=breakdown.length,
                reaction_count=breakdown.reaction_count,
                message_id=best_msg.message_id,
                ai_model=breakdown.ai_model,
                ai_best_text=breakdown.ai_best_text,
            )
            session.add(quote)
            await session.commit()
            await session.refresh(quote)
            quote_id = quote.id
            text += f"<a href='{msg_link}'>Оригинал</a> · <a href='https://t.me/{settings.BOT_USERNAME}?start=quote_{quote_id}'>Подробнее</a> · #quoto"

        sent = await bot.send_message(
            chat_id=group.chat_id, text=text
        )

        async with SessionLocal() as session:
            await session.execute(
                update(Quote).where(Quote.id == quote_id).values(bot_message_id=sent.message_id)
            )
            await session.commit()

        try:
            await bot.pin_chat_message(
                chat_id=group.chat_id,
                message_id=sent.message_id,
                disable_notification=True,
            )
        except Exception as e:
            log.warning(f"⚠️ Не удалось закрепить сообщение в {group.chat_id}: {e}")

        log.info(f"✅ Цитата дня отправлена в {group.name} ({group.chat_id})")

    except Exception as e:
        log.error(f"❌ Ошибка при отправке цитаты в {group.chat_id}: {e}")
        return

    await _clear_today_messages(group.chat_id)


async def _clear_today_messages(chat_id: int) -> None:
    """Удаление сообщений и реакций за сегодня для группы."""
    async with SessionLocal() as session:
        stmt = delete(Message).where(
            Message.chat_id == chat_id,
            func.date(Message.created_at) == func.current_date(),
        )
        result = await session.execute(stmt)
        await session.commit()
        log.debug(f"🗑️ Удалено {result.rowcount} сообщений в {chat_id}")


def setup_scheduler(bot: Bot) -> AsyncIOScheduler:
    """Создание и конфигурация планировщика."""
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
