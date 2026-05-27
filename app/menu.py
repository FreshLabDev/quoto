from __future__ import annotations

from dataclasses import dataclass

from aiogram import types

from . import i18n


CALLBACK_PREFIX = "menu"
SCOPE_PRIVATE = "p"
SCOPE_GROUP = "g"

ACTION_HOME = "home"
ACTION_CLOSE = "close"
ACTION_SETTINGS = "settings"
ACTION_GROUP_LANGUAGE = "lang"
ACTION_SET_GROUP_LANGUAGE = "setlang"
ACTION_AUTO_GROUP_LANGUAGE = "autolang"
ACTION_PRIVATE_LANGUAGE = "plang"
ACTION_SET_PRIVATE_LANGUAGE = "setplang"
ACTION_AUTO_PRIVATE_LANGUAGE = "autoplang"
ACTION_GROUP_TIME = "time"
ACTION_GROUP_TIME_ADJUST = "timeadj"
ACTION_GROUP_MIN_MESSAGES = "minmsg"
ACTION_GROUP_MIN_ADJUST = "minadj"
ACTION_TOGGLE_GROUP_SETTING = "toggle"
ACTION_CHAT_STATS = "chatstats"
ACTION_USER_STATS = "userstats"

LANGUAGE_BUTTON_ORDER = ("uk", "ru", "en", "de")
TOGGLE_ON = "◉"
TOGGLE_OFF = "◎"
CHEVRON = "›"


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
    language_source: str | None,
    bot_username: str,
) -> tuple[str, types.InlineKeyboardMarkup]:
    source_key = _private_language_source_key(language_source)
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
            + i18n.t(language, source_key)
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
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "menu.button.settings"),
                    callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_SETTINGS),
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
    boring_notice_enabled: bool,
    pin_enabled: bool,
    quote_context_enabled: bool,
) -> tuple[str, types.InlineKeyboardMarkup]:
    source_key = (
        "menu.language.source_manual"
        if group_language_source == i18n.LANGUAGE_SOURCE_MANUAL
        else "menu.language.source_auto"
        if group_language_source == i18n.LANGUAGE_SOURCE_AUTO
        else "menu.language.source_default"
    )
    rows = [
        _chevron_line(i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language))),
        _chevron_line(i18n.t(language, "menu.group.schedule_line", time=quote_time, min_messages=min_messages)),
        _toggle_line(i18n.t(language, "settings.group.context.short"), quote_context_enabled),
        _toggle_line(i18n.t(language, "settings.group.boring_notice.short"), boring_notice_enabled),
        _toggle_line(i18n.t(language, "settings.group.pin.short"), pin_enabled),
    ]
    text = "\n".join(
        [
            i18n.t(language, "menu.group.title"),
            "",
            i18n.t(language, "menu.group.admin_body" if is_admin else "menu.group.user_body"),
            "",
            "<blockquote>" + "\n".join(rows) + "</blockquote>",
            "",
            i18n.t(language, source_key),
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
                        text=i18n.t(language, "menu.button.settings"),
                        callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_SETTINGS),
                    )
                ],
            ]
        )
    keyboard_rows.append([_close_button(owner_id, SCOPE_GROUP, language)])
    return text, types.InlineKeyboardMarkup(inline_keyboard=keyboard_rows)


def build_private_settings(
    *,
    owner_id: int,
    language: str,
    language_source: str | None,
    bot_username: str,
) -> tuple[str, types.InlineKeyboardMarkup]:
    lines = [
        i18n.t(language, "settings.private.title"),
        i18n.t(language, "settings.private.hint"),
        "",
        *_section(
            i18n.t(language, "settings.block.interface"),
            [
                _chevron_line(
                    i18n.t(
                        language,
                        "menu.private.language",
                        language_name=i18n.language_name(language),
                    )
                ),
                i18n.t(language, _private_language_source_key(language_source)),
            ],
        ),
    ]
    if lines and lines[-1] == "":
        lines.pop()
    keyboard = types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.language"),
                    callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_PRIVATE_LANGUAGE),
                )
            ],
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "private.add_to_group"),
                    url=f"https://t.me/{bot_username}?startgroup=new",
                )
            ],
            [
                _back_button(owner_id, SCOPE_PRIVATE, language, ACTION_HOME),
                _close_button(owner_id, SCOPE_PRIVATE, language),
            ],
        ]
    )
    return "\n".join(lines), keyboard


