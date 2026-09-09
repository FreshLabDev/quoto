"""Local web hub for benchmarking quote-of-the-day models on real days.

Serves a small control panel on 127.0.0.1: pick a day recorded in
logs/ai_audit.jsonl, pick the models and the reasoning effort, hit run, and
watch every model think in real time before comparing what each of them chose.

Nothing here writes to the database or to Telegram, and no request is sent
until you press Run in the UI.

    .venv/bin/python scripts/bench_web.py --open      # http://127.0.0.1:8730

Temperature is deliberately never sent: production doesn't set it either, so
the benchmark samples exactly the way the bot does.
"""

import argparse
import asyncio
import json
import mimetypes
import sys
import time
import uuid
import webbrowser
from dataclasses import asdict, dataclass, field
from types import SimpleNamespace
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
from aiohttp import web

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app import ai, media, scoring  # noqa: E402
from app.config import settings  # noqa: E402

UI_FILE = Path(__file__).with_name("bench_web_ui.html")
MODEL_CATALOG_URL = "https://openrouter.ai/api/v1/models"
MODEL_CATALOG_TTL_SECONDS = 3600
REQUEST_TIMEOUT_SECONDS = 600.0
HISTORY_DIRNAME = "bench_runs"
MEDIA_DIRNAME = "bench_media"
MAX_UPLOAD_BYTES = 200 * 1024 * 1024

# Which input modality a media kind needs from the model.
KIND_MODALITY = {
    "photo": "image", "image": "image", "sticker": "image",
    "video": "video", "animation": "video", "video_note": "video",
    "voice": "audio", "audio": "audio",
}
SUFFIX_KIND = {
    ".jpg": "photo", ".jpeg": "photo", ".png": "photo", ".webp": "photo", ".heic": "photo",
    ".mp4": "video", ".mov": "video", ".mkv": "video", ".webm": "video", ".avi": "video",
    ".m4v": "video", ".gif": "animation",
    ".mp3": "audio", ".m4a": "audio", ".ogg": "audio", ".oga": "audio", ".opus": "voice",
    ".wav": "audio", ".flac": "audio",
}


# Typed keys keep aiohttp from warning about plain-string app state.
AUDIT_PATH = web.AppKey("audit_path", Path)
HISTORY_DIR = web.AppKey("history_dir", Path)
MEDIA_DIR = web.AppKey("media_dir", Path)
CATALOG_KEY: "web.AppKey[Any]" = web.AppKey("catalog")
CACHE_KEY: "web.AppKey[dict]" = web.AppKey("cache", dict)


# ─── audit log ────────────────────────────────────────────────────────────────


@dataclass
class AuditDay:
    day: str
    message_count: int
    include_day_verdict: bool
    body: dict[str, Any]
    messages: list[dict[str, Any]] = field(default_factory=list)
    production_model: str = "—"
    production_primary_id: int | None = None
    production_cost_usd: float | None = None


def _index_messages(user_content: str) -> list[dict[str, Any]]:
    try:
        parsed = json.loads(user_content)
    except json.JSONDecodeError:
        return []
    return [item for item in parsed if isinstance(item, dict) and "i" in item]


def load_days(audit_path: Path) -> dict[str, AuditDay]:
    if not audit_path.exists():
        return {}
    days: dict[str, AuditDay] = {}
    for line in audit_path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        body = (record.get("request") or {}).get("body") or {}
        messages = body.get("messages") or []
        if len(messages) < 2:
            continue
        day = str(record.get("created_at", ""))[:10]
        if not day:
            continue
        if day not in days:
            days[day] = AuditDay(
                day=day,
                message_count=int(record.get("message_count") or 0),
                include_day_verdict=bool(record.get("include_day_verdict")),
                body=body,
                messages=_index_messages(messages[1].get("content") or ""),
            )
        result = record.get("result") or {}
        choice = result.get("quote_choice") or {}
        if choice.get("primary_id") is not None:
            days[day].production_primary_id = choice["primary_id"]
            days[day].production_model = str(result.get("actual_model") or body.get("model") or "?")
        usage = record.get("usage") or {}
        if usage.get("cost_usd") is not None:
            days[day].production_cost_usd = usage["cost_usd"]
    return dict(sorted(days.items(), reverse=True))


