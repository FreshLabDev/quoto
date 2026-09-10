"""User-agreement / privacy document rendering and inline keyboards.

The document is authored once, as structure — a title, numbered sections, one
of them a list, an inline caution, and a signature — and rendered twice. On a
Bot API 10.3 server it goes out as rich Markdown, where Telegram draws real
headings, lists and blockquotes; anywhere else it falls back to Telegram HTML,
which can only imitate that with bold text inside an expandable blockquote.

Group admins accept it via inline buttons; the same view is a read-only tab of
the `/start` panel.
"""
from __future__ import annotations

from html import escape
from typing import Any, NamedTuple

from aiogram import types
from aiogram.enums import ButtonStyle

from . import i18n
from .menu import language_rows, panel
from .version import CONTACT, OPERATOR


CALLBACK_PREFIX = "agree"
ACTION_VIEW = "v"
ACTION_ACCEPT = "a"

# When this text last changed and when it started applying. The operator and the
# contact come from the project's identity, and the contact is substituted into
# the section that mentions it mid-sentence, so the whole document has one
# source for it rather than four locale copies.
DOCUMENT_VERSION = "1.0"
# ISO 8601: an effective date has to read the same in every locale.
EFFECTIVE_DATE = "2026-09-09"


class AgreementCallback(NamedTuple):
    action: str
    language: str
    # Set when the document was opened as a panel tab: the panel to return to.
    scope: str | None = None
    owner_id: int | None = None


class AgreementDocument(NamedTuple):
    markdown: str
    html: str
    keyboard: types.InlineKeyboardMarkup


def callback_data(
    action: str,
    language: str,
    *,
    scope: str | None = None,
    owner_id: int | None = None,
) -> str:
    data = f"{CALLBACK_PREFIX}:{action}:{language}"
    if scope and owner_id is not None:
        data = f"{data}:{scope}:{owner_id}"
    return data


def parse_callback(data: str | None) -> AgreementCallback | None:
    if not data or not data.startswith(f"{CALLBACK_PREFIX}:"):
        return None
    parts = data.split(":", 4)
    if len(parts) not in (3, 5):
        return None
    if len(parts) == 3:
        return AgreementCallback(parts[1], parts[2])
    try:
        owner_id = int(parts[4])
    except ValueError:
        return None
    return AgreementCallback(parts[1], parts[2], parts[3], owner_id)


def _language_rows(
    action: str,
    current_language: str,
    *,
    scope: str | None,
    owner_id: int | None,
) -> list[list[types.InlineKeyboardButton]]:
    """The document's language switcher is a language picker like any other, so
    it is drawn by the panel's grid rather than by a second format of its own."""
    return language_rows(
        current_language,
        lambda code: callback_data(action, code, scope=scope, owner_id=owner_id),
    )


def build_welcome_keyboard(language: str) -> types.InlineKeyboardMarkup:
    """Single button under the group welcome message: open the agreement."""
    return types.InlineKeyboardMarkup(
        inline_keyboard=[
            [
                # Until somebody reads and accepts this, quoto publishes
                # nothing — there is no other thing to do from here.
                types.InlineKeyboardButton(
                    text=i18n.t(language, "agreement.view_button"),
                    callback_data=callback_data(ACTION_VIEW, language),
                    style=ButtonStyle.PRIMARY,
                )
            ]
        ]
    )


def build_document(
    language: str,
    *,
    can_accept: bool,
    accepted: bool,
    scope: str | None = None,
    owner_id: int | None = None,
    nav_row: list[types.InlineKeyboardButton] | None = None,
) -> AgreementDocument:
    """The agreement in both renderings, with a language switcher and navigation.

    `scope` and `owner_id` are carried through every button so switching the
    document's language keeps it inside the panel it was opened from.
    """
    markdown = _render_markdown(language, can_accept=can_accept, accepted=accepted)
    html = _render_html(language, can_accept=can_accept, accepted=accepted)

    rows: list[list[types.InlineKeyboardButton]] = _language_rows(
        ACTION_VIEW, language, scope=scope, owner_id=owner_id
    )
    if can_accept and not accepted:
        rows.append(
            [
                # An admin who opened this screen came to accept; everything
                # else on it is reading material.
                types.InlineKeyboardButton(
                    text=i18n.t(language, "agreement.accept_button"),
                    callback_data=callback_data(
                        ACTION_ACCEPT, language, scope=scope, owner_id=owner_id
                    ),
                    style=ButtonStyle.PRIMARY,
                )
            ]
        )
    if nav_row:
        rows.append(list(nav_row))
    return AgreementDocument(markdown, html, types.InlineKeyboardMarkup(inline_keyboard=rows))


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


