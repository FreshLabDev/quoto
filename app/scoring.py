import logging
import re
from dataclasses import dataclass, field

from sqlalchemy import select
from sqlalchemy.orm import selectinload

from . import ai, ai_reports
from .config import settings, setup_logging
from .db import SessionLocal
from .models import Message
from .windows import QuoteWindow

log = setup_logging(logging.getLogger(__name__))
_QUOTO_HASHTAG_RE = re.compile(r"(?<![\w])#quoto(?![\w])", re.IGNORECASE)
_PRIMARY_EXCLUDED_CONTENT_TYPES = {"sticker"}
_MAX_CONTEXT_MESSAGES = 5


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
        return self.ai

    @property
    def stars(self) -> str:
        count = max(0, min(5, round(self.total * 5)))
        return "★" * count + "☆" * (5 - count)


@dataclass
class QuoteEvaluation:
    best_message: Message | None = None
    breakdown: ScoreBreakdown = field(default_factory=ScoreBreakdown)
    context_messages: list[Message] = field(default_factory=list)
    message_count: int = 0
    source_message_count: int = 0
    should_publish: bool = True
    day_reason_code: str | None = None
    day_reason_text: str = ""
    day_verdict_error: str = ""
    detected_language_code: str | None = None
    detected_chat_language: str = ""


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
    group_id: int | None = None,
    detect_interface_language: bool = False,
) -> QuoteEvaluation:
    async with SessionLocal() as session:
        stmt = (
            select(Message)
            .options(selectinload(Message.reactions), selectinload(Message.author), selectinload(Message.media_items))
            .where(
                Message.chat_id == chat_id,
                Message.created_at >= window.start_utc,
                Message.created_at < window.end_utc,
            )
            .order_by(Message.created_at.asc())
        )
        result = await session.execute(stmt)
        source_messages = result.scalars().all()

    if not source_messages:
        log.debug(f"{chat_id} | 📭 Нет сообщений за день {window.start_local} -> {window.end_local}")
        return QuoteEvaluation(message_count=0)

    messages = [message for message in source_messages if not _contains_quoto_hashtag(message.text)]
    if not messages:
        log.debug(
            f"{chat_id} | 📭 Нет сообщений для AI после фильтра #quoto "
            f"за день {window.start_local} -> {window.end_local}"
        )
        return QuoteEvaluation(message_count=0, source_message_count=len(source_messages))

    if day_verdict_min_messages is not None and len(messages) < day_verdict_min_messages:
        log.debug(
            f"{chat_id} | 🤫 Сообщений для AI после фильтра #quoto "
            f"{len(messages)} < {day_verdict_min_messages}"
        )
        return QuoteEvaluation(message_count=len(messages), source_message_count=len(source_messages))

    reaction_totals: dict[int, int] = {
        msg.id: sum(r.count for r in msg.reactions)
        for msg in messages
    }
    max_reactions = max(reaction_totals.values(), default=0)
    telegram_to_internal = {msg.message_id: msg.id for msg in messages}

    ai_payload = []
    for msg in messages:
        payload = {
            "id": msg.id,
            "author": msg.author.name if msg.author else "Unknown",
            "kind": getattr(msg, "content_type", None) or "text",
        }
        text_payload = _message_text_payload(msg)
        if text_payload:
            payload["text"] = text_payload
        caption = getattr(msg, "caption", None)
        if caption:
            payload["caption"] = caption
        media_description = _message_media_description(msg)
        if media_description:
            payload["desc"] = media_description
        reply_to_id = telegram_to_internal.get(getattr(msg, "reply_to_message_id", None))
        if reply_to_id is not None:
            payload["reply_to_id"] = reply_to_id
        reactions = _message_reactions_payload(msg)
        if reactions:
            payload["reactions"] = reactions
        ai_payload.append(payload)
    should_request_day_verdict = include_day_verdict
    if day_verdict_min_messages is not None and len(messages) < day_verdict_min_messages:
        should_request_day_verdict = False

    evaluation = await ai.evaluate_messages(
        ai_payload,
        include_day_verdict=should_request_day_verdict,
        detect_interface_language=detect_interface_language and should_request_day_verdict,
    )

    fallback_best_id = _fallback_primary_id(evaluation.scores, messages)
    ai_best_msg = next((m for m in messages if m.id == fallback_best_id), None) if fallback_best_id else None
    primary_id = _valid_primary_id(evaluation.quote_choice, messages) or fallback_best_id

    best_msg: Message | None = None
    best_breakdown = ScoreBreakdown()

    for msg in messages:
        if msg.id != primary_id:
            continue
        breakdown = ScoreBreakdown(
            reaction=calculate_reaction_score(reaction_totals.get(msg.id, 0), max_reactions),
            ai=evaluation.scores.get(msg.id, 0.5),
            length=calculate_length_score(msg.text),
            reaction_count=reaction_totals.get(msg.id, 0),
            ai_model=evaluation.actual_model,
        )
        best_breakdown = breakdown
        best_msg = msg
        break

    if ai_best_msg and best_msg and ai_best_msg.id != best_msg.id:
        best_breakdown.ai_best_text = ai_best_msg.text
    context_messages = _valid_context_messages(evaluation.quote_choice, messages, best_msg) if best_msg else []

    day_verdict = evaluation.day_verdict
    should_publish = day_verdict.should_publish if day_verdict else True

    if best_msg:
        log.debug(
            f"{chat_id} | 🏆 Лидер дня: «{best_msg.text}» ({best_msg.id}) "
            f"с оценкой {round(best_breakdown.total, 2)}"
        )

    if include_day_verdict and group_id is not None:
        await ai_reports.save_evaluation_report(
            group_id=group_id,
            chat_id=chat_id,
            window=window,
            source_messages=source_messages,
            scored_messages=messages,
            reaction_totals=reaction_totals,
            evaluation=evaluation,
            selected_message=best_msg,
        )

    return QuoteEvaluation(
        best_message=best_msg,
        breakdown=best_breakdown,
        context_messages=context_messages,
        message_count=len(messages),
        source_message_count=len(source_messages),
        should_publish=should_publish,
        day_reason_code=day_verdict.reason_code if day_verdict else None,
        day_reason_text=day_verdict.reason_text if day_verdict else "",
        day_verdict_error=evaluation.day_verdict_error or "",
        detected_language_code=(
            evaluation.language_choice.interface_language if evaluation.language_choice else None
        ),
        detected_chat_language=(
            evaluation.language_choice.chat_language if evaluation.language_choice else ""
        ),
    )


