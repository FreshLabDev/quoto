import logging
from collections import Counter

from aiogram import Bot, F, Router, types
from aiogram.filters import Command, CommandObject, CommandStart, and_f, or_f
from aiogram.utils.keyboard import InlineKeyboardBuilder

from . import core, scheduler, scoring
from .config import settings, setup_logging
from .quote_status import (
    STATUS_BORING_NOTICE_FAILED,
    STATUS_BORING_NOTICE_UNKNOWN,
    STATUS_PUBLISHED,
    STATUS_PUBLISH_FAILED,
    STATUS_PUBLISH_UNKNOWN,
    STATUS_SKIPPED_BORING,
)
from .windows import get_open_window

router = Router()
log = setup_logging(logging.getLogger(__name__))


@router.my_chat_member()
async def bot_added_to_chat_event(event: types.ChatMemberUpdated):
    chat = event.chat
    kb = InlineKeyboardBuilder()
    text = ""
    text_log = ""

    if (
        event.old_chat_member.status not in ["member", "administrator", "restricted"]
        and event.new_chat_member.status in ["member", "administrator"]
    ):
        text_log = f"{chat.id} | Бот добавлен в группу {chat.title}"
        await core.group_getOrCreate(chat)

        text = (
            f"👋 Привет, <b>{chat.title}</b>!\n\n"
            f"Я <b>Quoto</b> — бот, который в <b>{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d}</b> "
            "выбирает и публикует цитату окна.\n\n"
            "📩 Я собираю сообщения и реакции между двумя cutoff-точками.\n"
            "🔎 Команда /quote доступна только администраторам и показывает AI-preview текущего окна.\n"
            "⚙️ /publish_quote нужна админам только для ручного override после boring-day/ошибки."
        )

        if event.new_chat_member.status == "member":
            text += (
                "\n\n⚠️ Пожалуйста, <b>назначьте меня администратором</b> с правами на "
                "<i>закрепление</i> сообщений, чтобы я мог публиковать результат."
            )
            text_log += " без прав администратора!"

    elif event.new_chat_member.status == "administrator" and event.old_chat_member.status in ["member", "restricted"]:
        text = "✅ <b>Спасибо</b>, что назначили меня администратором!"
        text_log = f"{chat.id} | Бот назначен администратором"

    elif event.new_chat_member.status in ["member", "restricted"] and event.old_chat_member.status == "administrator":
        text = (
            "⚠️ Пожалуйста, <b>назначьте меня администратором</b> с правами на "
            "<i>закрепление</i> сообщений, чтобы я мог публиковать результат."
        )
        text_log = f"{chat.id} | Бот снят с администратора"

    if text:
        await event.answer(text, reply_markup=kb.as_markup())
        log.debug(text_log)


