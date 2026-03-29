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

    async def test_process_group_recovers_stale_quotes_before_day_lookup(self) -> None:
        calls: list[str] = []

        async def recover_stale_quotes(chat_id: int) -> int:
            self.assertEqual(chat_id, self.group.chat_id)
            calls.append("recover")
            return 1

        async def get_quote_for_day(group_id: int, quote_day: date):
            self.assertEqual(group_id, self.group.id)
            self.assertEqual(quote_day, self.window.quote_day)
            calls.append("lookup")
            return None

        with (
            patch.object(
                scheduler,
                "_recover_stale_quotes_for_chat",
                new=AsyncMock(side_effect=recover_stale_quotes),
            ),
            patch.object(
                scheduler.core,
                "get_quote_for_day",
                new=AsyncMock(side_effect=get_quote_for_day),
            ),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "clear_window_messages", new=AsyncMock()) as clear_messages,
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock()) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        self.assertEqual(calls, ["recover", "lookup"])
        clear_messages.assert_not_awaited()
        pick_best_quote.assert_not_awaited()

    async def test_process_group_skips_empty_window_before_ai(self) -> None:
        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
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
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(
                scheduler.core,
                "clear_window_messages",
                new=AsyncMock(return_value=message_count),
            ) as clear_messages,
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock()) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        clear_messages.assert_awaited_once_with(self.group.chat_id, self.window)
        pick_best_quote.assert_not_awaited()

    async def test_process_group_calls_ai_for_publishable_window(self) -> None:
        message_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        evaluation = scoring.QuoteEvaluation(message_count=message_count, best_message=None)

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(scheduler.core, "clear_window_messages", new=AsyncMock()) as clear_messages,
            patch.object(
                scheduler.scoring,
                "pick_best_quote",
                new=AsyncMock(return_value=evaluation),
            ) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        clear_messages.assert_not_awaited()
        pick_best_quote.assert_awaited_once_with(
            self.group.chat_id,
            self.window,
            include_day_verdict=True,
            day_verdict_min_messages=scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW,
        )

    async def test_send_boring_notice_escapes_ai_reason_html(self) -> None:
        bot = SimpleNamespace(send_message=AsyncMock(return_value=SimpleNamespace(message_id=101)))
        quote = SimpleNamespace(
            id=7,
            quote_day=self.window.quote_day,
            decision_reason="LLM says <boring> & unstable",
        )

        with (
            patch.object(scheduler.core, "update_quote_notice", new=AsyncMock()) as update_notice,
            patch.object(scheduler.core, "mark_quote_status", new=AsyncMock()) as mark_status,
        ):
            await scheduler._send_boring_notice(bot, self.group, quote, clear_window_after=False)

        sent_text = bot.send_message.await_args.kwargs["text"]
        self.assertIn("LLM says &lt;boring&gt; &amp; unstable", sent_text)
        update_notice.assert_awaited_once_with(quote.id, 101)
        mark_status.assert_not_awaited()

    async def test_publish_quote_message_escapes_quote_and_author_html(self) -> None:
        bot = SimpleNamespace(
            send_message=AsyncMock(return_value=SimpleNamespace(message_id=202)),
            pin_chat_message=AsyncMock(),
        )
        quote = SimpleNamespace(
            id=8,
            quote_day=self.window.quote_day,
            text="1 < 2 & 3",
            message_id=55,
            decision_reason=None,
        )
        breakdown = scoring.ScoreBreakdown(reaction=0.2, ai=0.7, length=0.1, reaction_count=1)

        with (
            patch.object(scheduler.core, "update_quote_publication", new=AsyncMock()) as update_publication,
            patch.object(scheduler.core, "mark_quote_status", new=AsyncMock()) as mark_status,
            patch.object(scheduler.core, "append_quote_operation_error", new=AsyncMock()) as append_error,
        ):
            result = await scheduler._publish_quote_message(
                bot=bot,
                group=self.group,
                quote=quote,
                author_name="Alice & Bob",
                breakdown=breakdown,
                forced_by_admin=False,
                clear_window_after=False,
            )

        self.assertTrue(result)
        sent_text = bot.send_message.await_args.kwargs["text"]
        self.assertIn("1 &lt; 2 &amp; 3", sent_text)
        self.assertIn("Alice &amp; Bob", sent_text)
        update_publication.assert_awaited_once_with(
            quote_id=quote.id,
            bot_message_id=202,
            forced_by_admin=False,
            decision_reason=None,
        )
        mark_status.assert_not_awaited()
        append_error.assert_not_awaited()


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
            decision_status=STATUS_PUBLISH_UNKNOWN,
            operation_error=None,
        )

    async def test_manual_publish_recovers_stale_quotes_before_claiming_candidate(self) -> None:
        calls: list[str] = []

        async def recover_stale_quotes(chat_id: int) -> int:
            self.assertEqual(chat_id, -100123456)
            calls.append("recover")
            return 1

        async def claim_candidate(chat_id: int):
            self.assertEqual(chat_id, -100123456)
            calls.append("claim")
            return None, None

        with (
            patch.object(
                scheduler,
                "_recover_stale_quotes_for_chat",
                new=AsyncMock(side_effect=recover_stale_quotes),
            ),
            patch.object(
                scheduler.core,
                "get_latest_manual_publish_candidate",
                new=AsyncMock(return_value=None),
            ),
            patch.object(
                scheduler.core,
                "claim_latest_manual_publish_candidate",
                new=AsyncMock(side_effect=claim_candidate),
            ),
        ):
            result = await scheduler.manual_publish_latest(SimpleNamespace(), -100123456)

        self.assertEqual(result, "nothing")
        self.assertEqual(calls, ["recover"])

    async def test_manual_publish_latest_accepts_timeout_based_publish_unknown(self) -> None:
        quote = self._make_quote()
        quote.operation_error = "In-progress quote timed out before final status confirmation."

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(
                scheduler.core,
                "get_latest_manual_publish_candidate",
                new=AsyncMock(return_value=quote),
            ),
            patch.object(
                scheduler.core,
                "claim_latest_manual_publish_candidate",
                new=AsyncMock(return_value=(quote, STATUS_PUBLISH_UNKNOWN)),
            ),
            patch.object(
                scheduler,
                "_publish_quote_message",
                new=AsyncMock(return_value=True),
            ) as publish_quote,
        ):
            result = await scheduler.manual_publish_latest(SimpleNamespace(), quote.group.chat_id)

        self.assertEqual(result, "published")
        self.assertTrue(publish_quote.await_args.kwargs["forced_by_admin"])
        self.assertTrue(publish_quote.await_args.kwargs["clear_window_after"])

    async def test_manual_publish_latest_skips_already_sent_publish_unknown(self) -> None:
        quote = self._make_quote()
        quote.operation_error = "Telegram message was sent, but DB finalization failed: lost DB connection"

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(
                scheduler.core,
                "get_latest_manual_publish_candidate",
                new=AsyncMock(return_value=quote),
            ),
            patch.object(
                scheduler.core,
                "claim_latest_manual_publish_candidate",
                new=AsyncMock(),
            ) as claim_candidate,
            patch.object(
                scheduler,
                "_publish_quote_message",
                new=AsyncMock(),
            ) as publish_quote,
        ):
            result = await scheduler.manual_publish_latest(SimpleNamespace(), quote.group.chat_id)

        self.assertEqual(result, "already_sent")
        claim_candidate.assert_not_awaited()
        publish_quote.assert_not_awaited()

    async def test_manual_publish_latest_accepts_boring_notice_unknown(self) -> None:
        quote = self._make_quote()
        quote.decision_status = STATUS_BORING_NOTICE_UNKNOWN

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(
                scheduler.core,
                "get_latest_manual_publish_candidate",
                new=AsyncMock(return_value=quote),
            ),
            patch.object(
                scheduler.core,
                "claim_latest_manual_publish_candidate",
                new=AsyncMock(return_value=(quote, STATUS_BORING_NOTICE_UNKNOWN)),
            ),
            patch.object(
                scheduler,
                "_publish_quote_message",
                new=AsyncMock(return_value=True),
            ) as publish_quote,
        ):
            result = await scheduler.manual_publish_latest(SimpleNamespace(), quote.group.chat_id)

        self.assertEqual(result, "published")
        self.assertTrue(publish_quote.await_args.kwargs["forced_by_admin"])
        self.assertTrue(publish_quote.await_args.kwargs["clear_window_after"])


class ManualPublishableStatusesTests(unittest.TestCase):
    def test_manual_publishable_statuses_include_unknown_recovery_states(self) -> None:
        self.assertIn(STATUS_PUBLISH_UNKNOWN, MANUAL_PUBLISHABLE_STATUSES)
        self.assertIn(STATUS_BORING_NOTICE_UNKNOWN, MANUAL_PUBLISHABLE_STATUSES)
