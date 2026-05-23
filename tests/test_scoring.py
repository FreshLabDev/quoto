import os
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import ai, scoring


def _message(
    internal_id: int,
    telegram_id: int,
    text: str,
    author: str = "Alice",
    reply_to: int | None = None,
    reactions: list[SimpleNamespace] | None = None,
    content_type: str = "text",
    caption: str | None = None,
    media_items: list[SimpleNamespace] | None = None,
) -> SimpleNamespace:
    return SimpleNamespace(
        id=internal_id,
        message_id=telegram_id,
        user_id=internal_id + 1000,
        text=text,
        content_type=content_type,
        caption=caption,
        author=SimpleNamespace(name=author),
        reply_to_message_id=reply_to,
        reactions=reactions or [],
        media_items=media_items or [],
    )


class _DummyResult:
    def __init__(self, messages: list[SimpleNamespace]) -> None:
        self._messages = messages

    def scalars(self):
        return self

    def all(self) -> list[SimpleNamespace]:
        return self._messages


class _DummySession:
    def __init__(self, messages: list[SimpleNamespace]) -> None:
        self._messages = messages

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def execute(self, _stmt):
        return _DummyResult(self._messages)


class ScoringTests(unittest.IsolatedAsyncioTestCase):
    def test_score_breakdown_total_uses_ai_only(self) -> None:
        breakdown = scoring.ScoreBreakdown(reaction=1.0, ai=0.2, length=1.0)

        self.assertEqual(breakdown.total, 0.2)

    async def test_pick_best_quote_sends_reactions_context_and_uses_ai_winner(self) -> None:
        messages = [
            _message(
                1,
                11,
                "popular but weaker",
                "Alice",
                reactions=[
                    SimpleNamespace(emoji="😂", count=3),
                    SimpleNamespace(emoji="❤️", count=1),
                ],
            ),
            _message(2, 12, "better AI quote", "Bob"),
        ]
        window = SimpleNamespace(start_utc=1, end_utc=2, start_local=1, end_local=2)
        evaluation_result = ai.EvaluationResult(
            scores={1: 0.1, 2: 0.9},
            actual_model="openrouter/test",
        )

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock(return_value=evaluation_result)) as evaluate,
        ):
            result = await scoring.pick_best_quote(-100123456, window)

        self.assertEqual(result.best_message.id, 2)
        self.assertEqual(result.breakdown.total, 0.9)
        ai_payload = evaluate.await_args.args[0]
        self.assertNotIn("message_id", ai_payload[0])
        self.assertEqual(ai_payload[0]["reactions"], {"😂": 3, "❤️": 1})
        self.assertNotIn("reactions", ai_payload[1])

    async def test_pick_best_quote_filters_only_exact_quoto_hashtag_before_ai(self) -> None:
        messages = [
            _message(1, 11, "previous report #quoto", "Alice"),
            _message(2, 12, "plain quoto word stays", "Bob"),
            _message(3, 13, "Подробности дня #60 stays", "Cara"),
            _message(4, 14, "other #quotoday stays", "Dan"),
            _message(5, 15, "mixed #QuOtO punctuation", "Eve"),
        ]
        window = SimpleNamespace(start_utc=1, end_utc=2, start_local=1, end_local=2)
        evaluation_result = ai.EvaluationResult(
            scores={2: 0.6, 3: 0.7, 4: 0.8},
            actual_model="openrouter/test",
        )

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock(return_value=evaluation_result)) as evaluate,
        ):
            result = await scoring.pick_best_quote(-100123456, window)

        self.assertEqual(result.best_message.id, 4)
        ai_payload = evaluate.await_args.args[0]
        self.assertEqual([item["id"] for item in ai_payload], [2, 3, 4])

    async def test_pick_best_quote_sends_internal_reply_id_and_media_description(self) -> None:
        messages = [
            _message(1, 11, "setup", "Alice"),
            _message(
                2,
                12,
                "photo: человек показывает табличку",
                "Bob",
                reply_to=11,
                content_type="photo",
                caption="смотри",
                media_items=[SimpleNamespace(description_snapshot="человек показывает табличку")],
            ),
        ]
        window = SimpleNamespace(start_utc=1, end_utc=2, start_local=1, end_local=2)
        evaluation_result = ai.EvaluationResult(scores={1: 0.2, 2: 0.9}, actual_model="openrouter/test")

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock(return_value=evaluation_result)) as evaluate,
        ):
            await scoring.pick_best_quote(-100123456, window)

        payload = evaluate.await_args.args[0]
        self.assertEqual(payload[1]["reply_to_id"], 1)
        self.assertEqual(payload[1]["kind"], "photo")
        self.assertEqual(payload[1]["caption"], "смотри")
        self.assertEqual(payload[1]["desc"], "человек показывает табличку")
        self.assertNotIn("text", payload[1])
        self.assertNotIn("message_id", payload[1])

    async def test_pick_best_quote_does_not_allow_sticker_as_primary(self) -> None:
        messages = [
            _message(1, 11, "normal text", "Alice"),
            _message(
                2,
                12,
                "sticker: смешной стикер",
                "Bob",
                content_type="sticker",
                media_items=[SimpleNamespace(description_snapshot="смешной стикер")],
            ),
        ]
        window = SimpleNamespace(start_utc=1, end_utc=2, start_local=1, end_local=2)
        evaluation_result = ai.EvaluationResult(
            scores={1: 0.4, 2: 0.95},
            actual_model="openrouter/test",
            quote_choice=ai.QuoteContextChoice(primary_id=2, context_ids=[1, 2], context_needed=True),
        )

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock(return_value=evaluation_result)),
        ):
            result = await scoring.pick_best_quote(-100123456, window)

        self.assertEqual(result.best_message.id, 1)
        self.assertEqual([message.id for message in result.context_messages], [1, 2])

    async def test_pick_best_quote_skips_ai_when_all_messages_filtered(self) -> None:
        messages = [
            _message(1, 11, "#quoto", "Alice"),
            _message(2, 12, "daily card #QUOTO", "Bob"),
        ]
        window = SimpleNamespace(start_utc=1, end_utc=2, start_local=1, end_local=2)

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock()) as evaluate,
        ):
            result = await scoring.pick_best_quote(-100123456, window)

        self.assertEqual(result.message_count, 0)
        self.assertEqual(result.source_message_count, 2)
        evaluate.assert_not_awaited()

    async def test_pick_best_quote_uses_ai_primary_and_valid_contiguous_context(self) -> None:
        messages = [
            _message(1, 11, "setup", "Alice"),
            _message(2, 12, "bridge", "Bob"),
            _message(3, 13, "punchline", "Cara"),
        ]
        window = SimpleNamespace(start_utc=1, end_utc=2, start_local=1, end_local=2)
        evaluation_result = ai.EvaluationResult(
            scores={1: 0.1, 2: 0.3, 3: 0.8},
            actual_model="openrouter/test",
            quote_choice=ai.QuoteContextChoice(
                primary_id=3,
                context_ids=[1, 2, 3],
                context_needed=True,
            ),
        )

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock(return_value=evaluation_result)),
            patch.object(scoring.ai_reports, "save_evaluation_report", new=AsyncMock()),
        ):
            result = await scoring.pick_best_quote(-100123456, window)

        self.assertEqual(result.best_message.id, 3)
        self.assertEqual([message.id for message in result.context_messages], [1, 2, 3])

    async def test_pick_best_quote_persists_daily_evaluation_report(self) -> None:
        messages = [
            _message(1, 11, "setup #quoto", "Alice"),
            _message(2, 12, "real setup", "Bob"),
            _message(3, 13, "real punchline", "Cara", reply_to=12),
        ]
        window = SimpleNamespace(
            quote_day="2026-05-20",
            start_utc=1,
            end_utc=2,
            start_local=1,
            end_local=2,
        )
        evaluation_result = ai.EvaluationResult(
            scores={2: 0.4, 3: 0.9},
            actual_model="openrouter/test",
            requested_model="openrouter/requested",
            quote_choice=ai.QuoteContextChoice(
                primary_id=3,
                context_ids=[2, 3],
                context_needed=True,
            ),
        )

        with (
            patch.object(scoring, "SessionLocal", return_value=_DummySession(messages)),
            patch.object(scoring.ai, "evaluate_messages", new=AsyncMock(return_value=evaluation_result)),
            patch.object(scoring.ai_reports, "save_evaluation_report", new=AsyncMock()) as save_report,
        ):
            result = await scoring.pick_best_quote(
                -100123456,
                window,
                include_day_verdict=True,
                group_id=42,
            )

        self.assertEqual(result.best_message.id, 3)
        save_report.assert_awaited_once()
        kwargs = save_report.await_args.kwargs
        self.assertEqual(kwargs["group_id"], 42)
        self.assertEqual([message.id for message in kwargs["source_messages"]], [1, 2, 3])
        self.assertEqual([message.id for message in kwargs["scored_messages"]], [2, 3])

    def test_context_validator_accepts_reply_connected_noncontiguous_thread(self) -> None:
        messages = [
            _message(1, 11, "root", "Alice"),
            _message(2, 12, "unrelated", "Bob"),
            _message(3, 13, "reply", "Cara", reply_to=11),
            _message(4, 14, "other", "Dan"),
            _message(5, 15, "punchline", "Eve", reply_to=13),
        ]
        choice = ai.QuoteContextChoice(
            primary_id=5,
            context_ids=[1, 3, 5],
            context_needed=True,
        )

        result = scoring._valid_context_messages(choice, messages, messages[4])

        self.assertEqual([message.id for message in result], [1, 3, 5])

    def test_context_validator_rejects_unrelated_noncontiguous_messages(self) -> None:
        messages = [
            _message(1, 11, "root", "Alice"),
            _message(2, 12, "unrelated", "Bob"),
            _message(3, 13, "punchline", "Cara"),
        ]
        choice = ai.QuoteContextChoice(
            primary_id=3,
            context_ids=[1, 3],
            context_needed=True,
        )

        result = scoring._valid_context_messages(choice, messages, messages[2])

        self.assertEqual(result, [])

    def test_context_validator_rejects_invalid_context_contracts(self) -> None:
        messages = [
            _message(1, 11, "one", "Alice"),
            _message(2, 12, "two", "Bob"),
            _message(3, 13, "three", "Cara"),
            _message(4, 14, "four", "Dan"),
            _message(5, 15, "five", "Eve"),
            _message(6, 16, "six", "Fred"),
        ]

        too_many = ai.QuoteContextChoice(primary_id=6, context_ids=[1, 2, 3, 4, 5, 6], context_needed=True)
        missing_primary = ai.QuoteContextChoice(primary_id=6, context_ids=[1, 2, 3], context_needed=True)
        outside_day = ai.QuoteContextChoice(primary_id=6, context_ids=[4, 5, 999, 6], context_needed=True)

        self.assertEqual(scoring._valid_context_messages(too_many, messages, messages[5]), [])
        self.assertEqual(scoring._valid_context_messages(missing_primary, messages, messages[5]), [])
        self.assertEqual(scoring._valid_context_messages(outside_day, messages, messages[5]), [])
