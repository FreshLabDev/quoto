"""group language

Revision ID: 20260527_01
Revises: 20260523_01
Create Date: 2026-05-27 18:30:00
"""
from __future__ import annotations

from alembic import op
import sqlalchemy as sa


revision = "20260527_01"
down_revision = "20260523_01"
branch_labels = None
depends_on = None


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())
    if "groups" not in tables:
        return

    columns = {column["name"] for column in inspector.get_columns("groups")}
    if "language_code" not in columns:
        op.add_column("groups", sa.Column("language_code", sa.String(), nullable=True))
    if "language_source" not in columns:
        op.add_column("groups", sa.Column("language_source", sa.String(), nullable=True))


def downgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())
    if "groups" not in tables:
        return

    columns = {column["name"] for column in inspector.get_columns("groups")}
    if "language_source" in columns:
        op.drop_column("groups", "language_source")
    if "language_code" in columns:
        op.drop_column("groups", "language_code")
