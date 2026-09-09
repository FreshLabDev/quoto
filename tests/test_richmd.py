import os
import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from aiogram.exceptions import TelegramBadRequest, TelegramNotFound, TelegramUnauthorizedError

from app import richmd


def _api_error(kind, message: str):
    return kind(method=SimpleNamespace(), message=message)


class FakeBot:
    """A Bot stand-in: `bot(method)` is how a raw API method is called."""

    def __init__(self, *, raises: Exception | None = None) -> None:
        self.raises = raises
        self.calls: list[object] = []

    async def __call__(self, method):
        self.calls.append(method)
        if self.raises is not None:
            raise self.raises
        return SimpleNamespace(message_id=1)


class RichMarkdownPreflightTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        self._restore = richmd._supported
        richmd._supported = None

    def tearDown(self) -> None:
        richmd._supported = self._restore

    async def test_bad_request_on_the_empty_probe_means_the_method_exists(self) -> None:
        bot = FakeBot(raises=_api_error(TelegramBadRequest, "message text is empty"))

        self.assertTrue(await richmd.preflight(bot))
        self.assertTrue(richmd.available())
        self.assertEqual(bot.calls[0].__api_method__, "sendRichMessage")

    async def test_method_not_found_disables_the_rich_path(self) -> None:
        bot = FakeBot(raises=_api_error(TelegramNotFound, "method not found"))

        with patch.object(richmd.utils, "notify_developers", new=AsyncMock()) as notified:
            self.assertFalse(await richmd.preflight(bot))

        self.assertFalse(richmd.available())
        notified.assert_awaited()

    async def test_refused_probe_says_nothing_about_the_method(self) -> None:
        bot = FakeBot(raises=_api_error(TelegramUnauthorizedError, "Unauthorized"))

        self.assertFalse(await richmd.preflight(bot))
        # Inconclusive, not "unsupported": HTML is the safe reading of silence.
        self.assertIsNone(richmd._supported)
        self.assertFalse(richmd.available())

    async def test_unreachable_server_leaves_the_capability_unknown(self) -> None:
        bot = FakeBot(raises=OSError("connection refused"))

        with patch.object(richmd.utils, "notify_developers", new=AsyncMock()) as notified:
            self.assertFalse(await richmd.preflight(bot))

        self.assertIsNone(richmd._supported)
        notified.assert_awaited()


class RichMarkdownDeliveryTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        self._restore = richmd._supported

    def tearDown(self) -> None:
        richmd._supported = self._restore

    async def test_edit_carries_the_markdown_in_rich_message(self) -> None:
        richmd._supported = True
        bot = FakeBot()

        await richmd.edit_markdown(bot, -100, 5, "# Title")

        method = bot.calls[0]
        self.assertEqual(method.__api_method__, "editMessageText")
        # Entity detection stays on, so the payload carries the markdown alone.
        self.assertEqual(method.rich_message, {"markdown": "# Title"})

    async def test_a_method_lost_at_runtime_degrades_once_and_loudly(self) -> None:
        richmd._supported = True

        with patch.object(richmd.utils, "notify_developers", new=AsyncMock()) as notified:
            await richmd.note_method_lost(_api_error(TelegramNotFound, "method not found"))

        notified.assert_awaited()
        self.assertFalse(richmd.available())


if __name__ == "__main__":
    unittest.main()
