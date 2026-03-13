from sqlalchemy import func, Column, Integer, BigInteger, String, ForeignKey, DateTime, Float, UniqueConstraint
from sqlalchemy.orm import relationship, declarative_base

Base = declarative_base()

# === МОДЕЛИ ===

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

    quotes = relationship("Quote", back_populates="group")


class Message(Base):
    """Временное хранение сообщений за текущий день (очищается после выбора цитаты)."""
    __tablename__ = "messages"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_id = Column(BigInteger, nullable=False)
    chat_id = Column(BigInteger, nullable=False)
    user_id = Column(BigInteger, ForeignKey("users.id"), nullable=False)
    text = Column(String, nullable=False)
    created_at = Column(DateTime, default=func.now(), nullable=False)

    author = relationship("User", back_populates="messages")
    reactions = relationship("Reaction", back_populates="message", cascade="all, delete-orphan")

    __table_args__ = (
        UniqueConstraint("message_id", "chat_id", name="uq_message_chat"),
    )


class Reaction(Base):
    """Реакции на сообщения (агрегированные по эмодзи)."""
    __tablename__ = "reactions"

    id = Column(BigInteger, primary_key=True, autoincrement=True)
    message_db_id = Column(BigInteger, ForeignKey("messages.id", ondelete="CASCADE"), nullable=False)
    emoji = Column(String, nullable=False)
    count = Column(Integer, default=1, nullable=False)

    message = relationship("Message", back_populates="reactions")

    __table_args__ = (
        UniqueConstraint("message_db_id", "emoji", name="uq_reaction_emoji"),
    )


class Quote(Base):
    """Архив выбранных цитат дня."""
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
    message_id = Column(BigInteger)           # telegram message_id оригинала
    bot_message_id = Column(BigInteger)       # telegram message_id отправленной ботом цитаты
    ai_model = Column(String, nullable=True)  # модель AI, оценившая цитаты
    ai_best_text = Column(String, nullable=True)  # текст лучшей цитаты по мнению AI (если отличается)
    created_at = Column(DateTime, default=func.now(), nullable=False)

    group = relationship("Group", back_populates="quotes")
    author = relationship("User", back_populates="quotes")