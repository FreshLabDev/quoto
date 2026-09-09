from datetime import datetime, timezone
import os
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import handlers
from app.quote_status import STATUS_PUBLISHED, STATUS_PUBLISH_FAILED


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
    def setUp(self) -> None:
        handlers._MENU_CALLBACK_LAST_SEEN.clear()

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
            "decision_status": STATUS_PUBLISHED,
            "decision_reason": "LLM rejected <auto> publication",
            "operation_error": "Telegram timeout & retry",
            "quote_day": None,
            "language_code": "uk",
        }

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "get_quote_detail", new=AsyncMock(return_value=detail)),
        ):
            await handlers.private_handler(
                message,
                SimpleNamespace(args=handlers.utils.quote_start_payload(7)),
            )

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
            "decision_status": STATUS_PUBLISHED,
            "decision_reason": "LLM rejected",
            "operation_error": None,
            "quote_day": None,
            "language_code": "uk",
        }

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "get_quote_detail", new=AsyncMock(return_value=detail)),
        ):
            await handlers.private_handler(
                message,
                SimpleNamespace(args=handlers.utils.quote_start_payload(7)),
            )

        self.assertIn("Decision reason", message.answers[0])
        self.assertNotIn("Причина решения", message.answers[0])
        self.assertNotIn("Причина рішення", message.answers[0])

    async def test_private_falls_back_to_english_for_unknown_telegram_language(self) -> None:
        message = DummyMessage(chat_type="private", language_code="ko")

        with patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()):
            await handlers.private_handler(message, SimpleNamespace(args=None))

        self.assertIn("<b>Quoto</b>", message.answers[0])
        self.assertIn("Add me to a group", message.answers[0])

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

        self.assertIn("<b>Quoto</b>", message.answers[0])
        labels = [
            button.text
            for row in message.answer_markups[0].inline_keyboard
            for button in row
        ]
        self.assertEqual(
            labels,
            [
                "Статистика",
                "Язык",
                "Цитата дня",
                "Публикация",
                "Соглашение",
                "О боте",
                "Закрыть",
            ],
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
                new=AsyncMock(return_value=SimpleNamespace(chat_id=-100123456, language_code="ru", language_source=None)),
            ),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
            patch.object(handlers.core, "set_group_language_manual", new=AsyncMock()) as set_language,
            patch.object(
                handlers.core,
                "get_group_by_chat_id",
                new=AsyncMock(
                    return_value=SimpleNamespace(
                        chat_id=-100123456,
                        language_code="uk",
                        language_source=handlers.i18n.LANGUAGE_SOURCE_MANUAL,
                    )
                ),
            ),
        ):
            await handlers.start_menu_callback(callback, SimpleNamespace())

        set_language.assert_awaited_once_with(-100123456, "uk")
        self.assertIn("Мова групи", panel.edits[0])
        callback.answer.assert_awaited()

    async def test_group_schedule_section_shows_time_and_min_controls(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:sched",
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

        self.assertIn("Цитата дня", panel.edits[0])
        labels = [
            button.text
            for row in panel.edit_markups[0].inline_keyboard
            for button in row
        ]
        self.assertIn("+1:00", labels)
        self.assertIn("+5", labels)
        self.assertEqual(labels[-2:], ["Назад", "Закрыть"])

    async def test_the_schedule_nudges_read_the_same_in_every_language(self) -> None:
        # These used to spell their units in Russian, on every locale's panel.
        for language in handlers.i18n.SUPPORTED_LANGUAGES:
            _, keyboard = handlers.menu.build_group_panel(
                owner_id=777,
                language=language,
                group_language=language,
                group_language_source=None,
                is_admin=True,
                quote_time="21:00",
                min_messages=10,
                timezone_name="Europe/Kyiv",
                boring_notice_enabled=True,
                pin_enabled=True,
                quote_context_enabled=True,
                section=handlers.menu.SECTION_SCHEDULE,
            )
            nudges = [button.text for row in keyboard.inline_keyboard[:3] for button in row]
            self.assertEqual(
                nudges,
                ["−1:00", "+1:00", "−0:15", "+0:15", "−5", "+5", "−1", "+1"],
                language,
            )

    async def test_group_publication_settings_explains_quote_context(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:behavior",
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

        self.assertIn("Публикация", panel.edits[0])
        self.assertIn("сообщения, которые ИИ выбрал", panel.edits[0])
        labels = [
            button.text
            for row in panel.edit_markups[0].inline_keyboard
            for button in row
        ]
        self.assertIn("◉ Контекст", labels)

    async def test_group_stats_panel_switches_between_user_and_chat_stats(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:userstats",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )
        user_stats = {
            "user_name": "Alice",
            "wins": 2,
            "avg_score": 0.8,
            "rank": 1,
            "total_participants": 5,
            "best_quote": None,
        }

        with (
            patch.object(
                handlers.core,
                "group_getOrCreate",
                new=AsyncMock(return_value=SimpleNamespace(id=1, language_code="ru", language_source=None)),
            ),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=False)),
            patch.object(handlers.core, "get_user_stats", new=AsyncMock(return_value=user_stats)),
        ):
            await handlers.start_menu_callback(callback, SimpleNamespace())

        self.assertIn("Твоя статистика", panel.edits[0])
        labels = [
            button.text
            for row in panel.edit_markups[0].inline_keyboard
            for button in row
        ]
        self.assertEqual(labels, ["◉ Моя статистика", "◎ Статистика чата", "Назад", "Закрыть"])

    async def test_agreement_tab_uses_rich_markdown_when_the_server_has_it(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:doc",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )
        group = SimpleNamespace(
            id=1, language_code="ru", language_source=None, agreement_accepted_at=None
        )
        bot = AsyncMock()

        with (
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock(return_value=group)),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
            patch.object(handlers.richmd, "available", return_value=True),
            patch.object(handlers.richmd, "edit_markdown", new=AsyncMock()) as edit_markdown,
        ):
            await handlers.start_menu_callback(callback, bot)

        edit_markdown.assert_awaited_once()
        markdown = edit_markdown.await_args.args[3]
        self.assertTrue(markdown.startswith("# "), markdown[:40])
        self.assertIn("@amtiyo", markdown)
        # The panel itself was never edited with HTML: the rich path replaced it.
        self.assertEqual(panel.edits, [])

    async def test_agreement_tab_falls_back_to_html_without_the_capability(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:doc",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )
        group = SimpleNamespace(
            id=1, language_code="ru", language_source=None, agreement_accepted_at=None
        )

        with (
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock(return_value=group)),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
            patch.object(handlers.richmd, "available", return_value=False),
        ):
            await handlers.start_menu_callback(callback, AsyncMock())

        self.assertIn("<blockquote expandable>", panel.edits[0])
        self.assertIn("@amtiyo", panel.edits[0])
        labels = [b.text for row in panel.edit_markups[0].inline_keyboard for b in row]
        self.assertEqual(labels[-2:], ["Назад", "Закрыть"])
        self.assertIn(handlers.i18n.t("ru", "agreement.accept_button"), labels)

    async def test_about_tab_shows_the_version_card(self) -> None:
        panel = DummyResponse(chat=SimpleNamespace(id=777, type="private", title=None), message_id=902)
        callback = SimpleNamespace(
            data="menu:777:p:about",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(
                handlers.core, "user_language_state", new=AsyncMock(return_value=("ru", None))
            ),
        ):
            await handlers.start_menu_callback(callback, AsyncMock())

        self.assertEqual(panel.edits[0], handlers.menu.about_text("ru"))
        labels = [b.text for row in panel.edit_markups[0].inline_keyboard for b in row]
        self.assertEqual(labels, ["Назад"])

    async def test_agreement_language_switch_keeps_the_panel_it_came_from(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data=handlers.agreement.callback_data(
                handlers.agreement.ACTION_VIEW, "de", scope=handlers.menu.SCOPE_GROUP, owner_id=777
            ),
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )
        group = SimpleNamespace(
            id=1, language_code="ru", language_source=None, agreement_accepted_at=None
        )

        with (
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock(return_value=group)),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=False)),
            patch.object(handlers.richmd, "available", return_value=False),
        ):
            await handlers.agreement_callback(callback, AsyncMock())

        self.assertIn(handlers.i18n.t("de", "agreement.signature.title"), panel.edits[0])
        data = [b.callback_data for row in panel.edit_markups[0].inline_keyboard for b in row]
        self.assertIn(handlers.menu.callback_data(777, handlers.menu.SCOPE_GROUP, "home"), data)

    async def test_agreement_tab_belongs_to_whoever_opened_the_panel(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data=handlers.agreement.callback_data(
                handlers.agreement.ACTION_VIEW, "ru", scope=handlers.menu.SCOPE_GROUP, owner_id=777
            ),
            from_user=SimpleNamespace(id=999, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )

        await handlers.agreement_callback(callback, AsyncMock())

        self.assertEqual(panel.edits, [])
        callback.answer.assert_awaited_once()

    async def test_menu_callback_throttle_drops_fast_repeated_panel_clicks(self) -> None:
        panel = DummyResponse(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(
            data="menu:777:g:userstats",
            from_user=SimpleNamespace(id=777, language_code="ru"),
            message=panel,
            answer=AsyncMock(),
        )
        user_stats = {
            "user_name": "Alice",
            "wins": 2,
            "avg_score": 0.8,
            "rank": 1,
            "total_participants": 5,
            "best_quote": None,
        }

        with (
            patch.object(
                handlers.core,
                "group_getOrCreate",
                new=AsyncMock(return_value=SimpleNamespace(id=1, language_code="ru", language_source=None)),
            ),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=False)),
            patch.object(handlers.core, "get_user_stats", new=AsyncMock(return_value=user_stats)) as get_user_stats,
        ):
            await handlers.start_menu_callback(callback, SimpleNamespace())
            await handlers.start_menu_callback(callback, SimpleNamespace())

        get_user_stats.assert_awaited_once()
        self.assertEqual(len(panel.edits), 1)
        self.assertEqual(callback.answer.await_count, 2)

    async def test_edit_panel_does_not_send_fallback_message_when_flood_limited(self) -> None:
        class FloodLimitedPanel(DummyResponse):
            async def edit_text(self, text: str, reply_markup=None) -> None:
                raise handlers.TelegramRetryAfter(
                    method=SimpleNamespace(),
                    message="Too Many Requests",
                    retry_after=32,
                )

        panel = FloodLimitedPanel(
            chat=SimpleNamespace(id=-100123456, type="supergroup", title="Quoto Test Chat"),
            message_id=902,
        )
        callback = SimpleNamespace(message=panel, answer=AsyncMock())

        edited = await handlers._edit_panel(callback, "text", None)

        self.assertFalse(edited)
        callback.answer.assert_not_awaited()

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
            "decision_status": STATUS_PUBLISHED,
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
            await handlers.private_handler(
                message,
                SimpleNamespace(args=handlers.utils.quote_start_payload(8)),
            )

        self.assertIn("<b>Alice &lt;A&gt;:</b> setup &amp; context", message.answers[0])
        self.assertIn("<b>Bob:</b> <i>«punch &lt;line&gt;»</i>", message.answers[0])

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


