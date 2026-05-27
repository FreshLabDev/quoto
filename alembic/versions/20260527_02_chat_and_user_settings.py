"""chat and user settings

Revision ID: 20260527_02
Revises: 20260527_01
Create Date: 2026-05-27 22:15:00
"""
from __future__ import annotations

from alembic import op
import sqlalchemy as sa


revision = "20260527_02"
down_revision = "20260527_01"
branch_labels = None
depends_on = None


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "users" in tables:
        user_columns = {column["name"] for column in inspector.get_columns("users")}
        if "language_code" not in user_columns:
            op.add_column("users", sa.Column("language_code", sa.String(), nullable=True))
        if "language_source" not in user_columns:
            op.add_column("users", sa.Column("language_source", sa.String(), nullable=True))

    if "groups" in tables:
        group_columns = {column["name"] for column in inspector.get_columns("groups")}
        if "quote_hour" not in group_columns:
            op.add_column("groups", sa.Column("quote_hour", sa.Integer(), nullable=True))
        if "quote_minute" not in group_columns:
            op.add_column("groups", sa.Column("quote_minute", sa.Integer(), nullable=True))
        if "min_messages" not in group_columns:
            op.add_column("groups", sa.Column("min_messages", sa.Integer(), nullable=True))
        if "boring_notice_enabled" not in group_columns:
            op.add_column("groups", sa.Column("boring_notice_enabled", sa.Boolean(), nullable=True))
        if "pin_enabled" not in group_columns:
            op.add_column("groups", sa.Column("pin_enabled", sa.Boolean(), nullable=True))
        if "quote_context_enabled" not in group_columns:
            op.add_column("groups", sa.Column("quote_context_enabled", sa.Boolean(), nullable=True))


def downgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "groups" in tables:
        group_columns = {column["name"] for column in inspector.get_columns("groups")}
        for column_name in (
            "quote_context_enabled",
            "pin_enabled",
            "boring_notice_enabled",
            "min_messages",
            "quote_minute",
            "quote_hour",
        ):
            if column_name in group_columns:
                op.drop_column("groups", column_name)

    if "users" in tables:
        user_columns = {column["name"] for column in inspector.get_columns("users")}
        if "language_source" in user_columns:
            op.drop_column("users", "language_source")
        if "language_code" in user_columns:
            op.drop_column("users", "language_code")
