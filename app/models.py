from sqlalchemy import (
    func,
    Boolean,
    Column,
    Date,
    DateTime,
    Float,
    ForeignKey,
    Index,
    Integer,
    BigInteger,
    MetaData,
    String,
    Table,
    Text,
    UniqueConstraint,
)
from sqlalchemy.orm import declarative_base, relationship

# quoto's domain tables live in the `quoto` schema inside the shared core-postgres.
Base = declarative_base(metadata=MetaData(schema="quoto"))

# ---------------------------------------------------------------------------
# Shared-core FK-target stubs.
#
# Identity / presence / language live in the central `core` schema
# (core.person, core.chat), owned and migrated centrally in core-postgres.
# These lightweight Table stubs exist ONLY so SQLAlchemy can resolve the
# cross-schema ForeignKey("core.person...") / ("core.chat...") references when
# it sorts tables for a flush -- without them, every ORM flush that touches a
# core FK raises NoReferencedTableError -- and so quoto can read author / chat
# display names. They are declared with just the columns quoto needs; they are
# NEVER created, dropped or migrated by quoto (the alembic env excludes the
# `core` schema, and create_all is not used against core).
# ---------------------------------------------------------------------------
core_person = Table(
    "person",
    Base.metadata,
    Column("telegram_user_id", BigInteger, primary_key=True),
    Column("username", String),
    Column("first_name", String),
    Column("last_name", String),
    schema="core",
)

core_chat = Table(
    "chat",
    Base.metadata,
    Column("chat_id", BigInteger, primary_key=True),
    Column("type", String),
    Column("title", String),
    Column("username", String),
    schema="core",
)


class GroupSettings(Base):
    """Per-group quoto configuration, keyed on the natural Telegram ``chat_id``.

    Group identity (name) lives in ``core.chat`` and group language in the core
    language hub; this table holds only quoto's own per-group settings.
    """

    __tablename__ = "group_settings"

    chat_id = Column(BigInteger, ForeignKey("core.chat.chat_id"), primary_key=True)
    quote_hour = Column(Integer, nullable=True)
    quote_minute = Column(Integer, nullable=True)
    min_messages = Column(Integer, nullable=True)
    boring_notice_enabled = Column(Boolean, nullable=True)
    pin_enabled = Column(Boolean, nullable=True)
    quote_context_enabled = Column(Boolean, nullable=True)
    is_premium = Column(Boolean, nullable=True)
    timezone = Column(String, nullable=True)
    media_analysis_enabled = Column(Boolean, nullable=True)
    agreement_accepted_at = Column(DateTime(timezone=True), nullable=True)
    agreement_accepted_by = Column(BigInteger, nullable=True)
    agreement_language = Column(String, nullable=True)


class Message(Base):
    __tablename__ = "messages"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_id = Column(BigInteger, nullable=False)
    chat_id = Column(BigInteger, nullable=False)
    # Global Telegram user id -> core.person (was a surrogate users.id FK).
    user_id = Column(BigInteger, ForeignKey("core.person.telegram_user_id"), nullable=False)
    text = Column(Text, nullable=False)
    content_type = Column(String, nullable=False, default="text")
    caption = Column(Text, nullable=True)
    media_status = Column(String, nullable=True)
    reply_to_message_id = Column(BigInteger, nullable=True)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    reactions = relationship("Reaction", back_populates="message", cascade="all, delete-orphan")
    media_items = relationship("MessageMedia", back_populates="message", cascade="all, delete-orphan")

    __table_args__ = (
        UniqueConstraint("message_id", "chat_id", name="uq_message_chat"),
    )


class Reaction(Base):
    __tablename__ = "reactions"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_db_id = Column(BigInteger, ForeignKey("quoto.messages.id", ondelete="CASCADE"), nullable=False)
    emoji = Column(String, nullable=False)
    count = Column(Integer, default=1, nullable=False)

    message = relationship("Message", back_populates="reactions")

    __table_args__ = (
        UniqueConstraint("message_db_id", "emoji", name="uq_reaction_emoji"),
    )