def build_private_language(
    *,
    owner_id: int,
    language: str,
    current_language: str,
    language_source: str | None,
) -> tuple[str, types.InlineKeyboardMarkup]:
    text = "\n".join(
        [
            i18n.t(language, "settings.private.language_title"),
            i18n.t(language, "settings.private.language_hint"),
            "",
            "<blockquote>"
            + "\n".join(
                [
                    f"{_toggle_icon(code == current_language)} {i18n.language_name(code)}"
                    for code in LANGUAGE_BUTTON_ORDER
                ]
            )
            + "\n"
            + i18n.t(language, _private_language_source_key(language_source))
            + "</blockquote>",
        ]
    )
    rows: list[list[types.InlineKeyboardButton]] = []
    buttons: list[types.InlineKeyboardButton] = []
    for code in LANGUAGE_BUTTON_ORDER:
        buttons.append(
            types.InlineKeyboardButton(
                text=i18n.language_name(code),
                callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_SET_PRIVATE_LANGUAGE, code),
            )
        )
    rows.extend([buttons[:2], buttons[2:]])
    rows.append(
        [
            types.InlineKeyboardButton(
                text=i18n.t(language, "settings.private.telegram_language"),
                callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_AUTO_PRIVATE_LANGUAGE),
            )
        ]
    )
    rows.append(
        [
            _back_button(owner_id, SCOPE_PRIVATE, language, ACTION_SETTINGS),
            _close_button(owner_id, SCOPE_PRIVATE, language),
        ]
    )
    return text, types.InlineKeyboardMarkup(inline_keyboard=rows)


def build_group_settings(
    *,
    owner_id: int,
    language: str,
    group_language: str,
    group_language_source: str | None,
    quote_time: str,
    min_messages: int,
    boring_notice_enabled: bool,
    pin_enabled: bool,
    quote_context_enabled: bool,
) -> tuple[str, types.InlineKeyboardMarkup]:
    source_key = (
        "menu.language.source_manual"
        if group_language_source == i18n.LANGUAGE_SOURCE_MANUAL
        else "menu.language.source_auto"
        if group_language_source == i18n.LANGUAGE_SOURCE_AUTO
        else "menu.language.source_default"
    )
    lines = [
        i18n.t(language, "settings.group.title"),
        i18n.t(language, "settings.group.hint"),
        "",
        *_section(
            i18n.t(language, "settings.block.interface"),
            [
                _chevron_line(i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language))),
                i18n.t(language, source_key),
            ],
        ),
        *_section(
            i18n.t(language, "settings.block.quote_day"),
            [
                _chevron_line(i18n.t(language, "settings.group.time.line", time=quote_time)),
                _chevron_line(i18n.t(language, "settings.group.min_messages.line", count=min_messages)),
            ],
        ),
        *_section(
            i18n.t(language, "settings.block.behavior"),
            [
                _toggle_line(i18n.t(language, "settings.group.context.label"), quote_context_enabled),
                _toggle_line(i18n.t(language, "settings.group.boring_notice.label"), boring_notice_enabled),
                _toggle_line(i18n.t(language, "settings.group.pin.label"), pin_enabled),
            ],
        ),
        *_section(
            i18n.t(language, "settings.group.context.title"),
            [i18n.t(language, "settings.group.context.explain")],
        ),
    ]
    if lines and lines[-1] == "":
        lines.pop()

    keyboard = types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.language"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_LANGUAGE),
                ),
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.time"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_TIME),
                ),
            ],
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.min_messages"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_MIN_MESSAGES),
                ),
                types.InlineKeyboardButton(
                    text=_toggle_line(i18n.t(language, "settings.button.context"), quote_context_enabled),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_TOGGLE_GROUP_SETTING, "context"),
                ),
            ],
            [
                types.InlineKeyboardButton(
                    text=_toggle_line(i18n.t(language, "settings.button.boring_notice"), boring_notice_enabled),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_TOGGLE_GROUP_SETTING, "boring"),
                ),
                types.InlineKeyboardButton(
                    text=_toggle_line(i18n.t(language, "settings.button.pin"), pin_enabled),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_TOGGLE_GROUP_SETTING, "pin"),
                ),
            ],
            [
                _back_button(owner_id, SCOPE_GROUP, language, ACTION_HOME),
                _close_button(owner_id, SCOPE_GROUP, language),
            ],
        ]
    )
    return "\n".join(lines), keyboard


