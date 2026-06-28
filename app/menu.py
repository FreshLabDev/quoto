from __future__ import annotations

from aiogram import types

from . import i18n


CALLBACK_PREFIX = "menu"
SCOPE_PRIVATE = "p"
SCOPE_GROUP = "g"

ACTION_HOME = "home"
ACTION_CLOSE = "close"
ACTION_GROUP_LANGUAGE = "lang"
ACTION_SET_GROUP_LANGUAGE = "setlang"
ACTION_AUTO_GROUP_LANGUAGE = "autolang"
ACTION_GROUP_SCHEDULE = "sched"
ACTION_GROUP_TIME_ADJUST = "timeadj"
ACTION_GROUP_MIN_ADJUST = "minadj"
ACTION_GROUP_TIMEZONE = "tz"
ACTION_SET_GROUP_TIMEZONE = "settz"
ACTION_GROUP_BEHAVIOR = "behavior"
ACTION_TOGGLE_GROUP_SETTING = "toggle"
ACTION_CHAT_STATS = "chatstats"
ACTION_USER_STATS = "userstats"
ACTION_PRIVATE_LANGUAGE = "plang"
ACTION_SET_PRIVATE_LANGUAGE = "setplang"
ACTION_AUTO_PRIVATE_LANGUAGE = "autoplang"

SECTION_HOME = "home"
SECTION_LANGUAGE = "lang"
SECTION_SCHEDULE = "sched"
SECTION_TIMEZONE = "tz"
SECTION_BEHAVIOR = "behavior"
SECTION_STATS = "stats"

LANGUAGE_BUTTON_ORDER = ("uk", "ru", "en", "de")
TIMEZONE_CHOICES = (
    "UTC",
    "Europe/London",
    "Europe/Berlin",
    "Europe/Kyiv",
    "Europe/Moscow",
    "America/New_York",
    "America/Chicago",
    "America/Los_Angeles",
    "America/Sao_Paulo",
    "Asia/Dubai",
    "Asia/Kolkata",
    "Asia/Tokyo",
    "Australia/Sydney",
)
TOGGLE_ON = "◉"
TOGGLE_OFF = "◎"


class MenuCallback:
    __slots__ = ("owner_id", "scope", "action", "payload")

    def __init__(self, owner_id: int, scope: str, action: str, payload: str | None = None) -> None:
        self.owner_id = owner_id
        self.scope = scope
        self.action = action
        self.payload = payload


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


# ── shared building blocks ──────────────────────────────────────────────


def _toggle_icon(enabled: bool) -> str:
    return TOGGLE_ON if enabled else TOGGLE_OFF


def _toggle_line(label: str, enabled: bool) -> str:
    return f"{_toggle_icon(enabled)} {label}"


def _quote(lines: list[str]) -> str:
    return "<blockquote>" + "\n".join(lines) + "</blockquote>"


def _screen(*blocks: str) -> str:
    return "\n\n".join(block for block in blocks if block)


def _close_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.close"),
        callback_data=callback_data(owner_id, scope, ACTION_CLOSE),
    )


def _back_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.back"),
        callback_data=callback_data(owner_id, scope, ACTION_HOME),
    )


def _nav_row(owner_id: int, scope: str, language: str) -> list[types.InlineKeyboardButton]:
    return [_back_button(owner_id, scope, language), _close_button(owner_id, scope, language)]


def _private_language_source_key(language_source: str | None) -> str:
    if language_source == i18n.LANGUAGE_SOURCE_MANUAL:
        return "settings.private.language_source_manual"
    return "settings.private.language_source_telegram"


def _group_language_source_key(language_source: str | None) -> str:
    if language_source == i18n.LANGUAGE_SOURCE_MANUAL:
        return "menu.language.source_manual"
    if language_source == i18n.LANGUAGE_SOURCE_AUTO:
        return "menu.language.source_auto"
    return "menu.language.source_default"


def _language_buttons(
    owner_id: int,
    scope: str,
    action: str,
    current_language: str,
    *,
    mark_current: bool,
) -> list[list[types.InlineKeyboardButton]]:
    buttons: list[types.InlineKeyboardButton] = []
    for code in LANGUAGE_BUTTON_ORDER:
        name = i18n.language_name(code)
        text = f"{_toggle_icon(code == current_language)} {name}" if mark_current else name
        buttons.append(
            types.InlineKeyboardButton(
                text=text,
                callback_data=callback_data(owner_id, scope, action, code),
            )
        )
    return [buttons[:2], buttons[2:]]


