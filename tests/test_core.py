from datetime import datetime, timezone
import os
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import core


class _DummySession:
    def __init__(self) -> None:
        self.commit = AsyncMock()
        self.refresh = AsyncMock()
        self.rollback = AsyncMock()
        self.added: list[object] = []

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    def add(self, value: object) -> None:
        self.added.append(value)


class CoreTests(unittest.IsolatedAsyncioTestCase):
    async def test_save_message_uses_telegram_message_timestamp(self) -> None:
        session = _DummySession()
        message_date = datetime(2026, 3, 27, 20, 30, tzinfo=timezone.utc)
        message = SimpleNamespace(
            text="hello",
            date=message_date,
            message_id=77,
            chat=SimpleNamespace(id=-100123456),
        )
        user = SimpleNamespace(id=5)

        with (
            patch.object(core, "SessionLocal", return_value=session),
            patch.object(core.models, "Message", side_effect=lambda **kwargs: SimpleNamespace(**kwargs)),
        ):
            saved = await core.save_message(message, user)

        self.assertEqual(saved.created_at, message_date)
        self.assertEqual(saved.message_id, 77)
        self.assertEqual(saved.chat_id, -100123456)
        self.assertEqual(saved.user_id, 5)
