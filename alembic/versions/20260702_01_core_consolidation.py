"""quoto core-postgres consolidation: single squashed baseline.

quoto's domain tables now live in the ``quoto`` schema inside the shared
core-postgres database; identity / presence / language live in the central
``core`` schema (core.person, core.chat, core.touch / core.set_language /
core.effective_language). This baseline replaces the prior 7-revision chain
(the standalone quoto database is retired). It creates only the ``quoto``
tables; ``core.person`` / ``core.chat`` are FK targets owned and migrated
centrally and are never created or dropped here.

Revision ID: 20260702_01
Revises:
Create Date: 2026-07-02 00:00:00
"""
from __future__ import annotations

from alembic import op

from app.models import Base

revision = "20260702_01"
down_revision = None
branch_labels = None
depends_on = None


def _quoto_tables():
    return [t for t in Base.metadata.sorted_tables if t.schema == "quoto"]


def upgrade() -> None:
    bind = op.get_bind()
    op.execute("CREATE SCHEMA IF NOT EXISTS quoto")
    # core.person / core.chat must already exist (shared core schema); the quoto
    # tables FK into them. create_all only builds the quoto-schema tables.
    Base.metadata.create_all(bind, tables=_quoto_tables())


def downgrade() -> None:
    bind = op.get_bind()
    Base.metadata.drop_all(bind, tables=list(reversed(_quoto_tables())))
