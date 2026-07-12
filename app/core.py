import logging
import json
import random
from datetime import date, datetime, timezone
from zoneinfo import ZoneInfo

from aiogram import types
from sqlalchemy import func, select, update
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import selectinload

from . import core_client, i18n, media, models
from .config import settings, setup_logging
from .db import SessionLocal
from .quote_status import (
    IN_PROGRESS_STATUSES,
    STATUS_PUBLISHED,
    STATUS_PUBLISHING,
    STATUS_SKIPPED_BORING,
    VISIBLE_IN_STATS,
)
from .scoring import ScoreBreakdown, resolved_message_text
from .windows import QuoteWindow, utc_now

log = setup_logging(logging.getLogger(__name__))
_UNSET = object()
MIN_MESSAGES_MIN = 1
MIN_MESSAGES_MAX = 200
GROUP_TOGGLE_FIELDS = {
    "context": "quote_context_enabled",
    "boring": "boring_notice_enabled",
    "pin": "pin_enabled",
    "media": "media_analysis_enabled",
}
# Best-effort default timezone when a group's language is auto-detected.
LANGUAGE_DEFAULT_TIMEZONE = {
    "ru": "Europe/Moscow",
    "uk": "Europe/Kyiv",
    "en": "UTC",
    "de": "Europe/Berlin",
}


def is_valid_timezone(name: str | None) -> bool:
    if not name:
        return False
    try:
        ZoneInfo(name)
        return True
    except Exception:
        return False


def effective_group_timezone_name(group: models.GroupSettings | None) -> str:
    name = getattr(group, "timezone", None)
    return name if is_valid_timezone(name) else settings.TIMEZONE


def default_timezone_for_language(language_code: str | None) -> str:
    code = i18n.normalize_language_code(language_code) or ""
    return LANGUAGE_DEFAULT_TIMEZONE.get(code, settings.TIMEZONE)


async def set_group_timezone(chat_id: int, timezone_name: str) -> models.GroupSettings | None:
    if not is_valid_timezone(timezone_name):
        return None
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None
        group.timezone = timezone_name
        await session.commit()
        await session.refresh(group)
        return await hydrate_group(session, group)


async def set_group_timezone_auto(chat_id: int, timezone_name: str) -> bool:
    """Set a default timezone only if the group has none yet."""
    if not is_valid_timezone(timezone_name):
        return False
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group or getattr(group, "timezone", None):
            return False
        group.timezone = timezone_name
        await session.commit()
        return True


async def user_getOrCreate(telegram_user: types.User) -> int:
    """Upsert the caller into the shared ``core.person`` hub; returns the global
    telegram id (which is what domain rows reference now that the local users
    table is gone)."""
    async with SessionLocal() as session:
        await core_client.touch_user(session, telegram_user)
        await session.commit()
        return telegram_user.id


def _jittered_quote_minute() -> int | None:
    """Spread new groups' cutoff across a few minutes to de-sync the herd."""
    jitter = getattr(settings, "QUOTE_MINUTE_JITTER", 0) or 0
    if jitter <= 0:
        return None
    return (settings.QUOTE_MINUTE + random.randint(0, jitter)) % 60


async def group_getOrCreate(
    chat: types.Chat, actor: types.User, hydrate: bool = False
) -> models.GroupSettings | None:
    """Upsert chat identity into ``core.chat`` (via ``core.touch``, which needs
    the acting user) and ensure a ``group_settings`` row exists. Returns the
    group's settings row (natural ``chat_id`` PK). ``actor`` is the user acting
    in the chat (sender / the user who added the bot / ran the command).

    Pass ``hydrate=True`` on display / broadcast paths to also attach the
    transient ``.name`` / ``.language_code`` / ``.language_source`` fields from
    core; the hot message-ingest path leaves it False (it only needs settings).
    """
    async with SessionLocal() as session:
        await core_client.touch_group(session, chat, actor)
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat.id)
        )
        group = result.scalars().first()
        if group is None:
            try:
                group = models.GroupSettings(
                    chat_id=chat.id,
                    is_active=True,
                    quote_minute=_jittered_quote_minute(),
                )
                session.add(group)
                await session.commit()
                await session.refresh(group)
                log.debug(f"{chat.id} | ✅ Создана группа")
            except IntegrityError:
                await session.rollback()
                group = (await session.execute(
                    select(models.GroupSettings).where(
                        models.GroupSettings.chat_id == chat.id
                    )
                )).scalars().first()
        else:
            group.is_active = True
            await session.commit()
        return await hydrate_group(session, group) if hydrate else group


