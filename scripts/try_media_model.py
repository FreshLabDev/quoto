import argparse
import asyncio
import mimetypes
import sys
import tempfile
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app import ai, media  # noqa: E402
from app.config import settings  # noqa: E402

VIDEO_SUFFIXES = {".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".gif"}
AUDIO_SUFFIXES = {".mp3", ".m4a", ".ogg", ".oga", ".opus", ".wav"}
IMAGE_SUFFIXES = {".jpg", ".jpeg", ".png", ".webp"}

KIND_MODEL_SETTING = {
    "video": "OPENROUTER_MEDIA_VIDEO_MODEL",
    "animation": "OPENROUTER_MEDIA_VIDEO_MODEL",
    "video_note": "OPENROUTER_MEDIA_VIDEO_MODEL",
    "voice": "OPENROUTER_MEDIA_AUDIO_MODEL",
    "audio": "OPENROUTER_MEDIA_AUDIO_MODEL",
    "photo": "OPENROUTER_MEDIA_IMAGE_MODEL",
    "image": "OPENROUTER_MEDIA_IMAGE_MODEL",
    "sticker": "OPENROUTER_MEDIA_IMAGE_MODEL",
}


def detect_kind(path: Path) -> str:
    suffix = path.suffix.lower()
    if suffix in VIDEO_SUFFIXES:
        return "animation" if suffix == ".gif" else "video"
    if suffix in AUDIO_SUFFIXES:
        return "audio"
    if suffix in IMAGE_SUFFIXES:
        return "photo"
    raise SystemExit(f"Unsupported extension {suffix!r}; pass --kind explicitly.")


async def try_model(path: Path, kind: str, mime: str, model: str, show_prompt: bool) -> None:
    setting = KIND_MODEL_SETTING[kind]
    saved = (
        getattr(settings, setting),
        settings.OPENROUTER_MEDIA_MODEL,
        settings.OPENROUTER_MEDIA_FALLBACK_MODEL,
    )
    setattr(settings, setting, model)
    settings.OPENROUTER_MEDIA_MODEL = ""
    settings.OPENROUTER_MEDIA_FALLBACK_MODEL = ""
    ai._media_breaker_reset()
    source = media.MediaSource(kind=kind, mime_type=mime)
    start = time.monotonic()
    try:
        with tempfile.TemporaryDirectory() as tempdir:
            normalized = await asyncio.to_thread(media._normalize_media, source, path, Path(tempdir))
            size_kb = normalized.path.stat().st_size / 1024
            print(f"--- model: {model}")
            print(f"payload: {normalized.mime_type}, {size_kb:.0f} KB after normalization")
            if show_prompt:
                print(f"prompt:\n{ai._media_description_prompt(kind)}\n")
            result = await ai.describe_media_file(
                path=normalized.path,
                mime_type=normalized.mime_type,
                media_kind=kind,
            )
            elapsed = time.monotonic() - start
            tokens = result.total_tokens if result.total_tokens is not None else "?"
            print(f"actual_model: {result.actual_model}")
            print(f"latency: {elapsed:.1f}s, tokens: {tokens}")
            print(f"description:\n{result.description}")
    except Exception as exc:
        elapsed = time.monotonic() - start
        print(f"FAILED after {elapsed:.1f}s: {type(exc).__name__}: {exc}")
    finally:
        setattr(settings, setting, saved[0])
        settings.OPENROUTER_MEDIA_MODEL = saved[1]
        settings.OPENROUTER_MEDIA_FALLBACK_MODEL = saved[2]
        ai._media_breaker_reset()


async def main() -> None:
    parser = argparse.ArgumentParser(description="Test one OpenRouter model against a media file.")
    parser.add_argument("file", type=Path, help="Path to a local media file")
    parser.add_argument("--kind", choices=sorted(KIND_MODEL_SETTING), default=None, help="Media kind override")
    parser.add_argument(
        "--model",
        action="append",
        dest="models",
        required=True,
        help="OpenRouter model id; repeat the flag to compare several models",
    )
    parser.add_argument("--show-prompt", action="store_true", help="Print the prompt sent to the model")
    args = parser.parse_args()

    path = args.file.resolve()
    if not path.is_file():
        raise SystemExit(f"File not found: {path}")

    kind = args.kind or detect_kind(path)
    mime = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
    print(f"file: {path.name}, kind: {kind}, mime: {mime}")

    for index, model in enumerate(args.models):
        if index:
            print()
        await try_model(path, kind, mime, model, args.show_prompt)


if __name__ == "__main__":
    asyncio.run(main())
