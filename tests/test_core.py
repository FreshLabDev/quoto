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
        self.flush = AsyncMock()
        self.refresh = AsyncMock()
        self.rollback = AsyncMock()
        self.added: list[object] = []

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    def add(self, value: object) -> None:
        self.added.append(value)


class _DummyResult:
    def __init__(self, value: object | None) -> None:
        self.value = value

    def scalars(self):
        return self

    def first(self):
        return self.value


class _UpdateSession(_DummySession):
    def __init__(self, value: object | None) -> None:
        super().__init__()
        self.value = value

    async def execute(self, _stmt):
        return _DummyResult(self.value)


class CoreTests(unittest.IsolatedAsyncioTestCase):
    async def test_manual_publish_candidate_filter_rejects_internal_reports(self) -> None:
        internal = SimpleNamespace(text="📊 Подробности дня #60\n💬 «Просто отказано»")
        reason_line = SimpleNamespace(text="💭 Причина решения: обычный день")
        normal = SimpleNamespace(text="Просто отказано")

        self.assertIsNone(core._first_visible_manual_publish_candidate([internal, reason_line]))
        self.assertIs(core._first_visible_manual_publish_candidate([internal, normal]), normal)

    async def test_save_message_uses_telegram_message_timestamp(self) -> None:
        session = _DummySession()
        message_date = datetime(2026, 3, 27, 20, 30, tzinfo=timezone.utc)
        message = SimpleNamespace(
            text="hello",
            date=message_date,
            message_id=77,
            chat=SimpleNamespace(id=-100123456),
            reply_to_message=SimpleNamespace(message_id=76),
        )
        user = SimpleNamespace(id=5)

        with (
            patch.object(core, "SessionLocal", return_value=session),
            patch.object(core.models, "Message", side_effect=lambda **kwargs: SimpleNamespace(**kwargs)),
        ):
            saved = await core.save_message(message, user)

        self.assertEqual(saved.created_at, message_date)
        self.assertEqual(saved.message_id, 77)
        self.assertEqual(saved.reply_to_message_id, 76)
        self.assertEqual(saved.chat_id, -100123456)
        self.assertEqual(saved.user_id, 5)
        session.flush.assert_not_awaited()

    async def test_save_message_creates_pending_media_metadata_row(self) -> None:
        session = _DummySession()
        message = SimpleNamespace(
            text=None,
            caption="caption",
            photo=[SimpleNamespace(file_id="file-1", file_unique_id="unique-1", file_size=10, width=100, height=100)],
            date=datetime(2026, 3, 27, 20, 30, tzinfo=timezone.utc),
            message_id=77,
            chat=SimpleNamespace(id=-100123456),
            reply_to_message=None,
        )
        user = SimpleNamespace(id=5)

        with (
            patch.object(core, "SessionLocal", return_value=session),
            patch.object(core.models, "Message", side_effect=lambda **kwargs: SimpleNamespace(id=88, **kwargs)),
        ):
            saved = await core.save_message(message, user)

        self.assertEqual(saved.media_status, "pending")
        session.flush.assert_awaited_once()
        self.assertEqual(len(session.added), 2)
        media_item = session.added[1]
        self.assertEqual(media_item.message_db_id, 88)
        self.assertEqual(media_item.media_kind, "photo")
        self.assertEqual(media_item.telegram_file_id, "file-1")
        self.assertEqual(media_item.telegram_file_unique_id, "unique-1")
        self.assertEqual(media_item.analysis_status, "pending")

    async def test_update_message_updates_text_without_changing_created_at(self) -> None:
        created_at = datetime(2026, 3, 27, 20, 30, tzinfo=timezone.utc)
        db_message = SimpleNamespace(
            message_id=77,
            chat_id=-100123456,
            text="old",
            reply_to_message_id=None,
            created_at=created_at,
        )
        session = _UpdateSession(db_message)
        edited = SimpleNamespace(
            text="new text",
            message_id=77,
            chat=SimpleNamespace(id=-100123456),
            reply_to_message=SimpleNamespace(message_id=76),
        )

        with patch.object(core, "SessionLocal", return_value=session):
            result = await core.update_message(edited)

        self.assertIs(result, db_message)
        self.assertEqual(db_message.text, "new text")
        self.assertEqual(db_message.reply_to_message_id, 76)
        self.assertEqual(db_message.created_at, created_at)
        session.commit.assert_awaited_once()

    async def test_update_message_does_not_create_unknown_message(self) -> None:
        session = _UpdateSession(None)
        edited = SimpleNamespace(
            text="new text",
            message_id=77,
            chat=SimpleNamespace(id=-100123456),
            reply_to_message=None,
        )

        with patch.object(core, "SessionLocal", return_value=session):
            result = await core.update_message(edited)

        self.assertIsNone(result)
        session.commit.assert_not_awaited()
