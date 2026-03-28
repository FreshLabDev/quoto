import logging
from datetime import date, datetime

from aiogram import types
from sqlalchemy import func, select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import selectinload

from . import models
from .config import setup_logging
from .db import SessionLocal
from .quote_status import (
    IN_PROGRESS_STATUSES,
    MANUAL_PUBLISHABLE_STATUSES,
    STATUS_PUBLISHED,
    STATUS_PUBLISHING,
    STATUS_SKIPPED_BORING,
    VISIBLE_IN_STATS,
)
from .scoring import ScoreBreakdown
from .windows import QuoteWindow, utc_now

log = setup_logging(logging.getLogger(__name__))
_UNSET = object()


async def user_getOrCreate(telegram_user: types.User) -> models.User:
    telegram_name = getattr(telegram_user, "full_name", None) or f"User {telegram_user.id}"

    async with SessionLocal() as session:
        result = await session.execute(
            select(models.User).where(models.User.telegram_id == telegram_user.id)
        )
        db_user = result.scalars().first()

        if db_user:
            if not db_user.name or db_user.name != telegram_name:
                db_user.name = telegram_name
                await session.commit()
            return db_user

        try:
            user = models.User(
                telegram_id=telegram_user.id,
                name=telegram_name,
            )
            session.add(user)
            await session.commit()
            await session.refresh(user)
            log.debug(f"{user.telegram_id} | ✅ Создан пользователь: {user.name}")
            return user
        except IntegrityError:
            await session.rollback()
            result = await session.execute(
                select(models.User).where(models.User.telegram_id == telegram_user.id)
            )
            return result.scalars().first()


async def group_getOrCreate(chat: types.Chat) -> models.Group:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Group).where(models.Group.chat_id == chat.id)
        )
        db_group = result.scalars().first()

        if db_group:
            if not db_group.name or db_group.name != chat.title:
                db_group.name = chat.title
                await session.commit()
            return db_group

        try:
            group = models.Group(
                chat_id=chat.id,
                name=chat.title,
            )
            session.add(group)
            await session.commit()
            await session.refresh(group)
            log.debug(f"{group.chat_id} | ✅ Создана группа: {group.name}")
            return group
        except IntegrityError:
            await session.rollback()
            result = await session.execute(
                select(models.Group).where(models.Group.chat_id == chat.id)
            )
            return result.scalars().first()


async def save_message(message: types.Message, user: models.User) -> models.Message | None:
    if not message.text:
        return None

    async with SessionLocal() as session:
        try:
            db_msg = models.Message(
                message_id=message.message_id,
                chat_id=message.chat.id,
                user_id=user.id,
                text=message.text,
                created_at=utc_now(),
            )
            session.add(db_msg)
            await session.commit()
            await session.refresh(db_msg)
            return db_msg
        except IntegrityError:
            await session.rollback()
            return None
        except Exception as e:
            log.error(f"❌ Ошибка сохранения сообщения {message.message_id}: {e}")
            return None


async def sync_reactions(chat_id: int, message_id: int, emoji_counts: dict[str, int]) -> None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Message).where(
                models.Message.message_id == message_id,
                models.Message.chat_id == chat_id,
            )
        )
        db_msg = result.scalars().first()

        if not db_msg:
            return

        result = await session.execute(
            select(models.Reaction).where(models.Reaction.message_db_id == db_msg.id)
        )
        existing = {reaction.emoji: reaction for reaction in result.scalars().all()}

        for emoji, reaction in existing.items():
            if emoji not in emoji_counts:
                await session.delete(reaction)

        for emoji, count in emoji_counts.items():
            if count <= 0:
                continue

            if emoji in existing:
                existing[emoji].count = count
            else:
                session.add(
                    models.Reaction(
                        message_db_id=db_msg.id,
                        emoji=emoji,
                        count=count,
                    )
                )

        await session.commit()
        log.debug(f"{chat_id} | 🔄 Синхронизированы реакции {dict(emoji_counts)} для сообщения {message_id}")


