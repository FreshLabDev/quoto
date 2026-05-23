from __future__ import annotations

import hashlib
import logging
import math
from dataclasses import dataclass
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Any

from aiogram import Bot, types
from sqlalchemy import select
from sqlalchemy.exc import IntegrityError

from . import ai, models
from .config import settings, setup_logging
from .db import SessionLocal

log = setup_logging(logging.getLogger(__name__))


@dataclass(frozen=True)
class MediaSource:
    kind: str
    file_id: str | None = None
    file_unique_id: str | None = None
    mime_type: str | None = None
    file_name: str | None = None
    file_size: int | None = None
    width: int | None = None
    height: int | None = None
    duration: float | None = None
    supports_analysis: bool = False


@dataclass(frozen=True)
class NormalizedMedia:
    path: Path
    mime_type: str
    sha256: str
    phash: str | None = None


def extract_media_source(message: types.Message) -> MediaSource | None:
    if getattr(message, "photo", None):
        photo = sorted(message.photo, key=lambda item: getattr(item, "file_size", 0) or 0)[-1]
        return MediaSource(
            kind="photo",
            file_id=getattr(photo, "file_id", None),
            file_unique_id=getattr(photo, "file_unique_id", None),
            mime_type="image/jpeg",
            file_size=getattr(photo, "file_size", None),
            width=getattr(photo, "width", None),
            height=getattr(photo, "height", None),
            supports_analysis=True,
        )
    if getattr(message, "video", None):
        video = message.video
        return MediaSource(
            kind="video",
            file_id=getattr(video, "file_id", None),
            file_unique_id=getattr(video, "file_unique_id", None),
            mime_type=getattr(video, "mime_type", None) or "video/mp4",
            file_name=getattr(video, "file_name", None),
            file_size=getattr(video, "file_size", None),
            width=getattr(video, "width", None),
            height=getattr(video, "height", None),
            duration=_float_or_none(getattr(video, "duration", None)),
            supports_analysis=True,
        )
    if getattr(message, "animation", None):
        animation = message.animation
        return MediaSource(
            kind="animation",
            file_id=getattr(animation, "file_id", None),
            file_unique_id=getattr(animation, "file_unique_id", None),
            mime_type=getattr(animation, "mime_type", None) or "video/mp4",
            file_name=getattr(animation, "file_name", None),
            file_size=getattr(animation, "file_size", None),
            width=getattr(animation, "width", None),
            height=getattr(animation, "height", None),
            duration=_float_or_none(getattr(animation, "duration", None)),
            supports_analysis=True,
        )
    if getattr(message, "video_note", None):
        video_note = message.video_note
        return MediaSource(
            kind="video_note",
            file_id=getattr(video_note, "file_id", None),
            file_unique_id=getattr(video_note, "file_unique_id", None),
            mime_type="video/mp4",
            file_size=getattr(video_note, "file_size", None),
            width=getattr(video_note, "length", None),
            height=getattr(video_note, "length", None),
            duration=_float_or_none(getattr(video_note, "duration", None)),
            supports_analysis=True,
        )
    if getattr(message, "voice", None):
        voice = message.voice
        return MediaSource(
            kind="voice",
            file_id=getattr(voice, "file_id", None),
            file_unique_id=getattr(voice, "file_unique_id", None),
            mime_type=getattr(voice, "mime_type", None) or "audio/ogg",
            file_size=getattr(voice, "file_size", None),
            duration=_float_or_none(getattr(voice, "duration", None)),
            supports_analysis=True,
        )
    if getattr(message, "audio", None):
        audio = message.audio
        return MediaSource(
            kind="audio",
            file_id=getattr(audio, "file_id", None),
            file_unique_id=getattr(audio, "file_unique_id", None),
            mime_type=getattr(audio, "mime_type", None) or "audio/mpeg",
            file_name=getattr(audio, "file_name", None),
            file_size=getattr(audio, "file_size", None),
            duration=_float_or_none(getattr(audio, "duration", None)),
            supports_analysis=True,
        )
    if getattr(message, "sticker", None):
        sticker = message.sticker
        is_video = bool(getattr(sticker, "is_video", False))
        is_animated = bool(getattr(sticker, "is_animated", False))
        return MediaSource(
            kind="sticker",
            file_id=getattr(sticker, "file_id", None),
            file_unique_id=getattr(sticker, "file_unique_id", None),
            mime_type="video/webm" if is_video else ("application/x-tgsticker" if is_animated else "image/webp"),
            file_size=getattr(sticker, "file_size", None),
            width=getattr(sticker, "width", None),
            height=getattr(sticker, "height", None),
            supports_analysis=not is_animated or is_video,
        )
    if getattr(message, "document", None):
        document = message.document
        mime_type = getattr(document, "mime_type", None) or "application/octet-stream"
        is_image = mime_type.startswith("image/")
        return MediaSource(
            kind="image" if is_image else "document",
            file_id=getattr(document, "file_id", None),
            file_unique_id=getattr(document, "file_unique_id", None),
            mime_type=mime_type,
            file_name=getattr(document, "file_name", None),
            file_size=getattr(document, "file_size", None),
            supports_analysis=is_image,
        )
    return None


