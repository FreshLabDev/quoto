"""quote reliability windowed flow

Revision ID: 20260326_01
Revises:
Create Date: 2026-03-26 21:30:00
"""
from __future__ import annotations

from collections import defaultdict
from datetime import date, datetime, time, timedelta, timezone
import os
from zoneinfo import ZoneInfo

from alembic import op
import sqlalchemy as sa


revision = "20260326_01"
down_revision = None
branch_labels = None
depends_on = None


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "users" not in tables:
        op.create_table(
            "users",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("telegram_id", sa.BigInteger(), nullable=False),
            sa.Column("name", sa.String(), nullable=True),
            sa.UniqueConstraint("telegram_id", name="users_telegram_id_key"),
        )

    if "groups" not in tables:
        op.create_table(
            "groups",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("chat_id", sa.BigInteger(), nullable=False),
            sa.Column("name", sa.String(), nullable=True),
            sa.UniqueConstraint("chat_id", name="groups_chat_id_key"),
        )

    if "messages" not in tables:
        op.create_table(
            "messages",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("message_id", sa.BigInteger(), nullable=False),
            sa.Column("chat_id", sa.BigInteger(), nullable=False),
            sa.Column("user_id", sa.BigInteger(), sa.ForeignKey("users.id"), nullable=False),
            sa.Column("text", sa.String(), nullable=False),
            sa.Column(
                "created_at",
                sa.DateTime(timezone=True),
                nullable=False,
                server_default=sa.text("CURRENT_TIMESTAMP"),
            ),
            sa.UniqueConstraint("message_id", "chat_id", name="uq_message_chat"),
        )
    else:
        _upgrade_created_at_type("messages", "created_at")
    _create_index_if_missing("ix_messages_chat_id_created_at", "messages", ["chat_id", "created_at"])

    if "reactions" not in tables:
        op.create_table(
            "reactions",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column(
                "message_db_id",
                sa.BigInteger(),
                sa.ForeignKey("messages.id", ondelete="CASCADE"),
                nullable=False,
            ),
            sa.Column("emoji", sa.String(), nullable=False),
            sa.Column("count", sa.Integer(), nullable=False, server_default="1"),
            sa.UniqueConstraint("message_db_id", "emoji", name="uq_reaction_emoji"),
        )

    if "quotes" not in tables:
        op.create_table(
            "quotes",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("group_id", sa.BigInteger(), sa.ForeignKey("groups.id"), nullable=False),
            sa.Column("author_id", sa.BigInteger(), sa.ForeignKey("users.id"), nullable=False),
            sa.Column("text", sa.String(), nullable=False),
            sa.Column("score", sa.Float(), nullable=False),
            sa.Column("reaction_score", sa.Float(), nullable=True, server_default="0"),
            sa.Column("ai_score", sa.Float(), nullable=True, server_default="0"),
            sa.Column("length_score", sa.Float(), nullable=True, server_default="0"),
            sa.Column("reaction_count", sa.Integer(), nullable=True, server_default="0"),
            sa.Column("message_id", sa.BigInteger(), nullable=True),
            sa.Column("bot_message_id", sa.BigInteger(), nullable=True),
            sa.Column("notice_message_id", sa.BigInteger(), nullable=True),
            sa.Column("ai_model", sa.String(), nullable=True),
            sa.Column("ai_best_text", sa.String(), nullable=True),
            sa.Column("quote_day", sa.Date(), nullable=False),
            sa.Column("window_start_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("window_end_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("decision_status", sa.String(), nullable=False, server_default="published"),
            sa.Column(
                "status_changed_at",
                sa.DateTime(timezone=True),
                nullable=False,
                server_default=sa.text("CURRENT_TIMESTAMP"),
            ),
            sa.Column("decision_reason", sa.String(), nullable=True),
            sa.Column("operation_error", sa.String(), nullable=True),
            sa.Column("forced_by_admin", sa.Boolean(), nullable=False, server_default=sa.false()),
            sa.Column(
                "created_at",
                sa.DateTime(timezone=True),
                nullable=False,
                server_default=sa.text("CURRENT_TIMESTAMP"),
            ),
            sa.UniqueConstraint("group_id", "quote_day", name="uq_quote_group_day"),
        )
    else:
        _upgrade_quotes_table(inspector)

    _create_index_if_missing("ix_quotes_group_id_window_end_at", "quotes", ["group_id", "window_end_at"])


def downgrade() -> None:
    pass


def _upgrade_quotes_table(inspector) -> None:
    columns = {column["name"] for column in inspector.get_columns("quotes")}

    _upgrade_created_at_type("quotes", "created_at")

    if "notice_message_id" not in columns:
        op.add_column("quotes", sa.Column("notice_message_id", sa.BigInteger(), nullable=True))
    if "quote_day" not in columns:
        op.add_column("quotes", sa.Column("quote_day", sa.Date(), nullable=True))
    if "window_start_at" not in columns:
        op.add_column("quotes", sa.Column("window_start_at", sa.DateTime(timezone=True), nullable=True))
    if "window_end_at" not in columns:
        op.add_column("quotes", sa.Column("window_end_at", sa.DateTime(timezone=True), nullable=True))
    if "decision_status" not in columns:
        op.add_column("quotes", sa.Column("decision_status", sa.String(), nullable=True))
    if "status_changed_at" not in columns:
        op.add_column("quotes", sa.Column("status_changed_at", sa.DateTime(timezone=True), nullable=True))
    if "decision_reason" not in columns:
        op.add_column("quotes", sa.Column("decision_reason", sa.String(), nullable=True))
    if "operation_error" not in columns:
        op.add_column("quotes", sa.Column("operation_error", sa.String(), nullable=True))
    if "forced_by_admin" not in columns:
        op.add_column("quotes", sa.Column("forced_by_admin", sa.Boolean(), nullable=True))

    _backfill_quotes_table()
    _deduplicate_quotes_by_day()

    op.execute("UPDATE quotes SET decision_status = COALESCE(decision_status, 'published')")
    op.execute("UPDATE quotes SET status_changed_at = COALESCE(status_changed_at, created_at)")
    op.execute("UPDATE quotes SET forced_by_admin = COALESCE(forced_by_admin, FALSE)")

    op.alter_column("quotes", "quote_day", nullable=False)
    op.alter_column("quotes", "window_start_at", nullable=False)
    op.alter_column("quotes", "window_end_at", nullable=False)
    op.alter_column("quotes", "decision_status", nullable=False)
    op.alter_column("quotes", "status_changed_at", nullable=False)
    op.alter_column("quotes", "forced_by_admin", nullable=False)

    constraints = {constraint["name"] for constraint in inspector.get_unique_constraints("quotes")}
    if "uq_quote_group_day" not in constraints:
        op.create_unique_constraint("uq_quote_group_day", "quotes", ["group_id", "quote_day"])


def _backfill_quotes_table() -> None:
    bind = op.get_bind()
    quotes = sa.table(
        "quotes",
        sa.column("id", sa.BigInteger()),
        sa.column("created_at", sa.DateTime(timezone=True)),
        sa.column("quote_day", sa.Date()),
        sa.column("window_start_at", sa.DateTime(timezone=True)),
        sa.column("window_end_at", sa.DateTime(timezone=True)),
    )

    rows = bind.execute(
        sa.select(
            quotes.c.id,
            quotes.c.created_at,
            quotes.c.quote_day,
            quotes.c.window_start_at,
            quotes.c.window_end_at,
        )
    ).mappings()

    for row in rows:
        quote_day, window_start_at, window_end_at = _legacy_window_from_created_at(row["created_at"])
        values: dict[str, object] = {}

        if row["quote_day"] is None:
            values["quote_day"] = quote_day
        if row["window_start_at"] is None:
            values["window_start_at"] = window_start_at
        if row["window_end_at"] is None:
            values["window_end_at"] = window_end_at

        if values:
            bind.execute(
                sa.update(quotes)
                .where(quotes.c.id == row["id"])
                .values(**values)
            )


def _deduplicate_quotes_by_day() -> None:
    bind = op.get_bind()
    quotes = sa.table(
        "quotes",
        sa.column("id", sa.BigInteger()),
        sa.column("group_id", sa.BigInteger()),
        sa.column("quote_day", sa.Date()),
        sa.column("bot_message_id", sa.BigInteger()),
        sa.column("created_at", sa.DateTime(timezone=True)),
    )

    rows = bind.execute(
        sa.select(
            quotes.c.id,
            quotes.c.group_id,
            quotes.c.quote_day,
            quotes.c.bot_message_id,
            quotes.c.created_at,
        )
    ).mappings()

    grouped: dict[tuple[int, date], list[dict[str, object]]] = defaultdict(list)
    for row in rows:
        grouped[(int(row["group_id"]), row["quote_day"])].append(dict(row))

    removed_ids: list[int] = []
    for duplicates in grouped.values():
        if len(duplicates) < 2:
            continue

        duplicates.sort(
            key=lambda item: (
                item["bot_message_id"] is not None,
                item["created_at"],
                int(item["id"]),
            ),
            reverse=True,
        )
        removed_ids.extend(int(item["id"]) for item in duplicates[1:])

    if removed_ids:
        print(f"[alembic] deduplicating quotes before uq_quote_group_day: removing ids {removed_ids}")
        bind.execute(sa.delete(quotes).where(quotes.c.id.in_(removed_ids)))


def _legacy_window_from_created_at(created_at: datetime) -> tuple[date, datetime, datetime]:
    local_created_at = _localize_legacy_datetime(created_at)
    quote_day = _legacy_quote_day_from_created_at(local_created_at)
    window_end_local = _cutoff_at(quote_day)
    window_start_local = _cutoff_at(quote_day - timedelta(days=1))
    return (
        quote_day,
        window_start_local.astimezone(timezone.utc),
        window_end_local.astimezone(timezone.utc),
    )


def _legacy_quote_day_from_created_at(local_created_at: datetime) -> date:
    return local_created_at.date()


def _localize_legacy_datetime(value: datetime) -> datetime:
    tz = _migration_timezone()
    if value.tzinfo is None:
        return value.replace(tzinfo=_legacy_source_timezone()).astimezone(tz)
    return value.astimezone(tz)


def _quote_day_from_local(local_dt: datetime) -> date:
    current_cutoff = _cutoff_at(local_dt.date())
    if local_dt >= current_cutoff:
        return local_dt.date()
    return local_dt.date() - timedelta(days=1)


def _cutoff_at(day: date) -> datetime:
    return datetime.combine(day, _migration_cutoff_time(), tzinfo=_migration_timezone())


def _migration_cutoff_time() -> time:
    return time(
        hour=int(os.getenv("QUOTE_HOUR", "21")),
        minute=int(os.getenv("QUOTE_MINUTE", "0")),
    )


def _migration_timezone_name() -> str:
    return os.getenv("TIMEZONE", "Europe/Kyiv")


def _migration_timezone() -> ZoneInfo:
    return ZoneInfo(_migration_timezone_name())


def _legacy_source_timezone_name() -> str:
    bind = op.get_bind()
    result = bind.exec_driver_sql("SHOW TIMEZONE")
    timezone_name = result.scalar()
    return str(timezone_name or "UTC")


def _legacy_source_timezone() -> ZoneInfo:
    return ZoneInfo(_legacy_source_timezone_name())


def _upgrade_created_at_type(table_name: str, column_name: str) -> None:
    source_timezone_name = _legacy_source_timezone_name().replace("'", "''")
    op.alter_column(
        table_name,
        column_name,
        existing_type=sa.DateTime(),
        type_=sa.DateTime(timezone=True),
        postgresql_using=f"{column_name} AT TIME ZONE '{source_timezone_name}'",
    )


def _create_index_if_missing(index_name: str, table_name: str, columns: list[str]) -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    indexes = {index["name"] for index in inspector.get_indexes(table_name)}
    if index_name not in indexes:
        op.create_index(index_name, table_name, columns, unique=False)