class CatchUpAfterAcceptTests(unittest.IsolatedAsyncioTestCase):
    async def test_catch_up_processes_current_closed_window(self) -> None:
        from app import scheduler

        group = SimpleNamespace(chat_id=-100, timezone="Europe/Kyiv")
        with patch.object(scheduler, "_process_group", new=AsyncMock()) as process_group:
            await handlers._catch_up_after_accept(SimpleNamespace(), group)

        process_group.assert_awaited_once()
        window = process_group.await_args.args[2]
        self.assertTrue(hasattr(window, "quote_day"))

    async def test_catch_up_swallows_errors(self) -> None:
        from app import scheduler

        group = SimpleNamespace(chat_id=-100, timezone="Europe/Kyiv")
        with (
            patch.object(scheduler, "_process_group", new=AsyncMock(side_effect=RuntimeError("boom"))),
            patch.object(handlers.utils, "notify_developers", new=AsyncMock()),
        ):
            # must not raise
            await handlers._catch_up_after_accept(SimpleNamespace(), group)


class GroupLifecycleTests(unittest.IsolatedAsyncioTestCase):
    async def test_bot_removal_suspends_group(self) -> None:
        event = SimpleNamespace(
            chat=SimpleNamespace(id=-100123456, title="Old chat"),
            from_user=SimpleNamespace(id=42, language_code="en"),
            old_chat_member=SimpleNamespace(status="administrator"),
            new_chat_member=SimpleNamespace(status="kicked"),
            answer=AsyncMock(),
        )
        with patch.object(
            handlers.core, "set_group_active", new=AsyncMock(return_value=True)
        ) as set_active:
            await handlers.bot_added_to_chat_event(event)
        set_active.assert_awaited_once_with(-100123456, False)
        event.answer.assert_not_awaited()


