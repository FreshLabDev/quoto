from __future__ import annotations

from collections.abc import Callable, Sequence

from aiogram import types
from aiogram.enums import ButtonStyle

from . import i18n
from .version import CONTACT, CONTACT_URL, LICENSE, REPOSITORY, REPOSITORY_URL, VERSION


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
ACTION_ABOUT = "about"
ACTION_AGREEMENT = "doc"

SECTION_HOME = "home"
SECTION_LANGUAGE = "lang"
SECTION_SCHEDULE = "sched"
SECTION_TIMEZONE = "tz"
SECTION_BEHAVIOR = "behavior"
SECTION_STATS = "stats"
SECTION_ABOUT = "about"

# The scoring provider named on the About card. Which model is used is a
# setting; who evaluates is a fact about the product.
EVALUATOR = "OpenRouter"

# There is no language list here on purpose. The set is one list in one order
# and it lives in `i18n.language_options`; both of quoto's pickers read it from
# there, so neither can drift into an order or a label format of its own.

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


def _quote(lines: Sequence[str], *, expandable: bool = False) -> str:
    # Blank entries are kept: a caller uses one to group rows inside the quote.
    # Only a quote with nothing in it at all disappears.
    body = "\n".join(lines)
    if not body.strip():
        return ""
    tag = "<blockquote expandable>" if expandable else "<blockquote>"
    return f"{tag}{body}</blockquote>"


def panel(
    title: str,
    hint: str = "",
    lines: Sequence[str] = (),
    *,
    version: str | None = None,
    expandable: bool = False,
) -> str:
    """A panel is title, hint, quote.

    Every screen quoto draws is built here: the name of the screen in bold, one
    italic line saying what it is for, then only what the buttons below cannot
    say themselves, inside a blockquote. Titles and hints arrive as plain text
    and are marked up here, so a screen cannot end up with its own shape by
    forgetting a tag in a locale file.

    `version` is the About card's one addition — the family states the version
    beside the name on the title line. `expandable` is the agreement's: a legal
    document is long enough that Telegram should fold it.
    """
    head = f"<b>{title}</b>"
    if version:
        head = f"{head} · <i>{version}</i>"
    if hint:
        head = f"{head}\n<i>{hint}</i>"
    body = _quote(lines, expandable=expandable)
    return f"{head}\n\n{body}" if body else head


def _close_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    """Close dismisses the panel, so it is the one destructive button quoto has."""
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.close"),
        callback_data=callback_data(owner_id, scope, ACTION_CLOSE),
        style=ButtonStyle.DANGER,
    )


def _back_button(owner_id: int, scope: str, language: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=i18n.t(language, "menu.button.back"),
        callback_data=callback_data(owner_id, scope, ACTION_HOME),
    )


def nav_row(owner_id: int, scope: str, language: str) -> list[types.InlineKeyboardButton]:
    """Back, and Close only in a group: a private panel has nothing to close."""
    row = [_back_button(owner_id, scope, language)]
    if scope == SCOPE_GROUP:
        row.append(_close_button(owner_id, scope, language))
    return row


def _document_row(owner_id: int, scope: str, language: str) -> list[types.InlineKeyboardButton]:
    """The two read-only tabs both panels carry: the agreement and About."""
    return [
        types.InlineKeyboardButton(
            text=i18n.t(language, "menu.button.agreement"),
            callback_data=callback_data(owner_id, scope, ACTION_AGREEMENT),
        ),
        types.InlineKeyboardButton(
            text=i18n.t(language, "menu.button.about"),
            callback_data=callback_data(owner_id, scope, ACTION_ABOUT),
        ),
    ]


def about_text(language: str) -> str:
    """The family About card: name, version, one line of purpose, then the
    facts as `key · value` rows. The repository is a link in the text, so no
    button duplicates it."""
    return panel(
        "Quoto",
        i18n.t(language, "about.tagline"),
        [
            f"{i18n.t(language, 'about.eval')} · {EVALUATOR}",
            f"{i18n.t(language, 'about.sources')} · "
            f'<a href="{REPOSITORY_URL}">{REPOSITORY}</a> · {LICENSE}',
            f'{i18n.t(language, "about.admin")} · <a href="{CONTACT_URL}">{CONTACT}</a>',
        ],
        version=f"v{VERSION}",
    )


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


def _selectable(
    text: str, callback: str, *, selected: bool
) -> types.InlineKeyboardButton:
    """One option of a set the person picks exactly one of: the glyph says
    chosen or not chosen on every option, and the chosen one is also painted
    Success, because Success means "this is the state you are in"."""
    return types.InlineKeyboardButton(
        text=f"{_toggle_icon(selected)} {text}",
        callback_data=callback,
        style=ButtonStyle.SUCCESS if selected else None,
    )


