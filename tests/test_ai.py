import json
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock
from unittest.mock import patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import ai
from app.ai import DayVerdictParseError, _parse_day_payload
from app.quote_status import REASON_BORING_DAY


class DayVerdictParsingTests(unittest.TestCase):
    def test_string_false_is_parsed_strictly(self) -> None:
        entries, verdict, quote_choice = _parse_day_payload(
            '{"day":{"should_publish":"false","reason_text":"boring"},"messages":[{"id":1,"score":4}]}'
        )

        self.assertEqual(entries, [{"id": 1, "score": 4}])
        self.assertFalse(verdict.should_publish)
        self.assertEqual(verdict.reason_code, REASON_BORING_DAY)
        self.assertEqual(verdict.reason_text, "boring")
        self.assertIsNone(quote_choice)

    def test_missing_day_block_is_rejected(self) -> None:
        with self.assertRaises(DayVerdictParseError):
            _parse_day_payload('{"messages":[{"id":1,"score":5}]}')

    def test_legacy_array_payload_is_rejected(self) -> None:
        with self.assertRaises(DayVerdictParseError):
            _parse_day_payload('[{"id":1,"score":5}]')

    def test_parse_day_payload_ignores_surrounding_prose_and_braces(self) -> None:
        entries, verdict, quote_choice = _parse_day_payload(
            'note {"draft": true}\n'
            '```json\n'
            '{"day":{"should_publish":"false","reason_text":"boring"},'
            '"quote":{"primary_id":1,"context_ids":[1,2],"context_needed":true},'
            '"messages":[{"id":1,"score":4}]}\n'
            '```\n'
            "thanks"
        )

        self.assertEqual(entries, [{"id": 1, "score": 4}])
        self.assertFalse(verdict.should_publish)
        self.assertEqual(verdict.reason_code, REASON_BORING_DAY)
        self.assertEqual(quote_choice.primary_id, 1)
        self.assertEqual(quote_choice.context_ids, [1, 2])
        self.assertTrue(quote_choice.context_needed)


