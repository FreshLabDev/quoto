"""record token usage and cost per evaluation run

Revision ID: 20260907_01
Revises: 20260712_02
Create Date: 2026-09-07 00:00:01
"""
from __future__ import annotations

from alembic import op

revision = "20260907_01"
down_revision = "20260712_02"
branch_labels = None
depends_on = None

_COLUMNS = (
    ("prompt_tokens", "INTEGER"),
    ("completion_tokens", "INTEGER"),
    ("reasoning_tokens", "INTEGER"),
    ("total_tokens", "INTEGER"),
    ("cost_usd", "NUMERIC(12, 8)"),
)


def upgrade() -> None:
    for name, sql_type in _COLUMNS:
        op.execute(
            "ALTER TABLE quoto.ai_evaluation_runs "
            f"ADD COLUMN IF NOT EXISTS {name} {sql_type}"
        )


def downgrade() -> None:
    for name, _sql_type in reversed(_COLUMNS):
        op.execute(f"ALTER TABLE quoto.ai_evaluation_runs DROP COLUMN IF EXISTS {name}")