def context_messages(day: "AuditDay", choice: Any, primary_id: int | None) -> list[dict[str, Any]]:
    """The context block exactly as production would publish it.

    scoring._valid_context_messages owns the rules — the model's own order, the
    primary appended when missing, and the window cap — and it only reads `.id`,
    so lightweight stand-ins are enough to reuse it verbatim.
    """
    by_id = {item.get("i"): item for item in day.messages}
    if not choice or primary_id is None or primary_id not in by_id:
        return []
    stand_ins = [SimpleNamespace(id=item["i"]) for item in day.messages if item.get("i") is not None]
    primary = next((s for s in stand_ins if s.id == primary_id), None)
    if primary is None:
        return []
    ordered = scoring._valid_context_messages(choice, stand_ins, primary)
    return [
        {**message_view(by_id[item.id]), "is_primary": item.id == primary_id}
        for item in ordered
        if item.id in by_id
    ]


def message_view(item: dict[str, Any]) -> dict[str, Any]:
    return {
        "id": item.get("i"),
        "author": item.get("a") or "?",
        "kind": item.get("kind") or "text",
        "text": item.get("t") or "",
        "description": item.get("desc") or "",
        "reactions": item.get("re") or {},
        "reply_to": item.get("rp"),
    }


def day_view(day: AuditDay) -> dict[str, Any]:
    pick = next((m for m in day.messages if m.get("i") == day.production_primary_id), None)
    return {
        "day": day.day,
        "message_count": day.message_count,
        "include_day_verdict": day.include_day_verdict,
        "production_model": day.production_model,
        "production_primary_id": day.production_primary_id,
        "production_pick": message_view(pick) if pick else None,
        "production_cost_usd": day.production_cost_usd,
        "audited_max_tokens": day.body.get("max_tokens"),
        "audited_effort": (day.body.get("reasoning") or {}).get("effort"),
    }


# ─── model catalog ────────────────────────────────────────────────────────────


def catalog_entry(entry: dict[str, Any]) -> dict[str, Any] | None:
    """Map one OpenRouter model into what the hub shows, or None to hide it."""
    pricing = entry.get("pricing") or {}
    architecture = entry.get("architecture") or {}
    try:
        prompt_price = float(pricing.get("prompt") or 0)
        completion_price = float(pricing.get("completion") or 0)
    except (TypeError, ValueError):
        prompt_price = completion_price = 0.0
    supported = entry.get("supported_parameters") or []
    # OpenRouter publishes what reasoning each model actually accepts, so the UI
    # can offer only the efforts that work instead of the whole vocabulary.
    reasoning = entry.get("reasoning") or {}
    modalities = architecture.get("input_modalities") or []
    outputs = architecture.get("output_modalities") or ["text"]
    if "text" not in modalities:
        # The prompts are text; image- or audio-only endpoints can't take them.
        return None
    if "image" in outputs:
        # Image generators: they answer with pictures, not with a quote or a description.
        return None
    if str(entry.get("id") or "").endswith(":batch"):
        # Batch-tier ids are refused by /chat/completions ("only available through
        # the Batch API"), so they can't take part in a live comparison.
        return None
    return {
        "id": entry.get("id"),
        "name": entry.get("name") or entry.get("id"),
        "created": entry.get("created"),
        "context_length": entry.get("context_length"),
        "prompt_price": prompt_price,
        "completion_price": completion_price,
        "free": prompt_price == 0 and completion_price == 0,
        "modalities": modalities,
        "outputs": outputs,
        "supports_reasoning": "reasoning" in supported,
        "supports_effort": "reasoning_effort" in supported,
        "has_reasoning": bool(reasoning),
        "reasoning_mandatory": bool(reasoning.get("mandatory")),
        "reasoning_default_enabled": bool(reasoning.get("default_enabled")),
        "supported_efforts": reasoning.get("supported_efforts") or [],
        "default_effort": reasoning.get("default_effort"),
        "supports_reasoning_max_tokens": bool(reasoning.get("supports_max_tokens")),
        "supports_structured": "structured_outputs" in supported or "response_format" in supported,
    }


