import logging
import re
from collections import Counter
from html import escape

from aiogram import Bot, F, Router, types
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.filters import Command, CommandObject, CommandStart, and_f, or_f

from . import core, i18n, media, menu, scoring
from .config import settings, setup_logging
from .quote_status import (
    STATUS_BORING_NOTICE_FAILED,
    STATUS_BORING_NOTICE_UNKNOWN,
    STATUS_PUBLISHED,
    STATUS_PUBLISH_FAILED,
    STATUS_PUBLISH_UNKNOWN,
    STATUS_SKIPPED_BORING,
)

router = Router()
log = setup_logging(logging.getLogger(__name__))
_PANEL_COMMAND_MESSAGES: dict[tuple[int, int], tuple[int, int]] = {}
_LINK_ONLY_RE = re.compile(
    r"^(?:(?:https?://|tg://|www\.)\S+|(?:[a-z0-9-]+\.)+[a-z]{2,}(?:/[^\s]*)?)$",
    re.IGNORECASE,
)


def _html(value: object) -> str:
    return escape(str(value))


def _message_language(message: types.Message) -> str:
    user = getattr(message, "from_user", None)
    return i18n.language_or_default(getattr(user, "language_code", None))


def _time_label() -> str:
    return f"{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d}"


def _is_command_message(message: types.Message) -> bool:
    return bool(getattr(message, "text", None) and str(message.text).startswith("/"))


def _is_link_only_message(message: types.Message) -> bool:
    if media.extract_media_source(message):
        return False

    text = str(getattr(message, "text", "") or "").strip()
    if not text:
        return False

    if _LINK_ONLY_RE.fullmatch(text):
        return True

    entities = list(getattr(message, "entities", None) or [])
    if len(entities) != 1:
        return False

    entity = entities[0]
    entity_type = str(getattr(entity, "type", ""))
    return (
        entity_type in {"url", "text_link"}
        and int(getattr(entity, "offset", -1) or -1) == 0
        and int(getattr(entity, "length", 0) or 0) >= len(text)
    )


def _register_panel_message(panel: types.Message | None, trigger: types.Message | None) -> None:
    if not panel or not trigger or not _is_command_message(trigger):
        return
    _PANEL_COMMAND_MESSAGES[(panel.chat.id, panel.message_id)] = (
        trigger.chat.id,
        trigger.message_id,
    )


async def _delete_message_safely(bot: Bot, chat_id: int, message_id: int) -> None:
    try:
        await bot.delete_message(chat_id, message_id)
    except TelegramAPIError:
        return


async def _close_panel(callback: types.CallbackQuery, bot: Bot) -> None:
    panel = callback.message
    if not panel or not getattr(panel, "chat", None) or not getattr(panel, "message_id", None):
        await callback.answer()
        return

    command_ref = _PANEL_COMMAND_MESSAGES.pop((panel.chat.id, panel.message_id), None)
    await callback.answer()
    await _delete_message_safely(bot, panel.chat.id, panel.message_id)
    if command_ref:
        await _delete_message_safely(bot, command_ref[0], command_ref[1])


async def _edit_panel(
    callback: types.CallbackQuery,
    text: str,
    reply_markup: types.InlineKeyboardMarkup | None,
) -> None:
    panel = callback.message
    if not panel or not getattr(panel, "chat", None) or not getattr(panel, "edit_text", None):
        await callback.answer()
        return
    try:
        await panel.edit_text(text, reply_markup=reply_markup)
    except TelegramBadRequest as exc:
        if "message is not modified" in str(exc).lower():
            return
        await panel.answer(text, reply_markup=reply_markup)
    except TelegramAPIError:
        await panel.answer(text, reply_markup=reply_markup)


