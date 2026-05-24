import json
import os
from pathlib import Path
from types import SimpleNamespace
import subprocess
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import ai, media


class _DummyScalars:
    def __init__(self, values: list[object]) -> None:
        self.values = values

    def first(self):
        return self.values[0] if self.values else None

    def all(self):
        return self.values


class _DummyResult:
    def __init__(self, values: list[object]) -> None:
        self.values = values

    def scalars(self):
        return _DummyScalars(self.values)


class _DummySession:
    def __init__(self, results: list[list[object]]) -> None:
        self.results = results
        self.calls = 0
        self.commit = AsyncMock()
        self.refresh = AsyncMock()
        self.rollback = AsyncMock()
        self.added: list[object] = []

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def execute(self, _stmt):
        result = self.results[self.calls] if self.calls < len(self.results) else []
        self.calls += 1
        return _DummyResult(result)

    def add(self, value: object) -> None:
        self.added.append(value)


class MediaSourceTests(unittest.TestCase):
    def test_extract_media_source_accepts_static_sticker(self) -> None:
        message = SimpleNamespace(
            sticker=SimpleNamespace(
                file_id="file-1",
                file_unique_id="unique-1",
                is_video=False,
                is_animated=False,
                file_size=123,
                width=512,
                height=512,
            )
        )

        source = media.extract_media_source(message)

        self.assertEqual(source.kind, "sticker")
        self.assertEqual(source.mime_type, "image/webp")
        self.assertTrue(source.supports_analysis)

    def test_document_initial_text_is_metadata_only(self) -> None:
        message = SimpleNamespace(
            text=None,
            caption=None,
            document=SimpleNamespace(
                file_id="doc",
                file_unique_id="doc-u",
                mime_type="application/pdf",
                file_name="report.pdf",
                file_size=2048,
            ),
        )

        self.assertIn("document", media.initial_message_text(message))
        self.assertIn("report.pdf", media.initial_message_text(message))

    def test_source_from_media_item_reconstructs_retry_source(self) -> None:
        item = SimpleNamespace(
            media_kind="photo",
            telegram_file_id="file-1",
            telegram_file_unique_id="unique-1",
            mime_type="image/jpeg",
            file_name=None,
            file_size=123,
            width=640,
            height=480,
            duration=None,
        )

        source = media._source_from_media_item(item)

        self.assertEqual(source.kind, "photo")
        self.assertEqual(source.file_id, "file-1")
        self.assertEqual(source.file_unique_id, "unique-1")
        self.assertTrue(source.supports_analysis)

    def test_source_from_tgs_sticker_item_is_unsupported(self) -> None:
        item = SimpleNamespace(
            media_kind="sticker",
            telegram_file_id="file-1",
            telegram_file_unique_id="unique-1",
            mime_type="application/x-tgsticker",
            file_name=None,
            file_size=123,
            width=512,
            height=512,
            duration=None,
        )

        source = media._source_from_media_item(item)

        self.assertEqual(source.kind, "sticker")
        self.assertFalse(source.supports_analysis)


class MediaCacheTests(unittest.IsolatedAsyncioTestCase):
    async def test_find_cache_hits_file_unique_id(self) -> None:
        cache = SimpleNamespace(description="cached")
        source = media.MediaSource(kind="photo", file_id="file", file_unique_id="unique", supports_analysis=True)
        session = _DummySession([[cache]])

        with patch.object(media, "SessionLocal", return_value=session):
            result = await media._find_cache(source)

        self.assertIs(result, cache)
        self.assertEqual(session.calls, 1)

    async def test_find_cache_hits_sha256_after_id_miss(self) -> None:
        cache = SimpleNamespace(description="cached")
        source = media.MediaSource(kind="photo", file_id=None, file_unique_id=None, supports_analysis=True)
        session = _DummySession([[cache]])

        with patch.object(media, "SessionLocal", return_value=session):
            result = await media._find_cache(source, sha256="abc")

        self.assertIs(result, cache)

    async def test_find_cache_hits_phash_with_distance_five(self) -> None:
        cache = SimpleNamespace(description="cached", phash="0000000000000000")
        source = media.MediaSource(kind="photo", supports_analysis=True)
        session = _DummySession([[cache]])

        with (
            patch.object(media, "SessionLocal", return_value=session),
            patch.object(media.settings, "MEDIA_PHASH_DISTANCE", 5),
        ):
            result = await media._find_cache(source, phash="000000000000001f")

        self.assertIs(result, cache)


