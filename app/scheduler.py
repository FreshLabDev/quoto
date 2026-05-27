import logging
import json
from datetime import timedelta
from html import escape

from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.cron import CronTrigger
from apscheduler.triggers.interval import IntervalTrigger
from aiogram import Bot
from sqlalchemy import select

from . import core, i18n, media, scoring
from .config import settings, setup_logging
from .db import SessionLocal
from .models import Group, Quote
from .quote_status import (
    MANUAL_PUBLISHABLE_STATUSES,
    STATUS_BORING_NOTICE_FAILED,
    STATUS_BORING_NOTICE_UNKNOWN,
    STATUS_NOTIFYING_BORING,
    STATUS_PUBLISHED,
    STATUS_PUBLISH_FAILED,
    STATUS_PUBLISH_UNKNOWN,
    STATUS_PUBLISHING,
    STATUS_SKIPPED_BORING,
)
from .windows import QuoteWindow, get_closed_window, utc_now
from .windows import quote_timezone

log = setup_logging(logging.getLogger(__name__))

_STALE_QUOTE_AFTER = timedelta(minutes=15)
_MANUAL_PUBLISH_NOTHING = "nothing"
_MANUAL_PUBLISH_PUBLISHED = "published"
_MANUAL_PUBLISH_FAILED = "failed"
_MANUAL_PUBLISH_ALREADY_SENT = "already_sent"
_PUBLISH_UNKNOWN_SENT_PREFIX = "Telegram message was sent, but DB finalization failed:"
_MEDIA_COPY_CONTENT_TYPES = {"photo", "image", "video", "animation", "video_note", "voice", "audio"}
_MEDIA_CAPTION_LIMIT = 1024


async def quote_of_the_day_job(bot: Bot) -> None:
    window = get_closed_window()
    log.info(
        f"⏰ Запуск выбора цитаты дня за день "
        f"{window.start_local.isoformat()} -> {window.end_local.isoformat()}"
    )

    async with SessionLocal() as session:
        result = await session.execute(select(Group))
        groups = result.scalars().all()

    if not groups:
        log.info("📭 Нет групп для обработки")
        return

    for group in groups:
        try:
            await _process_group(bot, group, window)
        except Exception as e:
            log.error(f"❌ Ошибка при обработке группы {group.chat_id}: {e}")


async def recover_pending_media_job(bot: Bot) -> None:
    try:
        processed = await media.process_pending_media(
            bot,
            limit=settings.MEDIA_PENDING_RETRY_BATCH_SIZE,
        )
    except Exception as exc:
        log.warning(f"⚠️ Ошибка восстановления pending media: {exc}")
        return

    if processed:
        log.info(f"♻️ Обработано pending media: {processed}")