def language_rows(
    current_language: str,
    callback_for: Callable[[str], str],
) -> list[list[types.InlineKeyboardButton]]:
    """The family language grid: every language quoto speaks, in the family
    order, flag and native name, two to a row. Both of quoto's pickers — the
    panel's and the agreement's — render through here, and adding a locale adds
    a button without touching this."""
    codes = i18n.language_options()
    buttons = [
        _selectable(
            i18n.language_label(code),
            callback_for(code),
            selected=code == current_language,
        )
        for code in codes
    ]
    return [buttons[index : index + 2] for index in range(0, len(buttons), 2)]


def _language_buttons(
    owner_id: int,
    scope: str,
    action: str,
    current_language: str,
) -> list[list[types.InlineKeyboardButton]]:
    return language_rows(
        current_language,
        lambda code: callback_data(owner_id, scope, action, code),
    )


def _timezone_buttons(owner_id: int, current_timezone: str) -> list[list[types.InlineKeyboardButton]]:
    buttons = [
        _selectable(
            tz_name,
            callback_data(owner_id, SCOPE_GROUP, ACTION_SET_GROUP_TIMEZONE, tz_name),
            selected=tz_name == current_timezone,
        )
        for tz_name in TIMEZONE_CHOICES
    ]
    return [buttons[index : index + 2] for index in range(0, len(buttons), 2)]


# ── private hub ─────────────────────────────────────────────────────────