class MediaPendingTests(unittest.IsolatedAsyncioTestCase):
    async def test_process_pending_media_retries_pending_items(self) -> None:
        item = SimpleNamespace(
            message_db_id=55,
            media_kind="photo",
            telegram_file_id="file-1",
            telegram_file_unique_id="unique-1",
            mime_type="image/jpeg",
            file_name=None,
            file_size=123,
            width=640,
            height=480,
            duration=None,
        )
        bot = SimpleNamespace()

        with (
            patch.object(media, "_load_pending_media_items", new=AsyncMock(return_value=[item])),
            patch.object(media, "_process_media_source", new=AsyncMock()) as process_media_source,
            patch.object(media, "_mark_orphan_pending_messages_failed", new=AsyncMock(return_value=0)),
        ):
            processed = await media.process_pending_media(bot, limit=10)

        self.assertEqual(processed, 1)
        process_media_source.assert_awaited_once()
        self.assertEqual(process_media_source.await_args.kwargs["db_message_id"], 55)
        self.assertEqual(process_media_source.await_args.kwargs["source"].kind, "photo")
        self.assertEqual(process_media_source.await_args.kwargs["source"].file_id, "file-1")

    async def test_store_media_result_updates_existing_pending_item(self) -> None:
        message = SimpleNamespace(id=55, caption=None, text="old", media_status="pending")
        media_item = SimpleNamespace(
            id=1,
            message_db_id=55,
            media_cache_id=None,
            media_kind="photo",
            telegram_file_id="old-file",
            telegram_file_unique_id="old-unique",
            mime_type="image/jpeg",
            file_name=None,
            file_size=None,
            width=None,
            height=None,
            duration=None,
            sha256=None,
            phash=None,
            analysis_status="pending",
            analysis_error=None,
            description_snapshot=None,
        )
        session = _DummySession([[message], [media_item]])
        source = media.MediaSource(
            kind="photo",
            file_id="file-1",
            file_unique_id="unique-1",
            mime_type="image/jpeg",
            file_size=123,
            width=640,
            height=480,
            supports_analysis=True,
        )

        with patch.object(media, "SessionLocal", return_value=session):
            await media._store_media_result(
                db_message_id=55,
                source=source,
                status="analyzed",
                description="человек держит табличку",
                sha256="a" * 64,
                phash="b" * 16,
            )

        self.assertEqual(message.media_status, "analyzed")
        self.assertEqual(message.text, "photo: человек держит табличку")
        self.assertEqual(media_item.analysis_status, "analyzed")
        self.assertEqual(media_item.telegram_file_id, "file-1")
        self.assertEqual(media_item.telegram_file_unique_id, "unique-1")
        self.assertEqual(media_item.sha256, "a" * 64)
        self.assertEqual(media_item.phash, "b" * 16)
        self.assertEqual(media_item.description_snapshot, "человек держит табличку")
        self.assertEqual(session.added, [])
        session.commit.assert_awaited_once()