@router.message(or_f(and_f(F.chat.type == "private", CommandStart()), F.chat.type == "private"))
async def private_handler(message: types.Message, command: CommandObject = None):
    await core.user_getOrCreate(message.from_user)

    if command and command.args:
        args = command.args
        args_list = args.split("_")

        if args.startswith("quote_"):
            try:
                quote_id = int(args_list[1])
            except (IndexError, ValueError):
                await message.answer("❌ Неверная ссылка на цитату.")
                return

            detail = await core.get_quote_detail(quote_id)
            if not detail:
                await message.answer("❌ Цитата не найдена.")
                return

            status_map = {
                STATUS_PUBLISHED: "✅ Опубликовано",
                STATUS_SKIPPED_BORING: "😴 Скучное окно",
                STATUS_PUBLISH_FAILED: "⚠️ Публикация не удалась",
                STATUS_BORING_NOTICE_FAILED: "⚠️ Boring-day уведомление не удалось",
                STATUS_PUBLISH_UNKNOWN: "⚠️ Публикация в неопределённом состоянии",
                STATUS_BORING_NOTICE_UNKNOWN: "⚠️ Boring-day в неопределённом состоянии",
            }
            status_label = status_map.get(detail["decision_status"], detail["decision_status"])
            model_short = detail.get("ai_model") or "AI"
            if "/" in model_short:
                model_short = model_short.split("/")[-1]

            created = detail["created_at"]
            months = [
                "",
                "Января",
                "Февраля",
                "Марта",
                "Апреля",
                "Мая",
                "Июня",
                "Июля",
                "Августа",
                "Сентября",
                "Октября",
                "Ноября",
                "Декабря",
            ]
            date_str = f"{created.day} {months[created.month]} {created.year}"

            link_chat_id = None
            if detail.get("message_id") and detail.get("chat_id"):
                link_chat_id = (
                    str(detail["chat_id"]).replace("-100", "", 1)
                    if str(detail["chat_id"]).startswith("-100")
                    else str(detail["chat_id"])
                )

            text = (
                f"📊 <b>Подробности окна #{detail['id']}</b>\n\n"
                f"🏷️ {status_label}\n"
                f"💬 <i>«{detail['text']}»</i>\n"
                f"— <b>{detail['author_name']}</b>\n\n"
            )

            if link_chat_id:
                text += (
                    f"<a href='https://t.me/c/{link_chat_id}/{detail['message_id']}'>{detail['group_name']}</a>"
                    f"{' · ' + str(detail['reaction_count']) + '❤️' if detail['reaction_count'] > 0 else ''}"
                    f" · {date_str}\n\n"
                )
            else:
                text += f"{detail['group_name']} · {date_str}\n\n"

            text += (
                f"<b>Итого: {detail['score'] * 10:.1f}/10</b>\n"
                f"<code>{'Реакции':<10} {scoring.create_bar(int(detail['reaction_score'] * 100), 100)}</code> {detail['reaction_score'] * 10:.1f}/10 ({int(settings.WEIGHT_REACTIONS * 100)}%)\n"
                f"<code>{model_short:<10} {scoring.create_bar(int(detail['ai_score'] * 100), 100)}</code> {detail['ai_score'] * 10:.1f}/10 ({int(settings.WEIGHT_AI * 100)}%)\n"
                f"<code>{'Длина':<10} {scoring.create_bar(int(detail['length_score'] * 100), 100)}</code> {detail['length_score'] * 10:.1f}/10 ({int(settings.WEIGHT_LENGTH * 100)}%)\n"
            )

            if detail.get("decision_reason"):
                text += f"\n💭 <b>Причина решения:</b> {detail['decision_reason']}\n"

            if detail.get("operation_error"):
                text += f"⚠️ <b>Техническая ошибка:</b> {detail['operation_error']}\n"

            if detail.get("ai_best_text"):
                ai_text = detail["ai_best_text"][:100] + ("..." if len(detail["ai_best_text"]) > 100 else "")
                text += f"💡 <b>Выбор ИИ:</b> <i>«{ai_text}»</i>\n"

            if detail.get("forced_by_admin"):
                text += "⚙️ <i>Опубликовано администратором вручную.</i>\n"

            await message.answer(text)
            return

    kb = InlineKeyboardBuilder()
    kb.button(text="➕ Добавить в группу", url=f"https://t.me/{settings.BOT_USERNAME}?startgroup=new")

    await message.answer(
        text=(
            "🏆 <b>Привет! Я Quoto</b>\n\n"
            "Я веду окно цитаты между двумя daily cutoff-точками и публикую результат в группе.\n\n"
            "🔎 /quote — AI-preview текущего окна для администраторов.\n"
            "😴 Если день вышел скучным, бот честно скажет об этом и даст ссылку на детали."
        ),
        reply_markup=kb.as_markup(),
    )


@router.message(Command("start"), F.chat.type.in_({"group", "supergroup"}))
async def group_start_handler(message: types.Message):
    await core.group_getOrCreate(message.chat)
    await message.answer(
        "🏆 <b>Quoto — Цитата окна</b>\n\n"
        "Я собираю сообщения между двумя cutoff-точками и в конце окна выбираю победителя.\n\n"
        "📌 <b>Команды:</b>\n"
        "/quote — AI-preview текущего окна (только админы)\n"
        "/publish_quote — ручная публикация после boring-day/ошибки (только админы)\n"
        "/stats — статистика опубликованных цитат\n"
        "/mystats — твоя статистика\n"
        "/start — эта справка"
    )


@router.message(Command("quote"), F.chat.type.in_({"group", "supergroup"}))
async def manual_quote_handler(message: types.Message, bot: Bot):
    if not message.from_user:
        return

    await core.group_getOrCreate(message.chat)
    if not await _is_chat_admin(bot, message.chat.id, message.from_user.id):
        await message.answer("🔒 Команда /quote с AI-preview доступна только администраторам чата.")
        return

    window = get_open_window()
    evaluation = await scoring.pick_best_quote(message.chat.id, window)

    if evaluation.message_count == 0 or not evaluation.best_message:
        await message.answer("📭 В текущем окне пока нет сообщений для preview.")
        return

    author_name = evaluation.best_message.author.name if evaluation.best_message.author else "Аноним"
    parts = [
        "🔎 <b>Preview текущего окна</b>",
        "",
        f"💬 <i>«{evaluation.best_message.text}»</i>",
        f"— <b>{author_name}</b>",
        "",
        f"📨 Сообщений в окне: <b>{evaluation.message_count}</b>",
        f"⭐ Текущий скор: <b>{evaluation.breakdown.total * 10:.1f}/10</b>",
    ]

    if evaluation.message_count < settings.MIN_MESSAGES_FOR_AUTO_REVIEW:
        parts.append(
            f"\n🤫 Если к {settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d} сообщений останется меньше "
            f"{settings.MIN_MESSAGES_FOR_AUTO_REVIEW}, окно будет пропущено без публикации."
        )
    else:
        parts.append(
            f"\n🤖 В {settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d} ИИ дополнительно решит, "
            "достоин ли весь день публикации."
        )

    await message.answer("\n".join(parts))


