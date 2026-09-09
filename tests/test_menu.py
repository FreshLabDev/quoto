import os
import unittest

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import i18n, menu
from app.version import CONTACT, CONTACT_URL, LICENSE, REPOSITORY, REPOSITORY_URL, VERSION


def _labels(keyboard) -> list[str]:
    return [button.text for row in keyboard.inline_keyboard for button in row]


def _private(section: str = menu.SECTION_HOME):
    return menu.build_private_panel(
        owner_id=777,
        language="ru",
        language_source=None,
        bot_username="quoto_test_bot",
        section=section,
    )


def _group(section: str = menu.SECTION_HOME, *, is_admin: bool = True):
    return menu.build_group_panel(
        owner_id=777,
        language="ru",
        group_language="ru",
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


if __name__ == "__main__":
    unittest.main()
