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

    def test_message_cap_applies_to_regular_group(self) -> None:
        group = SimpleNamespace(chat_id=-100, is_premium=None)
        with (
            patch.object(core.settings, "MAX_MESSAGES_PER_DAILY_EVAL", 1500),
            patch.object(core.settings, "PREMIUM_CHAT_IDS", []),
        ):
            self.assertFalse(core.effective_group_is_premium(group))
            self.assertEqual(core.effective_group_message_cap(group), 1500)

    def test_premium_flag_bypasses_message_cap(self) -> None:
        group = SimpleNamespace(chat_id=-100, is_premium=True)
        with patch.object(core.settings, "PREMIUM_CHAT_IDS", []):
            self.assertTrue(core.effective_group_is_premium(group))
            self.assertIsNone(core.effective_group_message_cap(group))

    def test_premium_chat_ids_bypass_message_cap(self) -> None:
        group = SimpleNamespace(chat_id=-100, is_premium=None)
        with patch.object(core.settings, "PREMIUM_CHAT_IDS", [-100]):
            self.assertTrue(core.effective_group_is_premium(group))
            self.assertIsNone(core.effective_group_message_cap(group))

    def test_jittered_quote_minute_within_range(self) -> None:
        with (
            patch.object(core.settings, "QUOTE_MINUTE", 0),
            patch.object(core.settings, "QUOTE_MINUTE_JITTER", 10),
        ):
            for _ in range(50):
                self.assertIn(core._jittered_quote_minute(), range(0, 11))

    def test_jitter_disabled_returns_none(self) -> None:
        with patch.object(core.settings, "QUOTE_MINUTE_JITTER", 0):
            self.assertIsNone(core._jittered_quote_minute())

    def test_group_agreement_accepted_flag(self) -> None:
        self.assertFalse(core.group_agreement_accepted(SimpleNamespace(agreement_accepted_at=None)))
        self.assertTrue(
            core.group_agreement_accepted(
                SimpleNamespace(agreement_accepted_at=datetime.now(timezone.utc))
            )
        )

    async def test_accept_group_agreement_sets_fields(self) -> None:
        group = SimpleNamespace(
            agreement_accepted_at=None,
            agreement_accepted_by=None,
            agreement_language=None,
        )
        session = _UpdateSession(group)
        with patch.object(core, "SessionLocal", return_value=session):
            result = await core.accept_group_agreement(1, 42, "ru")

        self.assertIsNotNone(result.agreement_accepted_at)
        self.assertEqual(result.agreement_accepted_by, 42)
        self.assertEqual(result.agreement_language, "ru")
        session.commit.assert_awaited_once()

    async def test_accept_group_agreement_is_idempotent(self) -> None:
        existing = datetime(2026, 6, 1, tzinfo=timezone.utc)
        group = SimpleNamespace(
            agreement_accepted_at=existing,
            agreement_accepted_by=7,
            agreement_language="en",
        )
        session = _UpdateSession(group)
        with patch.object(core, "SessionLocal", return_value=session):
            result = await core.accept_group_agreement(1, 42, "ru")

        self.assertEqual(result.agreement_accepted_at, existing)
        self.assertEqual(result.agreement_accepted_by, 7)
        session.commit.assert_not_awaited()