@router.message(Command("publish_quote"), F.chat.type.in_({"group", "supergroup"}))
async def manual_publish_handler(message: types.Message, bot: Bot):
    if not message.from_user:
        return

    if not await _is_chat_admin(bot, message.chat.id, message.from_user.id):
        await message.answer("🔒 Эта команда доступна только администраторам чата.")
        return

    processing = await message.answer("⏳ Проверяю, есть ли окно для ручной публикации...")
    result = await scheduler.manual_publish_latest(bot, message.chat.id)

    if result is None:
        await processing.edit_text("📭 Нет boring-day/failed окна, которое можно опубликовать вручную.")
        return

    if result is False:
        await processing.edit_text("❌ Ручная публикация не удалась. Смотри логи бота.")
        return

    await processing.delete()


@router.message(Command("stats"), F.chat.type.in_({"group", "supergroup"}))
async def chat_stats_handler(message: types.Message):
    stats = await core.get_chat_stats(message.chat.id)

    if not stats:
        await message.answer("📊 Статистика пока недоступна — бот ещё не зарегистрировал эту группу.")
        return

    if stats["total_quotes"] == 0:
        await message.answer("📊 В этом чате пока нет ни одной опубликованной цитаты.")
        return

    medals = ["🥇", "🥈", "🥉"]
    top_lines = []
    for i, author in enumerate(stats["top_authors"]):
        medal = medals[i] if i < len(medals) else f"{i + 1}."
        top_lines.append(
            f"{medal} <b>{author['name']}</b> — {author['wins']} 🏆 (avg {author['avg_score'] * 10:.1f}/10)"
        )
    top_text = "\n".join(top_lines)

    text = (
        f"📊 <b>Статистика чата</b>\n\n"
        f"🏆 Всего цитат: <b>{stats['total_quotes']}</b>\n"
        f"👥 Уникальных авторов: <b>{stats['unique_authors']}</b>\n"
        f"📈 Средний рейтинг: <b>{stats['avg_score'] * 10:.1f}/10</b>\n\n"
        f"👑 <b>Топ авторов:</b>\n{top_text}"
    )

    if stats.get("best_quote"):
        bq = stats["best_quote"]
        quote_text = bq["text"][:80] + ("..." if len(bq["text"]) > 80 else "")
        text += (
            f"\n\n⭐ <b>Лучшая цитата:</b>\n"
            f"💬 <i>«{quote_text}»</i>\n"
            f"— {bq['author']} ({bq['score'] * 10:.1f}/10)"
        )

    await message.answer(text)


@router.message(Command("mystats"), F.chat.type.in_({"group", "supergroup"}))
async def user_stats_handler(message: types.Message):
    if not message.from_user:
        return

    stats = await core.get_user_stats(message.chat.id, message.from_user.id)

    if not stats:
        await message.answer("📊 Статистика пока недоступна.")
        return

    if stats["wins"] == 0:
        await message.answer(
            f"📊 <b>{stats['user_name']}</b>, у тебя пока нет опубликованных цитат в этом чате.\n"
            "Продолжай писать — твоё время придёт! 💪"
        )
        return

    text = (
        f"📊 <b>Твоя статистика</b>\n\n"
        f"👤 <b>{stats['user_name']}</b>\n"
        f"🏆 Побед: <b>{stats['wins']}</b>\n"
        f"📈 Средний рейтинг: <b>{stats['avg_score'] * 10:.1f}/10</b>\n"
        f"🏅 Место: <b>{stats['rank']}</b> из {stats['total_participants']}"
    )

    if stats.get("best_quote"):
        bq = stats["best_quote"]
        quote_text = bq["text"][:80] + ("..." if len(bq["text"]) > 80 else "")
        text += (
            f"\n\n⭐ <b>Лучшая цитата:</b>\n"
            f"💬 <i>«{quote_text}»</i> ({bq['score'] * 10:.1f}/10)"
        )

    await message.answer(text)


@router.message(F.chat.type.in_({"group", "supergroup"}), F.text)
async def group_message_handler(message: types.Message):
    if message.from_user.is_bot:
        return
    if message.text.startswith("/"):
        return

    user = await core.user_getOrCreate(message.from_user)
    await core.group_getOrCreate(message.chat)
    await core.save_message(message, user)


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