async def _process_group(bot: Bot, group: Group, window: QuoteWindow | None = None) -> None:
    window = window or get_closed_window()
    log.debug(f"{group.chat_id} | ⏳ Обработка дня {window.quote_day} для группы {group.name}")

    await _recover_stale_quotes_for_chat(group.chat_id)

    existing = await core.get_quote_for_day(group.id, window.quote_day)
    if existing:
        if existing and existing.decision_status in MANUAL_PUBLISHABLE_STATUSES:
            log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} ждёт ручного решения ({existing.decision_status})")
            return
        if existing:
            log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже обработан ({existing.decision_status})")
            return

    message_count = await core.count_window_messages(group.chat_id, window)

    if message_count == 0:
        log.info(f"{group.chat_id} | 📭 День {window.quote_day} пустой")
        return

    if message_count < settings.MIN_MESSAGES_FOR_AUTO_REVIEW:
        deleted = await core.clear_window_messages(group.chat_id, window)
        log.info(
            f"{group.chat_id} | 🤫 День {window.quote_day} пропущен: "
            f"сообщений {message_count} < {settings.MIN_MESSAGES_FOR_AUTO_REVIEW}. "
            f"Очищено {deleted} сообщений."
        )
        return

    evaluation = await scoring.pick_best_quote(
        group.chat_id,
        window,
        include_day_verdict=True,
        day_verdict_min_messages=settings.MIN_MESSAGES_FOR_AUTO_REVIEW,
        group_id=group.id,
        detect_interface_language=not i18n.group_language_is_set(group),
    )

    if not i18n.group_language_is_set(group) and evaluation.detected_language_code:
        if await core.set_group_language_auto(group.id, evaluation.detected_language_code):
            group.language_code = evaluation.detected_language_code
            group.language_source = i18n.LANGUAGE_SOURCE_AUTO
            log.info(
                f"{group.chat_id} | 🌐 Язык интерфейса выбран автоматически: "
                f"{evaluation.detected_language_code} ({evaluation.detected_chat_language or 'unknown'})"
            )

    if evaluation.message_count == 0:
        if evaluation.source_message_count:
            deleted = await core.clear_window_messages(group.chat_id, window)
            log.info(
                f"{group.chat_id} | 📭 День {window.quote_day} пустой после фильтра #quoto. "
                f"Очищено {deleted} сообщений."
            )
            return
        log.info(f"{group.chat_id} | 📭 День {window.quote_day} пустой")
        return

    if evaluation.message_count < settings.MIN_MESSAGES_FOR_AUTO_REVIEW:
        deleted = await core.clear_window_messages(group.chat_id, window)
        log.info(
            f"{group.chat_id} | 🤫 День {window.quote_day} пропущен: "
            f"сообщений {evaluation.message_count} < {settings.MIN_MESSAGES_FOR_AUTO_REVIEW}. "
            f"Очищено {deleted} сообщений."
        )
        return

    if not evaluation.best_message:
        log.warning(f"{group.chat_id} | ⚠️ Не удалось выбрать лидера дня {window.quote_day}")
        return

    if evaluation.day_verdict_error:
        quote, created = await core.create_quote_record(
            group=group,
            best_message=evaluation.best_message,
            breakdown=evaluation.breakdown,
            window=window,
            decision_status=STATUS_PUBLISH_FAILED,
            operation_error=evaluation.day_verdict_error,
            context_messages=evaluation.context_messages,
        )
        if created:
            log.error(
                f"{group.chat_id} | ⚠️ AI verdict для дня {window.quote_day} невалиден: "
                f"{evaluation.day_verdict_error}"
            )
        else:
            log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже забрал другой воркер")
        return

    author_name = evaluation.best_message.author.name if evaluation.best_message.author else "Аноним"

    if evaluation.should_publish:
        quote, created = await core.create_quote_record(
            group=group,
            best_message=evaluation.best_message,
            breakdown=evaluation.breakdown,
            window=window,
            decision_status=STATUS_PUBLISHING,
            decision_reason=evaluation.day_reason_text or None,
            context_messages=evaluation.context_messages,
        )
        if not created:
            log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже забрал другой воркер")
            return
        await _publish_quote_message(
            bot=bot,
            group=group,
            quote=quote,
            author_name=author_name,
            breakdown=evaluation.breakdown,
            forced_by_admin=False,
            clear_window_after=True,
        )
        return

    quote, created = await core.create_quote_record(
        group=group,
        best_message=evaluation.best_message,
        breakdown=evaluation.breakdown,
        window=window,
        decision_status=STATUS_NOTIFYING_BORING,
        decision_reason=evaluation.day_reason_text or None,
        context_messages=evaluation.context_messages,
    )
    if not created:
        log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже забрал другой воркер")
        return
    await _send_boring_notice(bot=bot, group=group, quote=quote, clear_window_after=True)


async def manual_publish_latest(bot: Bot, chat_id: int) -> str:
    await _recover_stale_quotes_for_chat(chat_id)

    latest = await core.get_latest_manual_publish_candidate(chat_id)
    if not latest or not latest.group:
        return _MANUAL_PUBLISH_NOTHING

    if (
        latest.decision_status == STATUS_PUBLISH_UNKNOWN
        and _publish_unknown_already_sent(latest.operation_error)
    ):
        return _MANUAL_PUBLISH_ALREADY_SENT

    quote, previous_status = await core.claim_latest_manual_publish_candidate(chat_id)
    if not quote or not quote.group:
        return _MANUAL_PUBLISH_NOTHING

    author_name = quote.author.name if quote.author else "Аноним"
    breakdown = scoring.ScoreBreakdown(
        reaction=quote.reaction_score or 0.0,
        ai=quote.ai_score or 0.0,
        length=quote.length_score or 0.0,
        reaction_count=quote.reaction_count or 0,
        ai_model=quote.ai_model or "",
        ai_best_text=quote.ai_best_text,
    )

    clear_window_after = _manual_publish_clears_window(previous_status)

    published = await _publish_quote_message(
        bot=bot,
        group=quote.group,
        quote=quote,
        author_name=author_name,
        breakdown=breakdown,
        forced_by_admin=True,
        clear_window_after=clear_window_after,
    )
    return _MANUAL_PUBLISH_PUBLISHED if published else _MANUAL_PUBLISH_FAILED


def _manual_publish_clears_window(previous_status: str | None) -> bool:
    return previous_status != STATUS_SKIPPED_BORING