def _timezone_buttons(owner_id: int, current_timezone: str) -> list[list[types.InlineKeyboardButton]]:
    rows: list[list[types.InlineKeyboardButton]] = []
    row: list[types.InlineKeyboardButton] = []
    for tz_name in TIMEZONE_CHOICES:
        text = f"{_toggle_icon(tz_name == current_timezone)} {tz_name}"
        row.append(
            types.InlineKeyboardButton(
                text=text,
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_SET_GROUP_TIMEZONE, tz_name),
            )
        )
        if len(row) == 2:
            rows.append(row)
            row = []
    if row:
        rows.append(row)
    return rows


# ── private hub ─────────────────────────────────────────────────────────


def build_private_panel(
    *,
    owner_id: int,
    language: str,
    language_source: str | None,
    bot_username: str,
    section: str = SECTION_HOME,
) -> tuple[str, types.InlineKeyboardMarkup]:
    header = i18n.t(language, "menu.private.title")
    source = i18n.t(language, _private_language_source_key(language_source))

    if section == SECTION_LANGUAGE:
        readout = _quote(
            [
                i18n.t(language, "menu.private.language", language_name=i18n.language_name(language)),
                source,
            ]
        )
        text = _screen(
            header,
            i18n.t(language, "settings.private.language_title"),
            i18n.t(language, "settings.private.language_hint"),
            readout,
        )
        rows = _language_buttons(
            owner_id, SCOPE_PRIVATE, ACTION_SET_PRIVATE_LANGUAGE, language, mark_current=True
        )
        rows.append(
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.private.telegram_language"),
                    callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_AUTO_PRIVATE_LANGUAGE),
                )
            ]
        )
        rows.append(_nav_row(owner_id, SCOPE_PRIVATE, language))
        return text, types.InlineKeyboardMarkup(inline_keyboard=rows)

    readout = _quote(
        [
            i18n.t(language, "menu.private.language", language_name=i18n.language_name(language)),
            source,
        ]
    )
    text = _screen(header, i18n.t(language, "menu.private.body"), readout)
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
            [_close_button(owner_id, SCOPE_PRIVATE, language)],
        ]
    )
    return text, keyboard


# ── group hub ───────────────────────────────────────────────────────────


def _group_dashboard(
    *,
    language: str,
    group_language: str,
    quote_time: str,
    min_messages: int,
    boring_notice_enabled: bool,
    pin_enabled: bool,
    quote_context_enabled: bool,
) -> str:
    return _quote(
        [
            i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language)),
            i18n.t(language, "menu.group.schedule_line", time=quote_time, min_messages=min_messages),
            _toggle_line(i18n.t(language, "settings.group.context.short"), quote_context_enabled),
            _toggle_line(i18n.t(language, "settings.group.boring_notice.short"), boring_notice_enabled),
            _toggle_line(i18n.t(language, "settings.group.pin.short"), pin_enabled),
        ]
    )


