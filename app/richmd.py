"""Rich Markdown documents (Bot API 10.3 `sendRichMessage`).

Telegram renders a structured Markdown payload natively — real headings, lists,
blockquotes, tables — instead of the bold-text imitation `parse_mode=HTML`
forces on a long document. The method exists only on a self-hosted Bot API
server: api.telegram.org answers `404 method not found`.

So the capability is probed once at startup (see `preflight`) and every caller
keeps a plain-HTML path. The probe matters more than a per-call error would:
`editMessageText` exists everywhere, and a cloud server handed a `rich_message`
field simply ignores it and complains that the text is empty — a 400 that says
nothing about the feature. Only `sendRichMessage` answers the question cleanly.
"""
from __future__ import annotations

import logging
from typing import Any, Union

from aiogram import Bot, types
from aiogram.exceptions import (
    TelegramAPIError,
    TelegramConflictError,
    TelegramForbiddenError,
    TelegramNotFound,
    TelegramRetryAfter,
    TelegramServerError,
    TelegramUnauthorizedError,
)
from aiogram.methods.base import TelegramMethod

from . import utils
from .config import setup_logging


log = setup_logging(logging.getLogger(__name__))

# None until the startup probe has answered. Only True enables the rich path:
# an inconclusive probe (a server that never answered) must not be read as a
# yes, because the fallback is correct and the rich attempt may not be.
_supported: bool | None = None


class EditMessageRich(TelegramMethod[Union[types.Message, bool]]):
    """`editMessageText` carrying `rich_message` instead of `text`."""

    __returning__ = Union[types.Message, bool]
    __api_method__ = "editMessageText"

    chat_id: Union[int, str]
    message_id: int
    rich_message: dict[str, Any]
    reply_markup: types.InlineKeyboardMarkup | None = None


class _ProbeRichMessage(TelegramMethod[bool]):
    """`sendRichMessage` with no parameters at all — the probe.

    Probing is a real call, so it is only safe because an empty body cannot do
    anything: the method rejects it on its parameters long before it would send
    a message. An existing method answers 400, a missing one 404.
    """

    __returning__ = bool
    __api_method__ = "sendRichMessage"


def available() -> bool:
    return _supported is True


async def preflight(bot: Bot) -> bool:
    """Ask the server whether it implements `sendRichMessage`, once, at startup.

    Unlike makeitMD, where the method is the whole product and its absence is a
    startup failure, Quoto only renders one document with it. A server without
    it is a degraded mode, not a dead bot — but never a silent one.
    """
    global _supported
    try:
        await bot(_ProbeRichMessage())
    except TelegramNotFound:
        _supported = False
        log.warning(
            "⚠️ Bot API server does not implement sendRichMessage — the user "
            "agreement falls back to plain HTML. Point TELEGRAM_BOT_API_BASE_URL "
            "at a Bot API 10.3 server to get the rich document back."
        )
        await utils.notify_developers(
            "Bot API server has no sendRichMessage (Bot API 10.3): the user "
            "agreement is rendered as plain HTML.",
            dedupe_key="richmd:unsupported",
        )
        return False
    except (TelegramUnauthorizedError, TelegramForbiddenError) as exc:
        # An answer about the caller, not about the method: rejected credentials
        # or something in front of the server refusing on its behalf.
        _supported = None
        log.warning(f"⚠️ sendRichMessage probe was refused ({exc}) — staying on plain HTML")
        return False
    except (TelegramRetryAfter, TelegramServerError, TelegramConflictError) as exc:
        # Not an answer about the method. Flood control, a 5xx and a webhook
        # conflict are all TelegramAPIError subclasses, so treating "any other
        # API error" as proof of existence would latch the rich path on for the
        # life of the process because the server was briefly busy at startup.
        _supported = None
        log.warning(f"⚠️ sendRichMessage probe was inconclusive ({exc}) — staying on plain HTML")
        return False
    except TelegramAPIError:
        # A 400 on the empty body: the method is there and only the parameters
        # were missing.
        _supported = True
        log.info("📄 Rich Markdown available: the user agreement renders natively")
        return True
    except Exception as exc:
        # The server never got as far as answering, so nothing is known about
        # the method. Staying on HTML is the safe reading of silence.
        _supported = None
        log.warning(f"⚠️ Could not probe sendRichMessage ({exc}) — staying on plain HTML")
        await utils.notify_developers(
            f"sendRichMessage preflight was inconclusive: {exc}",
            dedupe_key="richmd:preflight-failed",
        )
        return False


async def edit_markdown(
    bot: Bot,
    chat_id: int | str,
    message_id: int,
    markdown: str,
    reply_markup: types.InlineKeyboardMarkup | None = None,
) -> None:
    """Replace a message with a rich-Markdown document.

    Errors are raised, not swallowed: the caller already owns not-modified,
    flood-limit and fallback handling for the screen it is editing.
    """
    await bot(
        EditMessageRich(
            chat_id=chat_id,
            message_id=message_id,
            # Entity detection stays on: this is prose a person wrote, and the
            # operator contact in it is meant to become a mention.
            rich_message={"markdown": markdown},
            reply_markup=reply_markup,
        )
    )


async def note_method_lost(exc: TelegramNotFound) -> None:
    """The server answered the probe but lost the method — say so, then degrade."""
    global _supported
    _supported = False
    log.warning(f"⚠️ sendRichMessage disappeared mid-run ({exc}) — falling back to plain HTML")
    await utils.notify_developers(
        f"Rich Markdown stopped working at runtime: {exc}",
        dedupe_key="richmd:lost",
    )