class ModelCatalog:
    def __init__(self) -> None:
        self._models: list[dict[str, Any]] = []
        self._fetched_at = 0.0

    async def get(self) -> list[dict[str, Any]]:
        if self._models and time.monotonic() - self._fetched_at < MODEL_CATALOG_TTL_SECONDS:
            return self._models
        async with httpx.AsyncClient(timeout=30.0) as client:
            response = await client.get(MODEL_CATALOG_URL)
            response.raise_for_status()
            payload = response.json()
        models = [
            mapped
            for entry in payload.get("data", [])
            if (mapped := catalog_entry(entry)) is not None
        ]
        models.sort(key=lambda m: (-(m["created"] or 0), m["id"] or ""))
        self._models = models
        self._fetched_at = time.monotonic()
        return models


# ─── uploaded media ───────────────────────────────────────────────────────────


def detect_kind(filename: str) -> str:
    return SUFFIX_KIND.get(Path(filename).suffix.lower(), "photo")


def media_meta_path(media_dir: Path, media_id: str) -> Path:
    return media_dir / media_id / "meta.json"


def load_media_library(media_dir: Path) -> list[dict[str, Any]]:
    items = []
    for meta_path in sorted(media_dir.glob("*/meta.json"), reverse=True):
        try:
            items.append(json.loads(meta_path.read_text(encoding="utf-8")))
        except (OSError, json.JSONDecodeError):
            continue
    items.sort(key=lambda item: item.get("uploaded_at", ""), reverse=True)
    return items


async def store_upload(media_dir: Path, filename: str, kind: str, raw: bytes) -> dict[str, Any]:
    """Save an upload and run it through the bot's own normalization pipeline."""
    media_id = uuid.uuid4().hex[:12]
    folder = media_dir / media_id
    folder.mkdir(parents=True, exist_ok=True)
    suffix = Path(filename).suffix.lower() or ".bin"
    raw_path = folder / f"raw{suffix}"
    raw_path.write_bytes(raw)

    mime = mimetypes.guess_type(filename)[0] or "application/octet-stream"
    source = media.MediaSource(kind=kind, mime_type=mime)
    normalized = await asyncio.to_thread(media._normalize_media, source, raw_path, folder)
    norm_path = folder / f"normalized{normalized.path.suffix}"
    if normalized.path != norm_path:
        norm_path.write_bytes(normalized.path.read_bytes())

    duration = await asyncio.to_thread(media._probe_duration, norm_path)
    meta = {
        "id": media_id,
        "filename": filename,
        "kind": kind,
        "duration_seconds": duration,
        "modality": KIND_MODALITY.get(kind, "image"),
        "uploaded_at": datetime.now(timezone.utc).isoformat(),
        "raw_bytes": len(raw),
        "normalized_bytes": norm_path.stat().st_size,
        "raw_mime": mime,
        "normalized_mime": normalized.mime_type,
        "normalized_file": norm_path.name,
    }
    media_meta_path(media_dir, media_id).write_text(
        json.dumps(meta, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    return meta


async def stream_media_model(
    client: httpx.AsyncClient,
    item: dict[str, Any],
    media_part: dict[str, Any],
    model: str,
    attempt: int,
    options: dict[str, Any],
    queue: asyncio.Queue,
    semaphore: asyncio.Semaphore,
) -> None:
    """Describe one uploaded file with one model, streaming the reasoning."""
    key = f"{model}#{attempt}"
    effort = options.get("effort") or ""

    async def emit(event: dict[str, Any]) -> None:
        await queue.put(
            {"model": model, "attempt": attempt, "key": key, "effort": effort, **event}
        )

    body: dict[str, Any] = {
        "model": model,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": ai._media_description_prompt(item["kind"])},
                    media_part,
                ],
            }
        ],
        "stream": True,
        "usage": {"include": True},
    }
    if options.get("max_tokens"):
        body["max_tokens"] = int(options["max_tokens"])
    show_reasoning = bool(options.get("show_reasoning", True))
    if effort and effort != "off":
        body["reasoning"] = {"enabled": True, "effort": effort, "exclude": not show_reasoning}
    elif not effort:
        body["reasoning"] = {
            "enabled": True,
            "effort": settings.OPENROUTER_MEDIA_REASONING_EFFORT or "medium",
            "exclude": not show_reasoning,
        }
    # Temperature is never set — production doesn't set it either.

    started = time.monotonic()
    parts: list[str] = []
    usage_payload: dict[str, Any] = {}

    async with semaphore:
        await emit({"type": "start"})
        try:
            async with client.stream(
                "POST", settings.OPENROUTER_BASE_URL, json=body, headers=ai._openrouter_headers()
            ) as response:
                if response.status_code >= 400:
                    raw = await response.aread()
                    await emit(
                        {
                            "type": "result",
                            "status": f"http_{response.status_code}",
                            "message": ai._redact(raw.decode("utf-8", "replace"))[:400],
                            "seconds": time.monotonic() - started,
                            "usage": asdict(ai.TokenUsage()),
                        }
                    )
                    return
                async for line in response.aiter_lines():
                    kind, payload = parse_stream_line(line)
                    if kind == "skip":
                        continue
                    if kind == "done":
                        break
                    if kind == "chunk":
                        if payload.get("usage"):
                            usage_payload = payload
                        continue
                    delta, chunk = payload
                    if chunk.get("usage"):
                        usage_payload = chunk
                    if kind == "reasoning":
                        await emit({"type": "reasoning", "delta": delta})
                    else:
                        parts.append(delta)
                        await emit({"type": "content", "delta": delta})
        except Exception as exc:  # noqa: BLE001 — one model must not kill the batch
            await emit(
                {
                    "type": "result",
                    "status": type(exc).__name__,
                    "message": ai._redact(str(exc))[:400],
                    "seconds": time.monotonic() - started,
                    "usage": asdict(ai.TokenUsage()),
                }
            )
            return

    description = "".join(parts).strip()
    await emit(
        {
            "type": "result",
            "status": "ok" if description else "empty_response",
            "message": "" if description else "Модель вернула пустое описание",
            "description": description,
            "seconds": time.monotonic() - started,
            "usage": asdict(ai._extract_usage(usage_payload)),
        }
    )