async def set_group_active(chat_id: int, active: bool) -> bool:
    """Enable or suspend scheduler work without discarding group history/settings."""
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return False
        active = bool(active)
        if bool(getattr(group, "is_active", True)) == active:
            return True
        group.is_active = active
        await session.commit()
        return True


async def migrate_group_chat_id(old_chat_id: int, new_chat_id: int) -> bool:
    """Re-key a group when Telegram migrates it to a supergroup (chat_id changes)."""
    if old_chat_id == new_chat_id:
        return False
    async with SessionLocal() as session:
        # The new chat identity must already be in core.chat (touched) so the
        # repointed FKs hold; if not, skip -- the next message under the new id
        # recreates the group fresh.
        if (await session.execute(
            select(models.core_chat.c.chat_id).where(models.core_chat.c.chat_id == new_chat_id)
        )).first() is None:
            return False
        existing_new = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == new_chat_id)
        )
        if existing_new.scalars().first():
            return False
        group = (await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == old_chat_id)
        )).scalars().first()
        if not group:
            return False
        group.chat_id = new_chat_id
        await session.execute(
            update(models.Message).where(models.Message.chat_id == old_chat_id).values(chat_id=new_chat_id)
        )
        for model in (models.Quote, models.AIEvaluationRun, models.MessageAIScore):
            await session.execute(
                update(model).where(model.group_id == old_chat_id).values(group_id=new_chat_id)
            )
        for model in (models.AIEvaluationRun, models.MessageAIScore):
            await session.execute(
                update(model).where(model.chat_id == old_chat_id).values(chat_id=new_chat_id)
            )
        await session.commit()
        log.info(f"{old_chat_id} → {new_chat_id} | 🔀 Группа мигрировала в супергруппу")
        return True


async def _author_names(session, telegram_ids) -> dict[int, str]:
    """Resolve display names for a set of telegram ids from core.person."""
    ids = [i for i in {tid for tid in telegram_ids} if i is not None]
    if not ids:
        return {}
    rows = (await session.execute(
        select(
            models.core_person.c.telegram_user_id,
            models.core_person.c.first_name,
            models.core_person.c.last_name,
            models.core_person.c.username,
        ).where(models.core_person.c.telegram_user_id.in_(ids))
    )).all()
    out: dict[int, str] = {}
    for row in rows:
        name = (row.first_name or "").strip()
        if row.last_name:
            name = f"{name} {row.last_name.strip()}".strip()
        if not name:
            name = (row.username or "").strip()
        out[row.telegram_user_id] = name or "Аноним"
    return out


async def _chat_title(session, chat_id: int) -> str | None:
    """Resolve a group's display title from core.chat (was Group.name)."""
    return (await session.execute(
        select(models.core_chat.c.title).where(
            models.core_chat.c.chat_id == chat_id
        )
    )).scalar()


async def hydrate_group(session, group: models.GroupSettings | None) -> models.GroupSettings | None:
    """Attach transient identity/language fields onto a GroupSettings instance.

    quoto.group_settings holds only settings; the group's display name lives in
    core.chat and its language in the core language hub. Duck-typed helpers
    (i18n.group_language / group_language_is_set) and log lines expect
    ``.name`` / ``.language_code`` / ``.language_source`` on the group object, so
    we populate them here as NON-persisted attributes right after loading. Every
    code path that loads a group for display or broadcast must hydrate it.
    """
    if group is None:
        return None
    group.name = await _chat_title(session, group.chat_id)
    lang, source = await core_client.language_claim(
        session, core_client.SCOPE_CHAT, group.chat_id
    )
    group.language_code = lang
    group.language_source = source
    return group