class StatsPanelShapeTests(unittest.TestCase):
    """The stats tabs are screens like any other, so they are built by the
    same panel helper: title, hint, and one blockquote — never a second
    blockquote nested inside the first."""

    CHAT_STATS = {
        "total_quotes": 12,
        "unique_authors": 4,
        "avg_score": 0.82,
        "top_authors": [
            {"name": "Alice", "wins": 5, "avg_score": 0.9},
            {"name": "Bob", "wins": 4, "avg_score": 0.8},
        ],
        "best_quote": {"text": "a line", "author": "Alice", "score": 0.95},
    }
    USER_STATS = {
        "user_name": "Alice",
        "wins": 3,
        "avg_score": 0.7,
        "rank": 2,
        "total_participants": 9,
        "best_quote": {"text": "a line", "score": 0.9},
    }

    def _assert_panel(self, text: str) -> None:
        head, _, body = text.partition("\n\n")
        lines = head.split("\n")
        self.assertEqual(len(lines), 2, text)
        self.assertTrue(lines[0].startswith("<b>") and lines[0].endswith("</b>"), lines[0])
        self.assertTrue(lines[1].startswith("<i>") and lines[1].endswith("</i>"), lines[1])
        self.assertEqual(body.count("<blockquote>"), 1, text)
        self.assertTrue(body.startswith("<blockquote>"), body)
        self.assertTrue(body.endswith("</blockquote>"), body)

    def test_every_state_of_both_tabs_is_a_panel(self) -> None:
        for language in handlers.i18n.SUPPORTED_LANGUAGES:
            for stats in (None, {"total_quotes": 0}, self.CHAT_STATS):
                self._assert_panel(handlers._format_chat_stats_text(language, stats))
            for stats in (None, {"wins": 0, "user_name": "Alice"}, self.USER_STATS):
                self._assert_panel(handlers._format_user_stats_text(language, stats))