# ─── one streamed evaluation ──────────────────────────────────────────────────


def build_body(day: AuditDay, model: str, options: dict[str, Any]) -> dict[str, Any]:
    """Same request the bot sends, with the model and reasoning knobs swapped."""
    body: dict[str, Any] = {
        "model": model,
        "messages": day.body["messages"],
        "stream": True,
        "usage": {"include": True},
    }
    if day.body.get("response_format"):
        body["response_format"] = day.body["response_format"]
    max_tokens = options.get("max_tokens") or day.body.get("max_tokens")
    if max_tokens:
        body["max_tokens"] = int(max_tokens)

    effort = options.get("effort") or ""
    show_reasoning = bool(options.get("show_reasoning", True))
    if effort == "off":
        pass
    elif effort:
        body["reasoning"] = {"enabled": True, "effort": effort, "exclude": not show_reasoning}
    elif day.body.get("reasoning"):
        body["reasoning"] = {**day.body["reasoning"], "exclude": not show_reasoning}
    # Temperature is never set — production doesn't set it either.
    return body


def parse_selections(raw: Any, default_effort: str) -> list[tuple[str, str]]:
    """Normalise the models field into (model_id, effort) pairs.

    Accepts plain ids or {"id": ..., "effort": ...}; a missing or null effort
    means "use the global effort chosen in the UI".
    """
    selections: list[tuple[str, str]] = []
    for item in raw or []:
        if isinstance(item, str):
            model_id, effort = item.strip(), default_effort
        elif isinstance(item, dict):
            model_id = str(item.get("id") or "").strip()
            effort = item.get("effort")
            effort = default_effort if effort is None else str(effort)
        else:
            continue
        if model_id and not any(model_id == existing for existing, _ in selections):
            selections.append((model_id, effort))
    return selections


def parse_stream_line(line: str) -> tuple[str, Any]:
    """Classify one SSE line from OpenRouter.

    Returns ("skip"|"done"|"reasoning"|"content"|"chunk", payload). Keep-alive
    comments (": OPENROUTER PROCESSING") and blank lines are skipped rather than
    fed to json.loads, which would raise mid-stream.
    """
    line = line.strip()
    if not line or line.startswith(":") or not line.startswith("data: "):
        return "skip", None
    data = line[6:].strip()
    if data == "[DONE]":
        return "done", None
    try:
        chunk = json.loads(data)
    except json.JSONDecodeError:
        return "skip", None
    choices = chunk.get("choices") or []
    delta = (choices[0].get("delta") or {}) if choices else {}
    if delta.get("reasoning"):
        return "reasoning", (delta["reasoning"], chunk)
    if delta.get("content"):
        return "content", (delta["content"], chunk)
    return "chunk", chunk


