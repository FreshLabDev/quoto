import os
import unittest

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import agreement


def _flat(keyboard) -> list[str]:
    return [button.callback_data for row in keyboard.inline_keyboard for button in row]


class AgreementTests(unittest.TestCase):
    def test_callback_round_trip(self) -> None:
        data = agreement.callback_data(agreement.ACTION_ACCEPT, "ru")
        self.assertEqual(agreement.parse_callback(data), (agreement.ACTION_ACCEPT, "ru"))

    def test_parse_rejects_foreign_callbacks(self) -> None:
        self.assertIsNone(agreement.parse_callback("menu:1:p:home"))
        self.assertIsNone(agreement.parse_callback(None))

    def test_document_shows_accept_and_all_languages_when_allowed(self) -> None:
        text, keyboard = agreement.build_document("en", can_accept=True, accepted=False)
        flat = _flat(keyboard)
        self.assertIn(agreement.callback_data(agreement.ACTION_ACCEPT, "en"), flat)
        for code in ("uk", "ru", "en", "de"):
            self.assertIn(agreement.callback_data(agreement.ACTION_VIEW, code), flat)
        self.assertIn("OpenRouter", text)
        self.assertIn("@amti_yo", text)

    def test_document_hides_accept_when_already_accepted(self) -> None:
        _text, keyboard = agreement.build_document("en", can_accept=True, accepted=True)
        self.assertNotIn(agreement.callback_data(agreement.ACTION_ACCEPT, "en"), _flat(keyboard))

    def test_document_hides_accept_when_not_allowed(self) -> None:
        _text, keyboard = agreement.build_document("en", can_accept=False, accepted=False)
        self.assertNotIn(agreement.callback_data(agreement.ACTION_ACCEPT, "en"), _flat(keyboard))

    def test_welcome_keyboard_opens_view(self) -> None:
        keyboard = agreement.build_welcome_keyboard("de")
        self.assertEqual(
            keyboard.inline_keyboard[0][0].callback_data,
            agreement.callback_data(agreement.ACTION_VIEW, "de"),
        )