def message_content_type(message: types.Message) -> str:
    if getattr(message, "text", None):
        return "text"
    source = extract_media_source(message)
    if source:
        return source.kind
    return str(getattr(message, "content_type", None) or "unknown")


def message_caption(message: types.Message) -> str | None:
    caption = str(getattr(message, "caption", "") or "").strip()
    return caption or None


def initial_message_text(message: types.Message) -> str:
    text = str(getattr(message, "text", "") or "").strip()
    if text:
        return text
    caption = message_caption(message)
    source = extract_media_source(message)
    if source:
        return _canonical_text(source.kind, caption, _metadata_description(source))
    if caption:
        return caption
    return f"{message_content_type(message)}: сообщение без текста"


async def process_message_media(bot: Bot, message: types.Message, db_message: models.Message) -> None:
    source = extract_media_source(message)
    if not source:
        return

    if not settings.MEDIA_ANALYSIS_ENABLED or not source.supports_analysis:
        description = _metadata_description(source)
        await _store_media_result(
            db_message_id=db_message.id,
            source=source,
            status="unsupported",
            description=description,
        )
        return

    cache = await _find_cache(source)
    if cache:
        await _store_media_result(
            db_message_id=db_message.id,
            source=source,
            status="cached",
            description=cache.description,
            cache=cache,
        )
        return

    try:
        with tempfile.TemporaryDirectory() as tempdir:
            raw_path = await _download_telegram_file(bot, source, Path(tempdir))
            normalized = _normalize_media(source, raw_path, Path(tempdir))
            cache = await _find_cache(source, sha256=normalized.sha256, phash=normalized.phash)
            if cache:
                await _store_media_result(
                    db_message_id=db_message.id,
                    source=source,
                    status="cached",
                    description=cache.description,
                    cache=cache,
                    sha256=normalized.sha256,
                    phash=normalized.phash,
                )
                return

            description_result = await ai.describe_media_file(
                path=normalized.path,
                mime_type=normalized.mime_type,
                media_kind=_analysis_kind(source),
            )
            cache = await _create_cache(source, normalized, description_result)
            await _store_media_result(
                db_message_id=db_message.id,
                source=source,
                status="analyzed",
                description=description_result.description,
                cache=cache,
                sha256=normalized.sha256,
                phash=normalized.phash,
            )
    except Exception as exc:
        log.warning(f"Media analysis failed for message {db_message.message_id}: {exc}")
        await _store_media_result(
            db_message_id=db_message.id,
            source=source,
            status="failed",
            description=_metadata_description(source),
            error=str(exc)[:500],
        )


