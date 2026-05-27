from datetime import datetime, timezone
import os
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import handlers
from app.quote_status import STATUS_PUBLISH_FAILED


class DummyResponse:
    def __init__(self, chat=None, message_id: int = 900) -> None:
        self.chat = chat
        self.message_id = message_id
        self.edits: list[str] = []
        self.edit_markups = []
        self.deleted = False

    async def edit_text(self, text: str, reply_markup=None) -> None:
        self.edits.append(text)
        self.edit_markups.append(reply_markup)

    async def delete(self) -> None:
        self.deleted = True


class DummyMessage:
    def __init__(
        self,
        chat_type: str = "supergroup",
        language_code: str | None = None,
        text: str | None = None,
    ) -> None:
        self.chat = SimpleNamespace(id=-100123456, type=chat_type, title="Quoto Test Chat")
        self.from_user = SimpleNamespace(id=777, is_bot=False, language_code=language_code)
        self.message_id = 100
        self.text = text
        self.answers: list[str] = []
        self.answer_markups = []
        self.responses: list[DummyResponse] = []

    async def answer(self, text: str, reply_markup=None):
        self.answers.append(text)
        self.answer_markups.append(reply_markup)
        response = DummyResponse(chat=self.chat, message_id=900 + len(self.responses))
        self.responses.append(response)
        return response