async def author_name(user_id: int | None) -> str:
    """Display name for a single Telegram user, resolved from core.person."""
    async with SessionLocal() as session:
        names = await _author_names(session, [user_id])
    return names.get(user_id, "Аноним")


async def resolve_author_names(session, telegram_ids) -> dict[int, str]:
    """Public batch name resolver from core.person, on the caller's session.

    Lets scoring / reporting resolve author display names inside their own
    transaction without importing core at module level (core imports scoring).
    """
    return await _author_names(session, telegram_ids)


async def get_group_by_chat_id(chat_id: int) -> models.GroupSettings | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        return await hydrate_group(session, result.scalars().first())


async def set_group_language_auto(chat_id: int, language_code: str | None) -> bool:
    normalized = i18n.normalize_language_code(language_code)
    if not normalized:
        return False

    async with SessionLocal() as session:
        # Only auto-set when the chat has no language claim yet (core keeps any
        # manual claim winning; this just avoids churning an existing claim).
        existing = await core_client.effective_language(
            session, None, chat_id, core_client.SCOPE_CHAT
        )
        if existing:
            return False
        await core_client.set_language(
            session, core_client.SCOPE_CHAT, chat_id, normalized, core_client.SOURCE_AUTO
        )
        await session.commit()
        return True


async def set_group_language_manual(chat_id: int, language_code: str | None) -> bool:
    normalized = i18n.normalize_language_code(language_code)
    if not normalized:
        return False

    async with SessionLocal() as session:
        await core_client.set_language(
            session, core_client.SCOPE_CHAT, chat_id, normalized, core_client.SOURCE_MANUAL
        )
        await session.commit()
        return True


async def clear_group_language(chat_id: int) -> bool:
    async with SessionLocal() as session:
        await core_client.clear_language(session, core_client.SCOPE_CHAT, chat_id)
        await session.commit()
        return True


async def set_user_language_manual(telegram_id: int, language_code: str | None) -> bool:
    normalized = i18n.normalize_language_code(language_code)
    if not normalized:
        return False

    async with SessionLocal() as session:
        await core_client.set_language(
            session, core_client.SCOPE_USER, telegram_id, normalized, core_client.SOURCE_MANUAL
        )
        await session.commit()
        return True


async def clear_user_language(telegram_id: int) -> bool:
    async with SessionLocal() as session:
        await core_client.clear_language(session, core_client.SCOPE_USER, telegram_id)
        await session.commit()
        return True


async def user_language_state(
    telegram_id: int | None, telegram_language_code: str | None
) -> tuple[str, str | None]:
    """Resolve a user's personal language AND its source for the settings UI.

    Returns ``(language, source)`` where source is the core claim source
    ('manual' / 'auto' / 'client' / ...) or None when nothing is stored (falling
    back to the Telegram hint / default). The menu labels 'manual' distinctly and
    treats everything else as the Telegram default. The language is normalized to
    a supported code (unsupported locales -> default) so the panel renders and
    highlights the same language instead of showing an unselectable claim.
    """
    async with SessionLocal() as session:
        lang, source = await core_client.language_claim(
            session, core_client.SCOPE_USER, telegram_id
        )
    if lang:
        return i18n.language_or_default(lang), source
    return i18n.language_or_default(telegram_language_code), None


def effective_group_quote_hour(group: models.GroupSettings | None) -> int:
    value = getattr(group, "quote_hour", None)
    if value is None:
        value = settings.QUOTE_HOUR
    try:
        return int(value) % 24
    except (TypeError, ValueError):
        return settings.QUOTE_HOUR % 24


def effective_group_quote_minute(group: models.GroupSettings | None) -> int:
    value = getattr(group, "quote_minute", None)
    if value is None:
        value = settings.QUOTE_MINUTE
    try:
        return int(value) % 60
    except (TypeError, ValueError):
        return settings.QUOTE_MINUTE % 60