# ── rendering ───────────────────────────────────────────────────────────


def _sections(language: str) -> list[dict[str, Any]]:
    raw = i18n.value(language, "agreement.sections")
    return [section for section in raw if isinstance(section, dict)] if isinstance(raw, list) else []


def _text_of(section: dict[str, Any]) -> str:
    return str(section.get("text") or "").replace("{contact}", CONTACT)


def _signature_rows(language: str) -> list[tuple[str, str]]:
    return [
        (i18n.t(language, "agreement.signature.version_label"), DOCUMENT_VERSION),
        (i18n.t(language, "agreement.signature.effective_label"), EFFECTIVE_DATE),
        (i18n.t(language, "agreement.signature.operator_label"), OPERATOR),
        (i18n.t(language, "agreement.signature.contact_label"), CONTACT),
    ]


def _render_markdown(language: str, *, can_accept: bool, accepted: bool) -> str:
    """The document as Markdown. Locale text is ours and carries no markup, so
    nothing here needs escaping — it is prose, not user input."""
    blocks: list[str] = [
        f"# {i18n.t(language, 'agreement.title')}",
        i18n.t(language, "agreement.summary"),
    ]

    for index, section in enumerate(_sections(language), start=1):
        blocks.append(f"## {index}. {section.get('heading', '')}")
        text = _text_of(section)
        if text:
            blocks.append(text)
        items = section.get("items")
        if isinstance(items, list) and items:
            blocks.append("\n".join(f"- {item}" for item in items))
        note = section.get("note")
        if note:
            blocks.append(f"> {note}")

    if accepted:
        blocks.append(f"> {i18n.t(language, 'agreement.already')}")
    if can_accept and not accepted:
        blocks.append(i18n.t(language, "agreement.accept_hint"))

    blocks.append(f"## {i18n.t(language, 'agreement.signature.title')}")
    blocks.append(
        "\n".join(f"- **{label}** · {value}" for label, value in _signature_rows(language))
    )
    return "\n\n".join(blocks)


def _render_html(language: str, *, can_accept: bool, accepted: bool) -> str:
    """The same document in Telegram HTML, for a server without rich Markdown."""
    body: list[str] = []
    for index, section in enumerate(_sections(language), start=1):
        lines = [f"<b>{index}. {escape(str(section.get('heading', '')))}</b>"]
        text = _text_of(section)
        if text:
            lines.append(escape(text))
        items = section.get("items")
        if isinstance(items, list):
            lines.extend(f"— {escape(str(item))}" for item in items)
        note = section.get("note")
        if note:
            lines.append(f"<i>{escape(str(note))}</i>")
        body.append("\n".join(lines))

    signature = "\n".join(
        f"{escape(label)} · {escape(value)}" for label, value in _signature_rows(language)
    )
    # The document's head is a panel like every other screen: title, one line
    # saying what it is, then the substance quoted. Only the substance is long
    # enough that Telegram should fold it.
    parts = [
        panel(
            escape(i18n.t(language, "agreement.title")),
            escape(i18n.t(language, "agreement.summary")),
            ["\n\n".join(body)],
            expandable=True,
        )
    ]
    if accepted:
        parts.append(f"<i>{escape(i18n.t(language, 'agreement.already'))}</i>")
    if can_accept and not accepted:
        parts.append(f"<i>{escape(i18n.t(language, 'agreement.accept_hint'))}</i>")
    parts.append(
        f"<b>{escape(i18n.t(language, 'agreement.signature.title'))}</b>\n"
        f"<blockquote>{signature}</blockquote>"
    )
    return "\n\n".join(part for part in parts if part)
