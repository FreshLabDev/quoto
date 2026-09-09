import os
import re
import unittest

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from aiogram.enums import ButtonStyle

from app import i18n, menu
from app.version import CONTACT, CONTACT_URL, LICENSE, REPOSITORY, REPOSITORY_URL, VERSION


# A panel is title, hint, quote: a bold name, optionally the version beside it,
# one italic line under it, and then at most one blockquote.
_HEAD = re.compile(r"^<b>[^<]+</b>( · <i>[^<]+</i>)?(\n<i>.+</i>)?$", re.DOTALL)
_BODY = re.compile(r"^<blockquote( expandable)?>.*</blockquote>$", re.DOTALL)

# Anything pictographic. The language grid is allowed its flags — they are the
# content of those buttons — and nothing else on a panel may carry one.
_EMOJI = re.compile("[\U0001f000-\U0001faff☀-➿⬀-⯿←-⇿⌀-⏿]")


def _labels(keyboard) -> list[str]:
    return [button.text for row in keyboard.inline_keyboard for button in row]


def _buttons(keyboard) -> list:
    return [button for row in keyboard.inline_keyboard for button in row]


def _styles(keyboard, style) -> list[str]:
    return [button.text for button in _buttons(keyboard) if button.style == style]


def _private(section: str = menu.SECTION_HOME):
    return menu.build_private_panel(
        owner_id=777,
        language="ru",
        language_source=None,
        bot_username="quoto_test_bot",
        section=section,
    )


def _group(section: str = menu.SECTION_HOME, *, is_admin: bool = True, language: str = "ru"):
    return menu.build_group_panel(
        owner_id=777,
        language=language,
        group_language=language,
        group_language_source=None,
        is_admin=is_admin,
        quote_time="21:00",
        min_messages=10,
        timezone_name="Europe/Kyiv",
        boring_notice_enabled=True,
        pin_enabled=True,
        quote_context_enabled=True,
        section=section,
    )


def _every_screen(language: str = "ru"):
    """Every screen both panels can draw, named, for the rules that hold on
    all of them."""
    for section in (
        menu.SECTION_HOME,
        menu.SECTION_LANGUAGE,
        menu.SECTION_ABOUT,
    ):
        yield f"private/{section}", menu.build_private_panel(
            owner_id=777,
            language=language,
            language_source=None,
            bot_username="quoto_test_bot",
            section=section,
        )
    for section in (
        menu.SECTION_HOME,
        menu.SECTION_LANGUAGE,
        menu.SECTION_SCHEDULE,
        menu.SECTION_TIMEZONE,
        menu.SECTION_BEHAVIOR,
        menu.SECTION_STATS,
        menu.SECTION_ABOUT,
    ):
        yield f"group/{section}", _group(section, language=language)
    yield "group/home-member", _group(is_admin=False, language=language)