class AIRetryTests(unittest.IsolatedAsyncioTestCase):
    async def test_evaluate_messages_sends_reactions_when_present(self) -> None:
        captured_body: dict | None = None

        class FakeResponse:
            status_code = 200
            text = '{"choices":[{"message":{"content":"ok"}}]}'

            def raise_for_status(self) -> None:
                return None

            def json(self) -> dict:
                return {
                    "model": "openrouter/test",
                    "choices": [{"message": {"content": '[{"id":1,"score":7},{"id":2,"score":8}]'}}],
                }

        class FakeClient:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

            async def post(self, *_args, **kwargs):
                nonlocal captured_body
                captured_body = kwargs["json"]
                return FakeResponse()

        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai.settings, "OPENROUTER_MODEL", "openrouter/test"),
            patch.object(ai.settings, "OPENROUTER_REASONING_EFFORT", "low"),
            patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
        ):
            result = await ai.evaluate_messages(
                [
                    {
                        "id": 1,
                        "message_id": 11,
                        "author": "Alice",
                        "text": "reacted quote",
                        "reply_to_message_id": 10,
                        "reactions": {"😂": 3, "❤️": 1},
                    },
                    {"id": 2, "message_id": 12, "author": "Bob", "text": "plain quote"},
                ],
                include_day_verdict=False,
            )

        self.assertEqual(result.scores, {1: 0.7, 2: 0.8})
        self.assertIsNotNone(captured_body)
        self.assertEqual(
            captured_body["reasoning"],
            {"enabled": True, "effort": "low", "exclude": True},
        )
        user_payload = json.loads(captured_body["messages"][1]["content"])
        self.assertEqual(user_payload[0]["message_id"], 11)
        self.assertEqual(user_payload[0]["reply_to_message_id"], 10)
        self.assertEqual(user_payload[0]["reactions"], {"😂": 3, "❤️": 1})
        self.assertNotIn("reactions", user_payload[1])
        self.assertNotIn("reply_to_message_id", user_payload[1])

    async def test_evaluate_messages_retries_request_errors(self) -> None:
        attempts = 0

        class FakeResponse:
            status_code = 200
            text = '{"choices":[{"message":{"content":"ok"}}]}'

            def raise_for_status(self) -> None:
                return None

            def json(self) -> dict:
                return {
                    "model": "openrouter/test",
                    "choices": [{"message": {"content": '[{"id":1,"score":7}]'}}],
                }

        class FakeClient:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

            async def post(self, *_args, **_kwargs):
                nonlocal attempts
                attempts += 1
                if attempts == 1:
                    raise ai.httpx.ReadTimeout("temporary timeout")
                return FakeResponse()

        sleep = AsyncMock()

        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai.settings, "OPENROUTER_MODEL", "openrouter/test"),
            patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            patch.object(ai.asyncio, "sleep", new=sleep),
        ):
            result = await ai.evaluate_messages(
                [{"id": 1, "author": "Alice", "text": "hello"}],
                include_day_verdict=False,
            )

        self.assertEqual(attempts, 2)
        sleep.assert_awaited_once_with(5)
        self.assertEqual(result.actual_model, "openrouter/test")
        self.assertEqual(result.scores, {1: 0.7})

    async def test_evaluate_messages_writes_ai_audit_jsonl(self) -> None:
        response_content = (
            '{"quote":{"primary_id":1,"context_ids":[1,2],"context_needed":true},'
            '"messages":[{"id":1,"score":7},{"id":2,"score":6}]}'
        )

        class FakeResponse:
            status_code = 200
            text = json.dumps(
                {
                    "model": "openrouter/test",
                    "choices": [{"message": {"content": response_content}}],
                }
            )

            def raise_for_status(self) -> None:
                return None

            def json(self) -> dict:
                return json.loads(self.text)

        class FakeClient:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

            async def post(self, *_args, **_kwargs):
                return FakeResponse()

        with tempfile.TemporaryDirectory() as tempdir:
            with (
                patch.object(ai.settings, "LOGS_PATH", tempdir),
                patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
                patch.object(ai.settings, "OPENROUTER_MODEL", "openrouter/test"),
                patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            ):
                result = await ai.evaluate_messages(
                    [
                        {"id": 1, "message_id": 11, "author": "Alice", "text": "setup"},
                        {"id": 2, "message_id": 12, "author": "Bob", "text": "punchline"},
                    ],
                    include_day_verdict=False,
                )

            audit_path = Path(tempdir) / "ai_audit.jsonl"
            audit_record = json.loads(audit_path.read_text(encoding="utf-8").strip())

        self.assertEqual(result.quote_choice.context_ids, [1, 2])
        self.assertEqual(audit_record["request"]["body"]["model"], "openrouter/test")
        self.assertIn("use ONLY that `id`", audit_record["request"]["body"]["messages"][0]["content"])
        self.assertEqual(audit_record["response"]["content"], response_content)
        self.assertEqual(audit_record["result"]["quote_choice"]["context_ids"], [1, 2])
        self.assertNotIn("headers", audit_record["request"])

    async def test_evaluate_messages_prunes_ai_audit_entries_older_than_seven_days(self) -> None:
        now = datetime.now(timezone.utc)
        old_record = {"created_at": (now - timedelta(days=8)).isoformat(), "request_id": "old"}
        fresh_record = {"created_at": (now - timedelta(days=1)).isoformat(), "request_id": "fresh"}

        class FakeResponse:
            status_code = 200
            text = '{"model":"openrouter/test","choices":[{"message":{"content":"[{\\"id\\":1,\\"score\\":7}]"}}]}'

            def raise_for_status(self) -> None:
                return None

            def json(self) -> dict:
                return json.loads(self.text)

        class FakeClient:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

            async def post(self, *_args, **_kwargs):
                return FakeResponse()

        with tempfile.TemporaryDirectory() as tempdir:
            audit_path = Path(tempdir) / "ai_audit.jsonl"
            audit_path.write_text(
                "\n".join(
                    [
                        json.dumps(old_record),
                        json.dumps(fresh_record),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            with (
                patch.object(ai.settings, "LOGS_PATH", tempdir),
                patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
                patch.object(ai.settings, "OPENROUTER_MODEL", "openrouter/test"),
                patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            ):
                await ai.evaluate_messages(
                    [{"id": 1, "message_id": 11, "author": "Alice", "text": "hello"}],
                    include_day_verdict=False,
                )

            records = [
                json.loads(line)
                for line in audit_path.read_text(encoding="utf-8").splitlines()
                if line.strip()
            ]

        self.assertNotIn("old", {record.get("request_id") for record in records})
        self.assertIn("fresh", {record.get("request_id") for record in records})
        self.assertEqual(len(records), 2)
