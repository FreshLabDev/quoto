from aiogram import Router, types, F, Bot
from aiogram.enums import ParseMode
from aiogram.types import InlineKeyboardButton
from aiogram.filters import Command, CommandObject, CommandStart, or_f, and_f
from aiogram.client.default import DefaultBotProperties
from aiogram.utils.keyboard import InlineKeyboardBuilder
import logging

from .config import settings, setup_logging
from .scoring import create_bar
from . import core

router = Router()
log = setup_logging(logging.getLogger(__name__))


# === СОБЫТИЯ БОТА ===

@router.my_chat_member()
async def bot_added_to_chat_event(event: types.ChatMemberUpdated):
    """Приветствие при добавлении бота в группу."""
    chat = event.chat
    kb = InlineKeyboardBuilder()
    text = ""
    text_log = ""
    
    if event.old_chat_member.status not in ["member", "administrator", "restricted"] and event.new_chat_member.status in ["member", "administrator"]:
        text_log = (f"{chat.id} | Бот добавлен в группу {chat.title}")

        await core.group_getOrCreate(chat)

        text = (
            f"👋 Привет, <b>{chat.title}</b>!\n\n"
            f"Я <b>Quto</b> — бот, который каждый день выбирает лучшую цитату вашего чата.\n\n"
            f"📩 Просто общайтесь как обычно — я запоминаю сообщения и реакции.\n"
            f"🏆 Каждый день в <b>{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d}</b> "
            f"я выбираю и закрепляю <b>цитату дня</b>!"
        )

        #TODO: kb.button(text="❓ Помощь", switch_inline_query_current_chat="Помощь")

        if event.new_chat_member.status == "member":
            text += "\n\n⚠️ Пожалуйста, <b>назначьте меня администратором</b> с правами на <i>закрепление</i> и <i>удаление</i> сообщений, <b>чтобы я мог полноценно функционировать!</b>"
            text_log += " без прав администратора!"
        

    elif event.new_chat_member.status == "administrator" and event.old_chat_member.status in ["member", "restricted"]:
        text = "✅ <b>Спасибо</b>, что назначили меня администратором!"
        text_log = f"{chat.id} | Бот назначен администратором"
    
    elif event.new_chat_member.status in ["member", "restricted"] and event.old_chat_member.status == "administrator":
        text = "⚠️ Пожалуйста, <b>назначьте меня администратором</b> с правами на <i>закрепление</i> и <i>удаление</i> сообщений, <b>чтобы я мог полноценно функционировать!</b>"
        text_log = f"{chat.id} | Бот снят с администратора"

    if text:
        await event.answer(text, reply_markup=kb.as_markup())
        log.debug(text_log)


# === ЛИЧНЫЕ СООБЩЕНИЯ ===

@router.message(or_f(and_f(F.chat.type == "private", CommandStart()), F.chat.type == "private"))
async def private_handler(message: types.Message, command: CommandObject = None):
    """Личные сообщения и рефералы."""
    user = await core.user_getOrCreate(message.from_user)
    if command and command.args:
        args = command.args
        args_list = args.split("_")
        
        if args.startswith("ref_"):
            referrer_id = args_list[1]
            await message.answer(f"👋 Тебя пригласил поселенец с ID: {referrer_id}")
            return
        
        if args.startswith("menu_"):
            _, menu, submenu = args_list + [None]*(3 - len(args_list))
            log.debug(f"{message.from_user.id} | Вызвано меню: {menu} -> {submenu}")
            return

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

            bar = create_bar
            w_r = int(settings.WEIGHT_REACTIONS * 100)
            w_a = int(settings.WEIGHT_AI * 100)
            w_l = int(settings.WEIGHT_LENGTH * 100)

            # Дата
            created = detail["created_at"]
            months = [
                "", "Января", "Февраля", "Марта", "Апреля", "Мая", "Июня",
                "Июля", "Августа", "Сентября", "Октября", "Ноября", "Декабря",
            ]
            date_str = f"{created.day} {months[created.month]} {created.year}"

            text = (
                f"📊 <b>Подробности цитаты #{detail['id']}</b>\n\n"
                f"💬 <i>«{detail['text']}»</i>\n"
                f"— <b>{detail['author_name']}</b>\n\n"
                f"🏠 {detail['group_name']}{' · ' + str(detail['reaction_count']) + '❤️' if detail['reaction_count'] > 0 else ''} · {date_str}\n\n"
                f"<b>Итого: {detail['score'] * 10:.1f}/10</b>\n"
                f"<code>{'Реакции':<10} {bar(int(detail['reaction_score'] * 100), 100)}</code> {detail['reaction_score'] * 10:.1f}/10 ({w_r}%)\n"
                f"<code>{'ИИ':<10} {bar(int(detail['ai_score'] * 100), 100)}</code> {detail['ai_score'] * 10:.1f}/10 ({w_a}%)\n"
                f"<code>{'Длина':<10} {bar(int(detail['length_score'] * 100), 100)}</code> {detail['length_score'] * 10:.1f}/10 ({w_l}%)\n\n"
            )

            await message.answer(text)
            return

    kb = InlineKeyboardBuilder()
    kb.button(text="➕ Добавить в группу", url=f"https://t.me/{settings.BOT_USERNAME}?startgroup=new")
    
    await message.answer(
        text=(
            "🏆 <b>Привет! Я Quto</b>\n\n"
            "Я выбираю лучшую <b>цитату дня</b> из сообщений вашего чата!\n\n"
            "📩 Добавь меня в группу, и я начну работу.\n"
            f"⏰ Каждый день в <b>{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d}</b> "
            "я выберу самое яркое сообщение и закреплю его."
        ),
        reply_markup=kb.as_markup()
    )


