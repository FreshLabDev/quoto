import json
from datetime import datetime, timezone
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

import anthropic
import httpx
import httpx2

from app import agreement, ai, media

_MESSAGES = [
    {"id": 1, "author": "Alice", "text": "first"},
    {"id": 2, "author": "Bob", "text": "second"},
]
_DAY_ANSWER = json.dumps(
    {
        "day": {"should_publish": True, "reason_code": "worthy", "reason_text": "ok"},
        "quote": {"primary_id": 2, "context_ids": [2], "context_needed": False},
        "messages": [{"id": 2, "score": 8}, {"id": 1, "score": 3}],
    }
)


def _message(
    text: str,
    *,
    stop_reason: str = "end_turn",
    stop_details=None,
    input_tokens: int = 1000,
    output_tokens: int = 500,
    thinking_tokens: int = 200,
):
    return SimpleNamespace(
        id="msg_test",
        model="claude-sonnet-5-5",
        stop_reason=stop_reason,
        stop_details=stop_details,
        # With thinking on, the reply opens with a thinking block that is empty
        # under the default display; the answer is the text block after it.
        content=[
            SimpleNamespace(type="thinking", thinking=""),
            SimpleNamespace(type="text", text=text),
        ],
        usage=SimpleNamespace(
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            cache_creation_input_tokens=0,
            cache_read_input_tokens=0,
            output_tokens_details=SimpleNamespace(thinking_tokens=thinking_tokens),
        ),
    )


def _status_error(cls, status: int, error_type: str, message: str = "error"):
    request = httpx2.Request("POST", "https://api.anthropic.com/v1/messages")
    return cls(
        message,
        response=httpx2.Response(status, request=request),
        body={"type": "error", "error": {"type": error_type, "message": message}},
    )


class _FakeStream:
    def __init__(self, outcome) -> None:
        self._outcome = outcome

    async def __aenter__(self):
        if isinstance(self._outcome, BaseException):
            raise self._outcome
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def get_final_message(self):
        return self._outcome


class _FakeClaude:
    """Each call to messages.stream() takes the next outcome: a message or an error."""

    def __init__(self, *outcomes) -> None:
        self._outcomes = list(outcomes)
        self.bodies: list[dict] = []
        self.messages = SimpleNamespace(stream=self._stream)
        self.beta = SimpleNamespace(messages=self.messages)

    def _stream(self, **body):
        self.bodies.append(body)
        return _FakeStream(self._outcomes.pop(0))


class _FakeOpenRouterResponse:
    status_code = 200
    text = "{}"

    def __init__(self, content: str) -> None:
        self._content = content

    def raise_for_status(self) -> None:
        return None

    def json(self) -> dict:
        return {
            "model": "openai/gpt-6-luna",
            "choices": [{"message": {"content": self._content}}],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "cost": 0.0001},
        }


class _FakeOpenRouterClient:
    def __init__(self, content: str) -> None:
        self._content = content
        self.calls = 0

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def post(self, *_args, **_kwargs):
        self.calls += 1
        return _FakeOpenRouterResponse(self._content)


def _walk_keys(schema) -> set[str]:
    keys: set[str] = set()
    if isinstance(schema, dict):
        for key, value in schema.items():
            if key == "properties" and isinstance(value, dict):
                for sub in value.values():
                    keys |= _walk_keys(sub)
            else:
                keys.add(key)
                keys |= _walk_keys(value)
    elif isinstance(schema, list):
        for item in schema:
            keys |= _walk_keys(item)
    return keys


