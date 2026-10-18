import json
import os
from datetime import date, datetime, timezone
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

from sqlalchemy.exc import IntegrityError
from sqlalchemy.sql import Select

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
    content_type: str = "text",
    caption: str | None = None,
    media_status: str | None = None,
    media_items: list[SimpleNamespace] | None = None,
) -> SimpleNamespace:
    return SimpleNamespace(
        id=internal_id,
        message_id=telegram_id,
        user_id=internal_id + 100,
        text=text,
        content_type=content_type,
        caption=caption,
        media_status=media_status,
        author=SimpleNamespace(name=author),
        reply_to_message_id=reply_to,
        reactions=reactions or [],
        media_items=media_items or [],
    )


class _DummySession:
    def __init__(self) -> None:
        self.added: list[object] = []
        self.added_all: list[object] = []
        self.commit = AsyncMock()
        self.rollback = AsyncMock()
        # No report exists for the day yet.
        self.execute = AsyncMock(return_value=SimpleNamespace(first=lambda: None))

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


class _FakeDB:
    """Committed rows of ai_evaluation_runs and message_ai_scores."""

    def __init__(self) -> None:
        self.runs: list[models.AIEvaluationRun] = []
        self.scores: list[models.MessageAIScore] = []
        self.next_id = 1


class _FakeSession:
    """Transactional stand-in: uq_ai_evaluation_run_group_day is enforced on
    flush, and nothing reaches the FakeDB until commit."""

    def __init__(self, db: _FakeDB) -> None:
        self.db = db
        self.runs = list(db.runs)
        self.scores = list(db.scores)
        self.pending_runs: list[models.AIEvaluationRun] = []
        self.pending_scores: list[models.MessageAIScore] = []

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    def add(self, value: object) -> None:
        self.pending_runs.append(value)

    def add_all(self, values: list[object]) -> None:
        self.pending_scores.extend(values)

    async def execute(self, statement):
        params = statement.compile().params
        if isinstance(statement, Select):
            rows = [
                SimpleNamespace(id=run.id, status=run.status)
                for run in self.runs
                if run.group_id == params["group_id_1"] and run.quote_day == params["quote_day_1"]
            ]
            return SimpleNamespace(first=lambda: rows[0] if rows else None)
        if statement.table.name == "message_ai_scores":
            self.scores = [row for row in self.scores if row.run_id != params["run_id_1"]]
        else:
            self.runs = [run for run in self.runs if run.id != params["id_1"]]
        return SimpleNamespace(first=lambda: None)

    async def flush(self) -> None:
        for run in self.pending_runs:
            if any(r.group_id == run.group_id and r.quote_day == run.quote_day for r in self.runs):
                raise IntegrityError("INSERT", {}, Exception("uq_ai_evaluation_run_group_day"))
            run.id = self.db.next_id
            self.db.next_id += 1
            self.runs.append(run)
        self.pending_runs = []
        self.scores.extend(self.pending_scores)
        self.pending_scores = []

    async def commit(self) -> None:
        await self.flush()
        self.db.runs = list(self.runs)
        self.db.scores = list(self.scores)

    async def rollback(self) -> None:
        self.pending_runs = []
        self.pending_scores = []