async def apply_reaction_delta(chat_id: int, message_id: int, emoji_deltas: dict[str, int]) -> None:
    if not emoji_deltas:
        return

    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Message).where(
                models.Message.message_id == message_id,
                models.Message.chat_id == chat_id,
            )
        )
        db_msg = result.scalars().first()

        if not db_msg:
            return

        result = await session.execute(
            select(models.Reaction).where(models.Reaction.message_db_id == db_msg.id)
        )
        existing = {reaction.emoji: reaction for reaction in result.scalars().all()}

        for emoji, delta in emoji_deltas.items():
            if delta == 0:
                continue

            reaction = existing.get(emoji)
            next_count = (reaction.count if reaction else 0) + delta

            if next_count <= 0:
                if reaction:
                    await session.delete(reaction)
                continue

            if reaction:
                reaction.count = next_count
                continue

            session.add(
                models.Reaction(
                    message_db_id=db_msg.id,
                    emoji=emoji,
                    count=next_count,
                )
            )

        await session.commit()
        log.debug(f"{chat_id} | 🔄 Применены delta-реакции {dict(emoji_deltas)} для сообщения {message_id}")


def _extract_emoji(reaction_type: types.ReactionType) -> str | None:
    if hasattr(reaction_type, "emoji"):
        return reaction_type.emoji
    if hasattr(reaction_type, "custom_emoji_id"):
        return f"custom:{reaction_type.custom_emoji_id}"
    return None


async def get_quote_detail(quote_id: int) -> dict | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote)
            .options(
                selectinload(models.Quote.author),
                selectinload(models.Quote.group),
            )
            .where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return None

        return {
            "id": quote.id,
            "text": quote.text,
            "score": quote.score,
            "reaction_score": quote.reaction_score or 0.0,
            "ai_score": quote.ai_score or 0.0,
            "length_score": quote.length_score or 0.0,
            "reaction_count": quote.reaction_count or 0,
            "author_name": quote.author.name if quote.author else "Аноним",
            "group_name": quote.group.name if quote.group else "—",
            "created_at": quote.created_at,
            "ai_model": quote.ai_model,
            "ai_best_text": quote.ai_best_text,
            "message_id": quote.message_id,
            "chat_id": quote.group.chat_id if quote.group else None,
            "decision_status": quote.decision_status,
            "decision_reason": quote.decision_reason,
            "operation_error": quote.operation_error,
            "forced_by_admin": quote.forced_by_admin,
            "quote_day": quote.quote_day,
        }