# === КОМАНДЫ В ГРУППАХ ===

@router.message(Command("start"), F.chat.type.in_({"group", "supergroup"}))
async def group_start_handler(message: types.Message):
    """Краткая справка по боту в группе."""
    await core.group_getOrCreate(message.chat)

    await message.answer(
        "🏆 <b>Quto — Цитата дня</b>\n\n"
        "Я автоматически собираю сообщения и реакции в этом чате, "
        f"а в <b>{settings.QUOTE_HOUR:02d}:{settings.QUOTE_MINUTE:02d}</b> "
        "выбираю и закрепляю лучшую <b>цитату дня</b>.\n\n"
        "📌 <b>Команды:</b>\n"
        "/quote — выбрать цитату прямо сейчас\n"
        "/stats — статистика чата\n"
        "/mystats — твоя статистика\n"
        "/start — эта справка"
    )


@router.message(Command("quote"), F.chat.type.in_({"group", "supergroup"}))
async def manual_quote_handler(message: types.Message, bot: Bot):
    """Ручной выбор цитаты дня (доступно всем)."""
    from . import scheduler

    group = await core.group_getOrCreate(message.chat)
    processing = await message.answer("⏳ <b>Выбираю лучшую цитату...</b>")
    await scheduler._process_group(bot, group)
    await processing.delete()


@router.message(Command("stats"), F.chat.type.in_({"group", "supergroup"}))
async def chat_stats_handler(message: types.Message):
    """Статистика чата — топ авторов, лучшая цитата, общий счёт."""
    stats = await core.get_chat_stats(message.chat.id)

    if not stats:
        await message.answer("📊 Статистика пока недоступна — бот ещё не зарегистрировал эту группу.")
        return

    if stats["total_quotes"] == 0:
        await message.answer("📊 В этом чате пока нет ни одной цитаты дня.")
        return

    # Формируем топ авторов
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
    """Личная статистика пользователя в этом чате."""
    if not message.from_user:
        return

    stats = await core.get_user_stats(message.chat.id, message.from_user.id)

    if not stats:
        await message.answer("📊 Статистика пока недоступна.")
        return

    if stats["wins"] == 0:
        await message.answer(
            f"📊 <b>{stats['user_name']}</b>, у тебя пока нет цитат дня в этом чате.\n"
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


# === СБОР СООБЩЕНИЙ ===

@router.message(F.chat.type.in_({"group", "supergroup"}), F.text)
async def group_message_handler(message: types.Message):
    """Сбор текстовых сообщений из групп для скоринга."""
    if message.from_user.is_bot:
        return
    if message.text.startswith("/"):
        return

    user = await core.user_getOrCreate(message.from_user)
    await core.group_getOrCreate(message.chat)
    await core.save_message(message, user)


# === РЕАКЦИИ ===

@router.message_reaction()
async def reaction_handler(event: types.MessageReactionUpdated):
    """Учёт реакций пользователей на сообщения.

    ``new_reaction`` — список ``ReactionType`` (текущие реакции пользователя).
    Каждый вызов содержит полное новое состояние реакций этого пользователя.
    """
    from collections import Counter

    emoji_counter: Counter[str] = Counter()
    for reaction in event.new_reaction:
        emoji = core._extract_emoji(reaction)
        if emoji:
            emoji_counter[emoji] += 1

    # Передаём в core для синхронизации (upsert по emoji)
    await core.upsert_reactions(event.chat.id, event.message_id, emoji_counter)


@router.message_reaction_count()
async def reaction_count_handler(event: types.MessageReactionCountUpdated):
    """Учёт агрегированных (анонимных) реакций — для каналов и групп с анонимными реакциями."""
    from collections import Counter

    emoji_counter: Counter[str] = Counter()
    for reaction in event.reactions:
        emoji = core._extract_emoji(reaction.type)
        if emoji:
            emoji_counter[emoji] = reaction.count

    await core.upsert_reactions(event.chat.id, event.message_id, emoji_counter)