def _format_context_lines(context_messages: list[dict[str, object]], language: str) -> str:
    if len(context_messages) <= 1:
        return ""
    lines: list[str] = []
    for item in context_messages:
        author = _html(item.get("author") or i18n.t(language, "common.anonymous"))
        text = _html(item.get("text") or "")
        if item.get("is_primary"):
            lines.append(f"💬 <b>{author}:</b> <i>«{text}»</i>")
        else:
            lines.append(f"<b>{author}:</b> {text}")
    return "\n".join(lines)


def _format_chat_stats_text(language: str, stats: dict[str, object] | None) -> str:
    if not stats:
        return i18n.t(language, "stats.missing")

    if int(stats["total_quotes"]) == 0:
        return i18n.t(language, "stats.empty")

    medals = ["🥇", "🥈", "🥉"]
    top_lines = []
    for i, author in enumerate(stats["top_authors"]):
        medal = medals[i] if i < len(medals) else f"{i + 1}."
        top_lines.append(
            i18n.t(
                language,
                "stats.author_row",
                medal=medal,
                name=_html(author["name"]),
                wins=author["wins"],
                score=author["avg_score"] * 10,
            )
        )
    top_text = "\n".join(top_lines)

    text = (
        f"{i18n.t(language, 'stats.title')}\n\n"
        f"{i18n.t(language, 'stats.total_quotes', count=stats['total_quotes'])}\n"
        f"{i18n.t(language, 'stats.unique_authors', count=stats['unique_authors'])}\n"
        f"{i18n.t(language, 'stats.avg_score', score=stats['avg_score'] * 10)}\n\n"
        f"{i18n.t(language, 'stats.top_authors')}\n{top_text}"
    )

    if stats.get("best_quote"):
        bq = stats["best_quote"]
        quote_text = bq["text"][:80] + ("..." if len(bq["text"]) > 80 else "")
        text += (
            f"\n\n{i18n.t(language, 'stats.best_quote')}\n"
            f"💬 <i>«{_html(quote_text)}»</i>\n"
            f"— {_html(bq['author'])} ({bq['score'] * 10:.1f}/10)"
        )

    return text


def _format_user_stats_text(language: str, stats: dict[str, object] | None) -> str:
    if not stats:
        return i18n.t(language, "user_stats.missing")

    if int(stats["wins"]) == 0:
        return i18n.t(language, "user_stats.empty", user=_html(stats["user_name"]))

    text = (
        f"{i18n.t(language, 'user_stats.title')}\n\n"
        f"{i18n.t(language, 'user_stats.user', user=_html(stats['user_name']))}\n"
        f"{i18n.t(language, 'user_stats.wins', count=stats['wins'])}\n"
        f"{i18n.t(language, 'user_stats.avg_score', score=stats['avg_score'] * 10)}\n"
        f"{i18n.t(language, 'user_stats.rank', rank=stats['rank'], total=stats['total_participants'])}"
    )

    if stats.get("best_quote"):
        bq = stats["best_quote"]
        quote_text = bq["text"][:80] + ("..." if len(bq["text"]) > 80 else "")
        text += (
            f"\n\n{i18n.t(language, 'user_stats.best_quote')}\n"
            f"💬 <i>«{_html(quote_text)}»</i> ({bq['score'] * 10:.1f}/10)"
        )

    return text


def _decision_status_label(language: str, status: str) -> str:
    status_map = {
        STATUS_PUBLISHED: i18n.t(language, "status.published"),
        STATUS_SKIPPED_BORING: i18n.t(language, "status.skipped_boring"),
        STATUS_PUBLISH_FAILED: i18n.t(language, "status.publish_failed"),
        STATUS_BORING_NOTICE_FAILED: i18n.t(language, "status.boring_notice_failed"),
        STATUS_PUBLISH_UNKNOWN: i18n.t(language, "status.publish_unknown"),
        STATUS_BORING_NOTICE_UNKNOWN: i18n.t(language, "status.boring_notice_unknown"),
    }
    return status_map.get(status, status)


