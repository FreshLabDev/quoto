import logging
import json
import os
from datetime import date, datetime, time, timedelta, timezone
from html import escape

from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.interval import IntervalTrigger
from aiogram import Bot
from sqlalchemy import select

from . import agreement, core, i18n, media, scoring
from .config import settings, setup_logging
from .db import SessionLocal
from .models import Group, Quote
from .quote_status import (
    STATUS_BORING_NOTICE_FAILED,
    STATUS_BORING_NOTICE_UNKNOWN,
    STATUS_NOTIFYING_BORING,
    STATUS_PUBLISHED,
    STATUS_PUBLISH_FAILED,
    STATUS_PUBLISH_UNKNOWN,
    STATUS_PUBLISHING,
    STATUS_SKIPPED_BORING,
)
from .windows import QuoteWindow, closed_window_for_day, get_closed_window, utc_now
from .windows import quote_timezone

log = setup_logging(logging.getLogger(__name__))

_STALE_QUOTE_AFTER = timedelta(minutes=15)
_TECHNICAL_RETRY_AFTER = timedelta(minutes=15)
_QUOTE_JOB_CATCH_UP_WINDOW = timedelta(hours=2)
_MEDIA_COPY_CONTENT_TYPES = {"photo", "image", "video", "animation", "video_note", "voice", "audio"}
_MEDIA_CAPTION_LIMIT = 1024
_TERMINAL_EXISTING_STATUSES = {
    STATUS_PUBLISHED,
    STATUS_SKIPPED_BORING,
    STATUS_PUBLISH_UNKNOWN,
    STATUS_BORING_NOTICE_UNKNOWN,
}
# Дни, доведённые до терминального исхода в этом процессе: пока день в кэше,
# повторные тики catch-up окна не трогают группу. После рестарта кэш пуст —
# истину восстанавливает запись Quote в БД.
_completed_days: dict[int, date] = {}
# Дни, за которые группе уже отправлено напоминание о принятии соглашения.
_agreement_reminded: dict[int, date] = {}


async def quote_of_the_day_job(bot: Bot) -> None:
    now = utc_now()
    async with SessionLocal() as session:
        result = await session.execute(select(Group))
        groups = result.scalars().all()

    if not groups:
        return

    for group in groups:
        window = _due_closed_window_for_group(group, now)
        if not window:
            continue
        if _completed_days.get(group.id) == window.quote_day:
            continue

        log.info(
            f"{group.chat_id} | ⏰ Запуск выбора цитаты дня за день "
            f"{window.start_local.isoformat()} -> {window.end_local.isoformat()}"
        )
        try:
            await _process_group(bot, group, window)
        except Exception as e:
            log.error(f"❌ Ошибка при обработке группы {group.chat_id}: {e}")


def _mark_day_completed(group: Group, window: QuoteWindow) -> None:
    _completed_days[group.id] = window.quote_day


def _existing_quote_is_terminal(quote: Quote) -> bool:
    if quote.decision_status in _TERMINAL_EXISTING_STATUSES:
        return True
    if quote.decision_status == STATUS_PUBLISH_FAILED:
        return bool(getattr(quote, "bot_message_id", None))
    if quote.decision_status == STATUS_BORING_NOTICE_FAILED:
        return bool(getattr(quote, "notice_message_id", None))
    return False


def _due_closed_window_for_group(group: Group, now: datetime) -> QuoteWindow | None:
    quote_hour, quote_minute = core.effective_group_quote_time(group)
    cutoff = time(hour=quote_hour, minute=quote_minute)
    tz = quote_timezone()
    now_local = now.astimezone(tz)
    cutoff_dates = (now_local.date(), now_local.date() - timedelta(days=1))
    for cutoff_date in cutoff_dates:
        cutoff_local = datetime.combine(cutoff_date, cutoff, tzinfo=tz)
        delay = now_local - cutoff_local
        if timedelta(0) <= delay < _QUOTE_JOB_CATCH_UP_WINDOW:
            return closed_window_for_day(cutoff_local.date(), tz=tz, at_time=cutoff)

    return None


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


def _heartbeat_path() -> str:
    return os.environ.get("HEARTBEAT_FILE") or os.path.join(settings.LOGS_PATH, "heartbeat")


