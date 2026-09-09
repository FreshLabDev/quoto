from __future__ import annotations

import json
from pathlib import Path
from typing import Any


DEFAULT_LANGUAGE = "en"
SUPPORTED_LANGUAGES = (
    "ru", "uk", "en", "de", "es", "fr", "it", "pl",
    "cs", "tr", "sv", "be", "ca", "zh", "ja", "ar",
)
LANGUAGE_SOURCE_AUTO = "auto"
LANGUAGE_SOURCE_MANUAL = "manual"

# The family's language set, in the family's order, labelled the way every
# sibling bot labels it: flag plus native name. The order is the contract's, not
# quoto's, so a person who learns the grid in one bot reads it in all of them.
# Every code the family has is listed here even where quoto has no locale yet:
# `language_options` intersects this with SUPPORTED_LANGUAGES, so a new locale
# is one JSON file plus one code in SUPPORTED_LANGUAGES and its button appears
# in the right place with the right label on its own.
LANGUAGE_LABELS: dict[str, str] = {
    "en": "🇬🇧 English",
    "ru": "🇷🇺 Русский",
    "uk": "🇺🇦 Українська",
    "es": "🇪🇸 Español",
    "fr": "🇫🇷 Français",
    "de": "🇩🇪 Deutsch",
    "it": "🇮🇹 Italiano",
    "pl": "🇵🇱 Polski",
    "cs": "🇨🇿 Čeština",
    "tr": "🇹🇷 Türkçe",
    "sv": "🇸🇪 Svenska",
    "be": "🇧🇾 Беларуская",
    "ca": "🇦🇩 Català",
    "zh": "🇨🇳 中文",
    "ja": "🇯🇵 日本語",
    "ar": "🇦🇪 العربية",
}

_LOCALE_DIR = Path(__file__).resolve().parent / "locales"
_ALIASES = {
    "rus": "ru",
    "ru-ru": "ru",
    "russian": "ru",
    "ук": "uk",
    "ukr": "uk",
    "ua": "uk",
    "uk-ua": "uk",
    "ukrainian": "uk",
    "eng": "en",
    "en-us": "en",
    "en-gb": "en",
    "english": "en",
    "ger": "de",
    "de-de": "de",
    "deutsch": "de",
    "german": "de",
}
_CACHE: dict[str, dict[str, Any]] = {}


def normalize_language_code(value: object | None) -> str | None:
    if value is None:
        return None
    code = str(value).strip().lower().replace("_", "-")
    if not code:
        return None
    code = _ALIASES.get(code, code.split("-", 1)[0])
    return code if code in SUPPORTED_LANGUAGES else None


def language_or_default(value: object | None) -> str:
    return normalize_language_code(value) or DEFAULT_LANGUAGE


def group_language(group: object | None) -> str:
    return language_or_default(getattr(group, "language_code", None))


def group_language_is_set(group: object | None) -> bool:
    return normalize_language_code(getattr(group, "language_code", None)) is not None


def language_name(code: object | None) -> str:
    """The bare native name, for running text — "Interface language: Deutsch"."""
    lang = language_or_default(code)
    return str(_load(lang).get("language_name") or lang)


def language_label(code: object | None) -> str:
    """The button label: flag plus native name. Text uses `language_name`; a
    button in any picker uses this, so all of them read the same."""
    lang = language_or_default(code)
    return LANGUAGE_LABELS.get(lang) or language_name(lang)


def language_options() -> tuple[str, ...]:
    """Every language quoto can render, in the family order. One list, one
    order, read by every picker in the bot."""
    return tuple(code for code in LANGUAGE_LABELS if code in SUPPORTED_LANGUAGES)


def language_options_prompt() -> str:
    return ", ".join(f"{code}={language_name(code)}" for code in SUPPORTED_LANGUAGES)


def t(language: object | None, key: str, **kwargs: object) -> str:
    lang = language_or_default(language)
    value = _lookup(_load(lang), key)
    if value is None and lang != DEFAULT_LANGUAGE:
        value = _lookup(_load(DEFAULT_LANGUAGE), key)
    if value is None:
        value = key
    text = str(value)
    return text.format(**kwargs) if kwargs else text


def value(language: object | None, key: str) -> Any:
    """Read a non-string locale entry — a list or a dict — with the same
    fallback to English that `t` gives strings. The agreement is authored as
    structure so it can be rendered as Markdown or as HTML from one source."""
    lang = language_or_default(language)
    found = _lookup(_load(lang), key)
    if found is None and lang != DEFAULT_LANGUAGE:
        found = _lookup(_load(DEFAULT_LANGUAGE), key)
    return found


def month_name(language: object | None, month: int) -> str:
    lang = language_or_default(language)
    months = _load(lang).get("months")
    if not isinstance(months, list) and lang != DEFAULT_LANGUAGE:
        months = _load(DEFAULT_LANGUAGE).get("months")
    if isinstance(months, list) and 1 <= month <= len(months):
        return str(months[month - 1])
    return str(month)


def format_date(language: object | None, value, *, with_year: bool = False) -> str:
    """Render a date the way the language writes one.

    Gluing "{day} {month}" together at the call site only works for the
    languages that happen to write a date that way. Spanish needs a preposition
    before the month and another before the year, German and Czech put a period
    after the day, and Chinese and Japanese start with the year and end with a
    character. So the order lives in the locale beside the month names, not in
    the code.
    """
    lang = language_or_default(language)
    key = "date.day_month_year" if with_year else "date.day_month"
    return t(
        lang,
        key,
        day=value.day,
        month=month_name(lang, value.month),
        year=value.year,
    )


def _load(language: str) -> dict[str, Any]:
    lang = language_or_default(language)
    cached = _CACHE.get(lang)
    if cached is not None:
        return cached

    path = _LOCALE_DIR / f"{lang}.json"
    with path.open("r", encoding="utf-8") as handle:
        data = json.load(handle)
    _CACHE[lang] = data
    return data


def _lookup(data: dict[str, Any], key: str) -> Any:
    current: Any = data
    for part in key.split("."):
        if not isinstance(current, dict) or part not in current:
            return None
        current = current[part]
    return current