def build_private_panel(
    *,
    owner_id: int,
    language: str,
    language_source: str | None,
    bot_username: str,
    section: str = SECTION_HOME,
) -> tuple[str, types.InlineKeyboardMarkup]:
    source = i18n.t(language, _private_language_source_key(language_source))

    if section == SECTION_ABOUT:
        return about_text(language), types.InlineKeyboardMarkup(
            inline_keyboard=[nav_row(owner_id, SCOPE_PRIVATE, language)]
        )

    if section == SECTION_LANGUAGE:
        # Which language is current is on the buttons, so the only thing left
        # to say is where that language came from.
        text = panel(
            i18n.t(language, "settings.private.language_title"),
            i18n.t(language, "settings.private.language_hint"),
            [source],
        )
        rows = _language_buttons(owner_id, SCOPE_PRIVATE, ACTION_SET_PRIVATE_LANGUAGE, language)
        rows.append(
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.private.telegram_language"),
                    callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_AUTO_PRIVATE_LANGUAGE),
                )
            ]
        )
        rows.append(nav_row(owner_id, SCOPE_PRIVATE, language))
        return text, types.InlineKeyboardMarkup(inline_keyboard=rows)

    text = panel(
        i18n.t(language, "menu.private.title"),
        i18n.t(language, "menu.private.body"),
        [
            i18n.t(language, "menu.private.language", language_name=i18n.language_name(language)),
            source,
        ],
    )
    keyboard = types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.button.language"),
                    callback_data=callback_data(owner_id, SCOPE_PRIVATE, ACTION_PRIVATE_LANGUAGE),
                )
            ],
            [
                # Quoto does nothing in a direct chat, so the one thing this
                # screen leads to is putting it in a group.
                types.InlineKeyboardButton(
                    text=i18n.t(language, "private.add_to_group"),
                    url=f"https://t.me/{bot_username}?startgroup=new",
                    style=ButtonStyle.PRIMARY,
                )
            ],
            _document_row(owner_id, SCOPE_PRIVATE, language),
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
    media_analysis_enabled: bool = True,
) -> list[str]:
    """What the group panel's own buttons cannot say: how quoto is set up here.
    The buttons on this screen open sections, they do not carry these values,
    so reading them off is not the body repeating the keyboard."""
    return [
        i18n.t(language, "menu.group.language_line", language_name=i18n.language_name(group_language)),
        i18n.t(language, "menu.group.schedule_line", time=quote_time, min_messages=min_messages),
        _toggle_line(i18n.t(language, "settings.group.context.short"), quote_context_enabled),
        _toggle_line(i18n.t(language, "settings.group.boring_notice.short"), boring_notice_enabled),
        _toggle_line(i18n.t(language, "settings.group.pin.short"), pin_enabled),
        _toggle_line(i18n.t(language, "settings.group.media.short"), media_analysis_enabled),
    ]


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
    media_analysis_enabled: bool = True,
    section: str = SECTION_HOME,
    stats_text: str | None = None,
    stats_view: str = "user",
) -> tuple[str, types.InlineKeyboardMarkup]:
    if section == SECTION_ABOUT:
        return about_text(language), types.InlineKeyboardMarkup(
            inline_keyboard=[nav_row(owner_id, SCOPE_GROUP, language)]
        )

    if section == SECTION_LANGUAGE:
        # Which language is current is on the buttons; where it came from is not.
        text = panel(
            i18n.t(language, "menu.language.title"),
            i18n.t(language, "menu.language.hint"),
            [i18n.t(language, _group_language_source_key(group_language_source))],
        )
        rows = _language_buttons(owner_id, SCOPE_GROUP, ACTION_SET_GROUP_LANGUAGE, group_language)
        rows.append(
            [
                types.InlineKeyboardButton(
                    text=i18n.t(language, "settings.group.language_auto"),
                    callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_AUTO_GROUP_LANGUAGE),
                )
            ]
        )
        rows.append(nav_row(owner_id, SCOPE_GROUP, language))
        return text, types.InlineKeyboardMarkup(inline_keyboard=rows)

    if section == SECTION_SCHEDULE:
        # The buttons here are nudges, not values: nothing on the keyboard says
        # what the time and the threshold currently are.
        text = panel(
            i18n.t(language, "settings.group.quote_day.title"),
            i18n.t(language, "settings.group.quote_day.hint"),
            [
                i18n.t(language, "settings.group.time.line", time=quote_time),
                i18n.t(language, "settings.group.min_messages.line", count=min_messages),
                i18n.t(language, "settings.group.time.timezone", timezone=timezone_name),
            ],
        )
        keyboard = types.InlineKeyboardMarkup(
            inline_keyboard=[
                # A clock delta reads the same in every language, and the value
                # it moves is on the line right above it. Spelling the units out
                # would be a word to translate sixteen times for nothing — and
                # for a long while it was the Russian word, in every locale.
                [
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "-60", "−1:00"),
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "60", "+1:00"),
                ],
                [
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "-15", "−0:15"),
                    _adjust_button(owner_id, ACTION_GROUP_TIME_ADJUST, "15", "+0:15"),
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
                nav_row(owner_id, SCOPE_GROUP, language),
            ]
        )
        return text, keyboard

    if section == SECTION_TIMEZONE:
        # The chosen zone is marked on its own button, so repeating it here
        # would be the body saying what the keyboard already says.
        text = panel(
            i18n.t(language, "settings.group.tz.title"),
            i18n.t(language, "settings.group.tz.hint"),
        )
        rows = _timezone_buttons(owner_id, timezone_name)
        rows.append(nav_row(owner_id, SCOPE_GROUP, language))
        return text, types.InlineKeyboardMarkup(inline_keyboard=rows)

    if section == SECTION_BEHAVIOR:
        # Every switch and its state is on a button below. What the buttons
        # cannot fit is what "context" actually means, so that is the body.
        text = panel(
            i18n.t(language, "settings.group.publication.title"),
            i18n.t(language, "settings.group.publication.hint"),
            [i18n.t(language, "settings.group.context.explain")],
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
                [
                    types.InlineKeyboardButton(
                        text=_toggle_line(i18n.t(language, "settings.button.media"), media_analysis_enabled),
                        callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_TOGGLE_GROUP_SETTING, "media"),
                    ),
                ],
                nav_row(owner_id, SCOPE_GROUP, language),
            ]
        )
        return text, keyboard

    if section == SECTION_STATS:
        # The numbers are built by the caller, through the same panel helper.
        text = stats_text or panel(
            i18n.t(language, "user_stats.title"),
            i18n.t(language, "user_stats.hint"),
            [i18n.t(language, "user_stats.missing")],
        )
        keyboard = types.InlineKeyboardMarkup(
            inline_keyboard=[
                # Two tabs over one set of numbers: the open one is the state
                # the screen is in, not an action.
                [
                    _selectable(
                        i18n.t(language, "menu.button.user_stats"),
                        callback_data(owner_id, SCOPE_GROUP, ACTION_USER_STATS),
                        selected=stats_view == "user",
                    ),
                    _selectable(
                        i18n.t(language, "menu.button.chat_stats"),
                        callback_data(owner_id, SCOPE_GROUP, ACTION_CHAT_STATS),
                        selected=stats_view == "chat",
                    ),
                ],
                nav_row(owner_id, SCOPE_GROUP, language),
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
    text = panel(i18n.t(language, "menu.group.title"), i18n.t(language, body_key), dashboard)

    rows: list[list[types.InlineKeyboardButton]] = [
        [
            # Settings are set once; the standings are what the panel is
            # reopened for, and for a non-admin they are the only thing here.
            types.InlineKeyboardButton(
                text=i18n.t(language, "menu.button.stats"),
                callback_data=callback_data(owner_id, SCOPE_GROUP, ACTION_USER_STATS),
                style=ButtonStyle.PRIMARY,
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
    rows.append(_document_row(owner_id, SCOPE_GROUP, language))
    # Home is the top of the panel, so it is the one screen with nothing to go
    # back to. It still gets Close, from the same helper every other screen
    # uses, because in a group the panel is a message in somebody else's feed.
    rows.append([_close_button(owner_id, SCOPE_GROUP, language)])
    return text, types.InlineKeyboardMarkup(inline_keyboard=rows)


def _adjust_button(owner_id: int, action: str, payload: str, label: str) -> types.InlineKeyboardButton:
    return types.InlineKeyboardButton(
        text=label,
        callback_data=callback_data(owner_id, SCOPE_GROUP, action, payload),
    )