def _publish_unknown_already_sent(operation_error: str | None) -> bool:
    return bool(operation_error and operation_error.startswith(_PUBLISH_UNKNOWN_SENT_PREFIX))


async def _recover_stale_quotes_for_chat(chat_id: int) -> int:
    stale_quotes = await core.get_stale_in_progress_quotes(
        chat_id=chat_id,
        older_than=utc_now() - _STALE_QUOTE_AFTER,
    )

    recovered = 0
    for quote in stale_quotes:
        if await _recover_stale_quote(quote):
            recovered += 1

    return recovered


async def _publish_quote_message(
    bot: Bot,
    group: Group,
    quote: Quote,
    author_name: str,
    breakdown: scoring.ScoreBreakdown,
    forced_by_admin: bool,
    clear_window_after: bool,
) -> bool:
    language = i18n.group_language(group)
    info_parts = [breakdown.stars]
    if breakdown.reaction_count:
        info_parts.append(f"{breakdown.reaction_count}❤️")
    info_parts.append(_format_quote_day(quote.quote_day, language))
    info_line = " · ".join(info_parts)

    msg_link = _message_link(group.chat_id, quote.message_id)
    details_link = f"https://t.me/{settings.BOT_USERNAME}?start=quote_{quote.id}"
    forced_line = f"\n{i18n.t(language, 'quote_post.forced')}" if forced_by_admin else ""
    text = _build_quote_post_text(
        language=language,
        quote=quote,
        author_name=author_name,
        info_line=info_line,
        msg_link=msg_link,
        details_link=details_link,
        forced_line=forced_line,
    )

    media_copy_error: str | None = None
    try:
        if _can_copy_media_quote(quote):
            try:
                sent = await bot.copy_message(
                    chat_id=group.chat_id,
                    from_chat_id=group.chat_id,
                    message_id=quote.message_id,
                    caption=_build_quote_post_text(
                        language=language,
                        quote=quote,
                        author_name=author_name,
                        info_line=info_line,
                        msg_link=msg_link,
                        details_link=details_link,
                        forced_line=forced_line,
                        media_caption=True,
                    ),
                )
            except Exception as exc:
                media_copy_error = str(exc)[:200]
                log.warning(f"⚠️ Не удалось скопировать медиа цитаты в {group.chat_id}: {exc}")
                sent = await bot.send_message(chat_id=group.chat_id, text=text)
        else:
            sent = await bot.send_message(chat_id=group.chat_id, text=text)
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_PUBLISH_FAILED,
            operation_error=str(e)[:250],
        )
        log.error(f"❌ Ошибка при отправке цитаты в {group.chat_id}: {e}")
        return False

    try:
        await core.update_quote_publication(
            quote_id=quote.id,
            bot_message_id=sent.message_id,
            forced_by_admin=forced_by_admin,
            decision_reason=quote.decision_reason,
        )
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_PUBLISH_UNKNOWN,
            operation_error=f"Telegram message was sent, but DB finalization failed: {str(e)[:200]}",
        )
        log.error(f"❌ Цитата отправлена, но не удалось зафиксировать статус в БД для {group.chat_id}: {e}")
        return False

    if media_copy_error:
        try:
            await core.append_quote_operation_error(quote.id, f"Media copy failed, text fallback used: {media_copy_error}")
        except Exception as exc:
            log.warning(f"⚠️ Не удалось сохранить ошибку копирования медиа для quote #{quote.id}: {exc}")

    try:
        await bot.pin_chat_message(
            chat_id=group.chat_id,
            message_id=sent.message_id,
            disable_notification=True,
        )
    except Exception as e:
        await core.append_quote_operation_error(quote.id, f"Pin failed: {str(e)[:200]}")
        log.warning(f"⚠️ Не удалось закрепить сообщение в {group.chat_id}: {e}")

    if clear_window_after:
        try:
            deleted = await core.clear_window_messages(group.chat_id, _window_from_quote(quote))
            log.debug(f"{group.chat_id} | 🗑️ Очищено {deleted} сообщений после публикации")
        except Exception as e:
            await core.append_quote_operation_error(quote.id, f"Cleanup failed: {str(e)[:200]}")
            log.warning(f"⚠️ Не удалось очистить день после публикации в {group.chat_id}: {e}")

    log.info(f"✅ Цитата дня {quote.quote_day} отправлена в {group.name} ({group.chat_id})")
    return True


