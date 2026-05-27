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


class PromptTests(unittest.TestCase):
    def test_day_prompt_uses_live_curator_policy(self) -> None:
        self.assertIn("live Telegram group quote curator", ai._DAY_PROMPT)
        self.assertIn("not a literary critic", ai._DAY_PROMPT)
        self.assertIn("Do not require polished jokes", ai._DAY_PROMPT)
        self.assertIn("full minimal context block", ai._DAY_PROMPT)
        self.assertIn("Set should_publish to false only when the best candidate is truly nothing", ai._DAY_PROMPT)
        self.assertIn("main language used in the chat that day", ai._DAY_PROMPT)

    def test_score_prompt_requires_model_owned_context(self) -> None:
        self.assertIn("full minimal context block", ai._SCORE_PROMPT)
        self.assertIn("set `context_needed` to true when the selected moment needs more than one message", ai._SCORE_PROMPT)


class DayVerdictParsingTests(unittest.TestCase):
    def test_string_false_is_parsed_strictly(self) -> None:
        entries, verdict, quote_choice, language_choice = _parse_day_payload(
            '{"day":{"should_publish":"false","reason_text":"boring"},"messages":[{"id":1,"score":4}]}'
        )

        self.assertEqual(entries, [{"id": 1, "score": 4}])
        self.assertFalse(verdict.should_publish)
        self.assertEqual(verdict.reason_code, REASON_BORING_DAY)
        self.assertEqual(verdict.reason_text, "boring")
        self.assertIsNone(quote_choice)
        self.assertIsNone(language_choice)

    def test_missing_day_block_is_rejected(self) -> None:
        with self.assertRaises(DayVerdictParseError):
            _parse_day_payload('{"messages":[{"id":1,"score":5}]}')

    def test_legacy_array_payload_is_rejected(self) -> None:
        with self.assertRaises(DayVerdictParseError):
            _parse_day_payload('[{"id":1,"score":5}]')

    def test_parse_day_payload_ignores_surrounding_prose_and_braces(self) -> None:
        entries, verdict, quote_choice, language_choice = _parse_day_payload(
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
        self.assertIsNone(language_choice)

    def test_parse_day_payload_reads_interface_language(self) -> None:
        entries, verdict, quote_choice, language_choice = _parse_day_payload(
            '{"day":{"should_publish":true,"reason_code":"worthy","reason_text":"ok"},'
            '"language":{"chat_language":"Ukrainian","interface_language":"uk"},'
            '"quote":{"primary_id":1,"context_ids":[1],"context_needed":false},'
            '"messages":[{"id":1,"score":8}]}'
        )

        self.assertEqual(entries, [{"id": 1, "score": 8}])
        self.assertTrue(verdict.should_publish)
        self.assertEqual(quote_choice.primary_id, 1)
        self.assertEqual(language_choice.chat_language, "Ukrainian")
        self.assertEqual(language_choice.interface_language, "uk")


class AIRetryTests(unittest.IsolatedAsyncioTestCase):
    async def test_evaluate_messages_sends_reactions_when_present(self) -> None:
        captured_body: dict | None = None
        captured_headers: dict | None = None

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
                nonlocal captured_body, captured_headers
                captured_body = kwargs["json"]
                captured_headers = kwargs["headers"]
                return FakeResponse()

        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai.settings, "OPENROUTER_EVAL_MODEL", "openrouter/test"),
            patch.object(ai.settings, "OPENROUTER_EVAL_REASONING_EFFORT", "medium"),
            patch.object(ai.settings, "OPENROUTER_EVAL_MAX_TOKENS", 32000),
            patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
        ):
            result = await ai.evaluate_messages(
                [
                    {
                        "id": 1,
                        "author": "Alice",
                        "text": "reacted quote",
                        "reply_to_id": 2,
                        "reactions": {"😂": 3, "❤️": 1},
                    },
                    {"id": 2, "author": "Bob", "text": "plain quote"},
                ],
                include_day_verdict=False,
            )

        self.assertEqual(result.scores, {1: 0.7, 2: 0.8})
        self.assertIsNotNone(captured_body)
        self.assertEqual(
            captured_body["reasoning"],
            {"enabled": True, "effort": "medium", "exclude": True},
        )
        self.assertEqual(captured_body["max_tokens"], 32000)
        self.assertEqual(captured_headers["HTTP-Referer"], "https://t.me/quototbot")
        self.assertEqual(captured_headers["X-OpenRouter-Title"], "Quoto")
        user_payload = json.loads(captured_body["messages"][1]["content"])
        self.assertEqual(user_payload[0]["i"], 1)
        self.assertEqual(user_payload[0]["rp"], 2)
        self.assertEqual(user_payload[0]["re"], {"😂": 3, "❤️": 1})
        self.assertNotIn("message_id", user_payload[0])
        self.assertNotIn("re", user_payload[1])
        self.assertNotIn("rp", user_payload[1])
        self.assertEqual(captured_body["response_format"]["type"], "json_schema")
        day_schema = ai._response_format(include_day_verdict=True)["json_schema"]["schema"]
        self.assertEqual(day_schema["properties"]["day"]["properties"]["reason_text"]["maxLength"], 200)
        language_schema = ai._response_format(
            include_day_verdict=True,
            detect_interface_language=True,
        )["json_schema"]["schema"]
        self.assertIn("language", language_schema["required"])
        self.assertEqual(
            language_schema["properties"]["language"]["properties"]["interface_language"]["enum"],
            ["ru", "uk", "en", "de"],
        )

    async def test_eval_max_tokens_is_high_guardrail(self) -> None:
        with patch.object(ai.settings, "OPENROUTER_EVAL_MAX_TOKENS", 32000):
            self.assertEqual(ai._eval_max_tokens(2, include_day_verdict=False), 32000)
            self.assertEqual(ai._eval_max_tokens(84, include_day_verdict=True), 32000)
            self.assertEqual(ai._eval_max_tokens(500, include_day_verdict=True), 32000)

        with patch.object(ai.settings, "OPENROUTER_EVAL_MAX_TOKENS", 0):
            self.assertEqual(ai._eval_max_tokens(500, include_day_verdict=True), 0)

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
            patch.object(ai.settings, "OPENROUTER_EVAL_MODEL", "openrouter/test"),
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
                patch.object(ai.settings, "OPENROUTER_EVAL_MODEL", "openrouter/test"),
                patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            ):
                result = await ai.evaluate_messages(
                    [
                        {"id": 1, "author": "Alice", "text": "setup"},
                        {"id": 2, "author": "Bob", "text": "punchline"},
                    ],
                    include_day_verdict=False,
                )

            audit_path = Path(tempdir) / "ai_audit.jsonl"
            audit_record = json.loads(audit_path.read_text(encoding="utf-8").strip())

        self.assertEqual(result.quote_choice.context_ids, [1, 2])
        self.assertEqual(audit_record["request"]["body"]["model"], "openrouter/test")
        self.assertIn("use ONLY `i`", audit_record["request"]["body"]["messages"][0]["content"])
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
                patch.object(ai.settings, "OPENROUTER_EVAL_MODEL", "openrouter/test"),
                patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            ):
                await ai.evaluate_messages(
                    [{"id": 1, "author": "Alice", "text": "hello"}],
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