class TelegramLanguageButtonTests(unittest.IsolatedAsyncioTestCase):
    """Quoto is the family's reference for "Telegram language": the button
    withdraws this bot's own claim and then asks core what answers instead.

    Withdrawing is not the same as choosing the client hint. clear_language
    removes quoto's observation and nobody else's, so a sibling bot's manual
    choice survives it and keeps winning. The other bots copy these semantics,
    so both outcomes are pinned here rather than left to the handler's shape."""

    def _callback(self, panel, language_code: str = "de"):
        return SimpleNamespace(
            data=f"menu:777:p:{handlers.menu.ACTION_AUTO_PRIVATE_LANGUAGE}",
            from_user=SimpleNamespace(id=777, language_code=language_code),
            message=panel,
            answer=AsyncMock(),
        )

    async def _press(self, before, after, *, message_id: int):
        """Press the button with core answering `before`, then `after`.

        Each case needs its own message id: the panel registry is keyed by one,
        and a second press on the same id is treated as a stale panel.
        """
        panel = DummyResponse(chat=SimpleNamespace(id=777, type="private", title=None), message_id=message_id)
        callback = self._callback(panel)
        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(
                handlers.core, "user_language_state", new=AsyncMock(side_effect=[before, after])
            ),
            patch.object(
                handlers.core, "clear_user_language", new=AsyncMock(return_value=True)
            ) as clear,
        ):
            await handlers.start_menu_callback(callback, AsyncMock())
        clear.assert_awaited_once_with(777)
        return panel.edits[0]

    async def test_with_nothing_left_the_client_hint_decides_again(self) -> None:
        # Quoto held the only claim, so withdrawing it leaves the German client.
        edit = await self._press(("ru", "manual"), ("de", None), message_id=902)
        self.assertIn(handlers.i18n.t("de", "settings.private.language_title"), edit)
        self.assertIn(handlers.i18n.t("de", "settings.private.language_source_telegram"), edit)

    async def test_a_sibling_s_surviving_choice_wins_over_the_client_hint(self) -> None:
        # Another bot still holds Russian by hand. The screen must not claim the
        # German client won: the very next update would replace it.
        edit = await self._press(("ru", "manual"), ("ru", "manual"), message_id=903)
        self.assertIn(handlers.i18n.t("ru", "settings.private.language_title"), edit)
        self.assertIn(handlers.i18n.t("ru", "settings.private.language_source_manual"), edit)

    async def test_it_sits_under_the_language_grid_and_over_the_navigation(self) -> None:
        _, keyboard = handlers.menu.build_private_panel(
            owner_id=777,
            language="en",
            language_source=None,
            bot_username="quoto_test_bot",
            section=handlers.menu.SECTION_LANGUAGE,
        )
        rows = keyboard.inline_keyboard
        grid = (len(handlers.i18n.language_options()) + 1) // 2  # two languages to a row
        self.assertEqual(
            [button.text for button in rows[grid]],
            [handlers.i18n.t("en", "settings.private.telegram_language")],
        )
        self.assertEqual(
            [button.text for button in rows[grid + 1]], [handlers.i18n.t("en", "menu.button.back")]
        )
        # It performs an action, so it is never painted as a state.
        self.assertIsNone(rows[grid][0].style)