async def _download_telegram_file(bot: Bot, source: MediaSource, tempdir: Path) -> Path:
    if not source.file_id:
        raise RuntimeError("Telegram file_id is missing.")
    telegram_file = await bot.get_file(source.file_id)
    file_path = getattr(telegram_file, "file_path", None)
    if not file_path:
        raise RuntimeError("Telegram did not return file_path.")
    suffix = _suffix_for_mime(source.mime_type) or Path(str(file_path)).suffix or ".bin"
    destination = tempdir / f"telegram{suffix}"
    await bot.download_file(file_path, destination=destination)
    return destination


def _normalize_media(source: MediaSource, raw_path: Path, tempdir: Path) -> NormalizedMedia:
    kind = _analysis_kind(source)
    if kind in {"photo", "image", "sticker"}:
        return _normalize_image(raw_path, tempdir)
    if kind in {"video", "animation", "video_note"}:
        return _normalize_video(raw_path, tempdir, source)
    if kind in {"voice", "audio"}:
        return _normalize_audio(raw_path, tempdir)
    raise RuntimeError(f"Unsupported media kind: {kind}")


def _normalize_image(raw_path: Path, tempdir: Path) -> NormalizedMedia:
    from PIL import Image

    output = tempdir / "normalized.jpg"
    with Image.open(raw_path) as image:
        image = image.convert("RGB")
        image.thumbnail((settings.MEDIA_IMAGE_MAX_SIDE, settings.MEDIA_IMAGE_MAX_SIDE))
        image.save(output, format="JPEG", quality=settings.MEDIA_IMAGE_QUALITY, optimize=True)
    return NormalizedMedia(
        path=output,
        mime_type="image/jpeg",
        sha256=_sha256_file(output),
        phash=_image_phash(output),
    )


def _normalize_video(raw_path: Path, tempdir: Path, source: MediaSource) -> NormalizedMedia:
    output = tempdir / "normalized.mp4"
    duration = _probe_duration(raw_path) or source.duration or 0
    max_seconds = settings.MEDIA_VIDEO_LOW_RES_MAX_SECONDS if duration > settings.MEDIA_VIDEO_MAX_SECONDS else settings.MEDIA_VIDEO_MAX_SECONDS
    seconds = max(1, min(int(duration or max_seconds), settings.MEDIA_VIDEO_LOW_RES_MAX_SECONDS, max_seconds))
    command = [
        "ffmpeg",
        "-y",
        "-i",
        str(raw_path),
        "-t",
        str(seconds),
        "-vf",
        f"fps={settings.MEDIA_VIDEO_FPS},scale=-2:{settings.MEDIA_VIDEO_MAX_HEIGHT}:force_original_aspect_ratio=decrease",
        "-c:v",
        "libx264",
        "-preset",
        "veryfast",
        "-crf",
        str(settings.MEDIA_VIDEO_CRF),
        "-c:a",
        "aac",
        "-b:a",
        settings.MEDIA_AUDIO_BITRATE,
        "-movflags",
        "+faststart",
        str(output),
    ]
    _run(command)
    return NormalizedMedia(
        path=output,
        mime_type="video/mp4",
        sha256=_sha256_file(output),
        phash=_video_phash(output, tempdir),
    )


def _normalize_audio(raw_path: Path, tempdir: Path) -> NormalizedMedia:
    output = tempdir / "normalized.mp3"
    command = [
        "ffmpeg",
        "-y",
        "-i",
        str(raw_path),
        "-vn",
        "-ac",
        "1",
        "-ar",
        str(settings.MEDIA_AUDIO_SAMPLE_RATE),
        "-b:a",
        settings.MEDIA_AUDIO_BITRATE,
        "-af",
        "silenceremove=start_periods=1:start_duration=0.2:start_threshold=-50dB:"
        "stop_periods=1:stop_duration=0.5:stop_threshold=-50dB",
        str(output),
    ]
    _run(command)
    return NormalizedMedia(
        path=output,
        mime_type="audio/mpeg",
        sha256=_sha256_file(output),
    )