class MediaCache(Base):
    __tablename__ = "media_cache"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    media_kind = Column(String, nullable=False)
    telegram_file_unique_id = Column(String, nullable=True)
    telegram_file_id = Column(String, nullable=True)
    sha256 = Column(String, nullable=False)
    phash = Column(String, nullable=True)
    phash_algo = Column(String, nullable=True)
    description = Column(Text, nullable=False)
    model = Column(String, nullable=False)
    prompt_version = Column(String, nullable=False)
    usage_prompt_tokens = Column(Integer, nullable=True)
    usage_completion_tokens = Column(Integer, nullable=True)
    usage_total_tokens = Column(Integer, nullable=True)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    media_items = relationship("MessageMedia", back_populates="cache")

    __table_args__ = (
        UniqueConstraint("prompt_version", "media_kind", "sha256", name="uq_media_cache_prompt_kind_sha256"),
        Index("ix_media_cache_file_unique_id", "telegram_file_unique_id"),
        Index("ix_media_cache_file_id", "telegram_file_id"),
        Index("ix_media_cache_sha256", "sha256"),
        Index("ix_media_cache_phash", "phash"),
    )


class MessageMedia(Base):
    __tablename__ = "message_media"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_db_id = Column(BigInteger, ForeignKey("quoto.messages.id", ondelete="CASCADE"), nullable=False)
    media_cache_id = Column(BigInteger, ForeignKey("quoto.media_cache.id", ondelete="SET NULL"), nullable=True)
    media_kind = Column(String, nullable=False)
    telegram_file_id = Column(String, nullable=True)
    telegram_file_unique_id = Column(String, nullable=True)
    mime_type = Column(String, nullable=True)
    file_name = Column(String, nullable=True)
    file_size = Column(BigInteger, nullable=True)
    width = Column(Integer, nullable=True)
    height = Column(Integer, nullable=True)
    duration = Column(Float, nullable=True)
    sha256 = Column(String, nullable=True)
    phash = Column(String, nullable=True)
    analysis_status = Column(String, nullable=False, default="pending")
    analysis_error = Column(Text, nullable=True)
    description_snapshot = Column(Text, nullable=True)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    message = relationship("Message", back_populates="media_items")
    cache = relationship("MediaCache", back_populates="media_items")

    __table_args__ = (
        Index("ix_message_media_message_db_id", "message_db_id"),
        Index("ix_message_media_file_unique_id", "telegram_file_unique_id"),
        Index("ix_message_media_sha256", "sha256"),
    )


class Quote(Base):
    __tablename__ = "quotes"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    # Natural Telegram chat_id / user id -> core.chat / core.person
    # (were surrogate groups.id / users.id FKs).
    group_id = Column(BigInteger, ForeignKey("core.chat.chat_id"), nullable=False)
    author_id = Column(BigInteger, ForeignKey("core.person.telegram_user_id"), nullable=False)
    text = Column(String, nullable=False)
    score = Column(Float, nullable=False)
    reaction_score = Column(Float, default=0.0)
    ai_score = Column(Float, default=0.0)
    length_score = Column(Float, default=0.0)
    reaction_count = Column(Integer, default=0)
    message_id = Column(BigInteger)
    content_type = Column(String, nullable=False, default="text")
    bot_message_id = Column(BigInteger)
    notice_message_id = Column(BigInteger)
    ai_model = Column(String, nullable=True)
    ai_best_text = Column(String, nullable=True)
    context_message_ids = Column(String, nullable=True)
    context_snapshot = Column(String, nullable=True)
    quote_day = Column(Date, nullable=False, index=True)
    window_start_at = Column(DateTime(timezone=True), nullable=False)
    window_end_at = Column(DateTime(timezone=True), nullable=False)
    decision_status = Column(String, nullable=False, default="published")
    status_changed_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)
    decision_reason = Column(String, nullable=True)
    operation_error = Column(String, nullable=True)
    forced_by_admin = Column(Boolean, nullable=False, default=False)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    __table_args__ = (
        UniqueConstraint("group_id", "quote_day", name="uq_quote_group_day"),
    )


