from datetime import date, datetime, time
import os
import unittest
from zoneinfo import ZoneInfo

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app.windows import legacy_window_from_created_at, quote_day_from_local


class WindowSemanticsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tz = ZoneInfo("Europe/Berlin")
        self.cutoff = time(21, 0)

    def test_quote_day_before_cutoff_belongs_to_previous_day(self) -> None:
        local_dt = datetime(2026, 3, 27, 20, 59, tzinfo=self.tz)

        self.assertEqual(quote_day_from_local(local_dt, at_time=self.cutoff), date(2026, 3, 26))

    def test_quote_day_after_cutoff_belongs_to_same_day(self) -> None:
        local_dt = datetime(2026, 3, 27, 21, 1, tzinfo=self.tz)

        self.assertEqual(quote_day_from_local(local_dt, at_time=self.cutoff), date(2026, 3, 27))

    def test_legacy_window_preserves_naive_local_wall_clock(self) -> None:
        window = legacy_window_from_created_at(
            datetime(2026, 3, 27, 21, 30),
            tz=self.tz,
            at_time=self.cutoff,
        )

        self.assertEqual(window.quote_day, date(2026, 3, 27))
        self.assertEqual(window.start_local, datetime(2026, 3, 26, 21, 0, tzinfo=self.tz))
        self.assertEqual(window.end_local, datetime(2026, 3, 27, 21, 0, tzinfo=self.tz))
