"""quote context threads

Revision ID: 20260513_01
Revises: 20260326_01
Create Date: 2026-05-13 16:30:00
"""
from __future__ import annotations

from alembic import op
import sqlalchemy as sa


revision = "20260513_01"
down_revision = "20260326_01"
branch_labels = None
depends_on = None


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)

    if "messages" in set(inspector.get_table_names()):
        message_columns = {column["name"] for column in inspector.get_columns("messages")}
        if "reply_to_message_id" not in message_columns:
            op.add_column("messages", sa.Column("reply_to_message_id", sa.BigInteger(), nullable=True))

    if "quotes" in set(inspector.get_table_names()):
        quote_columns = {column["name"] for column in inspector.get_columns("quotes")}
        if "context_message_ids" not in quote_columns:
            op.add_column("quotes", sa.Column("context_message_ids", sa.String(), nullable=True))
        if "context_snapshot" not in quote_columns:
            op.add_column("quotes", sa.Column("context_snapshot", sa.String(), nullable=True))


def downgrade() -> None:
    pass