def _video_phash(video_path: Path, tempdir: Path) -> str | None:
    duration = _probe_duration(video_path) or 0
    offsets = [0.0]
    if duration >= 2:
        offsets.append(duration / 2)
    if duration >= 3:
        offsets.append(max(0.0, duration - 1))
    hashes: list[str] = []
    for index, offset in enumerate(offsets):
        frame = tempdir / f"frame_{index}.jpg"
        try:
            _run(
                [
                    "ffmpeg",
                    "-y",
                    "-ss",
                    f"{offset:.3f}",
                    "-i",
                    str(video_path),
                    "-frames:v",
                    "1",
                    str(frame),
                ]
            )
            hashes.append(_image_phash(frame))
        except Exception:
            continue
    return ":".join(hashes) if hashes else None


def _image_phash(path: Path) -> str:
    from PIL import Image

    with Image.open(path) as image:
        image = image.convert("L").resize((32, 32))
        flattened_data = getattr(image, "get_flattened_data", None)
        pixels = list(flattened_data() if callable(flattened_data) else image.getdata())

    matrix = [pixels[row * 32 : (row + 1) * 32] for row in range(32)]
    coeffs: list[float] = []
    for u in range(8):
        for v in range(8):
            total = 0.0
            for x in range(32):
                for y in range(32):
                    total += (
                        matrix[x][y]
                        * math.cos(((2 * x + 1) * u * math.pi) / 64)
                        * math.cos(((2 * y + 1) * v * math.pi) / 64)
                    )
            cu = 1 / math.sqrt(2) if u == 0 else 1
            cv = 1 / math.sqrt(2) if v == 0 else 1
            coeffs.append(0.25 * cu * cv * total)

    sample = coeffs[1:]
    median = sorted(sample)[len(sample) // 2]
    bits = "".join("1" if value > median else "0" for value in coeffs)
    return f"{int(bits, 2):016x}"


async def _find_cache(
    source: MediaSource,
    *,
    sha256: str | None = None,
    phash: str | None = None,
) -> models.MediaCache | None:
    async with SessionLocal() as session:
        for column, value in (
            (models.MediaCache.telegram_file_unique_id, source.file_unique_id),
            (models.MediaCache.telegram_file_id, source.file_id),
            (models.MediaCache.sha256, sha256),
        ):
            if not value:
                continue
            result = await session.execute(
                select(models.MediaCache).where(
                    models.MediaCache.prompt_version == settings.MEDIA_CACHE_PROMPT_VERSION,
                    models.MediaCache.media_kind == _analysis_kind(source),
                    column == value,
                )
            )
            cache = result.scalars().first()
            if cache:
                return cache

        if phash:
            result = await session.execute(
                select(models.MediaCache).where(
                    models.MediaCache.prompt_version == settings.MEDIA_CACHE_PROMPT_VERSION,
                    models.MediaCache.media_kind == _analysis_kind(source),
                    models.MediaCache.phash.is_not(None),
                )
            )
            for cache in result.scalars().all():
                if _phash_distance(phash, cache.phash or "") <= settings.MEDIA_PHASH_DISTANCE:
                    return cache
    return None


async def _create_cache(
    source: MediaSource,
    normalized: NormalizedMedia,
    description: ai.MediaDescriptionResult,
) -> models.MediaCache:
    async with SessionLocal() as session:
        cache = models.MediaCache(
            media_kind=_analysis_kind(source),
            telegram_file_unique_id=source.file_unique_id,
            telegram_file_id=source.file_id,
            sha256=normalized.sha256,
            phash=normalized.phash,
            phash_algo="dct64" if normalized.phash else None,
            description=description.description,
            model=description.actual_model,
            prompt_version=settings.MEDIA_CACHE_PROMPT_VERSION,
            usage_prompt_tokens=description.prompt_tokens,
            usage_completion_tokens=description.completion_tokens,
            usage_total_tokens=description.total_tokens,
        )
        session.add(cache)
        try:
            await session.commit()
            await session.refresh(cache)
            return cache
        except IntegrityError:
            await session.rollback()
            result = await session.execute(
                select(models.MediaCache).where(
                    models.MediaCache.prompt_version == settings.MEDIA_CACHE_PROMPT_VERSION,
                    models.MediaCache.media_kind == _analysis_kind(source),
                    models.MediaCache.sha256 == normalized.sha256,
                )
            )
            existing = result.scalars().first()
            if existing:
                return existing
            raise


async def _store_media_result(
    *,
    db_message_id: int,
    source: MediaSource,
    status: str,
    description: str | None = None,
    cache: models.MediaCache | None = None,
    sha256: str | None = None,
    phash: str | None = None,
    error: str | None = None,
) -> None:
    async with SessionLocal() as session:
        result = await session.execute(
            select(models.Message).where(models.Message.id == db_message_id)
        )
        message = result.scalars().first()
        if not message:
            return
        if description:
            message.text = _canonical_text(source.kind, message.caption, description)
        message.media_status = status
        session.add(
            models.MessageMedia(
                message_db_id=db_message_id,
                media_cache_id=cache.id if cache else None,
                media_kind=source.kind,
                telegram_file_id=source.file_id,
                telegram_file_unique_id=source.file_unique_id,
                mime_type=source.mime_type,
                file_name=source.file_name,
                file_size=source.file_size,
                width=source.width,
                height=source.height,
                duration=source.duration,
                sha256=sha256 or (cache.sha256 if cache else None),
                phash=phash or (cache.phash if cache else None),
                analysis_status=status,
                analysis_error=error,
                description_snapshot=description,
            )
        )
        await session.commit()


def _canonical_text(kind: str, caption: str | None, description: str | None) -> str:
    parts: list[str] = []
    if caption:
        parts.append(caption.strip())
    if description:
        parts.append(f"{kind}: {description.strip()}")
    return "\n".join(part for part in parts if part) or f"{kind}: сообщение без текста"


def _metadata_description(source: MediaSource) -> str:
    details = [source.kind]
    if source.file_name:
        details.append(f"name={source.file_name}")
    if source.mime_type:
        details.append(f"mime={source.mime_type}")
    if source.file_size:
        details.append(f"size={source.file_size}")
    if source.duration:
        details.append(f"duration={source.duration:g}s")
    return "Файл: " + ", ".join(details)


def _analysis_kind(source: MediaSource) -> str:
    if source.kind == "image":
        return "image"
    if source.kind == "sticker" and source.mime_type == "video/webm":
        return "video"
    return source.kind


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _phash_distance(left: str, right: str) -> int:
    left_parts = [part for part in left.split(":") if part]
    right_parts = [part for part in right.split(":") if part]
    if not left_parts or not right_parts:
        return 10**9
    distances: list[int] = []
    for left_part in left_parts:
        for right_part in right_parts:
            try:
                distances.append((int(left_part, 16) ^ int(right_part, 16)).bit_count())
            except ValueError:
                continue
    return min(distances) if distances else 10**9


def _probe_duration(path: Path) -> float | None:
    if not shutil.which("ffprobe"):
        return None
    try:
        result = subprocess.run(
            [
                "ffprobe",
                "-v",
                "error",
                "-show_entries",
                "format=duration",
                "-of",
                "default=noprint_wrappers=1:nokey=1",
                str(path),
            ],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        return _float_or_none(result.stdout.strip())
    except Exception:
        return None


def _run(command: list[str]) -> None:
    if not shutil.which(command[0]):
        raise RuntimeError(f"{command[0]} is not installed.")
    subprocess.run(
        command,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )


def _suffix_for_mime(mime_type: str | None) -> str:
    mapping = {
        "image/jpeg": ".jpg",
        "image/png": ".png",
        "image/webp": ".webp",
        "video/mp4": ".mp4",
        "video/webm": ".webm",
        "audio/ogg": ".ogg",
        "audio/mpeg": ".mp3",
        "application/x-tgsticker": ".tgs",
    }
    return mapping.get(mime_type or "", "")


def _float_or_none(value: Any) -> float | None:
    try:
        return float(value)
    except (TypeError, ValueError):
        return None
