import logging
from sqlalchemy import func, select
from sqlalchemy.orm import selectinload
from dataclasses import dataclass

from .config import settings, setup_logging
from .db import SessionLocal
from .models import Message, Reaction
from . import ai

log = setup_logging(logging.getLogger(__name__))


def create_bar(current: int, total: int = 100, width: int = 6, style="circles") -> str:
    empty = " "
    if total <= 0: return empty * width

    if style == "default":
        symbols = empty + "▏▎▍▌▋▊▉" 

        ratio = max(0, min(current / total, 1))
        fill_v = int(ratio * width * 8)
        
        full, rem = divmod(fill_v, 8)

        bar = ("█" * full + (symbols[rem] if full < width else "") + empty * width)[:width]
        
    elif style == "circles" or style == "quads":
        if style == "quads":
            empty = "◻"
            symbols = "◼"
        elif style == "circles":
            empty = "○"
            symbols = "●"

        bar = (symbols * int((current / total) * width) + empty * width)[:width]

    return bar

@dataclass
class ScoreBreakdown:
    """Покомпонентная разбивка скоринга."""
    reaction: float = 0.0
    ai: float = 0.0
    length: float = 0.0
    reaction_count: int = 0

    @property
    def total(self) -> float:
        return (
            settings.WEIGHT_REACTIONS * self.reaction
            + settings.WEIGHT_AI * self.ai
            + settings.WEIGHT_LENGTH * self.length
        )

    @property
    def stars(self) -> str:
        """Звёзды: от 0 до 5 на основе total score."""
        count = round(self.total * 5)
        return "⭐️" * count + "☆" * (5 - count)


def calculate_length_score(text: str) -> float:
    """Оценка длины сообщения: 1.0 для оптимального диапазона, плавное затухание за пределами.

    Оптимальный диапазон задаётся через ``settings.LENGTH_OPTIMAL_MIN`` / ``settings.LENGTH_OPTIMAL_MAX``.
    """
    length = len(text)
    lo = settings.LENGTH_OPTIMAL_MIN
    hi = settings.LENGTH_OPTIMAL_MAX

    if lo <= length <= hi:
        return 1.0

    if length < lo:
        return max(0.0, length / lo) if lo > 0 else 0.0

    return max(0.0, hi / length)


def calculate_reaction_score(total: int, maximum: int) -> float:
    """Нормализация количества реакций относительно максимума в группе."""
    if maximum <= 0:
        return 1.0
    return min(1.0, total / maximum)


async def pick_best_quote(chat_id: int) -> tuple[Message | None, ScoreBreakdown]:
    """Выбор лучшего сообщения дня для группы.

    Returns:
        Кортеж ``(лучшее_сообщение, ScoreBreakdown)`` или ``(None, ScoreBreakdown())``.
    """
    async with SessionLocal() as session:
        today_start = func.current_date()
        stmt = (
            select(Message)
            .options(selectinload(Message.reactions), selectinload(Message.author))
            .where(
                Message.chat_id == chat_id,
                func.date(Message.created_at) == today_start,
            )
        )
        result = await session.execute(stmt)
        messages = result.scalars().all()

    if not messages:
        log.debug(f"📭 Нет сообщений за сегодня в чате {chat_id}")
        return None, ScoreBreakdown()

    # --- Реакции ---
    reaction_totals: dict[int, int] = {}
    for msg in messages:
        reaction_totals[msg.id] = sum(r.count for r in msg.reactions)
    max_reactions = max(reaction_totals.values(), default=0)

    # --- AI batch-оценка ---
    ai_payload = [
        {"id": msg.id, "text": msg.text, "author": msg.author.name if msg.author else "Unknown"}
        for msg in messages
    ]
    ai_scores = await ai.evaluate_messages(ai_payload)

    # --- Скоринг ---
    best_msg: Message | None = None
    best_breakdown = ScoreBreakdown()
    best_total: float = -1.0

    for msg in messages:
        r_score = calculate_reaction_score(reaction_totals.get(msg.id, 0), max_reactions)
        a_score = ai_scores.get(msg.id, 0.5)
        l_score = calculate_length_score(msg.text)

        breakdown = ScoreBreakdown(
            reaction=r_score, ai=a_score, length=l_score,
            reaction_count=reaction_totals.get(msg.id, 0),
        )

        if breakdown.total > best_total:
            best_total = breakdown.total
            best_breakdown = breakdown
            best_msg = msg

    log.debug(f"{chat_id} | 🏆 Цитата дня: {best_msg.text} ({best_msg.id}) с оценкой {best_total}")
    return best_msg, best_breakdown