async def stream_model(
    client: httpx.AsyncClient,
    day: AuditDay,
    model: str,
    attempt: int,
    options: dict[str, Any],
    queue: asyncio.Queue,
    semaphore: asyncio.Semaphore,
) -> None:
    key = f"{model}#{attempt}"
    effort = options.get("effort") or ""

    async def emit(event: dict[str, Any]) -> None:
        await queue.put(
            {"model": model, "attempt": attempt, "key": key, "effort": effort, **event}
        )

    body = build_body(day, model, options)
    started = time.monotonic()
    content_parts: list[str] = []
    reasoning_chars = 0
    usage_payload: dict[str, Any] = {}

    async with semaphore:
        await emit({"type": "start"})
        try:
            async with client.stream(
                "POST",
                settings.OPENROUTER_BASE_URL,
                json=body,
                headers=ai._openrouter_headers(),
            ) as response:
                if response.status_code >= 400:
                    raw = await response.aread()
                    await emit(
                        {
                            "type": "error",
                            "status": f"http_{response.status_code}",
                            "message": ai._redact(raw.decode("utf-8", "replace"))[:400],
                            "seconds": time.monotonic() - started,
                        }
                    )
                    return
                async for line in response.aiter_lines():
                    kind, payload = parse_stream_line(line)
                    if kind == "skip":
                        continue
                    if kind == "done":
                        break
                    if kind == "chunk":
                        if payload.get("usage"):
                            usage_payload = payload
                        continue
                    delta, chunk = payload
                    if chunk.get("usage"):
                        usage_payload = chunk
                    if kind == "reasoning":
                        reasoning_chars += len(delta)
                        await emit({"type": "reasoning", "delta": delta})
                    else:
                        content_parts.append(delta)
                        await emit({"type": "content", "delta": delta})
        except Exception as exc:  # noqa: BLE001 — one model must not kill the batch
            await emit(
                {
                    "type": "error",
                    "status": type(exc).__name__,
                    "message": ai._redact(str(exc))[:400],
                    "seconds": time.monotonic() - started,
                }
            )
            return

    elapsed = time.monotonic() - started
    content = "".join(content_parts).strip()
    usage = ai._extract_usage(usage_payload)
    result: dict[str, Any] = {
        "type": "result",
        "seconds": elapsed,
        "reasoning_chars": reasoning_chars,
        "usage": asdict(usage),
        "raw_content": content,
    }

    if not content:
        result.update({"status": "empty_response", "message": "Модель не вернула ответ"})
        await emit(result)
        return

    try:
        if day.include_day_verdict:
            entries, verdict, choice, _language, verdict_error = ai._parse_day_payload_safely(
                content, require_language=False
            )
            result["verdict_error"] = verdict_error
            if verdict:
                result["should_publish"] = verdict.should_publish
                result["reason_code"] = verdict.reason_code
                result["reason_text"] = verdict.reason_text
        else:
            entries, choice = ai._parse_score_payload(content)
    except (json.JSONDecodeError, ValueError) as exc:
        result.update({"status": "parse_failed", "message": str(exc)[:300]})
        await emit(result)
        return

    scores: dict[int, float] = {}
    for entry in entries or []:
        try:
            scores[int(entry["id"])] = max(0.0, min(10.0, float(entry["score"]))) / 10.0
        except (KeyError, TypeError, ValueError):
            continue
    ranked = sorted(scores.items(), key=lambda pair: pair[1], reverse=True)
    primary_id = (choice.primary_id if choice else None) or (ranked[0][0] if ranked else None)
    by_id = {m.get("i"): m for m in day.messages}

    result.update(
        {
            "status": "ok" if primary_id is not None else "no_choice",
            "primary_id": primary_id,
            "primary_score": scores.get(primary_id) if primary_id is not None else None,
            "pick": message_view(by_id[primary_id]) if primary_id in by_id else None,
            "context_ids": choice.context_ids if choice else [],
            "context_needed": bool(choice.context_needed) if choice else False,
            "context": context_messages(day, choice, primary_id),
            "matches_production": primary_id == day.production_primary_id,
            "top": [
                {
                    "id": msg_id,
                    "score": score,
                    "message": message_view(by_id[msg_id]) if msg_id in by_id else None,
                }
                for msg_id, score in ranked[:5]
            ],
        }
    )
    await emit(result)


# ─── http handlers ────────────────────────────────────────────────────────────


