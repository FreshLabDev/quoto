from datetime import date, datetime, timezone
import importlib.util
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import Mock
from unittest.mock import patch
from zoneinfo import ZoneInfo


def _load_migration_module():
    migration_path = (
        Path(__file__).resolve().parents[1]
        / "alembic"
        / "versions"
        / "20260326_01_quote_reliability.py"
    )

    fake_alembic = types.ModuleType("alembic")
    fake_alembic.op = object()
    fake_sqlalchemy = types.ModuleType("sqlalchemy")

    with patch.dict(
        sys.modules,
        {"alembic": fake_alembic, "sqlalchemy": fake_sqlalchemy},
    ):
        spec = importlib.util.spec_from_file_location("quote_reliability_migration", migration_path)
        assert spec is not None and spec.loader is not None

        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    return module


class MigrationLegacyTimestampTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.migration = _load_migration_module()

    @staticmethod
    def _bind_with_timezone(timezone_name: str):
        class FakeResult:
            def scalar(self_nonlocal):
                return timezone_name

        class FakeBind:
            def exec_driver_sql(self_nonlocal, sql: str):
                assert sql == "SHOW TIMEZONE"
                return FakeResult()

        return FakeBind()

    def test_localize_legacy_datetime_uses_session_timezone_for_naive_value(self) -> None:
        self.migration.op = types.SimpleNamespace(get_bind=lambda: self._bind_with_timezone("UTC"))

        with patch.dict(
            os.environ,
            {"TIMEZONE": "Europe/Berlin", "QUOTE_HOUR": "21", "QUOTE_MINUTE": "0"},
            clear=False,
        ):
            localized = self.migration._localize_legacy_datetime(datetime(2026, 3, 27, 20, 30))

        self.assertEqual(
            localized,
            datetime(2026, 3, 27, 21, 30, tzinfo=ZoneInfo("Europe/Berlin")),
        )

    def test_legacy_window_from_naive_utc_timestamp_uses_legacy_calendar_day(self) -> None:
        self.migration.op = types.SimpleNamespace(get_bind=lambda: self._bind_with_timezone("UTC"))

        with patch.dict(
            os.environ,
            {"TIMEZONE": "Europe/Berlin", "QUOTE_HOUR": "21", "QUOTE_MINUTE": "0"},
            clear=False,
        ):
            quote_day, window_start_at, window_end_at = self.migration._legacy_window_from_created_at(
                datetime(2026, 3, 27, 20, 30)
            )

        self.assertEqual(quote_day, date(2026, 3, 27))
        self.assertEqual(window_start_at, datetime(2026, 3, 26, 20, 0, tzinfo=timezone.utc))
        self.assertEqual(window_end_at, datetime(2026, 3, 27, 20, 0, tzinfo=timezone.utc))

    def test_legacy_quote_day_does_not_shift_pre_cutoff_manual_quote_to_previous_day(self) -> None:
        self.migration.op = types.SimpleNamespace(get_bind=lambda: self._bind_with_timezone("Europe/Berlin"))

        with patch.dict(
            os.environ,
            {"TIMEZONE": "Europe/Berlin", "QUOTE_HOUR": "21", "QUOTE_MINUTE": "0"},
            clear=False,
        ):
            quote_day, _, _ = self.migration._legacy_window_from_created_at(
                datetime(2026, 3, 27, 20, 30)
            )

        self.assertEqual(quote_day, date(2026, 3, 27))

    def test_upgrade_created_at_type_uses_session_timezone_for_legacy_values(self) -> None:
        alter_column = Mock()
        self.migration.op = types.SimpleNamespace(
            alter_column=alter_column,
            get_bind=lambda: self._bind_with_timezone("Europe/Berlin"),
        )
        self.migration.sa = types.SimpleNamespace(DateTime=lambda timezone=False: ("DateTime", timezone))

        with patch.dict(os.environ, {"TIMEZONE": "Europe/Berlin"}, clear=False):
            self.migration._upgrade_created_at_type("messages", "created_at")

        alter_column.assert_called_once_with(
            "messages",
            "created_at",
            existing_type=("DateTime", False),
            type_=("DateTime", True),
            postgresql_using="created_at AT TIME ZONE 'Europe/Berlin'",
        )

    def test_upgrade_quotes_table_backfills_and_requires_status_changed_at(self) -> None:
        add_column = Mock()
        execute = Mock()
        alter_column = Mock()
        create_unique_constraint = Mock()

        self.migration.op = types.SimpleNamespace(
            add_column=add_column,
            execute=execute,
            alter_column=alter_column,
            create_unique_constraint=create_unique_constraint,
        )
        self.migration.sa = types.SimpleNamespace(
            Column=lambda name, *_args, **_kwargs: name,
            BigInteger=lambda: "BigInteger",
            Date=lambda: "Date",
            DateTime=lambda timezone=False: ("DateTime", timezone),
            String=lambda: "String",
            Boolean=lambda: "Boolean",
        )

        fake_inspector = types.SimpleNamespace(
            get_columns=lambda _table: [{"name": "created_at"}],
            get_unique_constraints=lambda _table: [],
        )

        with (
            patch.object(self.migration, "_upgrade_created_at_type"),
            patch.object(self.migration, "_backfill_quotes_table"),
            patch.object(self.migration, "_deduplicate_quotes_by_day"),
        ):
            self.migration._upgrade_quotes_table(fake_inspector)

        execute.assert_any_call("UPDATE quotes SET status_changed_at = COALESCE(status_changed_at, created_at)")
        alter_column.assert_any_call("quotes", "status_changed_at", nullable=False)
