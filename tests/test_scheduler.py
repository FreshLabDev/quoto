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
    STATUS_SKIPPED_BORING,
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


class _DummyResult:
    def __init__(self, rows: list[SimpleNamespace]) -> None:
        self._rows = rows

    def scalars(self):
        return self

    def all(self) -> list[SimpleNamespace]:
        return self._rows


class _GroupsSession:
    def __init__(self, groups: list[SimpleNamespace]) -> None:
        self._groups = groups

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def execute(self, _stmt):
        return _DummyResult(self._groups)


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

    async def test_quote_job_catches_up_after_scheduled_minute(self) -> None:
        self.group.quote_hour = 21
        self.group.quote_minute = 0
        now = datetime(2026, 3, 27, 19, 3, tzinfo=timezone.utc)

        with (
            patch.object(scheduler, "SessionLocal", return_value=_GroupsSession([self.group])),
            patch.object(scheduler, "utc_now", return_value=now),
            patch.object(scheduler, "_process_group", new=AsyncMock()) as process_group,
        ):
            await scheduler.quote_of_the_day_job(SimpleNamespace())

        process_group.assert_awaited_once()
        window = process_group.await_args.args[2]
        self.assertEqual(window.end_local.hour, 21)
        self.assertEqual(window.end_local.minute, 0)

    async def test_quote_job_ignores_stale_unprocessed_cutoff(self) -> None:
        self.group.quote_hour = 21
        self.group.quote_minute = 0
        now = datetime(2026, 3, 27, 21, 1, tzinfo=timezone.utc)

        with (
            patch.object(scheduler, "SessionLocal", return_value=_GroupsSession([self.group])),
            patch.object(scheduler, "utc_now", return_value=now),
            patch.object(scheduler, "_process_group", new=AsyncMock()) as process_group,
        ):
            await scheduler.quote_of_the_day_job(SimpleNamespace())

        process_group.assert_not_awaited()

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
            group_id=self.group.id,
            detect_interface_language=True,
        )

    async def test_process_group_uses_group_min_messages_setting(self) -> None:
        self.group.min_messages = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW + 5
        message_count = self.group.min_messages - 1

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

    async def test_process_group_does_not_detect_language_when_already_set(self) -> None:
        self.group.language_code = "en"
        message_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        evaluation = scoring.QuoteEvaluation(message_count=message_count, best_message=None)

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(
                scheduler.scoring,
                "pick_best_quote",
                new=AsyncMock(return_value=evaluation),
            ) as pick_best_quote,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        self.assertFalse(pick_best_quote.await_args.kwargs["detect_interface_language"])

    async def test_process_group_persists_detected_language_before_publish(self) -> None:
        message_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        best_message = SimpleNamespace(author=SimpleNamespace(name="Alice"))
        evaluation = scoring.QuoteEvaluation(
            message_count=message_count,
            best_message=best_message,
            should_publish=True,
            detected_language_code="uk",
            detected_chat_language="Ukrainian",
        )
        quote = SimpleNamespace(id=5)

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock(return_value=evaluation)),
            patch.object(scheduler.core, "set_group_language_auto", new=AsyncMock(return_value=True)) as set_language,
            patch.object(scheduler.core, "create_quote_record", new=AsyncMock(return_value=(quote, True))),
            patch.object(scheduler, "_publish_quote_message", new=AsyncMock(return_value=True)) as publish,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        set_language.assert_awaited_once_with(self.group.id, "uk")
        self.assertEqual(self.group.language_code, "uk")
        publish.assert_awaited_once()

    async def test_process_group_disables_quote_context_storage(self) -> None:
        self.group.quote_context_enabled = False
        message_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        best_message = SimpleNamespace(author=SimpleNamespace(name="Alice"))
        context_message = SimpleNamespace()
        evaluation = scoring.QuoteEvaluation(
            message_count=message_count,
            best_message=best_message,
            context_messages=[context_message],
            should_publish=True,
        )
        quote = SimpleNamespace(id=5)

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock(return_value=evaluation)),
            patch.object(scheduler.core, "create_quote_record", new=AsyncMock(return_value=(quote, True))) as create_quote,
            patch.object(scheduler, "_publish_quote_message", new=AsyncMock(return_value=True)),
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        self.assertEqual(create_quote.await_args.kwargs["context_messages"], [])

    async def test_process_group_skips_boring_notice_when_disabled(self) -> None:
        self.group.boring_notice_enabled = False
        message_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        best_message = SimpleNamespace(author=SimpleNamespace(name="Alice"))
        evaluation = scoring.QuoteEvaluation(
            message_count=message_count,
            best_message=best_message,
            should_publish=False,
            day_reason_text="boring",
        )
        quote = SimpleNamespace(id=6)

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=message_count)),
            patch.object(scheduler.scoring, "pick_best_quote", new=AsyncMock(return_value=evaluation)),
            patch.object(scheduler.core, "create_quote_record", new=AsyncMock(return_value=(quote, True))) as create_quote,
            patch.object(
                scheduler.core,
                "clear_window_messages",
                new=AsyncMock(return_value=message_count),
            ) as clear_messages,
            patch.object(scheduler, "_send_boring_notice", new=AsyncMock()) as send_notice,
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        self.assertEqual(create_quote.await_args.kwargs["decision_status"], STATUS_SKIPPED_BORING)
        clear_messages.assert_awaited_once_with(self.group.chat_id, self.window)
        send_notice.assert_not_awaited()

    async def test_process_group_clears_window_when_all_messages_filtered_before_ai(self) -> None:
        source_count = scheduler.settings.MIN_MESSAGES_FOR_AUTO_REVIEW
        evaluation = scoring.QuoteEvaluation(message_count=0, source_message_count=source_count)

        with (
            patch.object(scheduler, "_recover_stale_quotes_for_chat", new=AsyncMock(return_value=0)),
            patch.object(scheduler.core, "get_quote_for_day", new=AsyncMock(return_value=None)),
            patch.object(scheduler.core, "count_window_messages", new=AsyncMock(return_value=source_count)),
            patch.object(
                scheduler.core,
                "clear_window_messages",
                new=AsyncMock(return_value=source_count),
            ) as clear_messages,
            patch.object(
                scheduler.scoring,
                "pick_best_quote",
                new=AsyncMock(return_value=evaluation),
            ),
        ):
            await scheduler._process_group(SimpleNamespace(), self.group, self.window)

        clear_messages.assert_awaited_once_with(self.group.chat_id, self.window)

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
                clear_window_after=False,
            )

        self.assertTrue(result)
        sent_text = bot.send_message.await_args.kwargs["text"]
        self.assertIn("1 &lt; 2 &amp; 3", sent_text)
        self.assertIn("Alice &amp; Bob", sent_text)
        update_publication.assert_awaited_once_with(
            quote_id=quote.id,
            bot_message_id=202,
            decision_reason=None,
        )
        mark_status.assert_not_awaited()
        append_error.assert_not_awaited()

    async def test_publish_quote_message_copies_media_with_day_title(self) -> None:
        bot = SimpleNamespace(
            send_message=AsyncMock(return_value=SimpleNamespace(message_id=404)),
            copy_message=AsyncMock(return_value=SimpleNamespace(message_id=204)),
            pin_chat_message=AsyncMock(),
        )
        quote = SimpleNamespace(
            id=10,
            quote_day=self.window.quote_day,
            text="photo: человек держит табличку",
            message_id=57,
            content_type="photo",
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
                author_name="Bob",
                breakdown=breakdown,
                clear_window_after=False,
            )

        self.assertTrue(result)
        bot.send_message.assert_not_awaited()
        bot.copy_message.assert_awaited_once()
        copy_kwargs = bot.copy_message.await_args.kwargs
        self.assertEqual(copy_kwargs["from_chat_id"], self.group.chat_id)
        self.assertEqual(copy_kwargs["message_id"], 57)
        self.assertIn("🏆 <b>Photo of the Day</b>", copy_kwargs["caption"])
        update_publication.assert_awaited_once_with(
            quote_id=quote.id,
            bot_message_id=204,
            decision_reason=None,
        )
        mark_status.assert_not_awaited()
        append_error.assert_not_awaited()

    async def test_publish_quote_message_falls_back_to_text_when_media_copy_fails(self) -> None:
        bot = SimpleNamespace(
            send_message=AsyncMock(return_value=SimpleNamespace(message_id=205)),
            copy_message=AsyncMock(side_effect=RuntimeError("copy failed")),
            pin_chat_message=AsyncMock(),
        )
        quote = SimpleNamespace(
            id=11,
            quote_day=self.window.quote_day,
            text="voice: смешная фраза",
            message_id=58,
            content_type="voice",
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
                author_name="Alice",
                breakdown=breakdown,
                clear_window_after=False,
            )

        self.assertTrue(result)
        bot.copy_message.assert_awaited_once()
        bot.send_message.assert_awaited_once()
        sent_text = bot.send_message.await_args.kwargs["text"]
        self.assertIn("🏆 <b>Voice of the Day</b>", sent_text)
        update_publication.assert_awaited_once_with(
            quote_id=quote.id,
            bot_message_id=205,
            decision_reason=None,
        )
        mark_status.assert_not_awaited()
        append_error.assert_any_await(quote.id, "Media copy failed, text fallback used: copy failed")

    async def test_publish_quote_message_renders_context_dialog(self) -> None:
        bot = SimpleNamespace(
            send_message=AsyncMock(return_value=SimpleNamespace(message_id=203)),
            pin_chat_message=AsyncMock(),
        )
        quote = SimpleNamespace(
            id=9,
            quote_day=self.window.quote_day,
            text="primary",
            message_id=56,
            decision_reason=None,
            context_snapshot=(
                '[{"message_id":55,"author":"Alice <A>","text":"setup & context","is_primary":false},'
                '{"message_id":56,"author":"Bob","text":"punch <line>","is_primary":true}]'
            ),
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
                author_name="Bob",
                breakdown=breakdown,
                clear_window_after=False,
            )

        self.assertTrue(result)
        sent_text = bot.send_message.await_args.kwargs["text"]
        self.assertIn("<b>Alice &lt;A&gt;:</b> setup &amp; context", sent_text)
        self.assertIn("<b>Bob:</b> <i>«punch &lt;line&gt;»</i>", sent_text)
        update_publication.assert_awaited_once()
        mark_status.assert_not_awaited()
        append_error.assert_not_awaited()

    async def test_publish_quote_message_can_skip_pin(self) -> None:
        bot = SimpleNamespace(
            send_message=AsyncMock(return_value=SimpleNamespace(message_id=202)),
            pin_chat_message=AsyncMock(),
        )
        quote = SimpleNamespace(
            id=12,
            quote_day=self.window.quote_day,
            text="quote",
            message_id=60,
            decision_reason=None,
        )
        breakdown = scoring.ScoreBreakdown(ai=0.7)

        with (
            patch.object(scheduler.core, "update_quote_publication", new=AsyncMock()),
            patch.object(scheduler.core, "mark_quote_status", new=AsyncMock()),
            patch.object(scheduler.core, "append_quote_operation_error", new=AsyncMock()) as append_error,
        ):
            result = await scheduler._publish_quote_message(
                bot=bot,
                group=self.group,
                quote=quote,
                author_name="Alice",
                breakdown=breakdown,
                clear_window_after=False,
                pin_enabled=False,
            )

        self.assertTrue(result)
        bot.pin_chat_message.assert_not_awaited()
        append_error.assert_not_awaited()

    async def test_recover_pending_media_job_uses_configured_batch_size(self) -> None:
        bot = SimpleNamespace()

        with (
            patch.object(scheduler.settings, "MEDIA_PENDING_RETRY_BATCH_SIZE", 7),
            patch.object(scheduler.media, "process_pending_media", new=AsyncMock(return_value=2)) as process_pending,
        ):
            await scheduler.recover_pending_media_job(bot)

        process_pending.assert_awaited_once_with(bot, limit=7)

    def test_setup_scheduler_registers_pending_media_recovery_job(self) -> None:
        sched = scheduler.setup_scheduler(SimpleNamespace())

        self.assertIsNotNone(sched.get_job("pending_media_recovery"))