class AIEvaluationRun(Base):
    __tablename__ = "ai_evaluation_runs"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    # Natural chat_id -> core.chat (was surrogate groups.id).
    group_id = Column(BigInteger, ForeignKey("core.chat.chat_id"), nullable=False)
    chat_id = Column(BigInteger, nullable=False)
    quote_day = Column(Date, nullable=False)
    window_start_at = Column(DateTime(timezone=True), nullable=False)
    window_end_at = Column(DateTime(timezone=True), nullable=False)
    requested_model = Column(String, nullable=False)
    actual_model = Column(String, nullable=False)
    status = Column(String, nullable=False)
    message_count = Column(Integer, nullable=False)
    source_message_count = Column(Integer, nullable=False)
    selected_message_db_id = Column(BigInteger, nullable=True)
    selected_telegram_message_id = Column(BigInteger, nullable=True)
    context_message_ids = Column(Text, nullable=True)
    context_needed = Column(Boolean, nullable=False, default=False)
    should_publish = Column(Boolean, nullable=True)
    day_reason_code = Column(String, nullable=True)
    day_reason_text = Column(Text, nullable=True)
    request_id = Column(String, nullable=True)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    __table_args__ = (
        UniqueConstraint("group_id", "quote_day", name="uq_ai_evaluation_run_group_day"),
        Index("ix_ai_evaluation_runs_chat_day", "chat_id", "quote_day"),
        Index("ix_ai_evaluation_runs_created_at", "created_at"),
    )


class MessageAIScore(Base):
    __tablename__ = "message_ai_scores"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    run_id = Column(BigInteger, ForeignKey("quoto.ai_evaluation_runs.id", ondelete="CASCADE"), nullable=False)
    # Natural chat_id -> core.chat (was surrogate groups.id).
    group_id = Column(BigInteger, ForeignKey("core.chat.chat_id"), nullable=False)
    chat_id = Column(BigInteger, nullable=False)
    quote_day = Column(Date, nullable=False)
    message_db_id = Column(BigInteger, ForeignKey("quoto.messages.id", ondelete="SET NULL"), nullable=True)
    telegram_message_id = Column(BigInteger, nullable=False)
    reply_to_message_id = Column(BigInteger, nullable=True)
    # Natural Telegram user id -> core.person (was surrogate users.id); nullable.
    user_id = Column(BigInteger, ForeignKey("core.person.telegram_user_id"), nullable=True)
    author_name_snapshot = Column(String, nullable=False)
    text_snapshot = Column(Text, nullable=False)
    content_type = Column(String, nullable=False, default="text")
    caption_snapshot = Column(Text, nullable=True)
    reactions_snapshot = Column(Text, nullable=True)
    reaction_count = Column(Integer, nullable=False, default=0)
    media_status = Column(String, nullable=True)
    media_description_snapshot = Column(Text, nullable=True)
    media_kind = Column(String, nullable=True)
    telegram_file_id = Column(String, nullable=True)
    telegram_file_unique_id = Column(String, nullable=True)
    mime_type = Column(String, nullable=True)
    file_name = Column(String, nullable=True)
    file_size = Column(BigInteger, nullable=True)
    width = Column(Integer, nullable=True)
    height = Column(Integer, nullable=True)
    duration = Column(Float, nullable=True)
    sha256 = Column(String, nullable=True)
    phash = Column(String, nullable=True)
    media_cache_id = Column(BigInteger, nullable=True)
    ai_score = Column(Float, nullable=False)
    ai_score_raw = Column(Float, nullable=False)
    rank = Column(Integer, nullable=False)
    is_selected_primary = Column(Boolean, nullable=False, default=False)
    is_selected_context = Column(Boolean, nullable=False, default=False)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    __table_args__ = (
        UniqueConstraint("run_id", "telegram_message_id", name="uq_message_ai_score_run_message"),
        Index("ix_message_ai_scores_chat_day_rank", "chat_id", "quote_day", "rank"),
        Index("ix_message_ai_scores_user_score", "user_id", "ai_score"),
        Index("ix_message_ai_scores_primary_day", "is_selected_primary", "quote_day"),
    )