async def heartbeat_job() -> None:
    """Refresh the liveness heartbeat; a stale file means the loop is wedged."""
    path = _heartbeat_path()
    try:
        directory = os.path.dirname(path)
        if directory:
            os.makedirs(directory, exist_ok=True)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(utc_now().isoformat())
    except Exception as exc:
        log.debug(f"heartbeat write failed: {exc}")


async def _remind_agreement(bot: Bot, group: Group, window: QuoteWindow, language: str) -> None:
    """Once per quote day, nudge the group to accept the user agreement."""
    if _agreement_reminded.get(group.id) == window.quote_day:
        return
    _agreement_reminded[group.id] = window.quote_day
    try:
        await bot.send_message(
            group.chat_id,
            i18n.t(language, "agreement.reminder"),
            reply_markup=agreement.build_welcome_keyboard(language),
        )
        log.info(f"{group.chat_id} | 📄 Отправлено напоминание о пользовательском соглашении")
    except Exception as exc:
        log.warning(f"{group.chat_id} | ⚠️ Не удалось отправить напоминание о соглашении: {exc}")


async def _process_group(bot: Bot, group: Group, window: QuoteWindow | None = None) -> None:
    quote_hour, quote_minute = core.effective_group_quote_time(group)
    window = window or get_closed_window(at_time=time(hour=quote_hour, minute=quote_minute))
    min_messages = core.effective_group_min_messages(group)
    context_messages_enabled = core.effective_group_quote_context_enabled(group)
    max_messages = core.effective_group_message_cap(group)
    language = i18n.group_language(group)
    log.debug(f"{group.chat_id} | ⏳ Обработка дня {window.quote_day} для группы {group.name}")

    if not core.group_agreement_accepted(group):
        log.info(f"{group.chat_id} | 📄 День {window.quote_day} пропущен: соглашение не принято")
        await _remind_agreement(bot, group, window, language)
        return

    await _recover_stale_quotes_for_chat(group.chat_id)

    existing = await core.get_quote_for_day(group.id, window.quote_day)
    if existing:
        if await _prepare_existing_quote_for_retry(bot, group, existing):
            log.info(
                f"{group.chat_id} | 🔁 День {window.quote_day} будет пересчитан после "
                f"технической ошибки ({existing.decision_status})"
            )
        else:
            if _existing_quote_is_terminal(existing):
                _mark_day_completed(group, window)
            log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже обработан ({existing.decision_status})")
            return

    message_count = await core.count_window_messages(group.chat_id, window)

    if message_count == 0:
        _mark_day_completed(group, window)
        log.info(f"{group.chat_id} | 📭 День {window.quote_day} пустой")
        return

    if message_count < min_messages:
        deleted = await core.clear_window_messages(group.chat_id, window)
        _mark_day_completed(group, window)
        log.info(
            f"{group.chat_id} | 🤫 День {window.quote_day} пропущен: "
            f"сообщений {message_count} < {min_messages}. "
            f"Очищено {deleted} сообщений."
        )
        return

    evaluation = await scoring.pick_best_quote(
        group.chat_id,
        window,
        include_day_verdict=True,
        day_verdict_min_messages=min_messages,
        group_id=group.id,
        detect_interface_language=not i18n.group_language_is_set(group),
        max_messages=max_messages,
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
        _mark_day_completed(group, window)
        if evaluation.source_message_count:
            deleted = await core.clear_window_messages(group.chat_id, window)
            log.info(
                f"{group.chat_id} | 📭 День {window.quote_day} пустой после фильтра #quoto. "
                f"Очищено {deleted} сообщений."
            )
            return
        log.info(f"{group.chat_id} | 📭 День {window.quote_day} пустой")
        return

    if evaluation.message_count < min_messages:
        deleted = await core.clear_window_messages(group.chat_id, window)
        _mark_day_completed(group, window)
        log.info(
            f"{group.chat_id} | 🤫 День {window.quote_day} пропущен: "
            f"сообщений {evaluation.message_count} < {min_messages}. "
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
            context_messages=_quote_context_messages(evaluation, context_messages_enabled),
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
            context_messages=_quote_context_messages(evaluation, context_messages_enabled),
        )
        if not created:
            log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже забрал другой воркер")
            return
        truncated_note = (
            i18n.t(
                language,
                "quote_post.context_truncated",
                shown=max_messages,
                total=evaluation.context_truncated_total,
            )
            if evaluation.context_truncated and max_messages
            else None
        )
        published = await _publish_quote_message(
            bot=bot,
            group=group,
            quote=quote,
            author_name=author_name,
            breakdown=evaluation.breakdown,
            clear_window_after=True,
            pin_enabled=core.effective_group_pin_enabled(group),
            extra_note=truncated_note,
        )
        if published:
            _mark_day_completed(group, window)
        return

    quote, created = await core.create_quote_record(
        group=group,
        best_message=evaluation.best_message,
        breakdown=evaluation.breakdown,
        window=window,
        decision_status=(
            STATUS_NOTIFYING_BORING
            if core.effective_group_boring_notice_enabled(group)
            else STATUS_SKIPPED_BORING
        ),
        decision_reason=evaluation.day_reason_text or None,
        context_messages=_quote_context_messages(evaluation, context_messages_enabled),
    )
    if not created:
        log.info(f"{group.chat_id} | ⏭️ День {window.quote_day} уже забрал другой воркер")
        return
    if not core.effective_group_boring_notice_enabled(group):
        deleted = await core.clear_window_messages(group.chat_id, window)
        _mark_day_completed(group, window)
        log.info(
            f"{group.chat_id} | 😴 День {window.quote_day} помечен скучным без уведомления. "
            f"Очищено {deleted} сообщений."
        )
        return
    if await _send_boring_notice(bot=bot, group=group, quote=quote, clear_window_after=True):
        _mark_day_completed(group, window)


def _quote_context_messages(
    evaluation: scoring.QuoteEvaluation,
    enabled: bool,
) -> list:
    return evaluation.context_messages if enabled else []


async def _prepare_existing_quote_for_retry(bot: Bot, group: Group, quote: Quote) -> bool:
    if quote.decision_status == STATUS_PUBLISH_FAILED:
        if getattr(quote, "bot_message_id", None) or not _technical_retry_due(quote):
            return False
        deleted = await core.delete_quote_record(quote.id)
        if not deleted:
            return False
        return True

    if quote.decision_status == STATUS_BORING_NOTICE_FAILED:
        if getattr(quote, "notice_message_id", None) or not _technical_retry_due(quote):
            return False
        if core.effective_group_boring_notice_enabled(group):
            await _send_boring_notice(bot=bot, group=group, quote=quote, clear_window_after=True)
            return False
        await core.mark_quote_status(
            quote.id,
            STATUS_SKIPPED_BORING,
            operation_error=None,
        )
        try:
            deleted = await core.clear_window_messages(group.chat_id, _window_from_quote(quote))
            log.debug(f"{group.chat_id} | 🗑️ Очищено {deleted} сообщений после отключённого boring notice")
        except Exception as exc:
            await core.append_quote_operation_error(quote.id, f"Cleanup failed: {str(exc)[:200]}")
            log.warning(f"⚠️ Не удалось очистить скучный день в {group.chat_id}: {exc}")
        return False

    return False


def _technical_retry_due(quote: Quote, now: datetime | None = None) -> bool:
    changed_at = getattr(quote, "status_changed_at", None) or getattr(quote, "created_at", None)
    if changed_at is None:
        return True
    return _as_utc(now or utc_now()) - _as_utc(changed_at) >= _TECHNICAL_RETRY_AFTER


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)


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
    clear_window_after: bool,
    pin_enabled: bool = True,
    extra_note: str | None = None,
) -> bool:
    language = i18n.group_language(group)
    info_parts = [f"<i>{breakdown.stars}</i>"]
    if breakdown.reaction_count:
        info_parts.append(f"{breakdown.reaction_count} ❤️")
    info_parts.append(_format_quote_day(quote.quote_day, language))
    info_line = " · ".join(info_parts)

    msg_link = _message_link(group.chat_id, quote.message_id)
    details_link = f"https://t.me/{settings.BOT_USERNAME}?start=quote_{quote.id}"
    text = _build_quote_post_text(
        language=language,
        quote=quote,
        author_name=author_name,
        info_line=info_line,
        msg_link=msg_link,
        details_link=details_link,
        extra_note=extra_note,
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

    if pin_enabled:
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
) -> bool:
    language = i18n.group_language(group)
    details_link = f"https://t.me/{settings.BOT_USERNAME}?start=quote_{quote.id}"
    reason_block = (
        f"\n\n<blockquote><i>{escape(quote.decision_reason)}</i></blockquote>"
        if quote.decision_reason
        else ""
    )
    text = (
        f"{i18n.t(language, 'boring.title')}\n\n"
        f"<i>{i18n.t(language, 'boring.body')}</i>"
        f"{reason_block}\n\n"
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
        return False

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
        return False

    if clear_window_after:
        try:
            deleted = await core.clear_window_messages(group.chat_id, _window_from_quote(quote))
            log.debug(f"{group.chat_id} | 🗑️ Очищено {deleted} сообщений после скучного дня")
        except Exception as e:
            await core.append_quote_operation_error(quote.id, f"Cleanup failed: {str(e)[:200]}")
            log.warning(f"⚠️ Не удалось очистить скучный день в {group.chat_id}: {e}")

    log.info(f"😴 День {quote.quote_day} помечен как скучный в {group.name} ({group.chat_id})")
    return True


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
    media_caption: bool = False,
    extra_note: str | None = None,
) -> str:
    quote_text = _display_quote_text(quote.text, media_caption=media_caption)
    safe_author_name = escape(author_name)
    context_lines = _load_quote_context_lines(quote)
    footer = (
        f"<a href='{msg_link}'>{i18n.t(language, 'common.original')}</a> · "
        f"<a href='{details_link}'>{i18n.t(language, 'common.details')}</a> · #quoto"
    )

    def _compose(body: str) -> str:
        parts = [
            _quote_day_title(quote, language),
            f"<blockquote>{body}</blockquote>",
            info_line,
        ]
        if extra_note and not media_caption:
            parts.append(f"<i>{extra_note}</i>")
        parts.append(footer)
        return "\n\n".join(parts)

    if context_lines and not media_caption:
        body = _format_context_quote_body(context_lines)
    elif context_lines:
        body = _format_context_quote_body(context_lines, text_limit=140)
    else:
        body = f"<i>«{escape(quote_text)}»</i>\n— <b>{safe_author_name}</b>"

    text = _compose(body)
    if not media_caption or len(text) <= _MEDIA_CAPTION_LIMIT:
        return text

    for limit in (320, 220, 140, 80):
        shortened = (
            f"<i>«{escape(_truncate_text(str(quote.text or ''), limit))}»</i>\n"
            f"— <b>{safe_author_name}</b>"
        )
        text = _compose(shortened)
        if len(text) <= _MEDIA_CAPTION_LIMIT:
            return text

    return _compose(f"— <b>{safe_author_name}</b>")


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
            rendered.append(f"<b>{author}:</b> <i>«{text}»</i>")
        else:
            rendered.append(f"<b>{author}:</b> {text}")
    return "\n".join(rendered)


def _format_quote_day(quote_day, language: str) -> str:
    return f"{quote_day.day} {i18n.month_name(language, quote_day.month)}"


def setup_scheduler(bot: Bot) -> AsyncIOScheduler:
    scheduler = AsyncIOScheduler(timezone=settings.TIMEZONE)
    scheduler.add_job(
        quote_of_the_day_job,
        trigger=IntervalTrigger(
            minutes=1,
            timezone=settings.TIMEZONE,
        ),
        args=[bot],
        id="quote_of_the_day",
        name="Цитата дня",
        replace_existing=True,
        coalesce=True,
        max_instances=1,
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
    scheduler.add_job(
        heartbeat_job,
        trigger=IntervalTrigger(seconds=30, timezone=settings.TIMEZONE),
        id="heartbeat",
        name="Heartbeat",
        replace_existing=True,
        coalesce=True,
        max_instances=1,
        next_run_time=utc_now(),
    )

    log.info(
        f"📅 Планировщик настроен: проверка цитаты дня каждую минуту "
        f"({settings.TIMEZONE}); время выбирается из настроек каждой группы"
    )
    return scheduler