async def handle_index(request: web.Request) -> web.Response:
    return web.Response(text=UI_FILE.read_text(encoding="utf-8"), content_type="text/html")


async def handle_days(request: web.Request) -> web.Response:
    days = load_days(request.app[AUDIT_PATH])
    request.app[CACHE_KEY]["days"] = days
    return web.json_response(
        {
            "audit_path": str(request.app[AUDIT_PATH]),
            "days": [day_view(day) for day in days.values()],
        }
    )


async def handle_day_messages(request: web.Request) -> web.Response:
    days = request.app[CACHE_KEY].get("days") or load_days(request.app[AUDIT_PATH])
    day = days.get(request.match_info["day"])
    if not day:
        raise web.HTTPNotFound(text="unknown day")
    return web.json_response({"day": day.day, "messages": [message_view(m) for m in day.messages]})


async def handle_models(request: web.Request) -> web.Response:
    try:
        models = await request.app[CATALOG_KEY].get()
    except Exception as exc:  # noqa: BLE001 — the UI still works with manual model ids
        return web.json_response({"models": [], "error": str(exc)[:200]})
    configured = [
        m
        for m in (
            settings.OPENROUTER_EVAL_MODEL,
            settings.OPENROUTER_EVAL_FALLBACK_MODEL,
        )
        if m
    ]
    return web.json_response({"models": models, "configured": configured})


async def handle_history(request: web.Request) -> web.Response:
    directory = request.app[HISTORY_DIR]
    entries = []
    for path in sorted(directory.glob("*.json"), reverse=True)[:50]:
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        entries.append(
            {
                "id": path.stem,
                "kind": payload.get("kind") or "eval",
                "media_filename": (payload.get("media") or {}).get("filename"),
                "day": payload.get("day"),
                "created_at": payload.get("created_at"),
                "models": payload.get("models", []),
                "effort": payload.get("effort"),
                "runs": payload.get("runs"),
                "total_cost_usd": payload.get("total_cost_usd"),
            }
        )
    return web.json_response({"history": entries})


async def handle_history_item(request: web.Request) -> web.Response:
    path = request.app[HISTORY_DIR] / f"{request.match_info['run_id']}.json"
    if not path.exists():
        raise web.HTTPNotFound(text="unknown run")
    return web.json_response(json.loads(path.read_text(encoding="utf-8")))


async def handle_media_list(request: web.Request) -> web.Response:
    return web.json_response({"media": load_media_library(request.app[MEDIA_DIR])})


async def handle_media_upload(request: web.Request) -> web.Response:
    reader = await request.multipart()
    filename = ""
    kind = ""
    raw = b""
    while True:
        part = await reader.next()
        if part is None:
            break
        if part.name == "kind":
            kind = (await part.text()).strip()
        elif part.name == "file":
            filename = part.filename or "upload.bin"
            chunks = []
            size = 0
            while True:
                chunk = await part.read_chunk()
                if not chunk:
                    break
                size += len(chunk)
                if size > MAX_UPLOAD_BYTES:
                    raise web.HTTPRequestEntityTooLarge(
                        max_size=MAX_UPLOAD_BYTES, actual_size=size
                    )
                chunks.append(chunk)
            raw = b"".join(chunks)
    if not raw:
        raise web.HTTPBadRequest(text="no file received")
    kind = kind or detect_kind(filename)
    if kind not in KIND_MODALITY:
        raise web.HTTPBadRequest(text=f"unknown kind {kind}")
    try:
        meta = await store_upload(request.app[MEDIA_DIR], filename, kind, raw)
    except Exception as exc:  # noqa: BLE001 — ffmpeg/Pillow failures belong in the UI
        raise web.HTTPBadRequest(text=f"{type(exc).__name__}: {exc}") from exc
    return web.json_response(meta)


def _media_item(request: web.Request) -> dict[str, Any]:
    path = media_meta_path(request.app[MEDIA_DIR], request.match_info["media_id"])
    if not path.exists():
        raise web.HTTPNotFound(text="unknown media")
    return json.loads(path.read_text(encoding="utf-8"))


async def handle_media_preview(request: web.Request) -> web.FileResponse:
    item = _media_item(request)
    folder = request.app[MEDIA_DIR] / item["id"]
    return web.FileResponse(folder / item["normalized_file"])