async def _send_boring_notice(
    bot: Bot,
    group: Group,
    quote: Quote,
    clear_window_after: bool,
) -> None:
    language = i18n.group_language(group)
    details_link = f"https://t.me/{settings.BOT_USERNAME}?start=quote_{quote.id}"
    reason_line = f"\n💭 {escape(quote.decision_reason)}" if quote.decision_reason else ""
    text = (
        f"{i18n.t(language, 'boring.title')}\n\n"
        f"{i18n.t(language, 'boring.body')}"
        f"{reason_line}\n\n"
        f"<a href='{details_link}'>{i18n.t(language, 'common.details')}</a> · #quoto"
    )

    try:
        sent = await bot.send_message(chat_id=group.chat_id, text=text)
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_BORING_NOTICE_FAILED,
            operation_error=str(e)[:250],
        )
        log.error(f"❌ Ошибка при отправке boring-day уведомления в {group.chat_id}: {e}")
        return

    try:
        await core.update_quote_notice(quote.id, sent.message_id)
    except Exception as e:
        await core.mark_quote_status(
            quote_id=quote.id,
            decision_status=STATUS_BORING_NOTICE_UNKNOWN,
            operation_error=f"Telegram notice was sent, but DB finalization failed: {str(e)[:200]}",
        )
        log.error(
            f"❌ Boring-day уведомление отправлено, но не удалось зафиксировать статус в БД для "
            f"{group.chat_id}: {e}"
        )
        return

    if clear_window_after:
        try:
            deleted = await core.clear_window_messages(group.chat_id, _window_from_quote(quote))
            log.debug(f"{group.chat_id} | 🗑️ Очищено {deleted} сообщений после скучного дня")
        except Exception as e:
            await core.append_quote_operation_error(quote.id, f"Cleanup failed: {str(e)[:200]}")
            log.warning(f"⚠️ Не удалось очистить скучный день в {group.chat_id}: {e}")

    log.info(f"😴 День {quote.quote_day} помечен как скучный в {group.name} ({group.chat_id})")


async def _recover_stale_quote(quote: Quote) -> bool:
    if quote.decision_status not in {STATUS_PUBLISHING, STATUS_NOTIFYING_BORING}:
        return False

    if utc_now() - quote.created_at < _STALE_QUOTE_AFTER:
        return False

    fallback_status = (
        STATUS_PUBLISH_UNKNOWN
        if quote.decision_status == STATUS_PUBLISHING
        else STATUS_BORING_NOTICE_UNKNOWN
    )
    await core.mark_quote_status(
        quote.id,
        fallback_status,
        operation_error="In-progress quote timed out before final status confirmation.",
    )
    log.warning(f"⚠️ Quote #{quote.id} помечен как {fallback_status} после таймаута")
    return True


def _window_from_quote(quote: Quote) -> QuoteWindow:
    tz = quote_timezone()
    return QuoteWindow(
        quote_day=quote.quote_day,
        start_local=quote.window_start_at.astimezone(tz),
        end_local=quote.window_end_at.astimezone(tz),
        start_utc=quote.window_start_at,
        end_utc=quote.window_end_at,
    )


def _message_link(chat_id: int, message_id: int | None) -> str:
    link_chat_id = str(chat_id).replace("-100", "", 1) if str(chat_id).startswith("-100") else str(chat_id)
    return f"https://t.me/c/{link_chat_id}/{message_id}"


def _load_quote_context_lines(quote: Quote) -> list[dict[str, object]]:
    raw = getattr(quote, "context_snapshot", None)
    if not raw:
        return []
    try:
        parsed = json.loads(raw)
    except (TypeError, json.JSONDecodeError):
        return []
    if not isinstance(parsed, list) or len(parsed) <= 1:
        return []

    lines: list[dict[str, object]] = []
    for item in parsed:
        if not isinstance(item, dict):
            continue
        text = str(item.get("text") or "").strip()
        if not text:
            continue
        lines.append(
            {
                "author": str(item.get("author") or "Аноним"),
                "text": text,
                "is_primary": bool(item.get("is_primary")),
            }
        )
    return lines if len(lines) > 1 else []


