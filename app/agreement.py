"""User-agreement / privacy document rendering and inline keyboards.

The document is rendered as Telegram HTML with an expandable blockquote so the
long body stays tidy. Group admins accept it via inline buttons; the same view
backs the /privacy command (read-only outside groups).
"""
from __future__ import annotations

from aiogram import types

from . import i18n


CALLBACK_PREFIX = "agree"
ACTION_VIEW = "v"
ACTION_ACCEPT = "a"

_LANGUAGE_ORDER = ("uk", "ru", "en", "de")


def callback_data(action: str, language: str) -> str:
    return f"{CALLBACK_PREFIX}:{action}:{language}"


def parse_callback(data: str | None) -> tuple[str, str] | None:
    if not data or not data.startswith(f"{CALLBACK_PREFIX}:"):
        return None
    parts = data.split(":", 2)
    if len(parts) != 3:
        return None
    return parts[1], parts[2]


def _language_row(action: str, current_language: str) -> list[types.InlineKeyboardButton]:
    row: list[types.InlineKeyboardButton] = []
    for code in _LANGUAGE_ORDER:
        name = i18n.language_name(code)
        text = f"· {name} ·" if code == current_language else name
        row.append(
            types.InlineKeyboardButton(text=text, callback_data=callback_data(action, code))
        )
    return row


def build_welcome_keyboard(language: str) -> types.InlineKeyboardMarkup:
    """Single button under the group welcome message: open the agreement."""
    return types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "agreement.view_button"),
                    callback_data=callback_data(ACTION_VIEW, language),
                )
            ]
        ]
    )


def build_document(
    language: str,
    *,
    can_accept: bool,
    accepted: bool,
) -> tuple[str, types.InlineKeyboardMarkup]:
    """Full agreement document with a language switcher (and Accept when allowed)."""
    parts = [
        i18n.t(language, "agreement.title"),
        i18n.t(language, "agreement.summary"),
        f"<blockquote expandable>{i18n.t(language, 'agreement.body')}</blockquote>",
    ]
    if accepted:
        parts.append(i18n.t(language, "agreement.already"))
    parts.append(i18n.t(language, "agreement.doc_footer"))
    text = "\n\n".join(part for part in parts if part)

    rows: list[list[types.InlineKeyboardButton]] = [_language_row(ACTION_VIEW, language)]
    if can_accept and not accepted:
        rows.append(
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "agreement.accept_button"),
                    callback_data=callback_data(ACTION_ACCEPT, language),
                )
            ]
        )
    return text, types.InlineKeyboardMarkup(inline_keyboard=rows)


def build_accepted(language: str) -> str:
    """Confirmation shown in-place after an admin accepts."""
    return "\n\n".join(
        part
        for part in (
            i18n.t(language, "agreement.accepted"),
            i18n.t(language, "agreement.accepted_admin_hint"),
        )
        if part
    )
