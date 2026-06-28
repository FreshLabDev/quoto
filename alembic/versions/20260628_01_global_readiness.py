"""global readiness: premium, per-group timezone, media toggle, agreement

Revision ID: 20260628_01
Revises: 20260527_02
Create Date: 2026-06-28 12:00:00
"""
from __future__ import annotations

from alembic import op
import sqlalchemy as sa


revision = "20260628_01"
down_revision = "20260527_02"
branch_labels = None
depends_on = None


_NEW_GROUP_COLUMNS = (
    ("is_premium", sa.Column("is_premium", sa.Boolean(), nullable=True)),
    ("timezone", sa.Column("timezone", sa.String(), nullable=True)),
    ("media_analysis_enabled", sa.Column("media_analysis_enabled", sa.Boolean(), nullable=True)),
    ("agreement_accepted_at", sa.Column("agreement_accepted_at", sa.DateTime(timezone=True), nullable=True)),
    ("agreement_accepted_by", sa.Column("agreement_accepted_by", sa.BigInteger(), nullable=True)),
    ("agreement_language", sa.Column("agreement_language", sa.String(), nullable=True)),
)


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "groups" in tables:
        group_columns = {column["name"] for column in inspector.get_columns("groups")}
        for name, column in _NEW_GROUP_COLUMNS:
            if name not in group_columns:
                op.add_column("groups", column)


def downgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "groups" in tables:
        group_columns = {column["name"] for column in inspector.get_columns("groups")}
        for name, _ in reversed(_NEW_GROUP_COLUMNS):
            if name in group_columns:
                op.drop_column("groups", name)
