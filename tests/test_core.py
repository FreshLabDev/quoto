from datetime import datetime, timezone
import json
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


class _SeqSession(_DummySession):
    """Returns a queued result per execute() call (for multi-query helpers)."""

    def __init__(self, results: list[object]) -> None:
        super().__init__()
        self._results = list(results)

    async def execute(self, _stmt):
        return self._results.pop(0)


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

    def test_effective_group_timezone_prefers_valid_group_value(self) -> None:
        with patch.object(core.settings, "TIMEZONE", "Europe/Kyiv"):
            self.assertEqual(
                core.effective_group_timezone_name(SimpleNamespace(timezone="Asia/Tokyo")),
                "Asia/Tokyo",
            )
            self.assertEqual(
                core.effective_group_timezone_name(SimpleNamespace(timezone="Bogus/Zone")),
                "Europe/Kyiv",
            )
            self.assertEqual(
                core.effective_group_timezone_name(SimpleNamespace(timezone=None)),
                "Europe/Kyiv",
            )

    def test_default_timezone_for_language(self) -> None:
        with patch.object(core.settings, "TIMEZONE", "Europe/Kyiv"):
            self.assertEqual(core.default_timezone_for_language("de"), "Europe/Berlin")
            self.assertEqual(core.default_timezone_for_language("xx"), "Europe/Kyiv")

    async def test_set_group_timezone_auto_only_when_empty(self) -> None:
        group = SimpleNamespace(timezone=None)
        session = _UpdateSession(group)
        with patch.object(core, "SessionLocal", return_value=session):
            ok = await core.set_group_timezone_auto(1, "Asia/Tokyo")
        self.assertTrue(ok)
        self.assertEqual(group.timezone, "Asia/Tokyo")

    async def test_set_group_timezone_auto_skips_when_already_set(self) -> None:
        group = SimpleNamespace(timezone="UTC")
        session = _UpdateSession(group)
        with patch.object(core, "SessionLocal", return_value=session):
            ok = await core.set_group_timezone_auto(1, "Asia/Tokyo")
        self.assertFalse(ok)
        self.assertEqual(group.timezone, "UTC")

    async def test_set_group_timezone_rejects_invalid(self) -> None:
        group = SimpleNamespace(timezone=None)
        session = _UpdateSession(group)
        with patch.object(core, "SessionLocal", return_value=session):
            result = await core.set_group_timezone(1, "Bogus/Zone")
        self.assertIsNone(result)

    def test_effective_group_media_analysis_respects_both_switches(self) -> None:
        with patch.object(core.settings, "MEDIA_ANALYSIS_ENABLED", True):
            self.assertTrue(
                core.effective_group_media_analysis_enabled(SimpleNamespace(media_analysis_enabled=None))
            )
            self.assertFalse(
                core.effective_group_media_analysis_enabled(SimpleNamespace(media_analysis_enabled=False))
            )
        with patch.object(core.settings, "MEDIA_ANALYSIS_ENABLED", False):
            self.assertFalse(
                core.effective_group_media_analysis_enabled(SimpleNamespace(media_analysis_enabled=True))
            )

    async def test_migrate_group_chat_id_rekeys_group(self) -> None:
        group = SimpleNamespace(chat_id=-100)
        session = _SeqSession([_DummyResult(None), _DummyResult(group), object()])
        with patch.object(core, "SessionLocal", return_value=session):
            ok = await core.migrate_group_chat_id(-100, -1001999)
        self.assertTrue(ok)
        self.assertEqual(group.chat_id, -1001999)
        session.commit.assert_awaited_once()

    async def test_migrate_group_chat_id_skips_when_target_exists(self) -> None:
        session = _SeqSession([_DummyResult(SimpleNamespace(chat_id=-1001999))])
        with patch.object(core, "SessionLocal", return_value=session):
            ok = await core.migrate_group_chat_id(-100, -1001999)
        self.assertFalse(ok)
        session.commit.assert_not_awaited()

    async def test_migrate_group_chat_id_noop_for_same_id(self) -> None:
        self.assertFalse(await core.migrate_group_chat_id(-100, -100))

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

    def test_serialize_context_messages_uses_media_description_not_stale_text(self) -> None:
        primary = SimpleNamespace(
            id=2,
            message_id=707000,
            text="нихуя себе зеля",
            content_type="text",
            caption=None,
            author=SimpleNamespace(name="_amti"),
            media_items=[],
        )
        photo_context = SimpleNamespace(
            id=1,
            message_id=706998,
            text="photo: Файл: photo, mime=image/jpeg, size=45388",
            content_type="photo",
            caption=None,
            author=SimpleNamespace(name="_amti"),
            media_items=[SimpleNamespace(description_snapshot="На экране приложения видно профиль пользователя.")],
        )

        ids_json, snapshot_json = core._serialize_context_messages(
            [photo_context, primary],
            primary_message_id=2,
        )

        snapshot = json.loads(snapshot_json)
        self.assertEqual(json.loads(ids_json), [706998, 707000])
        self.assertEqual(
            snapshot[0]["text"],
            "photo: На экране приложения видно профиль пользователя.",
        )
        self.assertEqual(snapshot[1]["text"], "нихуя себе зеля")
