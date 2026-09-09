from html import escape
import os
import unittest

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from aiogram.enums import ButtonStyle

from app import agreement, i18n, menu


def _flat(keyboard) -> list[str]:
    return [button.callback_data for row in keyboard.inline_keyboard for button in row]


class AgreementCallbackTests(unittest.TestCase):
    def test_callback_round_trip(self) -> None:
        data = agreement.callback_data(agreement.ACTION_ACCEPT, "ru")
        self.assertEqual(
            agreement.parse_callback(data),
            agreement.AgreementCallback(agreement.ACTION_ACCEPT, "ru"),
        )

    def test_callback_round_trip_keeps_the_panel_it_was_opened_from(self) -> None:
        data = agreement.callback_data(
            agreement.ACTION_VIEW, "de", scope=menu.SCOPE_GROUP, owner_id=777
        )
        self.assertEqual(
            agreement.parse_callback(data),
            agreement.AgreementCallback(agreement.ACTION_VIEW, "de", menu.SCOPE_GROUP, 777),
        )

    def test_parse_rejects_foreign_callbacks(self) -> None:
        self.assertIsNone(agreement.parse_callback("menu:1:p:home"))
        self.assertIsNone(agreement.parse_callback(None))
        self.assertIsNone(agreement.parse_callback("agree:v:en:g:not-a-number"))


class AgreementDocumentTests(unittest.TestCase):
    def test_document_shows_accept_and_all_languages_when_allowed(self) -> None:
        document = agreement.build_document("en", can_accept=True, accepted=False)
        flat = _flat(document.keyboard)
        self.assertIn(agreement.callback_data(agreement.ACTION_ACCEPT, "en"), flat)
        for code in ("uk", "ru", "en", "de"):
            self.assertIn(agreement.callback_data(agreement.ACTION_VIEW, code), flat)
        self.assertIn("OpenRouter", document.markdown)
        self.assertIn("OpenRouter", document.html)

    def test_document_hides_accept_when_already_accepted(self) -> None:
        document = agreement.build_document("en", can_accept=True, accepted=True)
        self.assertNotIn(
            agreement.callback_data(agreement.ACTION_ACCEPT, "en"), _flat(document.keyboard)
        )

    def test_document_hides_accept_when_not_allowed(self) -> None:
        document = agreement.build_document("en", can_accept=False, accepted=False)
        self.assertNotIn(
            agreement.callback_data(agreement.ACTION_ACCEPT, "en"), _flat(document.keyboard)
        )

    def test_document_discloses_extended_service_records(self) -> None:
        document = agreement.build_document("en", can_accept=False, accepted=False)
        for rendering in (document.markdown, document.html):
            self.assertIn("service records", rendering)
            self.assertIn("quality checks", rendering)

    def test_markdown_uses_real_headings_and_a_list(self) -> None:
        document = agreement.build_document("en", can_accept=False, accepted=False)
        lines = document.markdown.splitlines()
        self.assertTrue(lines[0].startswith("# "), lines[0])
        self.assertEqual(
            sum(1 for line in lines if line.startswith("## ")),
            len(i18n.value("en", "agreement.sections")) + 1,  # sections + signature
        )
        self.assertTrue(any(line.startswith("- ") for line in lines))
        self.assertTrue(any(line.startswith("> ") for line in lines))

    def test_html_fallback_carries_no_markdown_syntax(self) -> None:
        document = agreement.build_document("en", can_accept=True, accepted=False)
        self.assertNotIn("# ", document.html)
        self.assertIn("<blockquote expandable>", document.html)

    def test_html_opens_with_the_same_panel_shape_as_every_screen(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            document = agreement.build_document(language, can_accept=False, accepted=False)
            head, _, rest = document.html.partition("\n\n")
            self.assertEqual(
                head,
                f"<b>{escape(i18n.t(language, 'agreement.title'))}</b>\n"
                f"<i>{escape(i18n.t(language, 'agreement.summary'))}</i>",
            )
            self.assertTrue(rest.startswith("<blockquote expandable>"), rest[:60])

    def test_accept_is_the_one_thing_the_document_leads_to(self) -> None:
        document = agreement.build_document("en", can_accept=True, accepted=False)
        primary = [
            button.text
            for row in document.keyboard.inline_keyboard
            for button in row
            if button.style == ButtonStyle.PRIMARY
        ]
        self.assertEqual(primary, [i18n.t("en", "agreement.accept_button")])

    def test_the_language_switcher_is_the_family_grid(self) -> None:
        document = agreement.build_document("de", can_accept=False, accepted=False)
        codes = i18n.language_options()
        grid = document.keyboard.inline_keyboard[: (len(codes) + 1) // 2]
        self.assertEqual(
            [button.text for row in grid for button in row],
            [
                f"{menu.TOGGLE_ON if code == 'de' else menu.TOGGLE_OFF} {i18n.language_label(code)}"
                for code in codes
            ],
        )

    def test_document_is_signed_in_every_language(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            document = agreement.build_document(language, can_accept=False, accepted=False)
            signature_title = i18n.t(language, "agreement.signature.title")
            for rendering in (document.markdown, document.html):
                self.assertIn(signature_title, rendering)
                self.assertIn(agreement.DOCUMENT_VERSION, rendering)
                self.assertIn(agreement.EFFECTIVE_DATE, rendering)
                self.assertIn(agreement.OPERATOR, rendering)
                self.assertIn(agreement.CONTACT, rendering)

    def test_operator_contact_is_the_real_handle_in_every_language(self) -> None:
        for language in i18n.SUPPORTED_LANGUAGES:
            document = agreement.build_document(language, can_accept=False, accepted=False)
            for rendering in (document.markdown, document.html):
                self.assertIn("@amtiyo", rendering)
                self.assertNotIn("@amti_yo", rendering)
                self.assertNotIn("{contact}", rendering)

    def test_document_appends_the_panel_navigation_it_is_given(self) -> None:
        nav = menu.nav_row(777, menu.SCOPE_GROUP, "en")
        document = agreement.build_document(
            "en",
            can_accept=False,
            accepted=False,
            scope=menu.SCOPE_GROUP,
            owner_id=777,
            nav_row=nav,
        )
        self.assertEqual(document.keyboard.inline_keyboard[-1], nav)
        self.assertIn(
            agreement.callback_data(
                agreement.ACTION_VIEW, "ru", scope=menu.SCOPE_GROUP, owner_id=777
            ),
            _flat(document.keyboard),
        )

    def test_welcome_keyboard_opens_view(self) -> None:
        keyboard = agreement.build_welcome_keyboard("de")
        self.assertEqual(
            keyboard.inline_keyboard[0][0].callback_data,
            agreement.callback_data(agreement.ACTION_VIEW, "de"),
        )
