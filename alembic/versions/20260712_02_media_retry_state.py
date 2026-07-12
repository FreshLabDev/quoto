"""add bounded media retry state

Revision ID: 20260712_02
Revises: 20260712_01
Create Date: 2026-07-12 00:00:01
"""
from __future__ import annotations

from alembic import op

revision = "20260712_02"
down_revision = "20260712_01"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE quoto.message_media "
        "ADD COLUMN IF NOT EXISTS retry_count INTEGER NOT NULL DEFAULT 0"
    )
    op.execute(
        "ALTER TABLE quoto.message_media "
        "ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_message_media_pending_retry "
        "ON quoto.message_media (analysis_status, next_retry_at)"
    )


def downgrade() -> None:
    op.execute(
        "DROP INDEX IF EXISTS quoto.ix_message_media_pending_retry"
    )
    op.execute(
        "ALTER TABLE quoto.message_media DROP COLUMN IF EXISTS next_retry_at"
    )
    op.execute(
        "ALTER TABLE quoto.message_media DROP COLUMN IF EXISTS retry_count"
    )