class AIReportTests(unittest.IsolatedAsyncioTestCase):
    async def test_save_evaluation_report_stores_compact_run_and_score_rows(self) -> None:
        session = _DummySession()
        messages = [
            _message(1, 11, "setup", "Alice", reactions=[SimpleNamespace(emoji="😂", count=2)]),
            _message(
                2,
                12,
                "photo: человек держит табличку",
                "Bob",
                reply_to=11,
                content_type="photo",
                caption="смотри",
                media_status="analyzed",
                media_items=[
                    SimpleNamespace(
                        media_cache_id=77,
                        media_kind="photo",
                        telegram_file_id="file-1",
                        telegram_file_unique_id="unique-1",
                        mime_type="image/jpeg",
                        file_name=None,
                        file_size=12345,
                        width=640,
                        height=480,
                        duration=None,
                        sha256="a" * 64,
                        phash="b" * 16,
                        description_snapshot="человек держит табличку",
                    )
                ],
            ),
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
            usage=ai.TokenUsage(
                prompt_tokens=1200,
                completion_tokens=450,
                reasoning_tokens=300,
                total_tokens=1650,
                cost_usd=0.00042,
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
        self.assertEqual(run.prompt_tokens, 1200)
        self.assertEqual(run.completion_tokens, 450)
        self.assertEqual(run.reasoning_tokens, 300)
        self.assertEqual(run.total_tokens, 1650)
        self.assertAlmostEqual(run.cost_usd, 0.00042)

        rows = session.added_all
        self.assertEqual(len(rows), 2)
        self.assertEqual([row.telegram_message_id for row in sorted(rows, key=lambda item: item.rank)], [12, 11])
        selected = next(row for row in rows if row.telegram_message_id == 12)
        self.assertTrue(selected.is_selected_primary)
        self.assertTrue(selected.is_selected_context)
        self.assertEqual(selected.ai_score_raw, 9.0)
        self.assertEqual(selected.content_type, "photo")
        self.assertEqual(selected.caption_snapshot, "смотри")
        self.assertEqual(selected.media_status, "analyzed")
        self.assertEqual(selected.media_description_snapshot, "человек держит табличку")
        self.assertEqual(selected.media_kind, "photo")
        self.assertEqual(selected.telegram_file_id, "file-1")
        self.assertEqual(selected.telegram_file_unique_id, "unique-1")
        self.assertEqual(selected.mime_type, "image/jpeg")
        self.assertEqual(selected.file_size, 12345)
        self.assertEqual(selected.width, 640)
        self.assertEqual(selected.height, 480)
        self.assertEqual(selected.sha256, "a" * 64)
        self.assertEqual(selected.phash, "b" * 16)
        self.assertEqual(selected.media_cache_id, 77)
        reacted = next(row for row in rows if row.telegram_message_id == 11)
        self.assertEqual(reacted.content_type, "text")
        self.assertIsNone(reacted.media_description_snapshot)
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


def _day_window() -> SimpleNamespace:
    return SimpleNamespace(
        quote_day=date(2026, 10, 17),
        start_utc=datetime(2026, 10, 16, 18, 0, tzinfo=timezone.utc),
        end_utc=datetime(2026, 10, 17, 18, 0, tzinfo=timezone.utc),
    )


def _failed_evaluation() -> ai.EvaluationResult:
    return ai.EvaluationResult(
        scores={},
        actual_model="openrouter/fallback",
        requested_model="openrouter/fallback",
        status="ai_failed",
    )


def _parsed_evaluation(model: str, cost_usd: float) -> ai.EvaluationResult:
    return ai.EvaluationResult(
        scores={1: 0.4, 2: 0.9},
        actual_model=model,
        requested_model=model,
        request_id=f"req-{model}",
        quote_choice=ai.QuoteContextChoice(primary_id=2, context_ids=[1, 2], context_needed=True),
        day_verdict=ai.DayVerdict(should_publish=True, reason_code="worthy", reason_text="short reason"),
        usage=ai.TokenUsage(
            prompt_tokens=9000,
            completion_tokens=800,
            reasoning_tokens=300,
            total_tokens=9800,
            cost_usd=cost_usd,
        ),
    )


class AIReportReplacementTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        self.db = _FakeDB()
        self.messages = [_message(1, 11, "setup", "Alice"), _message(2, 12, "punchline", "Bob")]
        patcher = patch.object(ai_reports, "SessionLocal", side_effect=lambda: _FakeSession(self.db))
        patcher.start()
        self.addCleanup(patcher.stop)

    async def _save(self, evaluation: ai.EvaluationResult) -> None:
        await ai_reports.save_evaluation_report(
            group_id=7,
            chat_id=-100123456,
            window=_day_window(),
            source_messages=self.messages,
            scored_messages=self.messages,
            reaction_totals={1: 0, 2: 0},
            evaluation=evaluation,
            selected_message=self.messages[1],
        )

    async def test_successful_rerun_replaces_failed_report_of_the_same_day(self) -> None:
        await self._save(_failed_evaluation())
        failed_run_id = self.db.runs[0].id
        self.assertEqual([run.status for run in self.db.runs], ["ai_failed"])
        self.assertEqual(self.db.scores, [])

        await self._save(_parsed_evaluation("claude-opus-5-5", 0.11))

        self.assertEqual(len(self.db.runs), 1)
        run = self.db.runs[0]
        self.assertNotEqual(run.id, failed_run_id)
        self.assertEqual(run.status, "parsed")
        self.assertEqual(run.actual_model, "claude-opus-5-5")
        self.assertEqual(run.request_id, "req-claude-opus-5-5")
        self.assertEqual(run.total_tokens, 9800)
        self.assertAlmostEqual(run.cost_usd, 0.11)
        self.assertEqual(len(self.db.scores), 2)
        self.assertEqual({row.run_id for row in self.db.scores}, {run.id})

    async def test_rerun_drops_scores_of_the_replaced_report(self) -> None:
        await self._save(ai.EvaluationResult(scores={1: 0.5}, actual_model="first/model", status="parse_failed"))
        old_run = self.db.runs[0]
        self.db.scores.append(SimpleNamespace(run_id=old_run.id, telegram_message_id=11))

        await self._save(_parsed_evaluation("claude-opus-5-5", 0.11))

        self.assertEqual({row.run_id for row in self.db.scores}, {self.db.runs[0].id})
        self.assertEqual(len(self.db.scores), 2)

    async def test_parsed_report_is_not_touched_by_a_later_run(self) -> None:
        await self._save(_parsed_evaluation("claude-opus-5-5", 0.11))
        run = self.db.runs[0]
        scores = list(self.db.scores)

        await self._save(_parsed_evaluation("openrouter/fallback", 0.002))
        await self._save(_failed_evaluation())

        self.assertEqual(self.db.runs, [run])
        self.assertEqual(run.actual_model, "claude-opus-5-5")
        self.assertAlmostEqual(run.cost_usd, 0.11)
        self.assertEqual(self.db.scores, scores)

    async def test_failed_replacement_keeps_the_previous_report(self) -> None:
        await self._save(_failed_evaluation())
        failed_run = self.db.runs[0]

        with patch.object(ai_reports, "_score_rows", side_effect=RuntimeError("boom")):
            await self._save(_parsed_evaluation("claude-opus-5-5", 0.11))

        self.assertEqual(self.db.runs, [failed_run])
        self.assertEqual(failed_run.status, "ai_failed")
        self.assertEqual(self.db.scores, [])