def effective_group_quote_time(group: models.GroupSettings | None) -> tuple[int, int]:
    return effective_group_quote_hour(group), effective_group_quote_minute(group)


def effective_group_min_messages(group: models.GroupSettings | None) -> int:
    value = getattr(group, "min_messages", None)
    if value is None:
        value = settings.MIN_MESSAGES_FOR_AUTO_REVIEW
    return clamp_min_messages(value)


def effective_group_boring_notice_enabled(group: models.GroupSettings | None) -> bool:
    value = getattr(group, "boring_notice_enabled", None)
    return True if value is None else bool(value)


def effective_group_pin_enabled(group: models.GroupSettings | None) -> bool:
    value = getattr(group, "pin_enabled", None)
    return True if value is None else bool(value)


def effective_group_quote_context_enabled(group: models.GroupSettings | None) -> bool:
    value = getattr(group, "quote_context_enabled", None)
    return True if value is None else bool(value)


def group_media_analysis_setting(group: models.GroupSettings | None) -> bool:
    """The group's own media-analysis preference (default on), ignoring the global switch."""
    value = getattr(group, "media_analysis_enabled", None)
    return True if value is None else bool(value)


def effective_group_media_analysis_enabled(group: models.GroupSettings | None) -> bool:
    """Media is analysed only when both the global switch and the group allow it."""
    return bool(settings.MEDIA_ANALYSIS_ENABLED) and group_media_analysis_setting(group)


def effective_group_is_premium(group: models.GroupSettings | None) -> bool:
    if bool(getattr(group, "is_premium", None)):
        return True
    chat_id = getattr(group, "chat_id", None)
    return chat_id is not None and chat_id in settings.PREMIUM_CHAT_IDS


def effective_group_message_cap(group: models.GroupSettings | None) -> int | None:
    """Max messages to feed the AI for this group, or None for unlimited (premium)."""
    if effective_group_is_premium(group):
        return None
    cap = settings.MAX_MESSAGES_PER_DAILY_EVAL
    return cap if cap and cap > 0 else None


def group_agreement_accepted(group: models.GroupSettings | None) -> bool:
    return getattr(group, "agreement_accepted_at", None) is not None


async def accept_group_agreement(
    chat_id: int,
    user_id: int,
    language: str | None,
) -> models.GroupSettings | None:
    normalized = i18n.normalize_language_code(language) or i18n.DEFAULT_LANGUAGE
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None
        if group.agreement_accepted_at is None:
            group.agreement_accepted_at = utc_now()
            group.agreement_accepted_by = int(user_id)
            group.agreement_language = normalized
            await session.commit()
            await session.refresh(group)
        return await hydrate_group(session, group)


def clamp_min_messages(value: int | str) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        parsed = settings.MIN_MESSAGES_FOR_AUTO_REVIEW
    return max(MIN_MESSAGES_MIN, min(MIN_MESSAGES_MAX, parsed))


def normalize_quote_time(hour: int, minute: int) -> tuple[int, int]:
    total = (int(hour) * 60 + int(minute)) % (24 * 60)
    return divmod(total, 60)


async def adjust_group_quote_time(chat_id: int, delta_minutes: int) -> models.GroupSettings | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        hour, minute = effective_group_quote_time(group)
        group.quote_hour, group.quote_minute = normalize_quote_time(hour, minute + int(delta_minutes))
        await session.commit()
        await session.refresh(group)
        return await hydrate_group(session, group)


async def adjust_group_min_messages(chat_id: int, delta: int) -> models.GroupSettings | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        group.min_messages = clamp_min_messages(effective_group_min_messages(group) + int(delta))
        await session.commit()
        await session.refresh(group)
        return await hydrate_group(session, group)


async def toggle_group_setting(chat_id: int, setting_key: str) -> models.GroupSettings | None:
    field_name = GROUP_TOGGLE_FIELDS.get(setting_key)
    if not field_name:
        return None

    async with SessionLocal() as session:
        result = await session.execute(
            select(models.GroupSettings).where(models.GroupSettings.chat_id == chat_id)
        )
        group = result.scalars().first()
        if not group:
            return None

        current = _effective_group_toggle(group, setting_key)
        setattr(group, field_name, not current)
        await session.commit()
        await session.refresh(group)
        return await hydrate_group(session, group)