async def get_chat_stats(chat_id: int) -> dict | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Group).where(models.Group.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        total_q = await session.execute(
            select(func.count(models.Quote.id)).where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        total_quotes = total_q.scalar() or 0

        if total_quotes == 0:
            return {"group_name": group.name, "total_quotes": 0}

        unique_a = await session.execute(
            select(func.count(func.distinct(models.Quote.author_id))).where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        unique_authors = unique_a.scalar() or 0

        avg_s = await session.execute(
            select(func.avg(models.Quote.score)).where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        avg_score = avg_s.scalar() or 0.0

        top_authors_q = await session.execute(
            select(
                models.User.name,
                func.count(models.Quote.id).label("wins"),
                func.avg(models.Quote.score).label("avg_score"),
            )
            .join(models.User, models.Quote.author_id == models.User.id)
            .where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
            .group_by(models.User.id, models.User.name)
            .order_by(func.count(models.Quote.id).desc())
            .limit(3)
        )
        top_authors = [
            {"name": row.name, "wins": row.wins, "avg_score": float(row.avg_score or 0)}
            for row in top_authors_q
        ]

        best_q = await session.execute(
            select(models.Quote)
            .where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
            .order_by(models.Quote.score.desc())
            .limit(1)
        )
        best_quote = best_q.scalars().first()

        best_quote_info = None
        if best_quote:
            author_q = await session.execute(
                select(models.User.name).where(models.User.id == best_quote.author_id)
            )
            author_name = author_q.scalar() or "Аноним"
            best_quote_info = {
                "text": best_quote.text,
                "score": best_quote.score,
                "author": author_name,
                "date": best_quote.created_at,
            }

        return {
            "group_name": group.name,
            "total_quotes": total_quotes,
            "unique_authors": unique_authors,
            "avg_score": float(avg_score),
            "top_authors": top_authors,
            "best_quote": best_quote_info,
        }


async def get_user_stats(chat_id: int, telegram_id: int) -> dict | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Group).where(models.Group.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        result = await session.execute(
            select(models.User).where(models.User.telegram_id == telegram_id)
        )
        user = result.scalars().first()
        if not user:
            return None

        wins_q = await session.execute(
            select(func.count(models.Quote.id)).where(
                models.Quote.group_id == group.id,
                models.Quote.author_id == user.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        wins = wins_q.scalar() or 0

        if wins == 0:
            return {"user_name": user.name, "wins": 0}

        avg_q = await session.execute(
            select(func.avg(models.Quote.score)).where(
                models.Quote.group_id == group.id,
                models.Quote.author_id == user.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        avg_score = float(avg_q.scalar() or 0)

        best_q = await session.execute(
            select(models.Quote)
            .where(
                models.Quote.group_id == group.id,
                models.Quote.author_id == user.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
            .order_by(models.Quote.score.desc())
            .limit(1)
        )
        best = best_q.scalars().first()
        best_info = None
        if best:
            best_info = {
                "text": best.text,
                "score": best.score,
                "date": best.created_at,
            }

        rank_sub = (
            select(
                models.Quote.author_id,
                func.count(models.Quote.id).label("cnt"),
            )
            .where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
            .group_by(models.Quote.author_id)
            .subquery()
        )
        rank_q = await session.execute(
            select(func.count()).where(rank_sub.c.cnt > wins)
        )
        rank = (rank_q.scalar() or 0) + 1

        total_p = await session.execute(
            select(func.count(func.distinct(models.Quote.author_id))).where(
                models.Quote.group_id == group.id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        total_participants = total_p.scalar() or 0

        return {
            "user_name": user.name,
            "wins": wins,
            "avg_score": avg_score,
            "best_quote": best_info,
            "rank": rank,
            "total_participants": total_participants,
        }


async def get_quote_for_day(group_id: int, quote_day: date) -> models.Quote | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote)
            .options(selectinload(models.Quote.author), selectinload(models.Quote.group))
            .where(
                models.Quote.group_id == group_id,
                models.Quote.quote_day == quote_day,
            )
        )
        return result.scalars().first()


async def create_quote_record(
    group: models.Group,
    best_message: models.Message,
    breakdown: ScoreBreakdown,
    window: QuoteWindow,
    decision_status: str,
    decision_reason: str | None = None,
    operation_error: str | None = None,
) -> tuple[models.Quote, bool]:
    async with SessionLocal() as session:
        try:
            quote = models.Quote(
                group_id=group.id,
                author_id=best_message.user_id,
                text=best_message.text,
                score=breakdown.total,
                reaction_score=breakdown.reaction,
                ai_score=breakdown.ai,
                length_score=breakdown.length,
                reaction_count=breakdown.reaction_count,
                message_id=best_message.message_id,
                ai_model=breakdown.ai_model,
                ai_best_text=breakdown.ai_best_text,
                quote_day=window.quote_day,
                window_start_at=window.start_utc,
                window_end_at=window.end_utc,
                decision_status=decision_status,
                decision_reason=decision_reason,
                operation_error=operation_error,
                forced_by_admin=False,
                created_at=utc_now(),
            )
            session.add(quote)
            await session.commit()
            await session.refresh(quote)
            return quote, True
        except IntegrityError:
            await session.rollback()
            result = await session.execute(
                select(models.Quote).where(
                    models.Quote.group_id == group.id,
                    models.Quote.quote_day == window.quote_day,
                )
            )
            existing = result.scalars().first()
            if existing:
                return existing, False
            raise


async def update_quote_publication(
    quote_id: int,
    bot_message_id: int,
    forced_by_admin: bool,
    decision_reason: str | None = None,
) -> None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote).where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return

        quote.bot_message_id = bot_message_id
        quote.decision_status = STATUS_PUBLISHED
        if decision_reason is not None:
            quote.decision_reason = decision_reason
        quote.operation_error = None
        quote.forced_by_admin = forced_by_admin
        await session.commit()


async def update_quote_notice(quote_id: int, notice_message_id: int) -> None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote).where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return

        quote.notice_message_id = notice_message_id
        quote.decision_status = STATUS_SKIPPED_BORING
        quote.operation_error = None
        await session.commit()


async def mark_quote_status(
    quote_id: int,
    decision_status: str,
    decision_reason: str | object = _UNSET,
    operation_error: str | object = _UNSET,
) -> None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote).where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return

        quote.decision_status = decision_status
        if decision_reason is not _UNSET:
            quote.decision_reason = decision_reason
        if operation_error is not _UNSET:
            quote.operation_error = operation_error
        await session.commit()


async def append_quote_operation_error(quote_id: int, operation_error: str) -> None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote).where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return

        existing = (quote.operation_error or "").strip()
        new_error = operation_error.strip()[:250]
        quote.operation_error = f"{existing} | {new_error}".strip(" |") if existing else new_error
        await session.commit()


async def claim_latest_manual_publish_candidate(
    chat_id: int,
) -> tuple[models.Quote | None, str | None]:
    async with SessionLocal() as session:
        async with session.begin():
            result = await session.execute(
                select(models.Quote)
                .join(models.Group, models.Quote.group_id == models.Group.id)
                .options(selectinload(models.Quote.author), selectinload(models.Quote.group))
                .where(
                    models.Group.chat_id == chat_id,
                    models.Quote.decision_status.in_(MANUAL_PUBLISHABLE_STATUSES),
                )
                .order_by(models.Quote.window_end_at.desc())
                .limit(1)
                .with_for_update(skip_locked=True)
            )
            quote = result.scalars().first()
            if not quote:
                return None, None

            previous_status = quote.decision_status
            quote.decision_status = STATUS_PUBLISHING
            quote.operation_error = None

        return quote, previous_status


async def get_latest_manual_publish_candidate(chat_id: int) -> models.Quote | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote)
            .join(models.Group, models.Quote.group_id == models.Group.id)
            .options(selectinload(models.Quote.author), selectinload(models.Quote.group))
            .where(
                models.Group.chat_id == chat_id,
                models.Quote.decision_status.in_(MANUAL_PUBLISHABLE_STATUSES),
            )
            .order_by(models.Quote.window_end_at.desc())
            .limit(1)
        )
        return result.scalars().first()


async def get_stale_in_progress_quotes(chat_id: int, older_than: datetime) -> list[models.Quote]:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote)
            .join(models.Group, models.Quote.group_id == models.Group.id)
            .where(
                models.Group.chat_id == chat_id,
                models.Quote.decision_status.in_(IN_PROGRESS_STATUSES),
                models.Quote.created_at < older_than,
            )
            .order_by(models.Quote.window_end_at.desc())
        )
        return result.scalars().all()


async def count_window_messages(chat_id: int, window: QuoteWindow) -> int:
    async with SessionLocal() as session:
        result = await session.execute(
            select(func.count(models.Message.id)).where(
                models.Message.chat_id == chat_id,
                models.Message.created_at >= window.start_utc,
                models.Message.created_at < window.end_utc,
            )
        )
        return int(result.scalar() or 0)


async def clear_window_messages(chat_id: int, window: QuoteWindow) -> int:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Message).where(
                models.Message.chat_id == chat_id,
                models.Message.created_at >= window.start_utc,
                models.Message.created_at < window.end_utc,
            )
        )
        messages = result.scalars().all()
        deleted = len(messages)
        for message in messages:
            await session.delete(message)
        await session.commit()
        return deleted
