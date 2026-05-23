"""media pipeline

Revision ID: 20260523_01
Revises: 20260520_01
Create Date: 2026-05-23 18:00:00
"""
from __future__ import annotations

from alembic import op
import sqlalchemy as sa


revision = "20260523_01"
down_revision = "20260520_01"
branch_labels = None
depends_on = None


def _has_index(inspector: sa.Inspector, table_name: str, index_name: str) -> bool:
    return any(index["name"] == index_name for index in inspector.get_indexes(table_name))


def _has_unique_constraint(inspector: sa.Inspector, table_name: str, constraint_name: str) -> bool:
    return any(constraint["name"] == constraint_name for constraint in inspector.get_unique_constraints(table_name))


def upgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "messages" in tables:
        message_columns = {column["name"] for column in inspector.get_columns("messages")}
        if "content_type" not in message_columns:
            op.add_column(
                "messages",
                sa.Column("content_type", sa.String(), nullable=False, server_default="text"),
            )
            op.alter_column("messages", "content_type", server_default=None)
        if "caption" not in message_columns:
            op.add_column("messages", sa.Column("caption", sa.Text(), nullable=True))
        if "media_status" not in message_columns:
            op.add_column("messages", sa.Column("media_status", sa.String(), nullable=True))
        op.alter_column("messages", "text", type_=sa.Text(), existing_nullable=False)

    if "quotes" in tables:
        quote_columns = {column["name"] for column in inspector.get_columns("quotes")}
        if "content_type" not in quote_columns:
            op.add_column(
                "quotes",
                sa.Column("content_type", sa.String(), nullable=False, server_default="text"),
            )
            op.alter_column("quotes", "content_type", server_default=None)

    tables = set(sa.inspect(bind).get_table_names())
    if "media_cache" not in tables:
        op.create_table(
            "media_cache",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column("media_kind", sa.String(), nullable=False),
            sa.Column("telegram_file_unique_id", sa.String(), nullable=True),
            sa.Column("telegram_file_id", sa.String(), nullable=True),
            sa.Column("sha256", sa.String(), nullable=False),
            sa.Column("phash", sa.String(), nullable=True),
            sa.Column("phash_algo", sa.String(), nullable=True),
            sa.Column("description", sa.Text(), nullable=False),
            sa.Column("model", sa.String(), nullable=False),
            sa.Column("prompt_version", sa.String(), nullable=False),
            sa.Column("usage_prompt_tokens", sa.Integer(), nullable=True),
            sa.Column("usage_completion_tokens", sa.Integer(), nullable=True),
            sa.Column("usage_total_tokens", sa.Integer(), nullable=True),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
            sa.UniqueConstraint("prompt_version", "media_kind", "sha256", name="uq_media_cache_prompt_kind_sha256"),
        )

    inspector = sa.inspect(bind)
    if not _has_unique_constraint(inspector, "media_cache", "uq_media_cache_prompt_kind_sha256"):
        op.create_unique_constraint(
            "uq_media_cache_prompt_kind_sha256",
            "media_cache",
            ["prompt_version", "media_kind", "sha256"],
        )
    if not _has_index(inspector, "media_cache", "ix_media_cache_file_unique_id"):
        op.create_index("ix_media_cache_file_unique_id", "media_cache", ["telegram_file_unique_id"])
    if not _has_index(inspector, "media_cache", "ix_media_cache_file_id"):
        op.create_index("ix_media_cache_file_id", "media_cache", ["telegram_file_id"])
    if not _has_index(inspector, "media_cache", "ix_media_cache_sha256"):
        op.create_index("ix_media_cache_sha256", "media_cache", ["sha256"])
    if not _has_index(inspector, "media_cache", "ix_media_cache_phash"):
        op.create_index("ix_media_cache_phash", "media_cache", ["phash"])

    tables = set(sa.inspect(bind).get_table_names())
    if "message_media" not in tables:
        op.create_table(
            "message_media",
            sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
            sa.Column(
                "message_db_id",
                sa.BigInteger(),
                sa.ForeignKey("messages.id", ondelete="CASCADE"),
                nullable=False,
            ),
            sa.Column(
                "media_cache_id",
                sa.BigInteger(),
                sa.ForeignKey("media_cache.id", ondelete="SET NULL"),
                nullable=True,
            ),
            sa.Column("media_kind", sa.String(), nullable=False),
            sa.Column("telegram_file_id", sa.String(), nullable=True),
            sa.Column("telegram_file_unique_id", sa.String(), nullable=True),
            sa.Column("mime_type", sa.String(), nullable=True),
            sa.Column("file_name", sa.String(), nullable=True),
            sa.Column("file_size", sa.BigInteger(), nullable=True),
            sa.Column("width", sa.Integer(), nullable=True),
            sa.Column("height", sa.Integer(), nullable=True),
            sa.Column("duration", sa.Float(), nullable=True),
            sa.Column("sha256", sa.String(), nullable=True),
            sa.Column("phash", sa.String(), nullable=True),
            sa.Column("analysis_status", sa.String(), nullable=False, server_default="pending"),
            sa.Column("analysis_error", sa.Text(), nullable=True),
            sa.Column("description_snapshot", sa.Text(), nullable=True),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        )

    inspector = sa.inspect(bind)
    if not _has_index(inspector, "message_media", "ix_message_media_message_db_id"):
        op.create_index("ix_message_media_message_db_id", "message_media", ["message_db_id"])
    if not _has_index(inspector, "message_media", "ix_message_media_file_unique_id"):
        op.create_index("ix_message_media_file_unique_id", "message_media", ["telegram_file_unique_id"])
    if not _has_index(inspector, "message_media", "ix_message_media_sha256"):
        op.create_index("ix_message_media_sha256", "message_media", ["sha256"])


def downgrade() -> None:
    bind = op.get_bind()
    inspector = sa.inspect(bind)
    tables = set(inspector.get_table_names())

    if "message_media" in tables:
        op.drop_table("message_media")

    if "media_cache" in tables:
        op.drop_table("media_cache")

    if "messages" in tables:
        message_columns = {column["name"] for column in inspector.get_columns("messages")}
        if "media_status" in message_columns:
            op.drop_column("messages", "media_status")
        if "caption" in message_columns:
            op.drop_column("messages", "caption")
        if "content_type" in message_columns:
            op.drop_column("messages", "content_type")
        op.alter_column("messages", "text", type_=sa.String(), existing_nullable=False)

    if "quotes" in tables:
        quote_columns = {column["name"] for column in inspector.get_columns("quotes")}
        if "content_type" in quote_columns:
            op.drop_column("quotes", "content_type")
