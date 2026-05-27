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
STATE_ON = "✅"
STATE_OFF = "▫️"


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
    text = "\n".join(
        [
            i18n.t(language, "menu.private.title"),
            "",
            i18n.t(language, "menu.private.body"),
            "",
            "🌐 "
            + i18n.t(
                language,
                "menu.private.language",
                language_name=i18n.language_name(language),
            )
            + f" · {i18n.t(language, _private_language_source_short_key(language_source))}",
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
    text = "\n".join(
        [
            i18n.t(language, "menu.group.title"),
            "",
            i18n.t(language, "menu.group.admin_body" if is_admin else "menu.group.user_body"),
            "",
            f"⏰ <b>{quote_time}</b> · 💬 <b>{min_messages}+</b>",
            "🌐 "
            + i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language))
            + f" · {i18n.t(language, _group_language_source_short_key(group_language_source))}",
            _compact_toggle_line(
                language,
                [
                    ("settings.group.context.short", quote_context_enabled),
                    ("settings.group.boring_notice.short", boring_notice_enabled),
                    ("settings.group.pin.short", pin_enabled),
                ],
            ),
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
        "",
        f"<b>{i18n.t(language, 'settings.block.interface')}</b>",
        "🌐 "
        + i18n.t(
            language,
            "menu.private.language",
            language_name=i18n.language_name(language),
        ),
        f"↳ {i18n.t(language, _private_language_source_key(language_source))}",
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
            "",
            i18n.t(language, "settings.private.language_hint"),
            f"↳ {i18n.t(language, _private_language_source_key(language_source))}",
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
    lines = [
        i18n.t(language, "settings.group.title"),
        "",
        f"<b>{i18n.t(language, 'settings.block.quote_day')}</b>",
        f"⏰ {i18n.t(language, 'settings.group.time.line', time=quote_time)}",
        f"💬 {i18n.t(language, 'settings.group.min_messages.line', count=min_messages)}",
        "",
        f"<b>{i18n.t(language, 'settings.block.interface')}</b>",
        "🌐 "
        + i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language)),
        f"↳ {i18n.t(language, _group_language_source_short_key(group_language_source))}",
        "",
        f"<b>{i18n.t(language, 'settings.block.behavior')}</b>",
        _state_line(language, "settings.group.context.label", quote_context_enabled),
        f"↳ {i18n.t(language, 'settings.group.context.explain_short')}",
        _state_line(language, "settings.group.boring_notice.label", boring_notice_enabled),
        _state_line(language, "settings.group.pin.label", pin_enabled),
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
                )
            ],
            [
                types.InlineKeyboardButton(
                    text=_state_button_text(language, "settings.button.context", quote_context_enabled),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_TOGGLE_GROUP_SETTING, "context"),
                ),
            ],
            [
                types.InlineKeyboardButton(
                    text=_state_button_text(language, "settings.button.boring_notice", boring_notice_enabled),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_TOGGLE_GROUP_SETTING, "boring"),
                ),
                types.InlineKeyboardButton(
                    text=_state_button_text(language, "settings.button.pin", pin_enabled),
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
            "",
            i18n.t(language, "settings.group.time.hint"),
            "",
            f"⏰ {i18n.t(language, 'settings.group.time.line', time=quote_time)}",
            f"🌍 {i18n.t(language, 'settings.group.time.timezone', timezone=timezone_name)}",
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
            "",
            i18n.t(language, "settings.group.min_messages.hint"),
            "",
            f"💬 {i18n.t(language, 'settings.group.min_messages.line', count=min_messages)}",
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
            "🌐 "
            + i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(current_language)),
            f"↳ {i18n.t(language, source_key)}",
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


def _state_icon(enabled: bool) -> str:
    return STATE_ON if enabled else STATE_OFF


def _state_line(language: str, label_key: str, enabled: bool) -> str:
    return f"{_state_icon(enabled)} {i18n.t(language, label_key)}"


def _state_button_text(language: str, label_key: str, enabled: bool) -> str:
    return f"{_state_icon(enabled)} {i18n.t(language, label_key)}"


def _compact_toggle_line(language: str, items: list[tuple[str, bool]]) -> str:
    return " · ".join(f"{_state_icon(enabled)} {i18n.t(language, key)}" for key, enabled in items)


def _private_language_source_key(language_source: str | None) -> str:
    if language_source == i18n.LANGUAGE_SOURCE_MANUAL:
        return "settings.private.language_source_manual"
    return "settings.private.language_source_telegram"


def _private_language_source_short_key(language_source: str | None) -> str:
    if language_source == i18n.LANGUAGE_SOURCE_MANUAL:
        return "settings.private.language_source_manual_short"
    return "settings.private.language_source_telegram_short"


def _group_language_source_short_key(language_source: str | None) -> str:
    if language_source == i18n.LANGUAGE_SOURCE_MANUAL:
        return "menu.language.source_manual_short"
    if language_source == i18n.LANGUAGE_SOURCE_AUTO:
        return "menu.language.source_auto_short"
    return "menu.language.source_default_short"