def _effective_group_toggle(group: models.GroupSettings, setting_key: str) -> bool:
    if setting_key == "context":
        return effective_group_quote_context_enabled(group)
    if setting_key == "boring":
        return effective_group_boring_notice_enabled(group)
    if setting_key == "pin":
        return effective_group_pin_enabled(group)
    if setting_key == "media":
        return group_media_analysis_setting(group)
    return False


async def save_message(message: types.Message, user_id: int) -> models.Message | None:
    message_created_at = getattr(message, "date", None) or utc_now()
    if message_created_at.tzinfo is None:
        message_created_at = message_created_at.replace(tzinfo=timezone.utc)
    else:
        message_created_at = message_created_at.astimezone(timezone.utc)
    reply_to_message = getattr(message, "reply_to_message", None)
    reply_to_message_id = getattr(reply_to_message, "message_id", None) if reply_to_message else None
    source = media.extract_media_source(message)

    async with SessionLocal() as session:
        try:
            db_msg = models.Message(
                message_id=message.message_id,
                chat_id=message.chat.id,
                user_id=user_id,
                text=media.initial_message_text(message),
                content_type=media.message_content_type(message),
                caption=media.message_caption(message),
                media_status="pending" if source else None,
                reply_to_message_id=reply_to_message_id,
                created_at=message_created_at,
            )
            session.add(db_msg)
            if source:
                await session.flush()
                session.add(media.message_media_from_source(db_msg.id, source, status="pending"))
            await session.commit()
            await session.refresh(db_msg)
            return db_msg
        except IntegrityError:
            await session.rollback()
            return None
        except Exception as e:
            log.error(f"❌ Ошибка сохранения сообщения {message.message_id}: {e}")
            return None


async def update_message(message: types.Message) -> models.Message | None:
    reply_to_message = getattr(message, "reply_to_message", None)
    reply_to_message_id = getattr(reply_to_message, "message_id", None) if reply_to_message else None

    async with SessionLocal() as session:
        try:
            result = await session.execute(
                select(models.Message).where(
                    models.Message.message_id == message.message_id,
                    models.Message.chat_id == message.chat.id,
                )
            )
            db_msg = result.scalars().first()
            if not db_msg:
                return None

            db_msg.text = media.initial_message_text(message)
            db_msg.content_type = media.message_content_type(message)
            db_msg.caption = media.message_caption(message)
            db_msg.reply_to_message_id = reply_to_message_id
            await session.commit()
            await session.refresh(db_msg)
            return db_msg
        except Exception as e:
            log.error(f"❌ Ошибка обновления сообщения {message.message_id}: {e}")
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
            select(models.Quote).where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return None

        # group_id is the natural chat_id; identity/name live in core.
        author_names = await _author_names(session, [quote.author_id])
        group_name = await _chat_title(session, quote.group_id) or "—"

        return {
            "id": quote.id,
            "text": quote.text,
            "score": quote.score,
            "reaction_score": quote.reaction_score or 0.0,
            "ai_score": quote.ai_score or 0.0,
            "length_score": quote.length_score or 0.0,
            "reaction_count": quote.reaction_count or 0,
            "author_name": author_names.get(quote.author_id, "Аноним"),
            "group_name": group_name,
            "created_at": quote.created_at,
            "ai_model": quote.ai_model,
            "ai_best_text": quote.ai_best_text,
            "context_message_ids": _load_context_message_ids(quote.context_message_ids),
            "context_messages": _load_context_snapshot(quote.context_snapshot),
            "message_id": quote.message_id,
            "content_type": quote.content_type,
            "chat_id": quote.group_id,
            "decision_status": quote.decision_status,
            "decision_reason": quote.decision_reason,
            "operation_error": quote.operation_error,
            "quote_day": quote.quote_day,
        }


