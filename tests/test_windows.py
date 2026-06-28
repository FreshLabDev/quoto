from datetime import date, datetime, time
import os
import unittest
from zoneinfo import ZoneInfo

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app.windows import (
    closed_window_for_day,
    legacy_window_from_created_at,
    quote_day_from_local,
    resolve_timezone,
)


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


class PerGroupTimezoneTests(unittest.TestCase):
    def test_resolve_timezone_falls_back_on_bad_value(self) -> None:
        self.assertEqual(resolve_timezone("Asia/Tokyo"), ZoneInfo("Asia/Tokyo"))
        # invalid/empty -> global default (Europe/Kyiv in tests)
        self.assertEqual(resolve_timezone("Bogus/Zone").key, ZoneInfo("Europe/Kyiv").key)
        self.assertEqual(resolve_timezone(None).key, ZoneInfo("Europe/Kyiv").key)

    def test_window_endpoints_keep_local_cutoff_per_timezone(self) -> None:
        cutoff = time(21, 0)
        tokyo = ZoneInfo("Asia/Tokyo")
        ny = ZoneInfo("America/New_York")

        w_tokyo = closed_window_for_day(date(2026, 6, 15), tz=tokyo, at_time=cutoff)
        w_ny = closed_window_for_day(date(2026, 6, 15), tz=ny, at_time=cutoff)

        # Each group's window ends at 21:00 in ITS OWN local time.
        self.assertEqual(w_tokyo.end_local.hour, 21)
        self.assertEqual(w_ny.end_local.hour, 21)
        # But the UTC instants differ by the offset, so they are not the same window.
        self.assertNotEqual(w_tokyo.end_utc, w_ny.end_utc)

    def test_dst_spring_forward_keeps_wall_clock_day(self) -> None:
        # Europe/Berlin spring-forward is 2026-03-29 (clocks 02:00 -> 03:00).
        cutoff = time(21, 0)
        tz = ZoneInfo("Europe/Berlin")
        window = closed_window_for_day(date(2026, 3, 29), tz=tz, at_time=cutoff)

        # Window still runs 21:00 -> 21:00 wall-clock; the DST gap shortens the
        # UTC span to 23h, which is correct (that wall-clock day is 23h long).
        self.assertEqual(window.start_local.hour, 21)
        self.assertEqual(window.end_local.hour, 21)
        span_hours = (window.end_utc - window.start_utc).total_seconds() / 3600
        self.assertEqual(span_hours, 23)
