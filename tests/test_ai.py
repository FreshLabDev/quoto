import os
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
        entries, verdict = _parse_day_payload(
            '{"day":{"should_publish":"false","reason_text":"boring"},"messages":[{"id":1,"score":4}]}'
        )

        self.assertEqual(entries, [{"id": 1, "score": 4}])
        self.assertFalse(verdict.should_publish)
        self.assertEqual(verdict.reason_code, REASON_BORING_DAY)
        self.assertEqual(verdict.reason_text, "boring")

    def test_missing_day_block_is_rejected(self) -> None:
        with self.assertRaises(DayVerdictParseError):
            _parse_day_payload('{"messages":[{"id":1,"score":5}]}')

    def test_legacy_array_payload_is_rejected(self) -> None:
        with self.assertRaises(DayVerdictParseError):
            _parse_day_payload('[{"id":1,"score":5}]')

    def test_parse_day_payload_ignores_surrounding_prose_and_braces(self) -> None:
        entries, verdict = _parse_day_payload(
            'note {"draft": true}\n'
            '```json\n'
            '{"day":{"should_publish":"false","reason_text":"boring"},"messages":[{"id":1,"score":4}]}\n'
            '```\n'
            "thanks"
        )

        self.assertEqual(entries, [{"id": 1, "score": 4}])
        self.assertFalse(verdict.should_publish)
        self.assertEqual(verdict.reason_code, REASON_BORING_DAY)


class AIRetryTests(unittest.IsolatedAsyncioTestCase):
    async def test_evaluate_messages_retries_request_errors(self) -> None:
        attempts = 0

        class FakeResponse:
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