async def get_chat_stats(chat_id: int) -> dict | None:
    async with SessionLocal() as session:
        settings = await session.execute(
            select(models.GroupSettings.chat_id).where(
                models.GroupSettings.chat_id == chat_id
            )
        )
        if settings.scalar() is None:
            return None
        group_name = await _chat_title(session, chat_id) or "—"

        total_q = await session.execute(
            select(func.count(models.Quote.id)).where(
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        total_quotes = total_q.scalar() or 0

        if total_quotes == 0:
            return {"group_name": group_name, "total_quotes": 0}

        unique_a = await session.execute(
            select(func.count(func.distinct(models.Quote.author_id))).where(
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        unique_authors = unique_a.scalar() or 0

        avg_s = await session.execute(
            select(func.avg(models.Quote.score)).where(
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        avg_score = avg_s.scalar() or 0.0

        top_authors_q = await session.execute(
            select(
                models.Quote.author_id,
                func.count(models.Quote.id).label("wins"),
                func.avg(models.Quote.score).label("avg_score"),
            )
            .where(
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
            .group_by(models.Quote.author_id)
            .order_by(func.count(models.Quote.id).desc())
            .limit(3)
        )
        top_rows = top_authors_q.all()
        names = await _author_names(session, [r.author_id for r in top_rows])
        top_authors = [
            {
                "name": names.get(row.author_id, "Аноним"),
                "wins": row.wins,
                "avg_score": float(row.avg_score or 0),
            }
            for row in top_rows
        ]

        best_q = await session.execute(
            select(models.Quote)
            .where(
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
            .order_by(models.Quote.score.desc())
            .limit(1)
        )
        best_quote = best_q.scalars().first()

        best_quote_info = None
        if best_quote:
            author_names = await _author_names(session, [best_quote.author_id])
            best_quote_info = {
                "text": best_quote.text,
                "score": best_quote.score,
                "author": author_names.get(best_quote.author_id, "Аноним"),
                "date": best_quote.created_at,
            }

        return {
            "group_name": group_name,
            "total_quotes": total_quotes,
            "unique_authors": unique_authors,
            "avg_score": float(avg_score),
            "top_authors": top_authors,
            "best_quote": best_quote_info,
        }


async def get_user_stats(chat_id: int, telegram_id: int) -> dict | None:
    async with SessionLocal() as session:
        settings = await session.execute(
            select(models.GroupSettings.chat_id).where(
                models.GroupSettings.chat_id == chat_id
            )
        )
        if settings.scalar() is None:
            return None

        names = await _author_names(session, [telegram_id])
        user_name = names.get(telegram_id, "Аноним")

        wins_q = await session.execute(
            select(func.count(models.Quote.id)).where(
                models.Quote.group_id == chat_id,
                models.Quote.author_id == telegram_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        wins = wins_q.scalar() or 0

        if wins == 0:
            return {"user_name": user_name, "wins": 0}

        avg_q = await session.execute(
            select(func.avg(models.Quote.score)).where(
                models.Quote.group_id == chat_id,
                models.Quote.author_id == telegram_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        avg_score = float(avg_q.scalar() or 0)

        best_q = await session.execute(
            select(models.Quote)
            .where(
                models.Quote.group_id == chat_id,
                models.Quote.author_id == telegram_id,
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
                models.Quote.group_id == chat_id,
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
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(VISIBLE_IN_STATS),
            )
        )
        total_participants = total_p.scalar() or 0

        return {
            "user_name": user_name,
            "wins": wins,
            "avg_score": avg_score,
            "best_quote": best_info,
            "rank": rank,
            "total_participants": total_participants,
        }


async def get_quote_for_day(chat_id: int, quote_day: date) -> models.Quote | None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote).where(
                models.Quote.group_id == chat_id,
                models.Quote.quote_day == quote_day,
            )
        )
        return result.scalars().first()


async def create_quote_record(
    chat_id: int,
    best_message: models.Message,
    breakdown: ScoreBreakdown,
    window: QuoteWindow,
    decision_status: str,
    decision_reason: str | None = None,
    operation_error: str | None = None,
    context_messages: list[models.Message] | None = None,
) -> tuple[models.Quote, bool]:
    context_messages = context_messages or []
    async with SessionLocal() as session:
        # Snapshot author display names from core.person at capture time.
        context_names = await _author_names(
            session, [m.user_id for m in context_messages]
        )
        context_message_ids, context_snapshot = _serialize_context_messages(
            context_messages,
            primary_message_id=best_message.id,
            author_names=context_names,
        )
        try:
            quote = models.Quote(
                group_id=chat_id,
                author_id=best_message.user_id,
                text=resolved_message_text(best_message),
                score=breakdown.total,
                reaction_score=breakdown.reaction,
                ai_score=breakdown.ai,
                length_score=breakdown.length,
                reaction_count=breakdown.reaction_count,
                message_id=best_message.message_id,
                content_type=getattr(best_message, "content_type", None) or "text",
                ai_model=breakdown.ai_model,
                ai_best_text=breakdown.ai_best_text,
                context_message_ids=context_message_ids,
                context_snapshot=context_snapshot,
                quote_day=window.quote_day,
                window_start_at=window.start_utc,
                window_end_at=window.end_utc,
                decision_status=decision_status,
                status_changed_at=utc_now(),
                decision_reason=decision_reason,
                operation_error=operation_error,
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
                    models.Quote.group_id == chat_id,
                    models.Quote.quote_day == window.quote_day,
                )
            )
            existing = result.scalars().first()
            if existing:
                return existing, False
            raise


def _serialize_context_messages(
    context_messages: list[models.Message],
    primary_message_id: int,
    author_names: dict[int, str] | None = None,
) -> tuple[str | None, str | None]:
    if len(context_messages) <= 1:
        return None, None

    author_names = author_names or {}
    ids = [int(message.message_id) for message in context_messages]
    snapshot = [
        {
            "message_id": int(message.message_id),
            "author": author_names.get(getattr(message, "user_id", None), "Аноним"),
            "text": resolved_message_text(message),
            "is_primary": message.id == primary_message_id,
        }
        for message in context_messages
    ]
    return (
        json.dumps(ids, ensure_ascii=False),
        json.dumps(snapshot, ensure_ascii=False),
    )


def _load_context_message_ids(raw: str | None) -> list[int]:
    if not raw:
        return []
    try:
        parsed = json.loads(raw)
    except (TypeError, json.JSONDecodeError):
        return []
    if not isinstance(parsed, list):
        return []
    result: list[int] = []
    for value in parsed:
        try:
            result.append(int(value))
        except (TypeError, ValueError):
            continue
    return result


def _load_context_snapshot(raw: str | None) -> list[dict[str, object]]:
    if not raw:
        return []
    try:
        parsed = json.loads(raw)
    except (TypeError, json.JSONDecodeError):
        return []
    if not isinstance(parsed, list):
        return []

    result: list[dict[str, object]] = []
    for item in parsed:
        if not isinstance(item, dict):
            continue
        text = str(item.get("text") or "").strip()
        if not text:
            continue
        result.append(
            {
                "message_id": item.get("message_id"),
                "author": str(item.get("author") or "Аноним"),
                "text": text,
                "is_primary": bool(item.get("is_primary")),
            }
        )
    return result


async def update_quote_publication(
    quote_id: int,
    bot_message_id: int,
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
        quote.status_changed_at = utc_now()
        if decision_reason is not None:
            quote.decision_reason = decision_reason
        quote.operation_error = None
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
        quote.status_changed_at = utc_now()
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
        quote.status_changed_at = utc_now()
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


async def delete_quote_record(quote_id: int) -> bool:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote).where(models.Quote.id == quote_id)
        )
        quote = result.scalars().first()
        if not quote:
            return False

        await session.delete(quote)
        await session.commit()
        return True


async def get_stale_in_progress_quotes(chat_id: int, older_than: datetime) -> list[models.Quote]:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Quote)
            .where(
                models.Quote.group_id == chat_id,
                models.Quote.decision_status.in_(IN_PROGRESS_STATUSES),
                models.Quote.status_changed_at < older_than,
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