async def _send_start_menu(message: types.Message, bot: Bot | None = None) -> None:
    if not message.from_user:
        return

    if message.chat.type == "private":
        await core.user_getOrCreate(message.from_user)
        language = _message_language(message)
        text, reply_markup = menu.build_private_home(
            owner_id=message.from_user.id,
            language=language,
            bot_username=settings.BOT_USERNAME,
        )
    else:
        if bot is None:
            return
        group = await core.group_getOrCreate(message.chat)
        language = i18n.group_language(group)
        is_admin = await _is_chat_admin(bot, message.chat.id, message.from_user.id)
        text, reply_markup = menu.build_group_home(
            owner_id=message.from_user.id,
            language=language,
            group_language=language,
            group_language_source=group.language_source,
            is_admin=is_admin,
            quote_time=_time_label(),
            min_messages=settings.MIN_MESSAGES_FOR_AUTO_REVIEW,
        )

    panel = await message.answer(text, reply_markup=reply_markup)
    _register_panel_message(panel, message)


@router.my_chat_member()
async def bot_added_to_chat_event(event: types.ChatMemberUpdated):
    chat = event.chat
    text = ""
    text_log = ""
    language = i18n.language_or_default(getattr(getattr(event, "from_user", None), "language_code", None))
    safe_chat_title = _html(chat.title or "этот чат")

    if (
        event.old_chat_member.status not in ["member", "administrator", "restricted"]
        and event.new_chat_member.status in ["member", "administrator"]
    ):
        text_log = f"{chat.id} | Бот добавлен в группу {chat.title}"
        await core.group_getOrCreate(chat)

        text = i18n.t(
            language,
            "chat_member.added",
            chat_title=safe_chat_title,
            time=_time_label(),
        )

        if event.new_chat_member.status == "member":
            text += f"\n\n{i18n.t(language, 'chat_member.need_admin')}"
            text_log += " без прав администратора!"

    elif event.new_chat_member.status == "administrator" and event.old_chat_member.status in ["member", "restricted"]:
        text = i18n.t(language, "chat_member.admin_granted")
        text_log = f"{chat.id} | Бот назначен администратором"

    elif event.new_chat_member.status in ["member", "restricted"] and event.old_chat_member.status == "administrator":
        text = i18n.t(language, "chat_member.need_admin")
        text_log = f"{chat.id} | Бот снят с администратора"

    if text:
        await event.answer(text)
        log.debug(text_log)


