"""A date is written differently in each of the sixteen languages.

The old code glued "{day} {month}" together at the call site, which only works
for the languages that happen to write a date that way. It reached production
in four languages that all do; the other twelve exposed it.
"""
import datetime

import pytest

from app import i18n


TODAY = datetime.date(2026, 9, 9)

# What a reader of each language expects to see, not what is easy to build.
EXPECTED = {
    "en": ("9 September", "9 September 2026"),
    "ru": ("9 сентября", "9 сентября 2026"),
    "uk": ("9 вересня", "9 вересня 2026"),
    "be": ("9 верасня", "9 верасня 2026"),
    "de": ("9. September", "9. September 2026"),
    "cs": ("9. září", "9. září 2026"),
    "es": ("9 de septiembre", "9 de septiembre de 2026"),
    "ca": ("9 de setembre", "9 de setembre de 2026"),
    "fr": ("9 septembre", "9 septembre 2026"),
    "it": ("9 settembre", "9 settembre 2026"),
    "pl": ("9 września", "9 września 2026"),
    "sv": ("9 september", "9 september 2026"),
    "tr": ("9 Eylül", "9 Eylül 2026"),
    "zh": ("9月9日", "2026年9月9日"),
    "ja": ("9月9日", "2026年9月9日"),
    "ar": ("9 سبتمبر", "9 سبتمبر 2026"),
}


@pytest.mark.parametrize("language", i18n.SUPPORTED_LANGUAGES)
def test_every_language_writes_its_own_date(language):
    short, long = EXPECTED[language]
    assert i18n.format_date(language, TODAY) == short
    assert i18n.format_date(language, TODAY, with_year=True) == long


def test_the_table_covers_every_supported_language():
    """Adding a locale without a date format would silently fall back to English."""
    assert set(EXPECTED) == set(i18n.SUPPORTED_LANGUAGES)


@pytest.mark.parametrize("language", i18n.SUPPORTED_LANGUAGES)
def test_a_month_name_carries_no_preposition_or_separator(language):
    """The order belongs to the format string, not to the month.

    Spanish and Catalan used to bake "de " into the name, which read correctly
    without a year and dropped the second preposition with one.
    """
    for month in range(1, 13):
        name = i18n.month_name(language, month)
        assert name == name.strip()
        assert not name.lower().startswith(("de ", "d'"))


@pytest.mark.parametrize("language", ["ru", "uk", "be"])
def test_slavic_months_are_lower_case(language):
    """These render inside a line, never at the start of a sentence."""
    for month in range(1, 13):
        name = i18n.month_name(language, month)
        assert name[0].islower(), f"{language} month {month} is {name!r}"
