"""record which agreement version a group accepted

Revision ID: 20261018_01
Revises: 20260907_01
Create Date: 2026-10-18 00:00:00
"""
from __future__ import annotations

from alembic import op

revision = "20261018_01"
down_revision = "20260907_01"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # No backfill: a group that accepted before this column existed accepted
    # 1.0 or 1.1, and NULL is exactly "an older version" for the bot, which asks
    # it to accept 1.2 again.
    op.execute(
        "ALTER TABLE quoto.group_settings "
        "ADD COLUMN IF NOT EXISTS agreement_version VARCHAR"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE quoto.group_settings DROP COLUMN IF EXISTS agreement_version")