@router.message(or_f(and_f(F.chat.type == "private", CommandStart()), F.chat.type == "private"))
async def private_handler(message: types.Message, command: CommandObject = None):
    await core.user_getOrCreate(message.from_user)
    language = _message_language(message)

    if command and command.args:
        args = command.args
        args_list = args.split("_")

        if args.lower() == "settings":
            await _send_start_menu(message)
            return

        if args.startswith("quote_"):
            try:
                quote_id = int(args_list[1])
            except (IndexError, ValueError):
                await message.answer(i18n.t(language, "private.invalid_quote_link"))
                return

            detail = await core.get_quote_detail(quote_id)
            if not detail:
                await message.answer(i18n.t(language, "private.quote_not_found"))
                return

            status_label = _decision_status_label(language, detail["decision_status"])
            model_short = detail.get("ai_model") or "AI"
            if "/" in model_short:
                model_short = model_short.split("/")[-1]
            safe_model_short = _html(model_short)

            created = detail["created_at"]
            date_str = f"{created.day} {i18n.month_name(language, created.month)} {created.year}"

            link_chat_id = None
            if detail.get("message_id") and detail.get("chat_id"):
                link_chat_id = (
                    str(detail["chat_id"]).replace("-100", "", 1)
                    if str(detail["chat_id"]).startswith("-100")
                    else str(detail["chat_id"])
                )

            context_text = _format_context_lines(detail.get("context_messages") or [], language)
            if context_text:
                text = (
                    f"{i18n.t(language, 'details.title', id=detail['id'])}\n\n"
                    f"🏷️ {status_label}\n"
                    f"{context_text}\n\n"
                )
            else:
                text = (
                    f"{i18n.t(language, 'details.title', id=detail['id'])}\n\n"
                    f"🏷️ {status_label}\n"
                    f"💬 <i>«{_html(detail['text'])}»</i>\n"
                    f"— <b>{_html(detail['author_name'])}</b>\n\n"
                )

            if link_chat_id:
                text += (
                    f"<a href='https://t.me/c/{link_chat_id}/{detail['message_id']}'>{_html(detail['group_name'])}</a>"
                    f"{' · ' + str(detail['reaction_count']) + '❤️' if detail['reaction_count'] > 0 else ''}"
                    f" · {date_str}\n\n"
                )
            else:
                text += f"{_html(detail['group_name'])} · {date_str}\n\n"

            text += (
                f"<b>{i18n.t(language, 'details.total')}: {detail['score'] * 10:.1f}/10</b>\n"
                f"<code>{safe_model_short:<10} {scoring.create_bar(int(detail['ai_score'] * 100), 100)}</code> {detail['ai_score'] * 10:.1f}/10 (100%)\n"
                f"<code>{i18n.t(language, 'details.reactions'):<10} {scoring.create_bar(int(detail['reaction_score'] * 100), 100)}</code> {detail['reaction_score'] * 10:.1f}/10 ({i18n.t(language, 'common.context')})\n"
                f"<code>{i18n.t(language, 'details.length'):<10} {scoring.create_bar(int(detail['length_score'] * 100), 100)}</code> {detail['length_score'] * 10:.1f}/10 ({i18n.t(language, 'common.context')})\n"
            )

            if detail.get("decision_reason"):
                text += f"\n💭 <b>{i18n.t(language, 'details.decision_reason')}:</b> {_html(detail['decision_reason'])}\n"

            if detail.get("operation_error"):
                text += f"⚠️ <b>{i18n.t(language, 'details.operation_error')}:</b> {_html(detail['operation_error'])}\n"

            if detail.get("ai_best_text"):
                ai_text = detail["ai_best_text"][:100] + ("..." if len(detail["ai_best_text"]) > 100 else "")
                text += f"💡 <b>{i18n.t(language, 'details.ai_choice')}:</b> <i>«{_html(ai_text)}»</i>\n"

            await message.answer(text)
            return

    await _send_start_menu(message)


@router.message(Command("start"), F.chat.type.in_({"group", "supergroup"}))
async def group_start_handler(message: types.Message, bot: Bot):
    await _send_start_menu(message, bot)


async def chat_stats_handler(message: types.Message):
    group = await core.group_getOrCreate(message.chat)
    language = i18n.group_language(group)
    stats = await core.get_chat_stats(message.chat.id)

    if not stats:
        await message.answer(i18n.t(language, "stats.missing"))
        return

    if stats["total_quotes"] == 0:
        await message.answer(i18n.t(language, "stats.empty"))
        return

    medals = ["🥇", "🥈", "🥉"]
    top_lines = []
    for i, author in enumerate(stats["top_authors"]):
        medal = medals[i] if i < len(medals) else f"{i + 1}."
        top_lines.append(i18n.t(
            language,
            "stats.author_row",
            medal=medal,
            name=_html(author["name"]),
            wins=author["wins"],
            score=author["avg_score"] * 10,
        )
        )
    top_text = "\n".join(top_lines)

    text = (
        f"{i18n.t(language, 'stats.title')}\n\n"
        f"{i18n.t(language, 'stats.total_quotes', count=stats['total_quotes'])}\n"
        f"{i18n.t(language, 'stats.unique_authors', count=stats['unique_authors'])}\n"
        f"{i18n.t(language, 'stats.avg_score', score=stats['avg_score'] * 10)}\n\n"
        f"{i18n.t(language, 'stats.top_authors')}\n{top_text}"
    )

    if stats.get("best_quote"):
        bq = stats["best_quote"]
        quote_text = bq["text"][:80] + ("..." if len(bq["text"]) > 80 else "")
        text += (
            f"\n\n{i18n.t(language, 'stats.best_quote')}\n"
            f"💬 <i>«{_html(quote_text)}»</i>\n"
            f"— {_html(bq['author'])} ({bq['score'] * 10:.1f}/10)"
        )

    await message.answer(text)


