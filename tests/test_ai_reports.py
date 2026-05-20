import json
import os
from datetime import date, datetime, timezone
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import ai, ai_reports, models


def _message(
    internal_id: int,
    telegram_id: int,
    text: str,
    author: str,
    reply_to: int | None = None,
    reactions: list[SimpleNamespace] | None = None,
) -> SimpleNamespace:
    return SimpleNamespace(
        id=internal_id,
        message_id=telegram_id,
        user_id=internal_id + 100,
        text=text,
        author=SimpleNamespace(name=author),
        reply_to_message_id=reply_to,
        reactions=reactions or [],
    )


class _DummySession:
    def __init__(self) -> None:
        self.added: list[object] = []
        self.added_all: list[object] = []
        self.commit = AsyncMock()
        self.rollback = AsyncMock()

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    def add(self, value: object) -> None:
        self.added.append(value)

    def add_all(self, values: list[object]) -> None:
        self.added_all.extend(values)

    async def flush(self) -> None:
        self.added[0].id = 901


class AIReportTests(unittest.IsolatedAsyncioTestCase):
    async def test_save_evaluation_report_stores_compact_run_and_score_rows(self) -> None:
        session = _DummySession()
        messages = [
            _message(1, 11, "setup", "Alice", reactions=[SimpleNamespace(emoji="😂", count=2)]),
            _message(2, 12, "punchline", "Bob", reply_to=11),
        ]
        window = SimpleNamespace(
            quote_day=date(2026, 5, 20),
            start_utc=datetime(2026, 5, 19, 18, 0, tzinfo=timezone.utc),
            end_utc=datetime(2026, 5, 20, 18, 0, tzinfo=timezone.utc),
        )
        evaluation = ai.EvaluationResult(
            scores={1: 0.4, 2: 0.9},
            actual_model="actual/model",
            requested_model="requested/model",
            request_id="req-1",
            quote_choice=ai.QuoteContextChoice(
                primary_id=2,
                context_ids=[1, 2],
                context_needed=True,
            ),
            day_verdict=ai.DayVerdict(
                should_publish=True,
                reason_code="worthy",
                reason_text="short reason",
            ),
        )

        with patch.object(ai_reports, "SessionLocal", return_value=session):
            await ai_reports.save_evaluation_report(
                group_id=7,
                chat_id=-100123456,
                window=window,
                source_messages=messages,
                scored_messages=messages,
                reaction_totals={1: 2, 2: 0},
                evaluation=evaluation,
                selected_message=messages[1],
            )

        run = session.added[0]
        self.assertIsInstance(run, models.AIEvaluationRun)
        self.assertEqual(run.message_count, 2)
        self.assertEqual(run.source_message_count, 2)
        self.assertEqual(run.context_message_ids, "[11,12]")
        self.assertTrue(run.context_needed)
        self.assertTrue(run.should_publish)
        self.assertEqual(run.request_id, "req-1")

        rows = session.added_all
        self.assertEqual(len(rows), 2)
        self.assertEqual([row.telegram_message_id for row in sorted(rows, key=lambda item: item.rank)], [12, 11])
        selected = next(row for row in rows if row.telegram_message_id == 12)
        self.assertTrue(selected.is_selected_primary)
        self.assertTrue(selected.is_selected_context)
        self.assertEqual(selected.ai_score_raw, 9.0)
        reacted = next(row for row in rows if row.telegram_message_id == 11)
        self.assertEqual(json.loads(reacted.reactions_snapshot), {"😂": 2})
        self.assertEqual(reacted.reaction_count, 2)

    async def test_save_evaluation_report_skips_score_rows_for_failed_ai(self) -> None:
        session = _DummySession()
        message = _message(1, 11, "fallback", "Alice")
        window = SimpleNamespace(
            quote_day=date(2026, 5, 20),
            start_utc=datetime(2026, 5, 19, 18, 0, tzinfo=timezone.utc),
            end_utc=datetime(2026, 5, 20, 18, 0, tzinfo=timezone.utc),
        )
        evaluation = ai.EvaluationResult(
            scores={1: 0.5},
            actual_model="requested/model",
            requested_model="requested/model",
            status="ai_failed",
        )

        with patch.object(ai_reports, "SessionLocal", return_value=session):
            await ai_reports.save_evaluation_report(
                group_id=7,
                chat_id=-100123456,
                window=window,
                source_messages=[message],
                scored_messages=[message],
                reaction_totals={1: 0},
                evaluation=evaluation,
                selected_message=message,
            )

        run = session.added[0]
        self.assertEqual(run.status, "ai_failed")
        self.assertEqual(session.added_all, [])
