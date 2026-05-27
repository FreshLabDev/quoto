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
    String,
    Text,
    UniqueConstraint,
)
from sqlalchemy.orm import declarative_base, relationship

Base = declarative_base()


class User(Base):
    __tablename__ = "users"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    telegram_id = Column(BigInteger, unique=True, index=True, nullable=False)
    name = Column(String)

    quotes = relationship("Quote", back_populates="author")
    messages = relationship("Message", back_populates="author")


class Group(Base):
    __tablename__ = "groups"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    chat_id = Column(BigInteger, unique=True, index=True, nullable=False)
    name = Column(String)
    language_code = Column(String, nullable=True)
    language_source = Column(String, nullable=True)

    quotes = relationship("Quote", back_populates="group")


class Message(Base):
    __tablename__ = "messages"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_id = Column(BigInteger, nullable=False)
    chat_id = Column(BigInteger, nullable=False)
    user_id = Column(BigInteger, ForeignKey("users.id"), nullable=False)
    text = Column(Text, nullable=False)
    content_type = Column(String, nullable=False, default="text")
    caption = Column(Text, nullable=True)
    media_status = Column(String, nullable=True)
    reply_to_message_id = Column(BigInteger, nullable=True)
    created_at = Column(DateTime(timezone=True), default=func.now(), nullable=False)

    author = relationship("User", back_populates="messages")
    reactions = relationship("Reaction", back_populates="message", cascade="all, delete-orphan")
    media_items = relationship("MessageMedia", back_populates="message", cascade="all, delete-orphan")

    __table_args__ = (
        UniqueConstraint("message_id", "chat_id", name="uq_message_chat"),
    )


class Reaction(Base):
    __tablename__ = "reactions"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_db_id = Column(BigInteger, ForeignKey("messages.id", ondelete="CASCADE"), nullable=False)
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
    message_db_id = Column(BigInteger, ForeignKey("messages.id", ondelete="CASCADE"), nullable=False)
    media_cache_id = Column(BigInteger, ForeignKey("media_cache.id", ondelete="SET NULL"), nullable=True)
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
    group_id = Column(BigInteger, ForeignKey("groups.id"), nullable=False)
    author_id = Column(BigInteger, ForeignKey("users.id"), nullable=False)
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

    group = relationship("Group", back_populates="quotes")
    author = relationship("User", back_populates="quotes")

    __table_args__ = (
        UniqueConstraint("group_id", "quote_day", name="uq_quote_group_day"),
    )


class AIEvaluationRun(Base):
    __tablename__ = "ai_evaluation_runs"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    group_id = Column(BigInteger, ForeignKey("groups.id"), nullable=False)
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
    run_id = Column(BigInteger, ForeignKey("ai_evaluation_runs.id", ondelete="CASCADE"), nullable=False)
    group_id = Column(BigInteger, ForeignKey("groups.id"), nullable=False)
    chat_id = Column(BigInteger, nullable=False)
    quote_day = Column(Date, nullable=False)
    message_db_id = Column(BigInteger, ForeignKey("messages.id", ondelete="SET NULL"), nullable=True)
    telegram_message_id = Column(BigInteger, nullable=False)
    reply_to_message_id = Column(BigInteger, nullable=True)
    user_id = Column(BigInteger, ForeignKey("users.id"), nullable=True)
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