def build_group_panel(
    *,
    owner_id: int,
    language: str,
    group_language: str,
    group_language_source: str | None,
    is_admin: bool,
    quote_time: str,
    min_messages: int,
    timezone_name: str,
    boring_notice_enabled: bool,
    pin_enabled: bool,
    quote_context_enabled: bool,
    section: str = SECTION_HOME,
    stats_text: str | None = None,
    stats_view: str = "user",
) -> tuple[str, types.InlineKeyboardMarkup]:
    header = i18n.t(language, "menu.group.title")

    if section == SECTION_LANGUAGE:
        readout = _quote(
            [
                i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language)),
                i18n.t(language, _group_language_source_key(group_language_source)),
            ]
        )
        text = _screen(
            header,
            i18n.t(language, "menu.language.title"),
            i18n.t(language, "menu.language.hint"),
            readout,
        )
        rows = _language_buttons(
            owner_id, SCOPE_GROUP, ACTION_SET_GROUP_LANGUAGE, group_language, mark_current=True
        )
        rows.append(
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.group.language_auto"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_AUTO_GROUP_LANGUAGE),
                )
            ]
        )
        rows.append(_nav_row(owner_id, SCOPE_GROUP, language))
        return text, types.InlineKeyboardMarkup(inline_keyboard=rows)

    if section == SECTION_SCHEDULE:
        readout = _quote(
            [
                i18n.t(language, "settings.group.time.line", time=quote_time),
                i18n.t(language, "settings.group.min_messages.line", count=min_messages),
                i18n.t(language, "settings.group.time.timezone", timezone=timezone_name),
            ]
        )
        text = _screen(
            header,
            i18n.t(language, "settings.group.quote_day.title"),
            i18n.t(language, "settings.group.quote_day.hint"),
            readout,
        )
        keyboard = types.InlineKeyboardMarkup(
            inline_keyboard=[
                [
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "-60", "−1 ч"),
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "60", "+1 ч"),
                ],
                [
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "-15", "−15 м"),
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "15", "+15 м"),
                ],
                [
                    _adjust_button(owner_id, ACTION_GROUP_MIN_ADJUST, "-5", "−5"),
                    _adjust_button(owner_id, ACTION_GROUP_MIN_ADJUST, "5", "+5"),
                    _adjust_button(owner_id, ACTION_GROUP_MIN_ADJUST, "-1", "−1"),
                    _adjust_button(owner_id, ACTION_GROUP_MIN_ADJUST, "1", "+1"),
                ],
                [
                    types.InlineKeyboardButton(
                        text=i18n.t(language, "settings.button.timezone"),
                        callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_TIMEZONE),
                    )
                ],
                _nav_row(owner_id, SCOPE_GROUP, language),
            ]
        )
        return text, keyboard

    if section == SECTION_TIMEZONE:
        readout = _quote(
            [i18n.t(language, "settings.group.time.timezone", timezone=timezone_name)]
        )
        text = _screen(
            header,
            i18n.t(language, "settings.group.tz.title"),
            i18n.t(language, "settings.group.tz.hint"),
            readout,
        )
        rows = _timezone_buttons(owner_id, timezone_name)
        rows.append(_nav_row(owner_id, SCOPE_GROUP, language))
        return text, types.InlineKeyboardMarkup(inline_keyboard=rows)

    if section == SECTION_BEHAVIOR:
        readout = _quote(
            [
                _toggle_line(i18n.t(language, "settings.group.context.label"), quote_context_enabled),
                _toggle_line(i18n.t(language, "settings.group.boring_notice.label"), boring_notice_enabled),
                _toggle_line(i18n.t(language, "settings.group.pin.label"), pin_enabled),
            ]
        )
        text = _screen(
            header,
            i18n.t(language, "settings.group.publication.title"),
            i18n.t(language, "settings.group.publication.hint"),
            readout,
            i18n.t(language, "settings.group.context.explain"),
        )
        keyboard = types.InlineKeyboardMarkup(
            inline_keyboard=[
                [
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
                _nav_row(owner_id, SCOPE_GROUP, language),
            ]
        )
        return text, keyboard

    if section == SECTION_STATS:
        text = _screen(header, stats_text or "")
        keyboard = types.InlineKeyboardMarkup(
            inline_keyboard=[
                [
                    types.InlineKeyboardButton(
                        text=_toggle_line(i18n.t(language, "menu.button.user_stats"), stats_view == "user"),
                        callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_USER_STATS),
                    ),
                    types.InlineKeyboardButton(
                        text=_toggle_line(i18n.t(language, "menu.button.chat_stats"), stats_view == "chat"),
                        callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_CHAT_STATS),
                    ),
                ],
                _nav_row(owner_id, SCOPE_GROUP, language),
            ]
        )
        return text, keyboard

    # SECTION_HOME — dashboard
    dashboard = _group_dashboard(
        language=language,
        group_language=group_language,
        quote_time=quote_time,
        min_messages=min_messages,
        boring_notice_enabled=boring_notice_enabled,
        pin_enabled=pin_enabled,
        quote_context_enabled=quote_context_enabled,
    )
    body_key = "menu.group.admin_body" if is_admin else "menu.group.user_body"
    text = _screen(header, i18n.t(language, body_key), dashboard)

    rows: list[list[types.InlineKeyboardButton]] = [
        [
            types.InlineKeyboardButton(
                text=i18n.t(language, "menu.button.stats"),
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_USER_STATS),
            )
        ]
    ]
    if is_admin:
        rows.append(
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.language"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_LANGUAGE),
                ),
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.quote_day"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_SCHEDULE),
                ),
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.publication"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_GROUP_BEHAVIOR),
                ),
            ]
        )
    rows.append([_close_button(owner_id, SCOPE_GROUP, language)])
    return text, types.InlineKeyboardMarkup(inline_keyboard=rows)


def _adjust_button(owner_id: int, action: str, payload: str, label: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=label,
        callback_data=callback_data(owner_id, SCOPE_GROUP, action, payload),
    )
