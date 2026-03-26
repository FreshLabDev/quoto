import logging
from dataclasses import dataclass, field

from sqlalchemy import select
from sqlalchemy.orm import selectinload

from . import ai
from .config import settings, setup_logging
from .db import SessionLocal
from .models import Message
from .windows import QuoteWindow

log = setup_logging(logging.getLogger(__name__))


def create_bar(current: int, total: int = 100, width: int = 6, style: str = "circles") -> str:
    empty = " "
    if total <= 0:
        return empty * width

    if style == "default":
        symbols = empty + "▏▎▍▌▋▊▉"
        ratio = max(0, min(current / total, 1))
        fill_v = int(ratio * width * 8)
        full, rem = divmod(fill_v, 8)
        return ("█" * full + (symbols[rem] if full < width else "") + empty * width)[:width]

    if style == "quads":
        empty = "◻"
        symbols = "◼"
    else:
        empty = "○"
        symbols = "●"

    return (symbols * int((current / total) * width) + empty * width)[:width]


@dataclass
class ScoreBreakdown:
    reaction: float = 0.0
    ai: float = 0.0
    length: float = 0.0
    reaction_count: int = 0
    ai_model: str = ""
    ai_best_text: str | None = None

    @property
    def total(self) -> float:
        return (
            settings.WEIGHT_REACTIONS * self.reaction
            + settings.WEIGHT_AI * self.ai
            + settings.WEIGHT_LENGTH * self.length
        )

    @property
    def stars(self) -> str:
        count = round(self.total * 5)
        return "⭐️" * count + "☆" * (5 - count)


@dataclass
class QuoteEvaluation:
    best_message: Message | None = None
    breakdown: ScoreBreakdown = field(default_factory=ScoreBreakdown)
    message_count: int = 0
    should_publish: bool = True
    day_reason_code: str | None = None
    day_reason_text: str = ""


def calculate_length_score(text: str) -> float:
    length = len(text)
    lo = settings.LENGTH_OPTIMAL_MIN
    hi = settings.LENGTH_OPTIMAL_MAX

    if lo <= length <= hi:
        return 1.0

    if length < lo:
        return max(0.0, length / lo) if lo > 0 else 0.0

    return max(0.0, hi / length)


def calculate_reaction_score(total: int, maximum: int) -> float:
    if total <= 0 or maximum <= 0:
        return 0.0
    return min(1.0, total / maximum)


async def pick_best_quote(
    chat_id: int,
    window: QuoteWindow,
    include_day_verdict: bool = False,
    day_verdict_min_messages: int | None = None,
) -> QuoteEvaluation:
    async with SessionLocal() as session:
        stmt = (
            select(Message)
            .options(selectinload(Message.reactions), selectinload(Message.author))
            .where(
                Message.chat_id == chat_id,
                Message.created_at >= window.start_utc,
                Message.created_at < window.end_utc,
            )
            .order_by(Message.created_at.asc())
        )
        result = await session.execute(stmt)
        messages = result.scalars().all()

    if not messages:
        log.debug(f"{chat_id} | 📭 Нет сообщений в окне {window.start_local} -> {window.end_local}")
        return QuoteEvaluation(message_count=0)

    reaction_totals: dict[int, int] = {
        msg.id: sum(r.count for r in msg.reactions)
        for msg in messages
    }
    max_reactions = max(reaction_totals.values(), default=0)

    ai_payload = [
        {"id": msg.id, "text": msg.text, "author": msg.author.name if msg.author else "Unknown"}
        for msg in messages
    ]
    should_request_day_verdict = include_day_verdict
    if day_verdict_min_messages is not None and len(messages) < day_verdict_min_messages:
        should_request_day_verdict = False

    evaluation = await ai.evaluate_messages(
        ai_payload,
        include_day_verdict=should_request_day_verdict,
    )

    ai_best_id = max(evaluation.scores, key=evaluation.scores.get) if evaluation.scores else None
    ai_best_msg = next((m for m in messages if m.id == ai_best_id), None) if ai_best_id else None

    best_msg: Message | None = None
    best_breakdown = ScoreBreakdown()
    best_total = -1.0

    for msg in messages:
        breakdown = ScoreBreakdown(
            reaction=calculate_reaction_score(reaction_totals.get(msg.id, 0), max_reactions),
            ai=evaluation.scores.get(msg.id, 0.5),
            length=calculate_length_score(msg.text),
            reaction_count=reaction_totals.get(msg.id, 0),
            ai_model=evaluation.actual_model,
        )

        if breakdown.total > best_total:
            best_total = breakdown.total
            best_breakdown = breakdown
            best_msg = msg

    if ai_best_msg and best_msg and ai_best_msg.id != best_msg.id:
        best_breakdown.ai_best_text = ai_best_msg.text

    day_verdict = evaluation.day_verdict
    should_publish = day_verdict.should_publish if day_verdict else True

    if best_msg:
        log.debug(
            f"{chat_id} | 🏆 Лидер окна: «{best_msg.text}» ({best_msg.id}) "
            f"с оценкой {round(best_breakdown.total, 2)}"
        )

    return QuoteEvaluation(
        best_message=best_msg,
        breakdown=best_breakdown,
        message_count=len(messages),
        should_publish=should_publish,
        day_reason_code=day_verdict.reason_code if day_verdict else None,
        day_reason_text=day_verdict.reason_text if day_verdict else "",
    )