class HandlerTests(unittest.IsolatedAsyncioTestCase):
    async def test_private_quote_details_escape_reason_and_operation_error(self) -> None:
        message = DummyMessage(chat_type="private", language_code="ru")
        detail = {
            "id": 7,
            "text": "Цитата <script>",
            "score": 0.8,
            "reaction_score": 0.2,
            "ai_score": 0.5,
            "length_score": 0.1,
            "reaction_count": 3,
            "author_name": "Alice & Bob",
            "group_name": "Quoto <Test> Chat",
            "created_at": datetime(2026, 3, 27, 21, 0, tzinfo=timezone.utc),
            "ai_model": "openrouter/test",
            "ai_best_text": None,
            "message_id": 10,
            "chat_id": -100123456,
            "decision_status": STATUS_PUBLISH_FAILED,
            "decision_reason": "LLM rejected <auto> publication",
            "operation_error": "Telegram timeout & retry",
            "quote_day": None,
            "language_code": "uk",
        }

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "get_quote_detail", new=AsyncMock(return_value=detail)),
        ):
            await handlers.private_handler(message, SimpleNamespace(args="quote_7"))

        self.assertIn("Причина решения", message.answers[0])
        self.assertIn("LLM rejected &lt;auto&gt; publication", message.answers[0])
        self.assertIn("Техническая ошибка", message.answers[0])
        self.assertIn("Telegram timeout &amp; retry", message.answers[0])
        self.assertIn("Alice &amp; Bob", message.answers[0])
        self.assertIn("Quoto &lt;Test&gt; Chat", message.answers[0])
        self.assertIn("Цитата &lt;script&gt;", message.answers[0])

    async def test_private_uses_telegram_user_language_not_group_language(self) -> None:
        message = DummyMessage(chat_type="private", language_code="en-US")
        detail = {
            "id": 7,
            "text": "Цитата <script>",
            "score": 0.8,
            "reaction_score": 0.2,
            "ai_score": 0.5,
            "length_score": 0.1,
            "reaction_count": 0,
            "author_name": "Alice",
            "group_name": "Quoto Test Chat",
            "created_at": datetime(2026, 3, 27, 21, 0, tzinfo=timezone.utc),
            "ai_model": "openrouter/test",
            "ai_best_text": None,
            "message_id": 10,
            "chat_id": -100123456,
            "decision_status": STATUS_PUBLISH_FAILED,
            "decision_reason": "LLM rejected",
            "operation_error": None,
            "quote_day": None,
            "language_code": "uk",
        }

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "get_quote_detail", new=AsyncMock(return_value=detail)),
        ):
            await handlers.private_handler(message, SimpleNamespace(args="quote_7"))

        self.assertIn("Decision reason", message.answers[0])
        self.assertNotIn("Причина решения", message.answers[0])
        self.assertNotIn("Причина рішення", message.answers[0])

    async def test_private_falls_back_to_english_for_unknown_telegram_language(self) -> None:
        message = DummyMessage(chat_type="private", language_code="es")

        with patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()):
            await handlers.private_handler(message, SimpleNamespace(args=None))

        self.assertIn("<b>Quoto</b>", message.answers[0])
        self.assertIn("use /start there for controls", message.answers[0])

    async def test_group_start_menu_shows_admin_controls(self) -> None:
        message = DummyMessage(text="/start")

        with (
            patch.object(
                handlers.core,
                "group_getOrCreate",
                new=AsyncMock(return_value=SimpleNamespace(language_code="ru", language_source=None)),
            ),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
        ):
            await handlers.group_start_handler(message, SimpleNamespace())

        self.assertIn("Панель Quoto", message.answers[0])
        labels = [
            button.text
            for row in message.answer_markups[0].inline_keyboard
            for button in row
        ]
        self.assertEqual(
            labels,
            ["👤 Моя статистика", "📊 Статистика чата", "⚙️ Настройки", "Закрыть"],
        )

    async def test_close_panel_deletes_panel_and_command_message(self) -> None:
        panel = DummyResponse(chat=SimpleNamespace(id=-100123456), message_id=901)
        callback = SimpleNamespace(message=panel, answered=[])

        async def answer(text=None, show_alert=None):
            callback.answered.append((text, show_alert))

        callback.answer = answer
        bot = SimpleNamespace(delete_message=AsyncMock())
        handlers._PANEL_COMMAND_MESSAGES[(panel.chat.id, panel.message_id)] = (panel.chat.id, 100)

        await handlers._close_panel(callback, bot)

        bot.delete_message.assert_any_await(panel.chat.id, panel.message_id)
        bot.delete_message.assert_any_await(panel.chat.id, 100)
        self.assertNotIn((panel.chat.id, panel.message_id), handlers._PANEL_COMMAND_MESSAGES)

    async def test_group_language_callback_sets_manual_language(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:setlang:uk",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )

        with (
            patch.object(
                handlers.core,
                "group_getOrCreate",
                new=AsyncMock(return_value=SimpleNamespace(id=1, language_code="ru", language_source=None)),
            ),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
            patch.object(handlers.core, "set_group_language_manual", new=AsyncMock()) as set_language,
            patch.object(
                handlers.core,
                "get_group_by_chat_id",
                new=AsyncMock(
                    return_value=SimpleNamespace(
                        id=1,
                        language_code="uk",
                        language_source=handlers.i18n.LANGUAGE_SOURCE_MANUAL,
                    )
                ),
            ),
        ):
            await handlers.start_menu_callback(callback, SimpleNamespace())

        set_language.assert_awaited_once_with(1, "uk")
        self.assertIn("Мова групи", panel.edits[0])
        callback.answer.assert_awaited()

    async def test_group_settings_explains_quote_context(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:settings",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )

        with (
            patch.object(
                handlers.core,
                "group_getOrCreate",
                new=AsyncMock(
                    return_value=SimpleNamespace(
                        id=1,
                        language_code="ru",
                        language_source=None,
                        quote_context_enabled=True,
                    )
                ),
            ),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
        ):
            await handlers.start_menu_callback(callback, SimpleNamespace())

        self.assertIn("Настройки группы", panel.edits[0])
        self.assertIn("несколько соседних или reply-связанных сообщений", panel.edits[0])
        labels = [
            button.text
            for row in panel.edit_markups[0].inline_keyboard
            for button in row
        ]
        self.assertIn("◉ Контекст", labels)

    async def test_private_language_callback_sets_manual_user_language(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=777, type="private", title=None),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:p:setplang:de",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )

        with (
            patch.object(
                handlers.core,
                "user_getOrCreate",
                new=AsyncMock(return_value=SimpleNamespace(language_code=None, language_source=None)),
            ),
            patch.object(handlers.core, "set_user_language_manual", new=AsyncMock()) as set_language,
        ):
            await handlers.start_menu_callback(callback, SimpleNamespace())

        set_language.assert_awaited_once_with(777, "de")
        self.assertIn("Sprache im privaten Chat", panel.edits[0])
        callback.answer.assert_awaited()

    async def test_private_quote_details_renders_context_messages(self) -> None:
        message = DummyMessage(chat_type="private", language_code="ru")
        detail = {
            "id": 8,
            "text": "primary",
            "score": 0.8,
            "reaction_score": 0.2,
            "ai_score": 0.8,
            "length_score": 0.1,
            "reaction_count": 0,
            "author_name": "Bob",
            "group_name": "Quoto Test Chat",
            "created_at": datetime(2026, 3, 27, 21, 0, tzinfo=timezone.utc),
            "ai_model": "openrouter/test",
            "ai_best_text": None,
            "message_id": 10,
            "chat_id": -100123456,
            "decision_status": STATUS_PUBLISH_FAILED,
            "decision_reason": None,
            "operation_error": None,
            "quote_day": None,
            "context_messages": [
                {"message_id": 9, "author": "Alice <A>", "text": "setup & context", "is_primary": False},
                {"message_id": 10, "author": "Bob", "text": "punch <line>", "is_primary": True},
            ],
        }

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "get_quote_detail", new=AsyncMock(return_value=detail)),
        ):
            await handlers.private_handler(message, SimpleNamespace(args="quote_8"))

        self.assertIn("<b>Alice &lt;A&gt;:</b> setup &amp; context", message.answers[0])
        self.assertIn("💬 <b>Bob:</b> <i>«punch &lt;line&gt;»</i>", message.answers[0])

    async def test_reaction_handler_applies_non_anonymous_delta(self) -> None:
        event = SimpleNamespace(
            chat=SimpleNamespace(id=-100123456),
            message_id=42,
            old_reaction=[SimpleNamespace(emoji="🔥"), SimpleNamespace(emoji="❤️")],
            new_reaction=[SimpleNamespace(emoji="🔥"), SimpleNamespace(emoji="😂")],
        )

        with patch.object(handlers.core, "apply_reaction_delta", new=AsyncMock()) as apply_delta:
            await handlers.reaction_handler(event)

        apply_delta.assert_awaited_once_with(
            -100123456,
            42,
            {"❤️": -1, "😂": 1},
        )

    async def test_reaction_handler_skips_zero_net_delta(self) -> None:
        event = SimpleNamespace(
            chat=SimpleNamespace(id=-100123456),
            message_id=42,
            old_reaction=[SimpleNamespace(emoji="🔥")],
            new_reaction=[SimpleNamespace(emoji="🔥")],
        )

        with patch.object(handlers.core, "apply_reaction_delta", new=AsyncMock()) as apply_delta:
            await handlers.reaction_handler(event)

        apply_delta.assert_not_awaited()

    async def test_group_message_handler_ignores_sender_chat_updates_without_from_user(self) -> None:
        message = SimpleNamespace(
            from_user=None,
            text="anonymous admin message",
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()) as user_get_or_create,
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()) as group_get_or_create,
            patch.object(handlers.core, "save_message", new=AsyncMock()) as save_message,
            patch.object(handlers.media, "process_message_media", new=AsyncMock()) as process_media,
        ):
            await handlers.group_message_handler(message, SimpleNamespace())

        user_get_or_create.assert_not_awaited()
        group_get_or_create.assert_not_awaited()
        save_message.assert_not_awaited()
        process_media.assert_not_awaited()

    async def test_group_message_handler_accepts_media_messages(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=False),
            text=None,
            caption="photo caption",
            photo=[SimpleNamespace(file_id="f1", file_unique_id="u1", file_size=10, width=100, height=100)],
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )
        db_message = SimpleNamespace(id=88)
        bot = SimpleNamespace()

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock(return_value=SimpleNamespace(id=7))),
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "save_message", new=AsyncMock(return_value=db_message)) as save_message,
            patch.object(handlers.media, "process_message_media", new=AsyncMock()) as process_media,
        ):
            await handlers.group_message_handler(message, bot)

        save_message.assert_awaited_once()
        process_media.assert_awaited_once_with(bot, message, db_message)

    async def test_group_message_handler_ignores_link_only_text(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=False),
            text="https://vt.tiktok.com/ZSx5TH1DA/",
            entities=[SimpleNamespace(type="url", offset=0, length=31)],
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()) as user_get_or_create,
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()) as group_get_or_create,
            patch.object(handlers.core, "save_message", new=AsyncMock()) as save_message,
            patch.object(handlers.media, "process_message_media", new=AsyncMock()) as process_media,
        ):
            await handlers.group_message_handler(message, SimpleNamespace())

        user_get_or_create.assert_not_awaited()
        group_get_or_create.assert_not_awaited()
        save_message.assert_not_awaited()
        process_media.assert_not_awaited()

    async def test_group_message_handler_accepts_text_with_link(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=False),
            text="глянь https://example.com",
            entities=[SimpleNamespace(type="url", offset=6, length=19)],
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )
        db_message = SimpleNamespace(id=88)
        bot = SimpleNamespace()

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock(return_value=SimpleNamespace(id=7))),
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "save_message", new=AsyncMock(return_value=db_message)) as save_message,
            patch.object(handlers.media, "process_message_media", new=AsyncMock()) as process_media,
        ):
            await handlers.group_message_handler(message, bot)

        save_message.assert_awaited_once()
        process_media.assert_awaited_once_with(bot, message, db_message)

    async def test_group_message_handler_accepts_media_with_link_caption(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=False),
            text=None,
            caption="https://example.com",
            photo=[SimpleNamespace(file_id="f1", file_unique_id="u1", file_size=10, width=100, height=100)],
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )
        db_message = SimpleNamespace(id=88)
        bot = SimpleNamespace()

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock(return_value=SimpleNamespace(id=7))),
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "save_message", new=AsyncMock(return_value=db_message)) as save_message,
            patch.object(handlers.media, "process_message_media", new=AsyncMock()) as process_media,
        ):
            await handlers.group_message_handler(message, bot)

        save_message.assert_awaited_once()
        process_media.assert_awaited_once_with(bot, message, db_message)

    async def test_group_message_handler_ignores_bots(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=True),
            text="bot message",
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )

        with patch.object(handlers.core, "save_message", new=AsyncMock()) as save_message:
            await handlers.group_message_handler(message, SimpleNamespace())

        save_message.assert_not_awaited()

    async def test_edited_group_message_updates_existing_record(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=False),
            text="edited text",
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )

        with patch.object(handlers.core, "update_message", new=AsyncMock()) as update_message:
            await handlers.edited_group_message_handler(message)

        update_message.assert_awaited_once_with(message)

    async def test_edited_group_message_ignores_bot_edits(self) -> None:
        message = SimpleNamespace(
            from_user=SimpleNamespace(id=777, is_bot=True),
            text="bot edit",
            chat=SimpleNamespace(id=-100123456, type="supergroup"),
        )

        with patch.object(handlers.core, "update_message", new=AsyncMock()) as update_message:
            await handlers.edited_group_message_handler(message)

        update_message.assert_not_awaited()
