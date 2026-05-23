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

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        return False

    async def execute(self, _stmt):
        result = self.results[self.calls] if self.calls < len(self.results) else []
        self.calls += 1
        return _DummyResult(result)


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
        self.assertEqual(captured_body["messages"][0]["content"][1]["type"], "image_url")
