"""track active group lifecycle

Revision ID: 20260712_01
Revises: 20260702_01
Create Date: 2026-07-12 00:00:00
"""
from __future__ import annotations

from alembic import op

revision = "20260712_01"
down_revision = "20260702_01"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # The squashed baseline creates fresh tables from current ORM metadata, so
    # this must also be safe when the column already exists on a clean install.
    op.execute(
        "ALTER TABLE quoto.group_settings "
        "ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true"
    )


def downgrade() -> None:
    op.execute(
        "ALTER TABLE quoto.group_settings DROP COLUMN IF EXISTS is_active"
    )
