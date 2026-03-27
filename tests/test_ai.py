import os
import unittest

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

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