def build_group_time(
    *,
    owner_id: int,
    language: str,
    quote_time: str,
    timezone_name: str,
) -> tuple[str, types.InlineKeyboardMarkup]:
    text = "\n".join(
        [
            i18n.t(language, "settings.group.time.title"),
            i18n.t(language, "settings.group.time.hint"),
            "",
            "<blockquote>"
            + i18n.t(language, "settings.group.time.line", time=quote_time)
            + "\n"
            + i18n.t(language, "settings.group.time.timezone", timezone=timezone_name)
            + "</blockquote>",
        ]
    )
    return text, types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text="-1h",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_TIME_ADJUST, "-60"),
                ),
                types.InlineKeyboardButton(
                    text="+1h",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_TIME_ADJUST, "60"),
                ),
            ],
            [
                types.InlineKeyboardButton(
                    text="-15m",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_TIME_ADJUST, "-15"),
                ),
                types.InlineKeyboardButton(
                    text="+15m",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_TIME_ADJUST, "15"),
                ),
            ],
            [
                _back_button(owner_id, SCOPE_GROUP, language, ACTION_SETTINGS),
                _close_button(owner_id, SCOPE_GROUP, language),
            ],
        ]
    )


def build_group_min_messages(
    *,
    owner_id: int,
    language: str,
    min_messages: int,
) -> tuple[str, types.InlineKeyboardMarkup]:
    text = "\n".join(
        [
            i18n.t(language, "settings.group.min_messages.title"),
            i18n.t(language, "settings.group.min_messages.hint"),
            "",
            "<blockquote>"
            + i18n.t(language, "settings.group.min_messages.line", count=min_messages)
            + "</blockquote>",
        ]
    )
    return text, types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text="-5",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_MIN_ADJUST, "-5"),
                ),
                types.InlineKeyboardButton(
                    text="+5",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_MIN_ADJUST, "5"),
                ),
            ],
            [
                types.InlineKeyboardButton(
                    text="-1",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_MIN_ADJUST, "-1"),
                ),
                types.InlineKeyboardButton(
                    text="+1",
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_MIN_ADJUST, "1"),
                ),
            ],
            [
                _back_button(owner_id, SCOPE_GROUP, language, ACTION_SETTINGS),
                _close_button(owner_id, SCOPE_GROUP, language),
            ],
        ]
    )


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
        indicator = _toggle_icon(code == current_language)
        buttons.append(
            types.InlineKeyboardButton(
                text=f"{indicator} {i18n.language_name(code)}",
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_SET_GROUP_LANGUAGE, code),
            )
        )
    rows.extend([buttons[:2], buttons[2:]])
    rows.append(
        [
            types.InlineKeyboardButton(
                text=i18n.t(language, "settings.group.language_auto"),
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_AUTO_GROUP_LANGUAGE),
            )
        ]
    )
    rows.append(
        [
            _back_button(owner_id, SCOPE_GROUP, language, ACTION_SETTINGS),
            _close_button(owner_id, SCOPE_GROUP, language),
        ]
    )
    return text, types.InlineKeyboardMarkup(inline_keyboard=rows)


def build_back_close_keyboard(
    owner_id: int,
    scope: str,
    language: str,
    back_action: str = ACTION_HOME,
) -> types.InlineKeyboardMarkup:
    return types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                _back_button(owner_id, scope, language, back_action),
                _close_button(owner_id, scope, language),
            ]
        ]
    )


def _back_button(
    owner_id: int,
    scope: str,
    language: str,
    action: str = ACTION_HOME,
) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.back"),
        callback_data=callback_data(owner_id, scope, action),
    )


def _close_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.close"),
        callback_data=callback_data(owner_id, scope, ACTION_CLOSE),
    )


def _toggle_icon(enabled: bool) -> str:
    return TOGGLE_ON if enabled else TOGGLE_OFF


def _toggle_line(label: str, enabled: bool) -> str:
    return f"{_toggle_icon(enabled)} {label}"


def _chevron_line(label: str) -> str:
    return f"{CHEVRON} {label}"


def _section(title: str, rows: list[str]) -> list[str]:
    return [f"<b>{title}</b>", "<blockquote>" + "\n".join(rows) + "</blockquote>", ""]


def _private_language_source_key(language_source: str | None) -> str:
    if language_source == i18n.LANGUAGE_SOURCE_MANUAL:
        return "settings.private.language_source_manual"
    return "settings.private.language_source_telegram"
