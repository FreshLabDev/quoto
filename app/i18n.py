from __future__ import annotations

import json
from pathlib import Path
from typing import Any


DEFAULT_LANGUAGE = "en"
SUPPORTED_LANGUAGES = ("ru", "uk", "en", "de")
LANGUAGE_SOURCE_AUTO = "auto"
LANGUAGE_SOURCE_MANUAL = "manual"

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
    lang = language_or_default(code)
    return str(_load(lang).get("language_name") or lang)


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


def month_name(language: object | None, month: int) -> str:
    lang = language_or_default(language)
    months = _load(lang).get("months")
    if not isinstance(months, list) and lang != DEFAULT_LANGUAGE:
        months = _load(DEFAULT_LANGUAGE).get("months")
    if isinstance(months, list) and 1 <= month <= len(months):
        return str(months[month - 1])
    return str(month)


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
