"""Client for the shared cross-bot ``core`` hub (identity / presence / language),
keyed on global Telegram ids.

quoto's domain now lives in the ``quoto`` schema inside the same core-postgres
database, so these helpers run schema-qualified ``core.*`` SQL on the caller's
own :class:`AsyncSession`. Because the ``core.touch`` write happens in the same
transaction that later inserts the FK-child domain rows (message / quote /
score), the freshly upserted ``core.person`` / ``core.chat`` parent is already
visible to the in-transaction FK check -- no separate commit is required.

Writes go exclusively through the SECURITY DEFINER functions ``core.touch`` /
``core.set_language`` / ``core.clear_language``; reads through
``core.effective_language``. Errors are NOT swallowed here: a failed touch must
propagate so the caller aborts before a doomed FK insert (domain and core share
one database, so a touch failure means the domain write would fail too).
"""

from __future__ import annotations

from typing import Optional

from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession

# quoto's app key in the core hub (also drives the language bot-rank: quoto is
# the highest-priority language claimant).
APP = "quoto"

# Scopes / sources mirrored from core.
SCOPE_USER = "user"
SCOPE_CHAT = "chat"
SOURCE_MANUAL = "manual"
SOURCE_AUTO = "auto"


def _ns(value: Optional[str]) -> Optional[str]:
    """Map "" to None so empty strings become SQL NULL."""
    if value is None:
        return None
    value = value.strip() if isinstance(value, str) else value
    return value or None


async def touch(
    session: AsyncSession,
    *,
    user_id: int,
    username: Optional[str] = None,
    first_name: Optional[str] = None,
    last_name: Optional[str] = None,
    tg_lang: Optional[str] = None,
    is_bot: bool = False,
    chat_id: Optional[int] = None,
    chat_type: Optional[str] = None,
    chat_title: Optional[str] = None,
    chat_username: Optional[str] = None,
) -> None:
    """Upsert person (+ optionally chat) + presence via ``core.touch``.

    Awaited before any FK insert into ``quoto.*`` so ``core.person`` /
    ``core.chat`` exist first (within the same transaction).
    """
    await session.execute(
        text(
            "SELECT core.touch(:app, :user_id, :username, :first_name, :last_name, "
            ":tg_lang, :chat_id, :chat_type, :chat_title, :chat_username, :is_bot)"
        ),
        {
            "app": APP,
            "user_id": user_id,
            "username": _ns(username),
            "first_name": _ns(first_name),
            "last_name": _ns(last_name),
            "tg_lang": _ns(tg_lang),
            "chat_id": chat_id,
            "chat_type": _ns(chat_type),
            "chat_title": _ns(chat_title),
            "chat_username": _ns(chat_username),
            "is_bot": is_bot,
        },
    )


async def touch_user(session: AsyncSession, tg_user) -> int:
    """Touch a Telegram user object (person only); returns its telegram id."""
    await touch(
        session,
        user_id=tg_user.id,
        username=getattr(tg_user, "username", None),
        first_name=getattr(tg_user, "first_name", None),
        last_name=getattr(tg_user, "last_name", None),
        tg_lang=getattr(tg_user, "language_code", None),
        is_bot=bool(getattr(tg_user, "is_bot", False)),
    )
    return tg_user.id


async def touch_group(session: AsyncSession, tg_chat, actor) -> int:
    """Touch a group chat via ``core.touch`` (which needs the acting user).

    ``actor`` is the Telegram user acting in the chat (message sender / the user
    who ran the command / added the bot). Records both core.person(actor) and
    core.chat(tg_chat) + presence. Returns the chat_id.
    """
    await touch(
        session,
        user_id=actor.id,
        username=getattr(actor, "username", None),
        first_name=getattr(actor, "first_name", None),
        last_name=getattr(actor, "last_name", None),
        tg_lang=getattr(actor, "language_code", None),
        is_bot=bool(getattr(actor, "is_bot", False)),
        chat_id=tg_chat.id,
        chat_type=getattr(tg_chat, "type", None),
        chat_title=getattr(tg_chat, "title", None),
        chat_username=getattr(tg_chat, "username", None),
    )
    return tg_chat.id


async def set_language(
    session: AsyncSession, scope: str, subject_id: int, lang: str, source: str
) -> None:
    """Record quoto's language claim for a user (SCOPE_USER) or chat (SCOPE_CHAT)."""
    await session.execute(
        text("SELECT core.set_language(:app, :scope, :subject_id, :lang, :source)"),
        {"app": APP, "scope": scope, "subject_id": subject_id, "lang": lang, "source": source},
    )


async def clear_language(session: AsyncSession, scope: str, subject_id: int) -> None:
    """Clear quoto's language claim for a subject."""
    await session.execute(
        text("SELECT core.clear_language(:app, :scope, :subject_id)"),
        {"app": APP, "scope": scope, "subject_id": subject_id},
    )


async def language_claim(
    session: AsyncSession, scope: str, subject_id: int
) -> tuple[Optional[str], Optional[str]]:
    """Read the resolved language claim for one subject from core.language_pref.

    Returns ``(language, source)`` where source is the winning core lang_source
    ('manual' / 'auto' / 'client' / 'default'), or ``(None, None)`` when no claim
    exists. Unlike :func:`effective_language`, this reports the raw stored source
    (so the settings UI can label manual vs auto vs the Telegram hint) and does
    NOT cross scopes. Requires SELECT on core.language_pref (granted to *_core).
    """
    try:
        row = (await session.execute(
            text(
                "SELECT language, source FROM core.language_pref "
                "WHERE scope = :scope AND subject_id = :subject_id"
            ),
            {"scope": scope, "subject_id": subject_id},
        )).first()
    except Exception:
        return None, None
    if row is None:
        return None, None
    return row[0], row[1]


async def effective_language(
    session: AsyncSession,
    user_id: Optional[int],
    chat_id: Optional[int] = None,
    prefer: str = SCOPE_USER,
) -> Optional[str]:
    """Resolve the effective language for a user/chat, per surface.

    ``prefer`` = SCOPE_USER for personal screens, SCOPE_CHAT for group broadcasts.
    Returns the resolved language code, or None when nothing is set / on error.
    """
    if not user_id and not chat_id:
        return None
    try:
        result = await session.execute(
            text("SELECT core.effective_language(:user_id, :chat_id, :prefer)"),
            {"user_id": user_id, "chat_id": chat_id, "prefer": prefer},
        )
        return result.scalar()
    except Exception:
        return None
