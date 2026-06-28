import os
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import utils


class NotifyDevelopersTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        utils._last_notified.clear()

    async def test_throttles_duplicate_messages(self) -> None:
        with (
            patch.object(utils.settings, "ENABLE_DEVELOPERS_NOTIFY", True),
            patch.object(utils.settings, "DEVELOPER_IDS", [1]),
            patch.object(utils.bot, "send_message", new=AsyncMock()) as send,
        ):
            await utils.notify_developers("boom", dedupe_key="k")
            await utils.notify_developers("boom", dedupe_key="k")

        send.assert_awaited_once()

    async def test_scrubs_secrets(self) -> None:
        with (
            patch.object(utils.settings, "ENABLE_DEVELOPERS_NOTIFY", True),
            patch.object(utils.settings, "DEVELOPER_IDS", [1]),
            patch.object(utils.settings, "BOT_TOKEN", "SUPERSECRET"),
            patch.object(utils.bot, "send_message", new=AsyncMock()) as send,
        ):
            await utils.notify_developers("failed token=SUPERSECRET Bearer abc.def-123")

        sent_text = send.await_args.args[1]
        self.assertNotIn("SUPERSECRET", sent_text)
        self.assertIn("***", sent_text)

    async def test_disabled_is_noop(self) -> None:
        with (
            patch.object(utils.settings, "ENABLE_DEVELOPERS_NOTIFY", False),
            patch.object(utils.bot, "send_message", new=AsyncMock()) as send,
        ):
            await utils.notify_developers("anything")

        send.assert_not_awaited()
