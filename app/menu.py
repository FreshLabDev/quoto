from __future__ import annotations

from dataclasses import dataclass

from aiogram import types

from . import i18n


CALLBACK_PREFIX = "menu"
SCOPE_PRIVATE = "p"
SCOPE_GROUP = "g"

ACTION_HOME = "home"
ACTION_CLOSE = "close"
ACTION_GROUP_LANGUAGE = "lang"
ACTION_SET_GROUP_LANGUAGE = "setlang"
ACTION_CHAT_STATS = "chatstats"
ACTION_USER_STATS = "userstats"

LANGUAGE_BUTTON_ORDER = ("uk", "ru", "en", "de")


@dataclass(frozen=True)
class MenuCallback:
    owner_id: int
    scope: str
    action: str
    payload: str | None = None


def callback_data(owner_id: int, scope: str, action: str, payload: str | None = None) -> str:
    parts = [CALLBACK_PREFIX, str(owner_id), scope, action]
    if payload:
        parts.append(payload)
    return ":".join(parts)


def parse_callback_data(data: str | None) -> MenuCallback | None:
    if not data or not data.startswith(f"{CALLBACK_PREFIX}:"):
        return None
    parts = data.split(":", 4)
    if len(parts) < 4:
        return None
    try:
        owner_id = int(parts[1])
    except ValueError:
        return None
    return MenuCallback(
        owner_id=owner_id,
        scope=parts[2],
        action=parts[3],
        payload=parts[4] if len(parts) == 5 else None,
    )


def build_private_home(
    *,
    owner_id: int,
    language: str,
    bot_username: str,
) -> tuple[str, types.InlineKeyboardMarkup]:
    text = "\n".join(
        [
            i18n.t(language, "menu.private.title"),
            "",
            i18n.t(language, "menu.private.body"),
            "",
            "<blockquote>"
            + i18n.t(
                language,
                "menu.private.language",
                language_name=i18n.language_name(language),
            )
            + "\n"
            + i18n.t(language, "menu.private.language_note")
            + "</blockquote>",
        ]
    )
    keyboard = types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "private.add_to_group"),
                    url=f"https://t.me/{bot_username}?startgroup=new",
                )
            ],
            [_close_button(owner_id, SCOPE_PRIVATE, language)],
        ]
    )
    return text, keyboard


def build_group_home(
    *,
    owner_id: int,
    language: str,
    group_language: str,
    group_language_source: str | None,
    is_admin: bool,
    quote_time: str,
    min_messages: int,
) -> tuple[str, types.InlineKeyboardMarkup]:
    source_key = (
        "menu.language.source_manual"
        if group_language_source == i18n.LANGUAGE_SOURCE_MANUAL
        else "menu.language.source_auto"
        if group_language_source == i18n.LANGUAGE_SOURCE_AUTO
        else "menu.language.source_default"
    )
    rows = [
        i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language)),
        i18n.t(language, source_key),
        i18n.t(language, "menu.group.schedule_line", time=quote_time, min_messages=min_messages),
    ]
    text = "\n".join(
        [
            i18n.t(language, "menu.group.title"),
            "",
            i18n.t(language, "menu.group.admin_body" if is_admin else "menu.group.user_body"),
            "",
            "<blockquote>" + "\n".join(rows) + "</blockquote>",
        ]
    )

    keyboard_rows: list[list[types.InlineKeyboardButton]] = [
        [
            types.InlineKeyboardButton(
                text=i18n.t(language, "menu.button.user_stats"),
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_USER_STATS),
            ),
            types.InlineKeyboardButton(
                text=i18n.t(language, "menu.button.chat_stats"),
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_CHAT_STATS),
            ),
        ]
    ]
    if is_admin:
        keyboard_rows.extend(
            [
                [
                    types.InlineKeyboardButton(
                        text=i18n.t(language, "menu.button.group_language"),
                        callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_LANGUAGE),
                    )
                ],
            ]
        )
    keyboard_rows.append([_close_button(owner_id, SCOPE_GROUP, language)])
    return text, types.InlineKeyboardMarkup(inline_keyboard=keyboard_rows)


def build_group_language(
    *,
    owner_id: int,
    language: str,
    current_language: str,
    language_source: str | None,
) -> tuple[str, types.InlineKeyboardMarkup]:
    source_key = (
        "menu.language.source_manual"
        if language_source == i18n.LANGUAGE_SOURCE_MANUAL
        else "menu.language.source_auto"
        if language_source == i18n.LANGUAGE_SOURCE_AUTO
        else "menu.language.source_default"
    )
    text = "\n".join(
        [
            i18n.t(language, "menu.language.title"),
            "",
            "<blockquote>"
            + i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(current_language))
            + "\n"
            + i18n.t(language, source_key)
            + "</blockquote>",
            "",
            i18n.t(language, "menu.language.hint"),
        ]
    )
    rows: list[list[types.InlineKeyboardButton]] = []
    buttons: list[types.InlineKeyboardButton] = []
    for code in LANGUAGE_BUTTON_ORDER:
        indicator = "◉" if code == current_language else "○"
        buttons.append(
            types.InlineKeyboardButton(
                text=f"{indicator} {i18n.language_name(code)}",
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_SET_GROUP_LANGUAGE, code),
            )
        )
    rows.extend([buttons[:2], buttons[2:]])
    rows.append(
        [
            _back_button(owner_id, SCOPE_GROUP, language),
            _close_button(owner_id, SCOPE_GROUP, language),
        ]
    )
    return text, types.InlineKeyboardMarkup(inline_keyboard=rows)


def build_back_close_keyboard(owner_id: int, scope: str, language: str) -> types.InlineKeyboardMarkup:
    return types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                _back_button(owner_id, scope, language),
                _close_button(owner_id, scope, language),
            ]
        ]
    )


def _back_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.back"),
        callback_data=callback_data(owner_id, scope, ACTION_HOME),
    )


def _close_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.close"),
        callback_data=callback_data(owner_id, scope, ACTION_CLOSE),
    )
