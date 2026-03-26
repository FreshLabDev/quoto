"""quote reliability windowed flow

Revision ID: 20260326_01
Revises: 
Create Date: 2026-03-26 21:30:00
"""
from __future__ import annotations

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
        op.create_index("ix_users_telegram_id", "users", ["telegram_id"], unique=False)

    if "groups" not in tables:
        op.create_table(
            "groups",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("chat_id", sa.BigInteger(), nullable=False),
            sa.Column("name", sa.String(), nullable=True),
            sa.UniqueConstraint("chat_id", name="groups_chat_id_key"),
        )
        op.create_index("ix_groups_chat_id", "groups", ["chat_id"], unique=False)

    if "messages" not in tables:
        op.create_table(
            "messages",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("message_id", sa.BigInteger(), nullable=False),
            sa.Column("chat_id", sa.BigInteger(), nullable=False),
            sa.Column("user_id", sa.BigInteger(), sa.ForeignKey("users.id"), nullable=False),
            sa.Column("text", sa.String(), nullable=False),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.text("CURRENT_TIMESTAMP")),
            sa.UniqueConstraint("message_id", "chat_id", name="uq_message_chat"),
        )
    else:
        _upgrade_created_at_type("messages", "created_at")
    _create_index_if_missing("ix_messages_chat_id_created_at", "messages", ["chat_id", "created_at"])

    if "reactions" not in tables:
        op.create_table(
            "reactions",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("message_db_id", sa.BigInteger(), sa.ForeignKey("messages.id", ondelete="CASCADE"), nullable=False),
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
            sa.Column("decision_reason", sa.String(), nullable=True),
            sa.Column("forced_by_admin", sa.Boolean(), nullable=False, server_default=sa.false()),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.text("CURRENT_TIMESTAMP")),
            sa.UniqueConstraint("group_id", "quote_day", name="uq_quote_group_day"),
        )
    else:
        _upgrade_quotes_table(inspector)

    _create_index_if_missing("ix_quotes_group_id_quote_day", "quotes", ["group_id", "quote_day"])
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
    if "decision_reason" not in columns:
        op.add_column("quotes", sa.Column("decision_reason", sa.String(), nullable=True))
    if "forced_by_admin" not in columns:
        op.add_column("quotes", sa.Column("forced_by_admin", sa.Boolean(), nullable=True))

    op.execute("UPDATE quotes SET quote_day = COALESCE(quote_day, DATE(created_at))")
    op.execute("UPDATE quotes SET window_end_at = COALESCE(window_end_at, created_at)")
    op.execute("UPDATE quotes SET window_start_at = COALESCE(window_start_at, created_at - INTERVAL '1 day')")
    op.execute("UPDATE quotes SET decision_status = COALESCE(decision_status, 'published')")
    op.execute("UPDATE quotes SET forced_by_admin = COALESCE(forced_by_admin, FALSE)")

    op.alter_column("quotes", "quote_day", nullable=False)
    op.alter_column("quotes", "window_start_at", nullable=False)
    op.alter_column("quotes", "window_end_at", nullable=False)
    op.alter_column("quotes", "decision_status", nullable=False)
    op.alter_column("quotes", "forced_by_admin", nullable=False)

    constraints = {constraint["name"] for constraint in inspector.get_unique_constraints("quotes")}
    if "uq_quote_group_day" not in constraints:
        op.create_unique_constraint("uq_quote_group_day", "quotes", ["group_id", "quote_day"])


def _upgrade_created_at_type(table_name: str, column_name: str) -> None:
    op.alter_column(
        table_name,
        column_name,
        existing_type=sa.DateTime(),
        type_=sa.DateTime(timezone=True),
        postgresql_using=f"{column_name} AT TIME ZONE 'UTC'",
    )


def _create_index_if_missing(index_name: str, table_name: str, columns: list[str]) -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    indexes = {index["name"] for index in inspector.get_indexes(table_name)}
    if index_name not in indexes:
        op.create_index(index_name, table_name, columns, unique=False)