class ClaudeSchemaTests(unittest.TestCase):
    def test_schema_drops_only_keywords_claude_rejects(self) -> None:
        cleaned = ai._claude_schema(ai._DAY_RESPONSE_SCHEMA_WITH_LANGUAGE)
        self.assertFalse(_walk_keys(cleaned) & ai._CLAUDE_UNSUPPORTED_SCHEMA_KEYS)
        self.assertEqual(
            cleaned["properties"]["language"]["properties"]["interface_language"]["enum"],
            ai._LANGUAGE_RESPONSE_SCHEMA["properties"]["interface_language"]["enum"],
        )
        self.assertEqual(cleaned["required"], ["day", "quote", "messages", "language"])
        self.assertFalse(cleaned["additionalProperties"])
        # The original stays intact for OpenRouter.
        self.assertEqual(ai._SCORE_RESPONSE_SCHEMA["properties"]["messages"]["maxItems"], 10)

    def test_property_named_like_a_keyword_survives(self) -> None:
        schema = {"type": "object", "properties": {"maximum": {"type": "integer", "maximum": 3}}}
        self.assertEqual(
            ai._claude_schema(schema),
            {"type": "object", "properties": {"maximum": {"type": "integer"}}},
        )

    def test_redact_hides_anthropic_keys(self) -> None:
        with patch.object(ai.settings, "ANTHROPIC_API_KEY", "sk-ant-api03-secret"):
            text = ai._redact("key sk-ant-api03-secret and sk-ant-other-key leaked")
        self.assertNotIn("secret", text)
        self.assertNotIn("other-key", text)

    def test_cache_reads_are_priced_per_model(self) -> None:
        usage = SimpleNamespace(
            input_tokens=0,
            output_tokens=0,
            cache_creation_input_tokens=0,
            cache_read_input_tokens=1_000_000,
            output_tokens_details=None,
        )
        # Sonnet 5.5: $2 × 0.1. Opus 5.5: $4 × 0.05.
        self.assertAlmostEqual(ai._claude_usage("claude-sonnet-5-5", usage).cost_usd, 0.2)
        self.assertAlmostEqual(ai._claude_usage("claude-opus-5-5", usage).cost_usd, 0.2)
        self.assertIsNone(ai._claude_usage("claude-unknown", usage).cost_usd)



class ClaudeEvaluationTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        ai._breaker_record_success()
        self._logs = tempfile.TemporaryDirectory()
        self._patches = [
            patch.object(ai.settings, "LOGS_PATH", self._logs.name),
            patch.object(ai.settings, "ANTHROPIC_API_KEY", "sk-ant-test"),
            patch.object(ai.settings, "ANTHROPIC_EVAL_MODEL", "claude-sonnet-5-5"),
            patch.object(ai.settings, "ANTHROPIC_EVAL_EFFORT", "medium"),
            patch.object(ai.asyncio, "sleep", new=AsyncMock()),
        ]
        for item in self._patches:
            item.start()

    def tearDown(self) -> None:
        for item in reversed(self._patches):
            item.stop()
        self._logs.cleanup()
        ai._breaker_record_success()

    def _audit(self) -> list[dict]:
        path = Path(self._logs.name) / "ai_audit.jsonl"
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]

    async def test_claude_scores_the_day_with_structured_output(self) -> None:
        claude = _FakeClaude(_message(_DAY_ANSWER))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "parsed")
        self.assertEqual(result.actual_model, "claude-sonnet-5-5")
        self.assertEqual(result.quote_choice.primary_id, 2)
        self.assertTrue(result.day_verdict.should_publish)
        self.assertAlmostEqual(result.scores[2], 0.8)

        body = claude.bodies[0]
        self.assertEqual(body["model"], "claude-sonnet-5-5")
        self.assertEqual(body["thinking"], {"type": "adaptive"})
        self.assertEqual(body["output_config"]["effort"], "medium")
        self.assertEqual(body["output_config"]["format"]["type"], "json_schema")
        self.assertFalse(
            _walk_keys(body["output_config"]["format"]["schema"]) & ai._CLAUDE_UNSUPPORTED_SCHEMA_KEYS
        )
        self.assertIn("quote curator", body["system"])
        self.assertEqual(body["fallbacks"], "default")
        self.assertEqual(body["betas"], [ai._CLAUDE_FALLBACK_BETA])
        self.assertEqual(body["messages"][0]["role"], "user")
        self.assertEqual(json.loads(body["messages"][0]["content"])[1]["t"], "second")

        # 1000 in × $2 + 500 out × $10, per million tokens.
        self.assertAlmostEqual(result.usage.cost_usd, 0.007)
        self.assertEqual(result.usage.prompt_tokens, 1000)
        self.assertEqual(result.usage.completion_tokens, 500)
        self.assertEqual(result.usage.reasoning_tokens, 200)

        [record] = self._audit()
        self.assertEqual(record["provider"], "anthropic")
        self.assertEqual(record["result"]["status"], "parsed")
        self.assertEqual(record["response"]["stop_reason"], "end_turn")

    async def test_server_side_fallback_answer_is_used_and_every_attempt_is_billed(self) -> None:
        # Sonnet 5.5 declined before any output, Sonnet 5 answered in the same call.
        answered = _message(_DAY_ANSWER)
        answered.model = "claude-sonnet-5"
        answered.content.insert(
            0,
            SimpleNamespace(
                type="fallback",
                from_=SimpleNamespace(model="claude-sonnet-5-5"),
                to=SimpleNamespace(model="claude-sonnet-5"),
            ),
        )
        answered.usage.iterations = [
            SimpleNamespace(
                type="message",
                model="claude-sonnet-5-5",
                input_tokens=1000,
                output_tokens=0,
                cache_creation_input_tokens=0,
                cache_read_input_tokens=0,
            ),
            SimpleNamespace(
                type="fallback_message",
                model="claude-sonnet-5",
                input_tokens=1000,
                output_tokens=500,
                cache_creation_input_tokens=0,
                cache_read_input_tokens=0,
            ),
        ]
        claude = _FakeClaude(answered)
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client", return_value=claude),
            patch.object(ai.httpx, "AsyncClient") as openrouter,
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "parsed")
        self.assertEqual(result.actual_model, "claude-sonnet-5")
        self.assertEqual(result.quote_choice.primary_id, 2)
        openrouter.assert_not_called()
        # Declined attempt: 1000 in × $2. Answer: 1000 in × $2 + 500 out × $10.
        self.assertAlmostEqual(result.usage.cost_usd, 0.009)
        [record] = self._audit()
        self.assertEqual(record["response"]["model"], "claude-sonnet-5")

    async def test_refusal_falls_back_to_openrouter_without_retrying_claude(self) -> None:
        refusal = _message(
            "",
            stop_reason="refusal",
            stop_details=SimpleNamespace(category="general_harms", explanation="declined"),
        )
        claude = _FakeClaude(refusal)
        openrouter = _FakeOpenRouterClient(_DAY_ANSWER)
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client", return_value=claude),
            patch.object(ai.httpx, "AsyncClient", return_value=openrouter),
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "parsed")
        self.assertEqual(result.actual_model, "openai/gpt-6-luna")
        self.assertEqual(len(claude.bodies), 1)
        self.assertEqual(openrouter.calls, 1)
        records = self._audit()
        self.assertEqual(records[0]["result"], {
            "status": "refused",
            "actual_model": "claude-sonnet-5-5",
            "category": "general_harms",
        })
        self.assertEqual(records[1]["provider"], "openrouter")

    async def test_empty_balance_falls_back_without_retrying_claude(self) -> None:
        claude = _FakeClaude(_status_error(anthropic.APIStatusError, 402, "billing_error"))
        openrouter = _FakeOpenRouterClient(_DAY_ANSWER)
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client", return_value=claude),
            patch.object(ai.httpx, "AsyncClient", return_value=openrouter),
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "parsed")
        self.assertEqual(len(claude.bodies), 1)
        self.assertEqual(self._audit()[0]["error"]["status_code"], 402)

    async def test_overload_mid_stream_is_retried(self) -> None:
        # An error event mid-stream arrives on a 200 response; its type decides.
        overloaded = _status_error(anthropic.APIStatusError, 200, "overloaded_error")
        claude = _FakeClaude(overloaded, _message(_DAY_ANSWER))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "parsed")
        self.assertEqual(len(claude.bodies), 2)

    async def test_refusal_without_fallback_fails_the_day(self) -> None:
        claude = _FakeClaude(_message("", stop_reason="refusal"))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "ai_failed")
        self.assertIsNone(result.day_verdict)
        self.assertIn("declined", result.day_verdict_error)

    async def test_no_provider_key_fails_without_a_request(self) -> None:
        with (
            patch.object(ai.settings, "ANTHROPIC_API_KEY", ""),
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client") as client,
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "ai_failed")
        client.assert_not_called()

    async def test_empty_model_skips_claude(self) -> None:
        openrouter = _FakeOpenRouterClient(_DAY_ANSWER)
        with (
            patch.object(ai.settings, "ANTHROPIC_EVAL_MODEL", ""),
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client") as client,
            patch.object(ai.httpx, "AsyncClient", return_value=openrouter),
        ):
            result = await ai.evaluate_messages(_MESSAGES, include_day_verdict=True)

        self.assertEqual(result.status, "parsed")
        client.assert_not_called()


class ClaudeMediaTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self) -> None:
        ai._media_breaker_reset()
        self._dir = tempfile.TemporaryDirectory()
        self.image = Path(self._dir.name) / "normalized.jpg"
        self.image.write_bytes(b"\xff\xd8\xff fake jpeg")
        self._patches = [
            patch.object(ai.settings, "ANTHROPIC_API_KEY", "sk-ant-test"),
            patch.object(ai.settings, "ANTHROPIC_MEDIA_MODEL", "claude-sonnet-5-5"),
            patch.object(ai.settings, "ANTHROPIC_MEDIA_EFFORT", "medium"),
        ]
        for item in self._patches:
            item.start()

    def tearDown(self) -> None:
        for item in reversed(self._patches):
            item.stop()
        self._dir.cleanup()
        ai._media_breaker_reset()

    async def test_image_is_described_by_claude(self) -> None:
        claude = _FakeClaude(_message("Кот сидит на подоконнике."))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client", return_value=claude),
            patch.object(ai, "_post_media_request", new=AsyncMock()) as openrouter,
        ):
            result = await ai.describe_media_file(
                path=self.image, mime_type="image/jpeg", media_kind="photo"
            )

        self.assertEqual(result.description, "Кот сидит на подоконнике.")
        self.assertEqual(result.actual_model, "claude-sonnet-5-5")
        openrouter.assert_not_awaited()
        content = claude.bodies[0]["messages"][0]["content"]
        self.assertEqual(content[0]["type"], "image")
        self.assertEqual(content[0]["source"]["media_type"], "image/jpeg")
        self.assertEqual(content[0]["source"]["type"], "base64")
        self.assertEqual(content[1], {"type": "text", "text": ai._IMAGE_DESCRIPTION_PROMPT})
        self.assertEqual(claude.bodies[0]["output_config"], {"effort": "medium"})
        self.assertEqual(claude.bodies[0]["fallbacks"], "default")
        self.assertEqual(claude.bodies[0]["betas"], [ai._CLAUDE_FALLBACK_BETA])

    async def test_sticker_uses_the_sticker_prompt(self) -> None:
        claude = _FakeClaude(_message("Грустный кот."))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            await ai.describe_media_file(path=self.image, mime_type="image/jpeg", media_kind="sticker")
        self.assertEqual(claude.bodies[0]["messages"][0]["content"][1]["text"], ai._STICKER_DESCRIPTION_PROMPT)

    async def test_claude_failure_falls_back_to_openrouter(self) -> None:
        claude = _FakeClaude(_status_error(anthropic.InternalServerError, 500, "api_error"))
        response = SimpleNamespace(
            json=lambda: {"model": "google/gemini-2.5-flash-lite", "choices": [{"message": {"content": "Кот."}}]}
        )
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client", return_value=claude),
            patch.object(ai, "_post_media_request", new=AsyncMock(return_value=response)) as openrouter,
        ):
            result = await ai.describe_media_file(
                path=self.image, mime_type="image/jpeg", media_kind="photo"
            )

        self.assertEqual(result.actual_model, "google/gemini-2.5-flash-lite")
        openrouter.assert_awaited_once()

    async def test_video_never_goes_to_claude(self) -> None:
        response = SimpleNamespace(
            json=lambda: {"model": "google/gemini-2.5-flash-lite", "choices": [{"message": {"content": "Видео."}}]}
        )
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client") as client,
            patch.object(ai, "_post_media_request", new=AsyncMock(return_value=response)),
        ):
            await ai.describe_media_file(path=self.image, mime_type="video/mp4", media_kind="video")
        client.assert_not_called()

    async def test_group_without_consent_skips_claude(self) -> None:
        response = SimpleNamespace(
            json=lambda: {"model": "google/gemini-2.5-flash-lite", "choices": [{"message": {"content": "Кот."}}]}
        )
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client") as client,
            patch.object(ai, "_post_media_request", new=AsyncMock(return_value=response)),
        ):
            await ai.describe_media_file(
                path=self.image, mime_type="image/jpeg", media_kind="photo", allow_claude=False
            )
        client.assert_not_called()

    async def test_openrouter_cooldown_does_not_block_claude(self) -> None:
        ai._media_breaker_open()
        claude = _FakeClaude(_message("Кот."))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            result = await ai.describe_media_file(
                path=self.image, mime_type="image/jpeg", media_kind="photo"
            )
        self.assertEqual(result.description, "Кот.")

    async def test_empty_balance_without_fallback_waits_for_retry(self) -> None:
        claude = _FakeClaude(_status_error(anthropic.APIStatusError, 402, "billing_error"))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            with self.assertRaises(ai.MediaModelsExhaustedError) as caught:
                await ai.describe_media_file(
                    path=self.image, mime_type="image/jpeg", media_kind="photo"
                )
        self.assertTrue(ai.is_retryable_media_error(caught.exception))

    async def test_refused_image_without_fallback_is_retryable_response_error(self) -> None:
        claude = _FakeClaude(_message("", stop_reason="refusal"))
        with (
            patch.object(ai.settings, "OPENROUTER_API_KEY", ""),
            patch.object(ai, "_anthropic_client", return_value=claude),
        ):
            with self.assertRaises(ai.MediaModelsExhaustedError) as caught:
                await ai.describe_media_file(
                    path=self.image, mime_type="image/jpeg", media_kind="photo"
                )
        self.assertIn("declined", str(caught.exception))

    def test_connection_errors_are_retryable(self) -> None:
        request = httpx2.Request("POST", "https://api.anthropic.com/v1/messages")
        self.assertTrue(ai.is_retryable_media_error(anthropic.APIConnectionError(request=request)))
        self.assertFalse(
            ai.is_retryable_media_error(_status_error(anthropic.BadRequestError, 400, "invalid_request_error"))
        )
        # The OpenRouter path keeps its own classification.
        self.assertTrue(ai.is_retryable_media_error(httpx.ConnectError("down")))


class _AgreementSession:
    def __init__(self, row) -> None:
        self._row = row

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def execute(self, _stmt):
        return SimpleNamespace(first=lambda: self._row)


class MediaConsentTests(unittest.IsolatedAsyncioTestCase):
    async def test_claude_needs_the_current_agreement(self) -> None:
        accepted = datetime(2026, 10, 18, tzinfo=timezone.utc)
        cases = [
            ((accepted, agreement.DOCUMENT_VERSION), True),
            ((accepted, None), False),
            ((accepted, "1.1"), False),
            ((None, None), False),
            (None, False),
        ]
        for row, expected in cases:
            with self.subTest(row=row):
                with patch.object(media, "SessionLocal", return_value=_AgreementSession(row)):
                    self.assertIs(await media._group_allows_claude(1), expected)


if __name__ == "__main__":
    unittest.main()
