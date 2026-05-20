"""ai evaluation reports

Revision ID: 20260520_01
Revises: 20260513_01
Create Date: 2026-05-20 13:30:00
"""
from __future__ import annotations

from alembic import op
import sqlalchemy as sa


revision = "20260520_01"
down_revision = "20260513_01"
branch_labels = None
depends_on = None


def _has_index(inspector: sa.Inspector, table_name: str, index_name: str) -> bool:
    return any(index["name"] == index_name for index in inspector.get_indexes(table_name))


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "ai_evaluation_runs" not in tables:
        op.create_table(
            "ai_evaluation_runs",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("group_id", sa.BigInteger(), sa.ForeignKey("groups.id"), nullable=False),
            sa.Column("chat_id", sa.BigInteger(), nullable=False),
            sa.Column("quote_day", sa.Date(), nullable=False),
            sa.Column("window_start_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("window_end_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("requested_model", sa.String(), nullable=False),
            sa.Column("actual_model", sa.String(), nullable=False),
            sa.Column("status", sa.String(), nullable=False),
            sa.Column("message_count", sa.Integer(), nullable=False),
            sa.Column("source_message_count", sa.Integer(), nullable=False),
            sa.Column("selected_message_db_id", sa.BigInteger(), nullable=True),
            sa.Column("selected_telegram_message_id", sa.BigInteger(), nullable=True),
            sa.Column("context_message_ids", sa.Text(), nullable=True),
            sa.Column("context_needed", sa.Boolean(), nullable=False, server_default=sa.false()),
            sa.Column("should_publish", sa.Boolean(), nullable=True),
            sa.Column("day_reason_code", sa.String(), nullable=True),
            sa.Column("day_reason_text", sa.Text(), nullable=True),
            sa.Column("request_id", sa.String(), nullable=True),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
            sa.UniqueConstraint("group_id", "quote_day", name="uq_ai_evaluation_run_group_day"),
        )

    inspector = sa.inspect(bind)
    if not _has_index(inspector, "ai_evaluation_runs", "ix_ai_evaluation_runs_chat_day"):
        op.create_index("ix_ai_evaluation_runs_chat_day", "ai_evaluation_runs", ["chat_id", "quote_day"])
    if not _has_index(inspector, "ai_evaluation_runs", "ix_ai_evaluation_runs_created_at"):
        op.create_index("ix_ai_evaluation_runs_created_at", "ai_evaluation_runs", ["created_at"])

    tables = set(sa.inspect(bind).get_table_names())
    if "message_ai_scores" not in tables:
        op.create_table(
            "message_ai_scores",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column(
                "run_id",
                sa.BigInteger(),
                sa.ForeignKey("ai_evaluation_runs.id", ondelete="CASCADE"),
                nullable=False,
            ),
            sa.Column("group_id", sa.BigInteger(), sa.ForeignKey("groups.id"), nullable=False),
            sa.Column("chat_id", sa.BigInteger(), nullable=False),
            sa.Column("quote_day", sa.Date(), nullable=False),
            sa.Column(
                "message_db_id",
                sa.BigInteger(),
                sa.ForeignKey("messages.id", ondelete="SET NULL"),
                nullable=True,
            ),
            sa.Column("telegram_message_id", sa.BigInteger(), nullable=False),
            sa.Column("reply_to_message_id", sa.BigInteger(), nullable=True),
            sa.Column("user_id", sa.BigInteger(), sa.ForeignKey("users.id"), nullable=True),
            sa.Column("author_name_snapshot", sa.String(), nullable=False),
            sa.Column("text_snapshot", sa.Text(), nullable=False),
            sa.Column("reactions_snapshot", sa.Text(), nullable=True),
            sa.Column("reaction_count", sa.Integer(), nullable=False, server_default="0"),
            sa.Column("ai_score", sa.Float(), nullable=False),
            sa.Column("ai_score_raw", sa.Float(), nullable=False),
            sa.Column("rank", sa.Integer(), nullable=False),
            sa.Column("is_selected_primary", sa.Boolean(), nullable=False, server_default=sa.false()),
            sa.Column("is_selected_context", sa.Boolean(), nullable=False, server_default=sa.false()),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
            sa.UniqueConstraint("run_id", "telegram_message_id", name="uq_message_ai_score_run_message"),
        )

    inspector = sa.inspect(bind)
    if not _has_index(inspector, "message_ai_scores", "ix_message_ai_scores_chat_day_rank"):
        op.create_index("ix_message_ai_scores_chat_day_rank", "message_ai_scores", ["chat_id", "quote_day", "rank"])
    if not _has_index(inspector, "message_ai_scores", "ix_message_ai_scores_user_score"):
        op.create_index("ix_message_ai_scores_user_score", "message_ai_scores", ["user_id", "ai_score"])
    if not _has_index(inspector, "message_ai_scores", "ix_message_ai_scores_primary_day"):
        op.create_index("ix_message_ai_scores_primary_day", "message_ai_scores", ["is_selected_primary", "quote_day"])


def downgrade() -> None:
    pass