def _build_quote_post_text(
    *,
    language: str,
    quote: Quote,
    author_name: str,
    info_line: str,
    msg_link: str,
    details_link: str,
    forced_line: str,
    media_caption: bool = False,
) -> str:
    quote_text = _display_quote_text(quote.text, media_caption=media_caption)
    safe_author_name = escape(author_name)
    context_lines = _load_quote_context_lines(quote)

    if context_lines and not media_caption:
        quote_body = _format_context_quote_body(context_lines)
    elif context_lines:
        quote_body = _format_context_quote_body(context_lines, text_limit=140)
    else:
        quote_body = (
            f"💬 <i>«{escape(quote_text)}»</i>\n"
            f"— <b>{safe_author_name}</b>"
        )

    text = (
        f"{_quote_day_title(quote, language)}\n\n"
        f"{quote_body}\n\n"
        f"{info_line}\n\n"
        f"<a href='{msg_link}'>{i18n.t(language, 'common.original')}</a> · "
        f"<a href='{details_link}'>{i18n.t(language, 'common.details')}</a> · #quoto"
        f"{forced_line}"
    )
    if not media_caption or len(text) <= _MEDIA_CAPTION_LIMIT:
        return text

    for limit in (320, 220, 140, 80):
        shortened = (
            f"💬 <i>«{escape(_truncate_text(str(quote.text or ''), limit))}»</i>\n"
            f"— <b>{safe_author_name}</b>"
        )
        text = (
            f"{_quote_day_title(quote, language)}\n\n"
            f"{shortened}\n\n"
            f"{info_line}\n\n"
            f"<a href='{msg_link}'>{i18n.t(language, 'common.original')}</a> · "
            f"<a href='{details_link}'>{i18n.t(language, 'common.details')}</a> · #quoto"
            f"{forced_line}"
        )
        if len(text) <= _MEDIA_CAPTION_LIMIT:
            return text

    return (
        f"{_quote_day_title(quote, language)}\n\n"
        f"— <b>{safe_author_name}</b>\n\n"
        f"{info_line}\n\n"
        f"<a href='{msg_link}'>{i18n.t(language, 'common.original')}</a> · "
        f"<a href='{details_link}'>{i18n.t(language, 'common.details')}</a> · #quoto"
        f"{forced_line}"
    )


def _quote_day_title(quote: Quote, language: str) -> str:
    content_type = str(getattr(quote, "content_type", "") or "text")
    if content_type in {"photo", "image"}:
        return i18n.t(language, "quote_post.titles.photo")
    if content_type in {"video", "animation", "video_note"}:
        return i18n.t(language, "quote_post.titles.video")
    if content_type == "voice":
        return i18n.t(language, "quote_post.titles.voice")
    if content_type == "audio":
        return i18n.t(language, "quote_post.titles.audio")
    return i18n.t(language, "quote_post.titles.text")


def _can_copy_media_quote(quote: Quote) -> bool:
    content_type = str(getattr(quote, "content_type", "") or "text")
    return content_type in _MEDIA_COPY_CONTENT_TYPES and bool(getattr(quote, "message_id", None))


def _display_quote_text(text: str | None, *, media_caption: bool) -> str:
    raw = str(text or "").strip()
    if not media_caption:
        return raw
    return _truncate_text(raw, 520)


def _truncate_text(text: str, limit: int) -> str:
    text = text.strip()
    if len(text) <= limit:
        return text
    return text[: max(0, limit - 1)].rstrip() + "…"


def _format_context_quote_body(context_lines: list[dict[str, object]], text_limit: int | None = None) -> str:
    rendered: list[str] = []
    for line in context_lines:
        author = escape(str(line["author"]))
        line_text = str(line["text"])
        if text_limit is not None:
            line_text = _truncate_text(line_text, text_limit)
        text = escape(line_text)
        if line.get("is_primary"):
            rendered.append(f"💬 <b>{author}:</b> <i>«{text}»</i>")
        else:
            rendered.append(f"<b>{author}:</b> {text}")
    return "\n".join(rendered)


def _format_quote_day(quote_day, language: str) -> str:
    return f"{quote_day.day} {i18n.month_name(language, quote_day.month)}"


def setup_scheduler(bot: Bot) -> AsyncIOScheduler:
    scheduler = AsyncIOScheduler(timezone=settings.TIMEZONE)
    scheduler.add_job(
        quote_of_the_day_job,
        trigger=CronTrigger(
            hour=settings.QUOTE_HOUR,
            minute=settings.QUOTE_MINUTE,
            timezone=settings.TIMEZONE,
        ),
        args=[bot],
        id="quote_of_the_day",
        name="Цитата дня",
        replace_existing=True,
    )
    scheduler.add_job(
        recover_pending_media_job,
        trigger=IntervalTrigger(
            seconds=settings.MEDIA_PENDING_RETRY_INTERVAL_SECONDS,
            timezone=settings.TIMEZONE,
        ),
        args=[bot],
        id="pending_media_recovery",
        name="Повторная обработка pending media",
        replace_existing=True,
        coalesce=True,
        max_instances=1,
        next_run_time=utc_now() + timedelta(seconds=settings.MEDIA_PENDING_RETRY_INTERVAL_SECONDS),
    )

    log.info(
        f"📅 Планировщик настроен: цитата дня в "
        f"{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d} ({settings.TIMEZONE})"
    )
    return scheduler