class NavigationTests(unittest.TestCase):
    def test_close_exists_only_in_group_panels(self) -> None:
        close = i18n.t("ru", "menu.button.close")
        self.assertNotIn(close, _labels(_private()[1]))
        self.assertNotIn(close, _labels(_private(menu.SECTION_LANGUAGE)[1]))
        self.assertIn(close, _labels(_group()[1]))
        self.assertIn(close, _labels(_group(menu.SECTION_LANGUAGE)[1]))

    def test_navigation_labels_carry_no_arrows(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            for key in ("menu.button.back", "menu.button.close"):
                label = i18n.t(language, key)
                self.assertEqual(label, label.strip())
                for ornament in ("‹", "›", "<", ">", "←", "→", "×", "✕", "✖"):
                    self.assertNotIn(ornament, label)

    def test_selection_marker_is_the_family_pair(self) -> None:
        self.assertEqual((menu.TOGGLE_ON, menu.TOGGLE_OFF), ("◉", "◎"))
        labels = _labels(_group(menu.SECTION_TIMEZONE)[1])
        self.assertTrue(any(label.startswith("◉ ") for label in labels), labels)
        self.assertTrue(any(label.startswith("◎ ") for label in labels), labels)

    def test_close_is_the_only_destructive_button_and_is_always_painted(self) -> None:
        close = i18n.t("ru", "menu.button.close")
        for name, (_, keyboard) in _every_screen():
            danger = _styles(keyboard, ButtonStyle.DANGER)
            self.assertEqual(danger, [close] if close in _labels(keyboard) else [], name)

    def test_going_up_is_always_the_same_word(self) -> None:
        back = i18n.t("ru", "menu.button.back")
        for name, (_, keyboard) in _every_screen():
            if name.endswith("home") or name.endswith("home-member"):
                # Home is the top of the panel: there is nothing above it.
                self.assertNotIn(back, _labels(keyboard), name)
                continue
            self.assertIn(back, _labels(keyboard), name)


class ScreenShapeTests(unittest.TestCase):
    """R1 — a panel is title, hint, quote, and one helper builds all of them."""

    def test_every_screen_is_a_title_a_hint_and_at_most_one_quote(self) -> None:
        for name, (text, _) in _every_screen():
            head, _, body = text.partition("\n\n")
            self.assertRegex(head, _HEAD, name)
            if body:
                self.assertRegex(body, _BODY, name)

    def test_a_section_does_not_repeat_the_bot_name_above_its_own_title(self) -> None:
        for section in (
            menu.SECTION_LANGUAGE,
            menu.SECTION_SCHEDULE,
            menu.SECTION_TIMEZONE,
            menu.SECTION_BEHAVIOR,
        ):
            text, _ = _group(section)
            self.assertNotIn(i18n.t("ru", "menu.group.title"), text.split("\n\n")[0], section)

    def test_a_screen_never_lists_in_the_body_what_its_buttons_already_say(self) -> None:
        # The timezone and the language grids mark the chosen option on the
        # button; the body must not name it a second time.
        text, _ = _group(menu.SECTION_TIMEZONE)
        self.assertNotIn("Europe/Kyiv", text)
        for panel_text in (_group(menu.SECTION_LANGUAGE)[0], _private(menu.SECTION_LANGUAGE)[0]):
            self.assertNotIn(i18n.language_name("ru"), panel_text)
        # Publishing is four switches, each with its state on its own button.
        text, _ = _group(menu.SECTION_BEHAVIOR)
        self.assertNotIn(menu.TOGGLE_ON, text)
        self.assertNotIn(menu.TOGGLE_OFF, text)


class ColourTests(unittest.TestCase):
    """R4/R5/R6 — Primary leads, Success is a state, Danger destroys."""

    def test_no_screen_has_two_primaries(self) -> None:
        for name, (_, keyboard) in _every_screen():
            self.assertLessEqual(len(_styles(keyboard, ButtonStyle.PRIMARY)), 1, name)

    def test_the_primary_is_the_one_thing_each_home_leads_to(self) -> None:
        self.assertEqual(
            _styles(_private()[1], ButtonStyle.PRIMARY),
            [i18n.t("ru", "private.add_to_group")],
        )
        for is_admin in (True, False):
            self.assertEqual(
                _styles(_group(is_admin=is_admin)[1], ButtonStyle.PRIMARY),
                [i18n.t("ru", "menu.button.stats")],
            )

    def test_a_screen_that_leads_nowhere_gets_no_primary(self) -> None:
        for section in (
            menu.SECTION_LANGUAGE,
            menu.SECTION_SCHEDULE,
            menu.SECTION_TIMEZONE,
            menu.SECTION_BEHAVIOR,
            menu.SECTION_STATS,
            menu.SECTION_ABOUT,
        ):
            self.assertEqual(_styles(_group(section)[1], ButtonStyle.PRIMARY), [], section)
        self.assertEqual(_styles(_private(menu.SECTION_LANGUAGE)[1], ButtonStyle.PRIMARY), [])

    def test_success_marks_the_state_and_never_an_action(self) -> None:
        for name, (_, keyboard) in _every_screen():
            for button in _buttons(keyboard):
                if button.style == ButtonStyle.SUCCESS:
                    self.assertTrue(button.text.startswith(f"{menu.TOGGLE_ON} "), f"{name}: {button.text}")

    def test_the_chosen_option_of_every_set_is_the_success_one(self) -> None:
        for panel in (_private(menu.SECTION_LANGUAGE), _group(menu.SECTION_LANGUAGE)):
            self.assertEqual(
                _styles(panel[1], ButtonStyle.SUCCESS),
                [f"{menu.TOGGLE_ON} {i18n.language_label('ru')}"],
            )
        self.assertEqual(
            _styles(_group(menu.SECTION_TIMEZONE)[1], ButtonStyle.SUCCESS),
            [f"{menu.TOGGLE_ON} Europe/Kyiv"],
        )
        self.assertEqual(
            _styles(_group(menu.SECTION_STATS)[1], ButtonStyle.SUCCESS),
            [f"{menu.TOGGLE_ON} {i18n.t('ru', 'menu.button.user_stats')}"],
        )

    def test_toggles_stay_unpainted(self) -> None:
        # Four switches in the theme colour would be four things shouting, and
        # each one's state is already on its glyph.
        keyboard = _group(menu.SECTION_BEHAVIOR)[1]
        for button in _buttons(keyboard):
            if button.text.startswith((menu.TOGGLE_ON, menu.TOGGLE_OFF)):
                self.assertIsNone(button.style, button.text)


class LanguageGridTests(unittest.TestCase):
    """R8 — one list, one order, one format."""

    def test_the_family_order_and_labels_are_the_contract_s(self) -> None:
        self.assertEqual(
            list(i18n.LANGUAGE_LABELS),
            ["en", "ru", "uk", "es", "fr", "de", "it", "pl", "cs", "tr",
             "sv", "be", "ca", "zh", "ja", "ar"],
        )
        self.assertEqual(i18n.LANGUAGE_LABELS["en"], "🇬🇧 English")
        self.assertEqual(i18n.LANGUAGE_LABELS["uk"], "🇺🇦 Українська")

    def test_quoto_shows_the_languages_it_has_in_that_order(self) -> None:
        # Quoto has a locale for every language the family speaks, so its grid
        # is the family list itself, in the family order.
        self.assertEqual(i18n.language_options(), tuple(i18n.LANGUAGE_LABELS))
        self.assertEqual(set(i18n.language_options()), set(i18n.SUPPORTED_LANGUAGES))

    def test_every_language_button_is_a_flag_a_native_name_and_a_mark(self) -> None:
        rows = menu.language_rows("uk", lambda code: f"x:{code}")
        self.assertEqual([len(row) for row in rows], [2] * 8)
        self.assertEqual(
            [button.text for row in rows for button in row],
            [
                "◎ 🇬🇧 English",
                "◎ 🇷🇺 Русский",
                "◉ 🇺🇦 Українська",
                "◎ 🇪🇸 Español",
                "◎ 🇫🇷 Français",
                "◎ 🇩🇪 Deutsch",
                "◎ 🇮🇹 Italiano",
                "◎ 🇵🇱 Polski",
                "◎ 🇨🇿 Čeština",
                "◎ 🇹🇷 Türkçe",
                "◎ 🇸🇪 Svenska",
                "◎ 🇧🇾 Беларуская",
                "◎ 🇦🇩 Català",
                "◎ 🇨🇳 中文",
                "◎ 🇯🇵 日本語",
                "◎ 🇦🇪 العربية",
            ],
        )

    def test_both_pickers_render_the_same_grid(self) -> None:
        from app import agreement

        panel_rows = _group(menu.SECTION_LANGUAGE)[1].inline_keyboard[:2]
        document_rows = agreement.build_document(
            "ru", can_accept=False, accepted=False
        ).keyboard.inline_keyboard[:2]
        self.assertEqual(
            [button.text for row in panel_rows for button in row],
            [button.text for row in document_rows for button in row],
        )


class AboutTests(unittest.TestCase):
    def test_about_card_names_version_scoring_sources_and_admin(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            text = menu.about_text(language)
            self.assertIn(f"<b>Quoto</b> · <i>v{VERSION}</i>", text)
            self.assertIn(i18n.t(language, "about.tagline"), text)
            self.assertIn(f'<a href="{REPOSITORY_URL}">{REPOSITORY}</a>', text)
            self.assertIn(LICENSE, text)
            self.assertIn(f'<a href="{CONTACT_URL}">{CONTACT}</a>', text)
            self.assertIn("<blockquote>", text)

    def test_about_tab_has_no_source_button_duplicating_the_link(self) -> None:
        for panel in (_private(menu.SECTION_ABOUT), _group(menu.SECTION_ABOUT)):
            text, keyboard = panel
            self.assertEqual(text, menu.about_text("ru"))
            self.assertTrue(all(button.url is None for row in keyboard.inline_keyboard for button in row))

    def test_about_tab_navigation_follows_the_scope(self) -> None:
        self.assertEqual(
            _labels(_private(menu.SECTION_ABOUT)[1]), [i18n.t("ru", "menu.button.back")]
        )
        self.assertEqual(
            _labels(_group(menu.SECTION_ABOUT)[1]),
            [i18n.t("ru", "menu.button.back"), i18n.t("ru", "menu.button.close")],
        )

    def test_both_panels_offer_the_agreement_and_about_tabs(self) -> None:
        for owner_scope, keyboard in (
            (menu.SCOPE_PRIVATE, _private()[1]),
            (menu.SCOPE_GROUP, _group()[1]),
        ):
            data = [b.callback_data for row in keyboard.inline_keyboard for b in row]
            self.assertIn(menu.callback_data(777, owner_scope, menu.ACTION_AGREEMENT), data)
            self.assertIn(menu.callback_data(777, owner_scope, menu.ACTION_ABOUT), data)


class HeadlineTests(unittest.TestCase):
    def test_the_trophy_is_reserved_for_the_published_quote(self) -> None:
        decorated = ("menu.private.title", "menu.group.title", "start.group", "private.hello")
        for language in i18n.SUPPORTED_LANGUAGES:
            for key in decorated:
                self.assertNotIn("🏆", i18n.t(language, key))
            self.assertIn("🏆", i18n.t(language, "quote_post.titles.text"))

    def test_buttons_carry_no_emoji_icons(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            for key in ("agreement.view_button", "agreement.accept_button"):
                label = i18n.t(language, key)
                self.assertTrue(label[0].isalpha(), label)

    def test_no_panel_furniture_carries_an_emoji_in_any_locale(self) -> None:
        """R9 — titles, hints and button labels are words. The flags in the
        language picker are the content of those buttons, and the medals and
        status ticks live on lines a bare notice could not carry."""
        for language in i18n.SUPPORTED_LANGUAGES:
            for name, (text, keyboard) in _every_screen(language):
                head = text.split("\n\n")[0]
                self.assertEqual(_EMOJI.findall(head), [], f"{language} {name}: {head}")
                for label in _labels(keyboard):
                    bare = label
                    for known in i18n.LANGUAGE_LABELS.values():
                        bare = bare.replace(known, "")
                    self.assertEqual(_EMOJI.findall(bare), [], f"{language} {name}: {label}")


if __name__ == "__main__":
    unittest.main()