def _message_reactions_payload(message: Message) -> dict[str, int]:
    return {
        reaction.emoji: reaction.count
        for reaction in message.reactions
        if reaction.count > 0
    }


def _message_text_payload(message: Message) -> str:
    if getattr(message, "content_type", None) == "text":
        return message.text
    return ""


def _message_media_description(message: Message) -> str | None:
    media_items = getattr(message, "media_items", None) or []
    for item in media_items:
        description = getattr(item, "description_snapshot", None)
        if description:
            return str(description)
    if getattr(message, "content_type", None) != "text" and message.text:
        return message.text
    return None


def _contains_quoto_hashtag(text: str | None) -> bool:
    if not text:
        return False
    return bool(_QUOTO_HASHTAG_RE.search(text))


def _valid_primary_id(
    quote_choice: ai.QuoteContextChoice | None,
    messages: list[Message],
) -> int | None:
    if not quote_choice or quote_choice.primary_id is None:
        return None
    by_id = {message.id: message for message in messages}
    message = by_id.get(quote_choice.primary_id)
    if not message or not _is_primary_eligible(message):
        return None
    return quote_choice.primary_id


def _fallback_primary_id(scores: dict[int, float], messages: list[Message]) -> int | None:
    eligible_ids = {message.id for message in messages if _is_primary_eligible(message)}
    eligible_scores = {
        message_id: score
        for message_id, score in scores.items()
        if message_id in eligible_ids
    }
    return max(eligible_scores, key=eligible_scores.get) if eligible_scores else None


def _is_primary_eligible(message: Message) -> bool:
    content_type = str(getattr(message, "content_type", "") or "text")
    return content_type not in _PRIMARY_EXCLUDED_CONTENT_TYPES


def _valid_context_messages(
    quote_choice: ai.QuoteContextChoice | None,
    messages: list[Message],
    primary_message: Message,
) -> list[Message]:
    if not quote_choice or not quote_choice.context_needed:
        return []

    selected_ids = _dedupe_preserve_order(quote_choice.context_ids)
    if not selected_ids:
        return []

    by_id = {message.id: message for message in messages}
    selected = [by_id[message_id] for message_id in selected_ids if message_id in by_id]

    if primary_message.id not in {message.id for message in selected}:
        selected.append(primary_message)

    selected = sorted(selected, key=lambda message: _message_position(message, messages))
    if len(selected) <= 1:
        return []

    if len(selected) > _MAX_CONTEXT_MESSAGES:
        selected = _trim_context_around_primary(selected, primary_message, _MAX_CONTEXT_MESSAGES)

    return selected


def _trim_context_around_primary(
    selected: list[Message],
    primary_message: Message,
    limit: int,
) -> list[Message]:
    primary_index = next(
        (index for index, message in enumerate(selected) if message.id == primary_message.id),
        None,
    )
    if primary_index is None:
        return selected[:limit]

    start = primary_index - (limit // 2)
    end = start + limit
    if start < 0:
        start = 0
        end = limit
    if end > len(selected):
        end = len(selected)
        start = max(0, end - limit)
    return selected[start:end]


def _dedupe_preserve_order(values: list[int]) -> list[int]:
    seen: set[int] = set()
    result: list[int] = []
    for value in values:
        if value in seen:
            continue
        seen.add(value)
        result.append(value)
    return result


def _message_position(message: Message, messages: list[Message]) -> int:
    return next(index for index, candidate in enumerate(messages) if candidate.id == message.id)