async def user_stats_handler(message: types.Message):
    if not message.from_user:
        return

    group = await core.group_getOrCreate(message.chat)
    language = i18n.group_language(group)
    stats = await core.get_user_stats(message.chat.id, message.from_user.id)

    if not stats:
        await message.answer(i18n.t(language, "user_stats.missing"))
        return

    if stats["wins"] == 0:
        await message.answer(
            i18n.t(language, "user_stats.empty", user=_html(stats["user_name"]))
        )
        return

    text = (
        f"{i18n.t(language, 'user_stats.title')}\n\n"
        f"{i18n.t(language, 'user_stats.user', user=_html(stats['user_name']))}\n"
        f"{i18n.t(language, 'user_stats.wins', count=stats['wins'])}\n"
        f"{i18n.t(language, 'user_stats.avg_score', score=stats['avg_score'] * 10)}\n"
        f"{i18n.t(language, 'user_stats.rank', rank=stats['rank'], total=stats['total_participants'])}"
    )

    if stats.get("best_quote"):
        bq = stats["best_quote"]
        quote_text = bq["text"][:80] + ("..." if len(bq["text"]) > 80 else "")
        text += (
            f"\n\n{i18n.t(language, 'user_stats.best_quote')}\n"
            f"💬 <i>«{_html(quote_text)}»</i> ({bq['score'] * 10:.1f}/10)"
        )

    await message.answer(text)


async def _group_menu_context(callback: types.CallbackQuery, bot: Bot):
    panel = callback.message
    if not panel or not getattr(panel, "chat", None) or panel.chat.type not in {"group", "supergroup"}:
        return None
    group = await core.group_getOrCreate(panel.chat)
    language = i18n.group_language(group)
    is_admin = await _is_chat_admin(bot, panel.chat.id, callback.from_user.id)
    return panel, group, language, is_admin


