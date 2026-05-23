import json
import logging
from typing import Any

from sqlalchemy.exc import IntegrityError

from .config import settings, setup_logging
from .db import SessionLocal
from . import models
from .windows import QuoteWindow, utc_now

log = setup_logging(logging.getLogger(__name__))


async def save_evaluation_report(
    *,
    group_id: int,
    chat_id: int,
    window: QuoteWindow,
    source_messages: list[models.Message],
    scored_messages: list[models.Message],
    reaction_totals: dict[int, int],
    evaluation: Any,
    selected_message: models.Message | None,
) -> None:
    now = utc_now()
    quote_choice = getattr(evaluation, "quote_choice", None)
    day_verdict = getattr(evaluation, "day_verdict", None)
    context_internal_ids = _context_internal_ids(quote_choice)
    context_telegram_ids = _context_telegram_ids(context_internal_ids, scored_messages)
    status = getattr(evaluation, "status", None) or "parsed"

    run = models.AIEvaluationRun(
        group_id=group_id,
        chat_id=chat_id,
        quote_day=window.quote_day,
        window_start_at=window.start_utc,
        window_end_at=window.end_utc,
        requested_model=getattr(evaluation, "requested_model", None) or settings.OPENROUTER_MODEL,
        actual_model=getattr(evaluation, "actual_model", None) or settings.OPENROUTER_MODEL,
        status=status,
        message_count=len(scored_messages),
        source_message_count=len(source_messages),
        selected_message_db_id=selected_message.id if selected_message else None,
        selected_telegram_message_id=selected_message.message_id if selected_message else None,
        context_message_ids=(
            json.dumps(context_telegram_ids, ensure_ascii=False, separators=(",", ":"))
            if context_telegram_ids
            else None
        ),
        context_needed=bool(getattr(quote_choice, "context_needed", False)) if quote_choice else False,
        should_publish=day_verdict.should_publish if day_verdict else None,
        day_reason_code=day_verdict.reason_code if day_verdict else None,
        day_reason_text=day_verdict.reason_text if day_verdict else None,
        request_id=getattr(evaluation, "request_id", None),
        created_at=now,
    )

    async with SessionLocal() as session:
        try:
            session.add(run)
            await session.flush()

            if status == "parsed":
                session.add_all(
                    _score_rows(
                        run_id=run.id,
                        group_id=group_id,
                        chat_id=chat_id,
                        quote_day=window.quote_day,
                        messages=scored_messages,
                        reaction_totals=reaction_totals,
                        scores=getattr(evaluation, "scores", {}) or {},
                        selected_internal_id=selected_message.id if selected_message else None,
                        context_internal_ids=set(context_internal_ids),
                        created_at=now,
                    )
                )

            await session.commit()
        except IntegrityError:
            await session.rollback()
            log.info(f"{chat_id} | ⏭️ AI evaluation report for {window.quote_day} already exists")
        except Exception as exc:
            await session.rollback()
            log.error(f"{chat_id} | ❌ Не удалось сохранить AI evaluation report: {exc}")


def _score_rows(
    *,
    run_id: int,
    group_id: int,
    chat_id: int,
    quote_day,
    messages: list[models.Message],
    reaction_totals: dict[int, int],
    scores: dict[int, float],
    selected_internal_id: int | None,
    context_internal_ids: set[int],
    created_at,
) -> list[models.MessageAIScore]:
    ranked_ids = {
        message_id: rank
        for rank, message_id in enumerate(
            [
                message.id
                for message in sorted(
                    messages,
                    key=lambda item: (-(scores.get(item.id, 0.5)), _message_position(item, messages)),
                )
            ],
            start=1,
        )
    }

    rows: list[models.MessageAIScore] = []
    for message in messages:
        ai_score = max(0.0, min(1.0, float(scores.get(message.id, 0.5))))
        reactions = _message_reactions_payload(message)
        media_item = _primary_media_item(message)
        rows.append(
            models.MessageAIScore(
                run_id=run_id,
                group_id=group_id,
                chat_id=chat_id,
                quote_day=quote_day,
                message_db_id=message.id,
                telegram_message_id=message.message_id,
                reply_to_message_id=getattr(message, "reply_to_message_id", None),
                user_id=getattr(message, "user_id", None),
                author_name_snapshot=message.author.name if message.author else "Unknown",
                text_snapshot=message.text,
                content_type=getattr(message, "content_type", None) or "text",
                caption_snapshot=getattr(message, "caption", None),
                reactions_snapshot=(
                    json.dumps(reactions, ensure_ascii=False, separators=(",", ":"))
                    if reactions
                    else None
                ),
                reaction_count=reaction_totals.get(message.id, 0),
                media_status=getattr(message, "media_status", None),
                media_description_snapshot=getattr(media_item, "description_snapshot", None) if media_item else None,
                media_kind=getattr(media_item, "media_kind", None) if media_item else None,
                telegram_file_id=getattr(media_item, "telegram_file_id", None) if media_item else None,
                telegram_file_unique_id=(
                    getattr(media_item, "telegram_file_unique_id", None) if media_item else None
                ),
                mime_type=getattr(media_item, "mime_type", None) if media_item else None,
                file_name=getattr(media_item, "file_name", None) if media_item else None,
                file_size=getattr(media_item, "file_size", None) if media_item else None,
                width=getattr(media_item, "width", None) if media_item else None,
                height=getattr(media_item, "height", None) if media_item else None,
                duration=getattr(media_item, "duration", None) if media_item else None,
                sha256=getattr(media_item, "sha256", None) if media_item else None,
                phash=getattr(media_item, "phash", None) if media_item else None,
                media_cache_id=getattr(media_item, "media_cache_id", None) if media_item else None,
                ai_score=ai_score,
                ai_score_raw=ai_score * 10.0,
                rank=ranked_ids[message.id],
                is_selected_primary=message.id == selected_internal_id,
                is_selected_context=message.id in context_internal_ids,
                created_at=created_at,
            )
        )
    return rows


def _primary_media_item(message: models.Message) -> models.MessageMedia | None:
    media_items = list(getattr(message, "media_items", None) or [])
    return media_items[0] if media_items else None


def _context_internal_ids(quote_choice: Any) -> list[int]:
    if not quote_choice:
        return []
    result: list[int] = []
    for value in getattr(quote_choice, "context_ids", []) or []:
        try:
            message_id = int(value)
        except (TypeError, ValueError):
            continue
        if message_id not in result:
            result.append(message_id)
    return result


def _context_telegram_ids(context_internal_ids: list[int], messages: list[models.Message]) -> list[int]:
    by_id = {message.id: message for message in messages}
    result: list[int] = []
    for internal_id in context_internal_ids:
        message = by_id.get(internal_id)
        if not message:
            continue
        result.append(int(message.message_id))
    return result


def _message_reactions_payload(message: models.Message) -> dict[str, int]:
    return {
        reaction.emoji: reaction.count
        for reaction in message.reactions
        if reaction.count > 0
    }


def _message_position(message: models.Message, messages: list[models.Message]) -> int:
    return next(index for index, candidate in enumerate(messages) if candidate.id == message.id)