class MediaNormalizationTests(unittest.TestCase):
    def test_normalize_image_computes_sha_and_phash(self) -> None:
        from PIL import Image

        with tempfile.TemporaryDirectory() as tempdir:
            source = Path(tempdir) / "source.png"
            Image.new("RGB", (32, 32), color=(255, 0, 0)).save(source)

            normalized = media._normalize_image(source, Path(tempdir))

        self.assertEqual(normalized.mime_type, "image/jpeg")
        self.assertEqual(len(normalized.sha256), 64)
        self.assertEqual(len(normalized.phash), 16)

    def test_long_video_normalization_cuts_to_supported_low_resolution_limit(self) -> None:
        source = media.MediaSource(kind="video", duration=20_000, supports_analysis=True)
        commands: list[list[str]] = []

        with tempfile.TemporaryDirectory() as tempdir:
            raw = Path(tempdir) / "raw.mp4"
            raw.write_bytes(b"video")
            with (
                patch.object(media, "_run", side_effect=lambda command: commands.append(command)),
                patch.object(media, "_probe_duration", return_value=20_000),
                patch.object(media, "_sha256_file", return_value="a" * 64),
                patch.object(media, "_video_phash", return_value="b" * 16),
            ):
                normalized = media._normalize_video(raw, Path(tempdir), source)

        self.assertEqual(normalized.mime_type, "video/mp4")
        self.assertIn("-t", commands[0])
        self.assertEqual(commands[0][commands[0].index("-t") + 1], "10800")

    def test_audio_normalization_uses_mono_mp3_and_silence_trim(self) -> None:
        commands: list[list[str]] = []

        with tempfile.TemporaryDirectory() as tempdir:
            raw = Path(tempdir) / "raw.ogg"
            raw.write_bytes(b"audio")
            with (
                patch.object(media, "_run", side_effect=lambda command: commands.append(command)),
                patch.object(media, "_sha256_file", return_value="a" * 64),
            ):
                normalized = media._normalize_audio(raw, Path(tempdir))

        self.assertEqual(normalized.mime_type, "audio/mpeg")
        self.assertIn("-ac", commands[0])
        self.assertEqual(commands[0][commands[0].index("-ac") + 1], "1")
        self.assertIn("-af", commands[0])
        self.assertIn("silenceremove=", commands[0][commands[0].index("-af") + 1])

    def test_run_raises_runtime_error_on_command_timeout(self) -> None:
        with (
            patch.object(media.shutil, "which", return_value="/usr/bin/ffmpeg"),
            patch.object(media.settings, "MEDIA_COMMAND_TIMEOUT_SECONDS", 1),
            patch.object(
                media.subprocess,
                "run",
                side_effect=subprocess.TimeoutExpired(cmd=["ffmpeg"], timeout=1),
            ),
        ):
            with self.assertRaisesRegex(RuntimeError, "timed out after 1s"):
                media._run(["ffmpeg", "-version"])


class AIMediaPayloadTests(unittest.TestCase):
    def test_media_content_parts_use_openrouter_raw_schema(self) -> None:
        with tempfile.TemporaryDirectory() as tempdir:
            path = Path(tempdir) / "media.bin"
            path.write_bytes(b"media")

            image_part = ai._media_content_part(path=path, mime_type="image/jpeg", media_kind="photo")
            video_part = ai._media_content_part(path=path, mime_type="video/mp4", media_kind="video")
            audio_part = ai._media_content_part(path=path, mime_type="audio/mpeg", media_kind="voice")

        self.assertIn("image_url", image_part)
        self.assertIn("video_url", video_part)
        self.assertNotIn("videoUrl", video_part)
        self.assertIn("input_audio", audio_part)
        self.assertNotIn("inputAudio", audio_part)

    def test_media_description_prompt_is_audio_only_for_voice(self) -> None:
        prompt = ai._media_description_prompt("voice")

        self.assertIn("только аудио без изображения", prompt)
        self.assertIn("Запрещено описывать внешность", prompt)
        self.assertIn("камеру", prompt)
        self.assertIn("видеоряд", prompt)
        self.assertIn("транскрипт", prompt)

    def test_media_description_prompt_is_visual_only_for_photo(self) -> None:
        prompt = ai._media_description_prompt("photo")

        self.assertIn("статичное изображение", prompt)
        self.assertIn("только то, что реально видно", prompt)
        self.assertIn("Не выдумывай звук", prompt)

    def test_media_description_prompt_is_video_specific_for_video_note(self) -> None:
        prompt = ai._media_description_prompt("video_note")

        self.assertIn("видео, анимация или видеокружок", prompt)
        self.assertIn("последовательность событий", prompt)
        self.assertIn("речь и звуки", prompt)

    def test_media_description_prompt_is_sticker_specific(self) -> None:
        prompt = ai._media_description_prompt("sticker")

        self.assertIn("Telegram-стикер", prompt)
        self.assertIn("мем", prompt)
        self.assertIn("Не выдумывай звук", prompt)