async def handle_media_delete(request: web.Request) -> web.Response:
    item = _media_item(request)
    folder = request.app[MEDIA_DIR] / item["id"]
    for child in folder.iterdir():
        child.unlink()
    folder.rmdir()
    return web.json_response({"deleted": item["id"]})


async def handle_media_run(request: web.Request) -> web.StreamResponse:
    if not settings.OPENROUTER_API_KEY:
        raise web.HTTPBadRequest(text="OPENROUTER_API_KEY is empty — set it in .env")

    payload = await request.json()
    meta_path = media_meta_path(request.app[MEDIA_DIR], str(payload.get("media_id") or ""))
    if not meta_path.exists():
        raise web.HTTPBadRequest(text="unknown media")
    item = json.loads(meta_path.read_text(encoding="utf-8"))
    selections = parse_selections(payload.get("models"), payload.get("effort") or "")
    if not selections:
        raise web.HTTPBadRequest(text="pick at least one model")

    runs = max(1, min(int(payload.get("runs") or 1), 5))
    concurrency = max(1, min(int(payload.get("concurrency") or 4), 12))
    base_options = {
        "max_tokens": payload.get("max_tokens"),
        "show_reasoning": payload.get("show_reasoning", True),
    }

    folder = request.app[MEDIA_DIR] / item["id"]
    media_part = await asyncio.to_thread(
        ai._media_content_part,
        path=folder / item["normalized_file"],
        mime_type=item["normalized_mime"],
        media_kind=item["kind"],
    )

    response = web.StreamResponse(
        headers={
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        }
    )
    await response.prepare(request)

    async def send(event: dict[str, Any]) -> None:
        await response.write(f"data: {json.dumps(event, ensure_ascii=False)}\n\n".encode())

    queue: asyncio.Queue = asyncio.Queue()
    semaphore = asyncio.Semaphore(concurrency)
    collected: list[dict[str, Any]] = []

    async with httpx.AsyncClient(timeout=REQUEST_TIMEOUT_SECONDS) as client:
        tasks = [
            asyncio.create_task(
                stream_media_model(
                    client,
                    item,
                    media_part,
                    model,
                    attempt,
                    {**base_options, "effort": effort},
                    queue,
                    semaphore,
                )
            )
            for model, effort in selections
            for attempt in range(1, runs + 1)
        ]
        gathered = asyncio.gather(*tasks)
        try:
            await send(
                {
                    "type": "run_start",
                    "media": item,
                    "models": [model for model, _effort in selections],
                    "runs": runs,
                }
            )
            while not gathered.done() or not queue.empty():
                try:
                    event = await asyncio.wait_for(queue.get(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                if event.get("type") == "result":
                    collected.append(event)
                await send(event)
            await gathered
        except ConnectionResetError:
            gathered.cancel()
            return response

    total_cost = sum((r.get("usage") or {}).get("cost_usd") or 0.0 for r in collected)
    record = {
        "id": uuid.uuid4().hex[:12],
        "created_at": datetime.now(timezone.utc).isoformat(),
        "kind": "media",
        "media": item,
        "models": [model for model, _effort in selections],
        "efforts": {model: effort for model, effort in selections},
        "runs": runs,
        "total_cost_usd": total_cost,
        "results": collected,
    }
    (request.app[HISTORY_DIR] / f"{record['id']}.json").write_text(
        json.dumps(record, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    await send({"type": "run_done", "total_cost_usd": total_cost, "history_id": record["id"]})
    await response.write_eof()
    return response


async def handle_run(request: web.Request) -> web.StreamResponse:
    if not settings.OPENROUTER_API_KEY:
        raise web.HTTPBadRequest(text="OPENROUTER_API_KEY is empty — set it in .env")

    payload = await request.json()
    days = request.app[CACHE_KEY].get("days") or load_days(request.app[AUDIT_PATH])
    day = days.get(payload.get("day"))
    if not day:
        raise web.HTTPBadRequest(text="unknown day")
    default_effort = payload.get("effort") or ""
    selections = parse_selections(payload.get("models"), default_effort)
    if not selections:
        raise web.HTTPBadRequest(text="pick at least one model")
    models = [model_id for model_id, _effort in selections]

    base_options = {
        "max_tokens": payload.get("max_tokens"),
        "show_reasoning": payload.get("show_reasoning", True),
    }
    runs = max(1, min(int(payload.get("runs") or 1), 5))
    concurrency = max(1, min(int(payload.get("concurrency") or 4), 12))

    response = web.StreamResponse(
        headers={
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        }
    )
    await response.prepare(request)

    async def send(event: dict[str, Any]) -> None:
        await response.write(f"data: {json.dumps(event, ensure_ascii=False)}\n\n".encode())

    queue: asyncio.Queue = asyncio.Queue()
    semaphore = asyncio.Semaphore(concurrency)
    collected: list[dict[str, Any]] = []

    async with httpx.AsyncClient(timeout=REQUEST_TIMEOUT_SECONDS) as client:
        tasks = [
            asyncio.create_task(
                stream_model(
                    client, day, model, attempt, {**base_options, "effort": effort}, queue, semaphore
                )
            )
            for model, effort in selections
            for attempt in range(1, runs + 1)
        ]
        gathered = asyncio.gather(*tasks)
        try:
            await send({"type": "run_start", "day": day.day, "models": models, "runs": runs})
            while not gathered.done() or not queue.empty():
                try:
                    event = await asyncio.wait_for(queue.get(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                if event.get("type") in {"result", "error"}:
                    collected.append(event)
                await send(event)
            await gathered
        except ConnectionResetError:
            gathered.cancel()
            return response

    total_cost = sum(
        (item.get("usage") or {}).get("cost_usd") or 0.0
        for item in collected
        if item.get("type") == "result"
    )
    record = {
        "id": uuid.uuid4().hex[:12],
        "created_at": datetime.now(timezone.utc).isoformat(),
        "day": day.day,
        "models": models,
        "efforts": {model: effort for model, effort in selections},
        "runs": runs,
        "effort": default_effort,
        "max_tokens": base_options["max_tokens"],
        "production_primary_id": day.production_primary_id,
        "production_model": day.production_model,
        "total_cost_usd": total_cost,
        "results": collected,
    }
    path = request.app[HISTORY_DIR] / f"{record['id']}.json"
    path.write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding="utf-8")

    await send({"type": "run_done", "total_cost_usd": total_cost, "history_id": record["id"]})
    await response.write_eof()
    return response


def build_app(audit_path: Path, history_dir: Path, media_dir: Path | None = None) -> web.Application:
    app = web.Application(client_max_size=MAX_UPLOAD_BYTES)
    app[AUDIT_PATH] = audit_path
    app[HISTORY_DIR] = history_dir
    app[MEDIA_DIR] = media_dir or history_dir.parent / MEDIA_DIRNAME
    app[MEDIA_DIR].mkdir(parents=True, exist_ok=True)
    app[CATALOG_KEY] = ModelCatalog()
    # Mutated per request, so it lives inside a container rather than on the app.
    app[CACHE_KEY] = {}
    history_dir.mkdir(parents=True, exist_ok=True)
    app.add_routes(
        [
            web.get("/", handle_index),
            web.get("/api/days", handle_days),
            web.get("/api/days/{day}/messages", handle_day_messages),
            web.get("/api/models", handle_models),
            web.get("/api/history", handle_history),
            web.get("/api/history/{run_id}", handle_history_item),
            web.post("/api/run", handle_run),
            web.get("/api/media", handle_media_list),
            web.post("/api/media", handle_media_upload),
            web.get("/api/media/{media_id}/preview", handle_media_preview),
            web.delete("/api/media/{media_id}", handle_media_delete),
            web.post("/api/media/run", handle_media_run),
        ]
    )
    return app


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--audit", default=str(Path(settings.LOGS_PATH) / "ai_audit.jsonl"))
    parser.add_argument("--history-dir", default=str(Path(settings.LOGS_PATH) / HISTORY_DIRNAME))
    parser.add_argument("--media-dir", default=str(Path(settings.LOGS_PATH) / MEDIA_DIRNAME))
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8730)
    parser.add_argument("--open", action="store_true", help="Open the hub in a browser on start.")
    args = parser.parse_args()

    app = build_app(Path(args.audit), Path(args.history_dir), Path(args.media_dir))
    url = f"http://{args.host}:{args.port}"
    print(f"Quoto bench hub → {url}   (audit: {args.audit})")
    if args.open:
        webbrowser.open(url)
    web.run_app(app, host=args.host, port=args.port, print=None)


if __name__ == "__main__":
    main()
