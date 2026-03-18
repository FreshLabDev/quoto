from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import selectinload
from sqlalchemy import select, update, delete, func, literal_column
from aiogram import types
import logging

from . import models
from .config import settings, setup_logging
from .db import SessionLocal

log = setup_logging(logging.getLogger(__name__))


async def user_getOrCreate(telegram_user: types.User) -> models.User:
    """Получение или создание пользователя."""
    telegram_name = getattr(telegram_user, 'full_name', None) or f"User {telegram_user.id}"

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
                name=telegram_name
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
        except Exception as e:
            log.error(f"{telegram_user.id} | ❌ Ошибка при создании пользователя: {e}")
            raise


async def group_getOrCreate(chat: types.Chat) -> models.Group:
    """Получение или создание группы."""
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
        except Exception as e:
            log.error(f"❌ Ошибка при создании группы {chat.id}: {e}")
            raise


async def save_message(message: types.Message, user: models.User) -> models.Message | None:
    """Сохранение текстового сообщения из группы."""
    if not message.text:
        return None

    async with SessionLocal() as session:
        try:
            db_msg = models.Message(
                message_id=message.message_id,
                chat_id=message.chat.id,
                user_id=user.id,
                text=message.text,
            )
            session.add(db_msg)
            await session.commit()
            await session.refresh(db_msg)
            return db_msg
        except IntegrityError:
            # Дубликат — уже сохранено
            await session.rollback()
            return None
        except Exception as e:
            log.error(f"❌ Ошибка сохранения сообщения {message.message_id}: {e}")
            return None


async def upsert_reactions(chat_id: int, message_id: int, emoji_counts: dict[str, int]) -> None:
    """Обновление реакций для сообщения (инкрементальный upsert по emoji)."""
    if not emoji_counts:
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

        for emoji, count in emoji_counts.items():
            result = await session.execute(
                select(models.Reaction).where(
                    models.Reaction.message_db_id == db_msg.id,
                    models.Reaction.emoji == emoji,
                )
            )
            existing = result.scalars().first()

            if existing:
                existing.count = max(existing.count, count)
            else:
                session.add(models.Reaction(
                    message_db_id=db_msg.id,
                    emoji=emoji,
                    count=count,
                ))

        await session.commit()
        log.debug(f"{chat_id} | 🔄 Реакция {dict(emoji_counts)} для сообщения {message_id}")



def _extract_emoji(reaction_type: types.ReactionType) -> str | None:
    """Извлечение строки эмодзи из ReactionType."""
    if hasattr(reaction_type, 'emoji'):
        return reaction_type.emoji
    if hasattr(reaction_type, 'custom_emoji_id'):
        return f"custom:{reaction_type.custom_emoji_id}"
    return None


async def get_quote_detail(quote_id: int) -> dict | None:
    """Получение подробной информации о цитате по ID."""
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
        }


async def get_chat_stats(chat_id: int) -> dict | None:
    """Статистика группы: кол-во цитат, топ авторов, лучшая цитата."""
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Group).where(models.Group.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        total_q = await session.execute(
            select(func.count(models.Quote.id)).where(models.Quote.group_id == group.id)
        )
        total_quotes = total_q.scalar() or 0

        if total_quotes == 0:
            return {"group_name": group.name, "total_quotes": 0}

        unique_a = await session.execute(
            select(func.count(func.distinct(models.Quote.author_id))).where(
                models.Quote.group_id == group.id
            )
        )
        unique_authors = unique_a.scalar() or 0

        avg_s = await session.execute(
            select(func.avg(models.Quote.score)).where(models.Quote.group_id == group.id)
        )
        avg_score = avg_s.scalar() or 0.0
        top_authors_q = await session.execute(
            select(
                models.User.name,
                func.count(models.Quote.id).label("wins"),
                func.avg(models.Quote.score).label("avg_score"),
            )
            .join(models.User, models.Quote.author_id == models.User.id)
            .where(models.Quote.group_id == group.id)
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
            .where(models.Quote.group_id == group.id)
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
    """Статистика пользователя в конкретной группе."""
    async with SessionLocal() as session:
        # Группа
        result = await session.execute(
            select(models.Group).where(models.Group.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        # Юзер
        result = await session.execute(
            select(models.User).where(models.User.telegram_id == telegram_id)
        )
        user = result.scalars().first()
        if not user:
            return None

        # Кол-во побед
        wins_q = await session.execute(
            select(func.count(models.Quote.id)).where(
                models.Quote.group_id == group.id,
                models.Quote.author_id == user.id,
            )
        )
        wins = wins_q.scalar() or 0

        if wins == 0:
            return {"user_name": user.name, "wins": 0}

        # Средний скор
        avg_q = await session.execute(
            select(func.avg(models.Quote.score)).where(
                models.Quote.group_id == group.id,
                models.Quote.author_id == user.id,
            )
        )
        avg_score = float(avg_q.scalar() or 0)

        # Лучшая цитата юзера
        best_q = await session.execute(
            select(models.Quote)
            .where(
                models.Quote.group_id == group.id,
                models.Quote.author_id == user.id,
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

        # Место в рейтинге (по кол-ву побед)
        rank_sub = (
            select(
                models.Quote.author_id,
                func.count(models.Quote.id).label("cnt"),
            )
            .where(models.Quote.group_id == group.id)
            .group_by(models.Quote.author_id)
            .subquery()
        )
        rank_q = await session.execute(
            select(func.count()).where(rank_sub.c.cnt > wins)
        )
        rank = (rank_q.scalar() or 0) + 1

        # Общее число участников
        total_p = await session.execute(
            select(func.count(func.distinct(models.Quote.author_id))).where(
                models.Quote.group_id == group.id
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