class AIMediaRequestTests(unittest.IsolatedAsyncioTestCase):
    async def test_describe_media_file_uses_media_model_and_medium_reasoning(self) -> None:
        captured_body: dict | None = None

        class FakeResponse:
            status_code = 200
            text = json.dumps(
                {
                    "model": "google/gemini-3.1-flash-lite",
                    "choices": [{"message": {"content": "на фото человек держит табличку"}}],
                    "usage": {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
                }
            )

            def raise_for_status(self) -> None:
                return None

            def json(self) -> dict:
                return json.loads(self.text)

        class FakeClient:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

            async def post(self, *_args, **kwargs):
                nonlocal captured_body
                captured_body = kwargs["json"]
                return FakeResponse()

        with tempfile.TemporaryDirectory() as tempdir:
            image_path = Path(tempdir) / "image.jpg"
            image_path.write_bytes(b"image")
            with (
                patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
                patch.object(ai.settings, "OPENROUTER_MEDIA_MODEL", "google/gemini-3.1-flash-lite"),
                patch.object(ai.settings, "OPENROUTER_MEDIA_REASONING_EFFORT", "medium"),
                patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            ):
                result = await ai.describe_media_file(
                    path=image_path,
                    mime_type="image/jpeg",
                    media_kind="photo",
                )

        self.assertEqual(result.description, "на фото человек держит табличку")
        self.assertEqual(result.prompt_tokens, 100)
        self.assertEqual(captured_body["model"], "google/gemini-3.1-flash-lite")
        self.assertEqual(captured_body["reasoning"], {"enabled": True, "effort": "medium", "exclude": True})
        self.assertIn("статичное изображение", captured_body["messages"][0]["content"][0]["text"])
        self.assertEqual(captured_body["messages"][0]["content"][1]["type"], "image_url")

    async def test_describe_media_file_uses_audio_only_prompt_for_voice(self) -> None:
        captured_body: dict | None = None

        class FakeResponse:
            status_code = 200
            text = json.dumps(
                {
                    "model": "google/gemini-3.1-flash-lite",
                    "choices": [{"message": {"content": "Слышен мужской голос: «тест». Фон тихий."}}],
                    "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
                }
            )

            def raise_for_status(self) -> None:
                return None

            def json(self) -> dict:
                return json.loads(self.text)

        class FakeClient:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

            async def post(self, *_args, **kwargs):
                nonlocal captured_body
                captured_body = kwargs["json"]
                return FakeResponse()

        with tempfile.TemporaryDirectory() as tempdir:
            audio_path = Path(tempdir) / "voice.mp3"
            audio_path.write_bytes(b"audio")
            with (
                patch.object(ai.settings, "OPENROUTER_API_KEY", "test-key"),
                patch.object(ai.settings, "OPENROUTER_MEDIA_MODEL", "google/gemini-3.1-flash-lite"),
                patch.object(ai.settings, "OPENROUTER_MEDIA_REASONING_EFFORT", "medium"),
                patch.object(ai.httpx, "AsyncClient", return_value=FakeClient()),
            ):
                result = await ai.describe_media_file(
                    path=audio_path,
                    mime_type="audio/mpeg",
                    media_kind="voice",
                )

        self.assertEqual(result.description, "Слышен мужской голос: «тест». Фон тихий.")
        prompt = captured_body["messages"][0]["content"][0]["text"]
        self.assertIn("только аудио без изображения", prompt)
        self.assertIn("Запрещено описывать внешность", prompt)
        self.assertEqual(captured_body["messages"][0]["content"][1]["type"], "input_audio")
