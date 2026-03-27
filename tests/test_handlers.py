from datetime import datetime, timezone
import os
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import handlers, scoring
from app.quote_status import STATUS_PUBLISH_FAILED


class DummyResponse:
    def __init__(self) -> None:
        self.edits: list[str] = []
        self.deleted = False

    async def edit_text(self, text: str) -> None:
        self.edits.append(text)

    async def delete(self) -> None:
        self.deleted = True


class DummyMessage:
    def __init__(self, chat_type: str = "supergroup") -> None:
        self.chat = SimpleNamespace(id=-100123456, type=chat_type, title="Quoto Test Chat")
        self.from_user = SimpleNamespace(id=777, is_bot=False)
        self.answers: list[str] = []
        self.responses: list[DummyResponse] = []

    async def answer(self, text: str, reply_markup=None):
        self.answers.append(text)
        response = DummyResponse()
        self.responses.append(response)
        return response


class HandlerTests(unittest.IsolatedAsyncioTestCase):
    async def test_quote_preview_requires_admin(self) -> None:
        message = DummyMessage()

        with (
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=False)),
        ):
            await handlers.manual_quote_handler(message, SimpleNamespace())

        self.assertEqual(
            message.answers,
            ["🔒 Команда /quote с AI-preview доступна только администраторам чата."],
        )

    async def test_quote_preview_works_for_admin(self) -> None:
        message = DummyMessage()
        best_message = SimpleNamespace(
            text="Лучший тестовый панчлайн",
            author=SimpleNamespace(name="Alice"),
        )
        evaluation = scoring.QuoteEvaluation(
            best_message=best_message,
            breakdown=scoring.ScoreBreakdown(reaction=0.2, ai=0.7, length=0.1),
            message_count=12,
        )

        with (
            patch.object(handlers.core, "group_getOrCreate", new=AsyncMock()),
            patch.object(handlers, "_is_chat_admin", new=AsyncMock(return_value=True)),
            patch.object(handlers.scoring, "pick_best_quote", new=AsyncMock(return_value=evaluation)),
        ):
            await handlers.manual_quote_handler(message, SimpleNamespace())

        self.assertIn("Preview текущего окна", message.answers[0])
        self.assertIn("Лучший тестовый панчлайн", message.answers[0])
        self.assertIn("Alice", message.answers[0])

    async def test_private_quote_details_show_reason_and_operation_error(self) -> None:
        message = DummyMessage(chat_type="private")
        detail = {
            "id": 7,
            "text": "Цитата",
            "score": 0.8,
            "reaction_score": 0.2,
            "ai_score": 0.5,
            "length_score": 0.1,
            "reaction_count": 3,
            "author_name": "Alice",
            "group_name": "Quoto Test Chat",
            "created_at": datetime(2026, 3, 27, 21, 0, tzinfo=timezone.utc),
            "ai_model": "openrouter/test",
            "ai_best_text": None,
            "message_id": 10,
            "chat_id": -100123456,
            "decision_status": STATUS_PUBLISH_FAILED,
            "decision_reason": "LLM rejected auto publication",
            "operation_error": "Telegram timeout",
            "forced_by_admin": False,
            "quote_day": None,
        }

        with (
            patch.object(handlers.core, "user_getOrCreate", new=AsyncMock()),
            patch.object(handlers.core, "get_quote_detail", new=AsyncMock(return_value=detail)),
        ):
            await handlers.private_handler(message, SimpleNamespace(args="quote_7"))

        self.assertIn("Причина решения", message.answers[0])
        self.assertIn("LLM rejected auto publication", message.answers[0])
        self.assertIn("Техническая ошибка", message.answers[0])
        self.assertIn("Telegram timeout", message.answers[0])
