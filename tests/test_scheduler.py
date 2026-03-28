from datetime import date, datetime, timezone
import os
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import scheduler, scoring
from app.quote_status import (
    MANUAL_PUBLISHABLE_STATUSES,
    STATUS_BORING_NOTICE_UNKNOWN,
    STATUS_PUBLISH_UNKNOWN,
)


def _make_window() -> SimpleNamespace:
    start_utc = datetime(2026, 3, 26, 20, 0, tzinfo=timezone.utc)
    end_utc = datetime(2026, 3, 27, 20, 0, tzinfo=timezone.utc)
    return SimpleNamespace(
        quote_day=date(2026, 3, 27),
        start_local=start_utc,
        end_local=end_utc,
        start_utc=start_utc,
        end_utc=end_utc,
    )


class SchedulerFlowTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        self.group = SimpleNamespace(id=1, chat_id=-100123456, name="Quoto Test Chat")
        self.window = _make_window()

    async def test_process_group_skips_empty_window_before_ai(self) -> None:
        with (
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "clear_window_messages", new=AsyncMock()) as clear_messages,
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock()) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        clear_messages.assert_not_awaited()
        pick_best_quote.assert_not_awaited()

    async def test_process_group_skips_subthreshold_window_before_ai(self) -> None:
        message_count = max(1, scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW - 1)

        with (
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(scheduler.core, "clear_window_messages", new=AsyncMock(return_value=message_count)) as clear_messages,
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock()) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        clear_messages.assert_awaited_once_with(self.group.chat_id, self.window)
        pick_best_quote.assert_not_awaited()

    async def test_process_group_calls_ai_for_publishable_window(self) -> None:
        message_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        evaluation = scoring.QuoteEvaluation(message_count=message_count, best_message=None)

        with (
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(scheduler.core, "clear_window_messages", new=AsyncMock()) as clear_messages,
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock(return_value=evaluation)) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        clear_messages.assert_not_awaited()
        pick_best_quote.assert_awaited_once_with(
            self.group.chat_id,
            self.window,
            include_day_verdict=True,
            day_verdict_min_messages=scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW,
        )


class ManualPublishRecoveryTests(unittest.IsolatedAsyncioTestCase):
    def _make_quote(self) -> SimpleNamespace:
        return SimpleNamespace(
            group=SimpleNamespace(chat_id=-100123456, name="Quoto Test Chat"),
            author=SimpleNamespace(name="Alice"),
            reaction_score=0.2,
            ai_score=0.7,
            length_score=0.1,
            reaction_count=3,
            ai_model="openrouter/test",
            ai_best_text=None,
        )

    async def test_manual_publish_latest_accepts_publish_unknown(self) -> None:
        quote = self._make_quote()

        with (
            patch.object(
                scheduler.core,
                "claim_latest_manual_publish_candidate",
                new=AsyncMock(return_value=(quote, STATUS_PUBLISH_UNKNOWN)),
            ),
            patch.object(scheduler, "_publish_quote_message", new=AsyncMock(return_value=True)) as publish_quote,
        ):
            result = await scheduler.manual_publish_latest(SimpleNamespace(), quote.group.chat_id)

        self.assertTrue(result)
        self.assertTrue(publish_quote.await_args.kwargs["forced_by_admin"])
        self.assertTrue(publish_quote.await_args.kwargs["clear_window_after"])

    async def test_manual_publish_latest_accepts_boring_notice_unknown(self) -> None:
        quote = self._make_quote()

        with (
            patch.object(
                scheduler.core,
                "claim_latest_manual_publish_candidate",
                new=AsyncMock(return_value=(quote, STATUS_BORING_NOTICE_UNKNOWN)),
            ),
            patch.object(scheduler, "_publish_quote_message", new=AsyncMock(return_value=True)) as publish_quote,
        ):
            result = await scheduler.manual_publish_latest(SimpleNamespace(), quote.group.chat_id)

        self.assertTrue(result)
        self.assertTrue(publish_quote.await_args.kwargs["forced_by_admin"])
        self.assertTrue(publish_quote.await_args.kwargs["clear_window_after"])


class ManualPublishableStatusesTests(unittest.TestCase):
    def test_manual_publishable_statuses_include_unknown_recovery_states(self) -> None:
        self.assertIn(STATUS_PUBLISH_UNKNOWN, MANUAL_PUBLISHABLE_STATUSES)
        self.assertIn(STATUS_BORING_NOTICE_UNKNOWN, MANUAL_PUBLISHABLE_STATUSES)