@router.callback_query(F.data.startswith(f"{menu.CALLBACK_PREFIX}:"))
async def start_menu_callback(callback: types.CallbackQuery, bot: Bot):
    parsed = menu.parse_callback_data(callback.data)
    if not parsed:
        await callback.answer()
        return

    user = callback.from_user
    language = i18n.language_or_default(getattr(user, "language_code", None))
    if user.id != parsed.owner_id:
        await callback.answer(i18n.t(language, "menu.other_user"), show_alert=True)
        return

    if parsed.action == menu.ACTION_CLOSE:
        await _close_panel(callback, bot)
        return

    if parsed.scope == menu.SCOPE_PRIVATE:
        if parsed.action != menu.ACTION_HOME:
            await callback.answer()
            return
        text, reply_markup = menu.build_private_home(
            owner_id=parsed.owner_id,
            language=language,
            bot_username=settings.BOT_USERNAME,
        )
        await _edit_panel(callback, text, reply_markup)
        await callback.answer()
        return

    context = await _group_menu_context(callback, bot)
    if not context:
        await callback.answer(i18n.t(language, "menu.group_context_required"), show_alert=True)
        return
    panel, group, language, is_admin = context

    if parsed.action == menu.ACTION_HOME:
        text, reply_markup = menu.build_group_home(
            owner_id=parsed.owner_id,
            language=language,
            group_language=language,
            group_language_source=group.language_source,
            is_admin=is_admin,
            quote_time=_time_label(),
            min_messages=settings.MIN_MESSAGES_FOR_AUTO_REVIEW,
        )
        await _edit_panel(callback, text, reply_markup)
        await callback.answer()
        return

    if parsed.action in {
        menu.ACTION_GROUP_LANGUAGE,
        menu.ACTION_SET_GROUP_LANGUAGE,
    } and not is_admin:
        await callback.answer(i18n.t(language, "admin.admin_only"), show_alert=True)
        return

    if parsed.action == menu.ACTION_GROUP_LANGUAGE:
        text, reply_markup = menu.build_group_language(
            owner_id=parsed.owner_id,
            language=language,
            current_language=language,
            language_source=group.language_source,
        )
        await _edit_panel(callback, text, reply_markup)
        await callback.answer()
        return

    if parsed.action == menu.ACTION_SET_GROUP_LANGUAGE:
        selected_language = i18n.normalize_language_code(parsed.payload)
        if not selected_language:
            await callback.answer(i18n.t(language, "menu.language.invalid"), show_alert=True)
            return
        await core.set_group_language_manual(group.id, selected_language)
        group = await core.get_group_by_chat_id(panel.chat.id) or group
        language = i18n.group_language(group)
        text, reply_markup = menu.build_group_language(
            owner_id=parsed.owner_id,
            language=language,
            current_language=language,
            language_source=group.language_source,
        )
        await _edit_panel(callback, text, reply_markup)
        await callback.answer(
            i18n.t(language, "menu.language.updated", language_name=i18n.language_name(language))
        )
        return

    if parsed.action == menu.ACTION_CHAT_STATS:
        stats = await core.get_chat_stats(panel.chat.id)
        text = _format_chat_stats_text(language, stats)
        await _edit_panel(
            callback,
            text,
            menu.build_back_close_keyboard(parsed.owner_id, menu.SCOPE_GROUP, language),
        )
        await callback.answer()
        return

    if parsed.action == menu.ACTION_USER_STATS:
        stats = await core.get_user_stats(panel.chat.id, user.id)
        text = _format_user_stats_text(language, stats)
        await _edit_panel(
            callback,
            text,
            menu.build_back_close_keyboard(parsed.owner_id, menu.SCOPE_GROUP, language),
        )
        await callback.answer()
        return

    await callback.answer()


@router.message(F.chat.type.in_({"group", "supergroup"}))
async def group_message_handler(message: types.Message, bot: Bot):
    if not message.from_user:
        return
    if message.from_user.is_bot:
        return
    if _is_command_message(message):
        return
    if _is_link_only_message(message):
        return

    user = await core.user_getOrCreate(message.from_user)
    await core.group_getOrCreate(message.chat)
    db_message = await core.save_message(message, user)
    if db_message:
        await media.process_message_media(bot, message, db_message)


@router.edited_message(F.chat.type.in_({"group", "supergroup"}))
async def edited_group_message_handler(message: types.Message):
    if not message.from_user:
        return
    if message.from_user.is_bot:
        return
    if _is_command_message(message):
        return
    if _is_link_only_message(message):
        return

    await core.update_message(message)


@router.message_reaction()
async def reaction_handler(event: types.MessageReactionUpdated):
    emoji_deltas: Counter[str] = Counter()

    for reaction in event.old_reaction:
        emoji = core._extract_emoji(reaction)
        if emoji:
            emoji_deltas[emoji] -= 1

    for reaction in event.new_reaction:
        emoji = core._extract_emoji(reaction)
        if emoji:
            emoji_deltas[emoji] += 1

    filtered_deltas = {emoji: delta for emoji, delta in emoji_deltas.items() if delta}
    if not filtered_deltas:
        return

    await core.apply_reaction_delta(event.chat.id, event.message_id, filtered_deltas)


@router.message_reaction_count()
async def reaction_count_handler(event: types.MessageReactionCountUpdated):
    emoji_counter: Counter[str] = Counter()
    for reaction in event.reactions:
        emoji = core._extract_emoji(reaction.type)
        if emoji:
            emoji_counter[emoji] = reaction.count

    await core.sync_reactions(event.chat.id, event.message_id, emoji_counter)


async def _is_chat_admin(bot: Bot, chat_id: int, user_id: int) -> bool:
    member = await bot.get_chat_member(chat_id, user_id)
    return member.status in {"administrator", "creator"}
