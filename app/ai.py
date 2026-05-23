import asyncio
import base64
from datetime import datetime, timedelta, timezone
import json
import logging
import os
import re
from dataclasses import dataclass, field
from pathlib import Path
from threading import Lock
from typing import Any
from uuid import uuid4

import httpx

from .config import settings, setup_logging
from .quote_status import REASON_BORING_DAY, REASON_WORTHY

log = setup_logging(logging.getLogger(__name__))
_AUDIT_LOG_NAME = "ai_audit.jsonl"
_AUDIT_RETENTION = timedelta(days=7)
_audit_log_lock = Lock()

_SCORE_PROMPT = (
    "You are a live Telegram group quote curator, not a literary critic. "
    "You will receive a JSON array of compact message objects. `i` is the internal message id; use ONLY `i` "
    "as output `id`, `primary_id`, and `context_ids`. `rp` is the internal id of the replied-to message. "
    "`kind` is the Telegram message format. `desc` is a neutral media description prepared by another model. "
    "`caption`/`t` are user text. Judge the whole moment: text, caption, media description, reactions, and reply context. "
    "Use reactions as lightweight context when present. "
    "Rate each message on a scale from 0 to 10 based on how quotable it feels inside this chat: a funny word, "
    "absurd phrase, rude but memorable line, chaotic combination, dry reply, local meme, or short dialogue can win. "
    "Do not require polished jokes. Slang, typos, profanity, dumb-funny phrasing, and local chaos are valid. "
    "A quote can be one standalone message or a short dialogue. If the selected quote is short, a reply, "
    "a punchline, a refusal, a reaction, or only funny because of setup, include the full minimal context block "
    "instead of publishing the punchline alone. "
    "Use `context_ids` only for consecutive messages or a real reply thread. Keep them in reading order, include "
    "`primary_id`, and set `context_needed` to true when the selected moment needs more than one message. "
    "Never combine unrelated messages. Max 5 context messages. "
    "Respond ONLY with a valid JSON object in this format: "
    "{\"quote\": {\"primary_id\": <id>, \"context_ids\": [<id>], \"context_needed\": <true|false>}, "
    "\"messages\": [{\"id\": <id>, \"score\": <0-10>}]}."
)

_DAY_PROMPT = (
    "You are a live Telegram group quote curator, not a literary critic. "
    "You will receive a JSON array of compact message objects from one quote day. `i` is the internal message id; "
    "use ONLY `i` as output `id`, `primary_id`, and `context_ids`. `rp` is the internal id of the replied-to message. "
    "`kind` is the Telegram message format. `desc` is a neutral media description prepared by another model. "
    "`caption`/`t` are user text. Judge the whole moment: text, caption, media description, reactions, and reply context. "
    "Use reactions as lightweight context when present. "
    "First, rate every message from 0 to 10 based on how quotable it feels inside this chat: a funny word, "
    "absurd phrase, rude but memorable line, chaotic combination, dry reply, local meme, or short dialogue can win. "
    "Do not require polished jokes. Slang, typos, profanity, dumb-funny phrasing, and local chaos are valid. "
    "Then decide whether the day is worthy of a public 'quote of the day' announcement. "
    "Prefer publishing if there is at least one mildly funny, weird, memorable, or locally quotable moment. "
    "Set should_publish to false only when the best candidate is truly nothing: generic small talk, link-only noise, "
    "admin/meta noise, empty reaction noise, or a phrase that would look dull as quote of the day. "
    "A quote can be one standalone message or a short dialogue. If the selected quote is short, a reply, "
    "a punchline, a refusal, a reaction, or only funny because of setup, include the full minimal context block "
    "instead of publishing the punchline alone. "
    "Use `context_ids` only for consecutive messages or a real reply thread. Keep them in reading order, include "
    "`primary_id`, and set `context_needed` to true when the selected moment needs more than one message. "
    "Never combine unrelated messages. Max 5 context messages. "
    "Respond ONLY with a valid JSON object in this format: "
    "{\"day\": {\"should_publish\": true, \"reason_code\": \"worthy\", \"reason_text\": \"short reason\"}, "
    "\"quote\": {\"primary_id\": <id>, \"context_ids\": [<id>], \"context_needed\": <true|false>}, "
    "\"messages\": [{\"id\": <id>, \"score\": <0-10>}]}."
    "Respond in the language of the messages for reason_text."
)

_MEDIA_DESCRIPTION_PROMPT = (
    "Опиши содержимое этого медиа для человека, который его не видит или не слышит. "
    "Одним цельным текстом: кто или что изображено/звучит, что происходит, действия, обстановка, "
    "важные детали, эмоции, заметный текст/надписи, речь/звуки и последовательность событий для видео "
    "или анимации. Пиши кратко, но достаточно полно. Ответ должен состоять только из описания."
)

_SCORE_RESPONSE_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "quote": {
            "type": "object",
            "additionalProperties": False,
            "properties": {
                "primary_id": {"type": ["integer", "null"]},
                "context_ids": {"type": "array", "items": {"type": "integer"}},
                "context_needed": {"type": "boolean"},
            },
            "required": ["primary_id", "context_ids", "context_needed"],
        },
        "messages": {
            "type": "array",
            "items": {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "id": {"type": "integer"},
                    "score": {"type": "number", "minimum": 0, "maximum": 10},
                },
                "required": ["id", "score"],
            },
        },
    },
    "required": ["quote", "messages"],
}

_DAY_RESPONSE_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "day": {
            "type": "object",
            "additionalProperties": False,
            "properties": {
                "should_publish": {"type": "boolean"},
                "reason_code": {"type": "string"},
                "reason_text": {"type": "string"},
            },
            "required": ["should_publish", "reason_code", "reason_text"],
        },
        **_SCORE_RESPONSE_SCHEMA["properties"],
    },
    "required": ["day", "quote", "messages"],
}


@dataclass
class DayVerdict:
    should_publish: bool = True
    reason_code: str = REASON_WORTHY
    reason_text: str = ""


@dataclass
class QuoteContextChoice:
    primary_id: int | None = None
    context_ids: list[int] = field(default_factory=list)
    context_needed: bool = False


@dataclass
class EvaluationResult:
    scores: dict[int, float]
    actual_model: str
    requested_model: str | None = None
    status: str = "parsed"
    request_id: str | None = None
    day_verdict: DayVerdict | None = None
    day_verdict_error: str | None = None
    quote_choice: QuoteContextChoice | None = None


class DayVerdictParseError(ValueError):
    pass


@dataclass
class MediaDescriptionResult:
    description: str
    actual_model: str
    prompt_tokens: int | None = None
    completion_tokens: int | None = None
    total_tokens: int | None = None


def _is_retryable_http_status(status_code: int) -> bool:
    return status_code == 429 or 500 <= status_code < 600


async def evaluate_messages(
    messages: list[dict[str, Any]],
    include_day_verdict: bool = False,
) -> EvaluationResult:
    if not messages:
        return EvaluationResult(
            scores={},
            actual_model=settings.OPENROUTER_EVAL_MODEL,
            requested_model=settings.OPENROUTER_EVAL_MODEL,
            status="empty",
        )

    neutral_scores = {int(msg["id"]): 0.5 for msg in messages}
    default_model = settings.OPENROUTER_EVAL_MODEL
    default_verdict = DayVerdict()

    if not settings.OPENROUTER_API_KEY:
        log.warning("⚠️ OPENROUTER_API_KEY не задан — AI-оценка пропущена")
        return EvaluationResult(
            scores=neutral_scores,
            actual_model=default_model,
            requested_model=default_model,
            status="ai_failed",
            day_verdict=default_verdict if not include_day_verdict else None,
            day_verdict_error=(
                "OPENROUTER_API_KEY is not configured for automatic day verdict evaluation."
                if include_day_verdict
                else None
            ),
        )

    request_id = uuid4().hex
    user_payload = json.dumps([_message_payload(m) for m in messages], ensure_ascii=False)

    body = {
        "model": settings.OPENROUTER_EVAL_MODEL,
        "messages": [
            {"role": "system", "content": _DAY_PROMPT if include_day_verdict else _SCORE_PROMPT},
            {"role": "user", "content": user_payload},
        ],
        "response_format": _response_format(include_day_verdict),
    }
    if settings.OPENROUTER_EVAL_REASONING_EFFORT:
        body["reasoning"] = {
            "enabled": True,
            "effort": settings.OPENROUTER_EVAL_REASONING_EFFORT,
            "exclude": True,
        }

    headers = {
        "Authorization": f"Bearer {settings.OPENROUTER_API_KEY}",
        "Content-Type": "application/json",
    }

    max_retries = 3
    retry_delays = [5, 15, 30]

    for attempt in range(max_retries):
        audit_record: dict[str, Any] = {
            "request_id": request_id,
            "attempt": attempt + 1,
            "include_day_verdict": include_day_verdict,
            "message_count": len(messages),
            "request": {
                "url": settings.OPENROUTER_BASE_URL,
                "body": body,
            },
        }
        try:
            async with httpx.AsyncClient(timeout=60.0) as client:
                response = await client.post(
                    settings.OPENROUTER_BASE_URL,
                    json=body,
                    headers=headers,
                )
                audit_record["response"] = {
                    "status_code": response.status_code,
                    "text": response.text,
                }
                response.raise_for_status()

            data = response.json()
            actual_model = data.get("model", settings.OPENROUTER_EVAL_MODEL)
            content = (data.get("choices") or [{}])[0].get("message", {}).get("content", "") or ""
            content = content.strip()
            audit_record["response"]["model"] = actual_model
            audit_record["response"]["content"] = content

            if not content:
                audit_record["result"] = {
                    "status": "empty_response",
                    "actual_model": actual_model,
                }
                _write_ai_audit_record(audit_record)
                if attempt < max_retries - 1:
                    delay = retry_delays[attempt]
                    log.warning(
                        f"⏳ Пустой ответ AI, повтор через {delay}с (попытка {attempt + 1}/{max_retries})"
                    )
                    await asyncio.sleep(delay)
                    continue
                log.warning(f"⚠️ {actual_model} вернул пустой ответ после всех попыток")
                return EvaluationResult(
                    scores=neutral_scores,
                    actual_model=actual_model,
                    requested_model=default_model,
                    status="ai_failed",
                    request_id=request_id,
                    day_verdict=default_verdict if not include_day_verdict else None,
                    day_verdict_error=(
                        "AI returned an empty response for automatic day verdict evaluation."
                        if include_day_verdict
                        else None
                    ),
                )

            if include_day_verdict:
                entries, verdict, quote_choice, verdict_error = _parse_day_payload_safely(content)
            else:
                entries, quote_choice = _parse_score_payload(content)
                verdict = None
                verdict_error = None

            scores = _normalize_scores(messages, entries)
            audit_record["result"] = {
                "status": "parsed",
                "actual_model": actual_model,
                "score_count": len(scores),
                "quote_choice": _quote_choice_audit_payload(quote_choice),
                "day_verdict": _day_verdict_audit_payload(verdict),
                "day_verdict_error": verdict_error,
            }
            _write_ai_audit_record(audit_record)
            log.debug(f"🤖 {actual_model} оценил {len(scores)} сообщений")
            return EvaluationResult(
                scores=scores,
                actual_model=actual_model,
                requested_model=default_model,
                status="parsed",
                request_id=request_id,
                day_verdict=verdict,
                day_verdict_error=verdict_error,
                quote_choice=quote_choice,
            )

        except httpx.HTTPStatusError as e:
            audit_record["error"] = {
                "type": type(e).__name__,
                "message": str(e),
                "status_code": e.response.status_code,
            }
            _write_ai_audit_record(audit_record)
            if _is_retryable_http_status(e.response.status_code) and attempt < max_retries - 1:
                delay = retry_delays[attempt]
                log.warning(
                    f"⏳ OpenRouter HTTP {e.response.status_code}, повтор через {delay}с "
                    f"(попытка {attempt + 1}/{max_retries})"
                )
                await asyncio.sleep(delay)
                continue
            log.error(f"OpenRouter HTTP {e.response.status_code}: {e.response.text[:200]}")
        except httpx.RequestError as e:
            audit_record["error"] = {
                "type": type(e).__name__,
                "message": str(e),
            }
            _write_ai_audit_record(audit_record)
            if attempt < max_retries - 1:
                delay = retry_delays[attempt]
                log.warning(
                    f"⏳ Ошибка сети OpenRouter ({type(e).__name__}), повтор через {delay}с "
                    f"(попытка {attempt + 1}/{max_retries})"
                )
                await asyncio.sleep(delay)
                continue
            log.error(f"Ошибка сети OpenRouter: {e}")
        except (json.JSONDecodeError, KeyError, IndexError, ValueError) as e:
            audit_record["error"] = {
                "type": type(e).__name__,
                "message": str(e),
            }
            _write_ai_audit_record(audit_record)
            log.error(f"Ошибка парсинга ответа AI: {e}")
        except Exception as e:
            audit_record["error"] = {
                "type": type(e).__name__,
                "message": str(e),
            }
            _write_ai_audit_record(audit_record)
            log.error(f"Ошибка при запросе к OpenRouter: {e}")

        break

    failure_status = "parse_failed" if "audit_record" in locals() and audit_record.get("error", {}).get("type") in {
        "JSONDecodeError",
        "KeyError",
        "IndexError",
        "ValueError",
    } else "ai_failed"

    return EvaluationResult(
        scores=neutral_scores,
        actual_model=default_model,
        requested_model=default_model,
        status=failure_status,
        request_id=request_id if "request_id" in locals() else None,
        day_verdict=default_verdict if not include_day_verdict else None,
        day_verdict_error=(
            "AI evaluation failed before a valid automatic day verdict was produced."
            if include_day_verdict
            else None
        ),
    )


def _write_ai_audit_record(record: dict[str, Any]) -> None:
    try:
        path = Path(settings.LOGS_PATH) / _AUDIT_LOG_NAME
        os.makedirs(path.parent, exist_ok=True)
        now = datetime.now(timezone.utc)
        payload = {
            "created_at": now.isoformat(),
            **record,
        }
        line = json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
        with _audit_log_lock:
            _prune_ai_audit_log(path, now)
            with path.open("a", encoding="utf-8") as handle:
                handle.write(line)
                handle.write("\n")
    except Exception:
        pass


def _prune_ai_audit_log(path: Path, now: datetime) -> None:
    if not path.exists():
        return

    cutoff = now - _AUDIT_RETENTION
    kept_lines: list[str] = []
    changed = False

    with path.open("r", encoding="utf-8") as handle:
        for raw_line in handle:
            line = raw_line.rstrip("\n")
            if not line.strip():
                changed = True
                continue
            if _is_audit_line_expired(line, cutoff):
                changed = True
                continue
            kept_lines.append(line)

    if not changed:
        return

    with path.open("w", encoding="utf-8") as handle:
        for line in kept_lines:
            handle.write(line)
            handle.write("\n")


def _is_audit_line_expired(line: str, cutoff: datetime) -> bool:
    try:
        parsed = json.loads(line)
        created_at = parsed.get("created_at")
        if not isinstance(created_at, str):
            return False
        created = datetime.fromisoformat(created_at)
        if created.tzinfo is None:
            created = created.replace(tzinfo=timezone.utc)
        return created.astimezone(timezone.utc) < cutoff
    except (TypeError, ValueError, json.JSONDecodeError):
        return False


def _quote_choice_audit_payload(quote_choice: QuoteContextChoice | None) -> dict[str, Any] | None:
    if not quote_choice:
        return None
    return {
        "primary_id": quote_choice.primary_id,
        "context_ids": quote_choice.context_ids,
        "context_needed": quote_choice.context_needed,
    }


def _day_verdict_audit_payload(verdict: DayVerdict | None) -> dict[str, Any] | None:
    if not verdict:
        return None
    return {
        "should_publish": verdict.should_publish,
        "reason_code": verdict.reason_code,
        "reason_text": verdict.reason_text,
    }


def _response_format(include_day_verdict: bool) -> dict[str, Any]:
    return {
        "type": "json_schema",
        "json_schema": {
            "name": "quoto_day_verdict" if include_day_verdict else "quoto_scores",
            "strict": True,
            "schema": _DAY_RESPONSE_SCHEMA if include_day_verdict else _SCORE_RESPONSE_SCHEMA,
        },
    }


async def describe_media_file(
    *,
    path: Path,
    mime_type: str,
    media_kind: str,
) -> MediaDescriptionResult:
    if not settings.OPENROUTER_API_KEY:
        raise RuntimeError("OPENROUTER_API_KEY is not configured for media analysis.")

    media_part = _media_content_part(path=path, mime_type=mime_type, media_kind=media_kind)
    body: dict[str, Any] = {
        "model": settings.OPENROUTER_MEDIA_MODEL,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": _MEDIA_DESCRIPTION_PROMPT},
                    media_part,
                ],
            }
        ],
    }
    if settings.OPENROUTER_MEDIA_REASONING_EFFORT:
        body["reasoning"] = {
            "enabled": True,
            "effort": settings.OPENROUTER_MEDIA_REASONING_EFFORT,
            "exclude": True,
        }

    headers = {
        "Authorization": f"Bearer {settings.OPENROUTER_API_KEY}",
        "Content-Type": "application/json",
    }
    async with httpx.AsyncClient(timeout=180.0) as client:
        response = await client.post(
            settings.OPENROUTER_BASE_URL,
            json=body,
            headers=headers,
        )
        response.raise_for_status()

    data = response.json()
    content = (data.get("choices") or [{}])[0].get("message", {}).get("content", "") or ""
    usage = data.get("usage") or {}
    description = str(content).strip()
    if not description:
        raise RuntimeError("Media model returned an empty description.")
    return MediaDescriptionResult(
        description=description,
        actual_model=data.get("model", settings.OPENROUTER_MEDIA_MODEL),
        prompt_tokens=_optional_int(usage.get("prompt_tokens")),
        completion_tokens=_optional_int(usage.get("completion_tokens")),
        total_tokens=_optional_int(usage.get("total_tokens")),
    )


def _media_content_part(*, path: Path, mime_type: str, media_kind: str) -> dict[str, Any]:
    raw = path.read_bytes()
    encoded = base64.b64encode(raw).decode("ascii")
    if media_kind in {"photo", "image", "sticker"}:
        return {
            "type": "image_url",
            "image_url": {"url": f"data:{mime_type};base64,{encoded}"},
        }
    if media_kind in {"video", "animation", "video_note"}:
        return {
            "type": "video_url",
            "video_url": {"url": f"data:{mime_type};base64,{encoded}"},
        }
    if media_kind in {"voice", "audio"}:
        return {
            "type": "input_audio",
            "input_audio": {
                "data": encoded,
                "format": _audio_format(mime_type, path),
            },
        }
    raise ValueError(f"Unsupported media kind for Gemini Lite: {media_kind}")


def _audio_format(mime_type: str, path: Path) -> str:
    if "wav" in mime_type:
        return "wav"
    if "mpeg" in mime_type or "mp3" in mime_type:
        return "mp3"
    if "ogg" in mime_type or path.suffix.lower() == ".ogg":
        return "ogg"
    if "flac" in mime_type:
        return "flac"
    if "aac" in mime_type:
        return "aac"
    if "mp4" in mime_type or "m4a" in mime_type or path.suffix.lower() in {".m4a", ".mp4"}:
        return "m4a"
    return path.suffix.lower().lstrip(".") or "mp3"


def _optional_int(value: object) -> int | None:
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _normalize_scores(
    messages: list[dict[str, Any]],
    entries: list[dict],
) -> dict[int, float]:
    result: dict[int, float] = {}
    known_ids = {int(m["id"]) for m in messages}

    for entry in entries:
        msg_id = int(entry["id"])
        if msg_id not in known_ids:
            continue
        raw = max(0.0, min(10.0, float(entry["score"])))
        result[msg_id] = raw / 10.0

    for msg in messages:
        msg_id = int(msg["id"])
        if msg_id not in result:
            result[msg_id] = 0.5

    return result


def _message_payload(message: dict[str, Any]) -> dict[str, Any]:
    payload = {
        "i": message["id"],
        "a": message["author"],
    }
    if message.get("text"):
        payload["t"] = message["text"]
    if message.get("caption"):
        payload["caption"] = message["caption"]
    if message.get("kind") and message.get("kind") != "text":
        payload["kind"] = message["kind"]
    if message.get("desc"):
        payload["desc"] = message["desc"]
    if message.get("reply_to_id") is not None:
        payload["rp"] = message["reply_to_id"]
    reactions = message.get("reactions")
    if reactions:
        payload["re"] = reactions
    return payload


def _parse_day_payload_safely(content: str) -> tuple[list[dict], DayVerdict | None, QuoteContextChoice | None, str | None]:
    try:
        entries, verdict, quote_choice = _parse_day_payload(content)
        return entries, verdict, quote_choice, None
    except (json.JSONDecodeError, DayVerdictParseError, ValueError) as exc:
        log.error(f"Ошибка day verdict ответа AI: {exc}")
        try:
            entries, quote_choice = _parse_score_payload(content)
        except (json.JSONDecodeError, ValueError):
            entries = []
            quote_choice = None
        return entries, None, quote_choice, str(exc)


def _parse_day_payload(content: str) -> tuple[list[dict], DayVerdict, QuoteContextChoice | None]:
    cleaned = _extract_json_payload(content)
    parsed = json.loads(cleaned)

    if isinstance(parsed, list):
        raise DayVerdictParseError("AI response must include a `day` verdict object.")

    if not isinstance(parsed, dict):
        raise DayVerdictParseError("Expected dict payload for day verdict.")

    entries = parsed.get("messages")
    if entries is None:
        entries = []
    if not isinstance(entries, list):
        raise DayVerdictParseError("AI response field `messages` must be a list.")

    day = parsed.get("day")
    if not isinstance(day, dict):
        raise DayVerdictParseError("AI response is missing a valid `day` verdict block.")

    should_publish = _parse_bool_like(day.get("should_publish"))
    reason_code = str(
        day.get("reason_code") or (REASON_WORTHY if should_publish else REASON_BORING_DAY)
    )
    reason_text = str(day.get("reason_text") or "").strip()

    return (
        entries,
        DayVerdict(
            should_publish=should_publish,
            reason_code=reason_code,
            reason_text=reason_text,
        ),
        _parse_quote_choice(parsed),
    )


def _parse_bool_like(value: object) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, int) and value in {0, 1}:
        return bool(value)
    if isinstance(value, str):
        normalized = value.strip().lower()
        if normalized in {"true", "yes", "1"}:
            return True
        if normalized in {"false", "no", "0"}:
            return False
    raise DayVerdictParseError("AI response field `day.should_publish` must be a strict boolean value.")


def _parse_score_payload(content: str) -> tuple[list[dict], QuoteContextChoice | None]:
    cleaned = _extract_json_payload(content)
    parsed = json.loads(cleaned)

    if isinstance(parsed, dict):
        return parsed.get("messages") or [], _parse_quote_choice(parsed)

    if not isinstance(parsed, list):
        raise json.JSONDecodeError("Expected list payload", cleaned, 0)

    return parsed, None


def _parse_scores(content: str) -> list[dict]:
    entries, _quote_choice = _parse_score_payload(content)
    return entries


def _parse_quote_choice(payload: dict[str, Any]) -> QuoteContextChoice | None:
    quote = payload.get("quote")
    if not isinstance(quote, dict):
        return None

    primary_id = _parse_optional_int(quote.get("primary_id"))
    context_ids_raw = quote.get("context_ids") or []
    context_ids: list[int] = []
    if isinstance(context_ids_raw, list):
        for item in context_ids_raw:
            context_id = _parse_optional_int(item)
            if context_id is not None:
                context_ids.append(context_id)

    try:
        context_needed = _parse_bool_like(quote.get("context_needed"))
    except DayVerdictParseError:
        context_needed = False

    return QuoteContextChoice(
        primary_id=primary_id,
        context_ids=context_ids,
        context_needed=context_needed,
    )


def _parse_optional_int(value: object) -> int | None:
    if value is None:
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _extract_json_payload(content: str) -> str:
    cleaned = content.strip()
    cleaned = re.sub(r"<think>[\s\S]*?</think>", "", cleaned).strip()
    cleaned = re.sub(r"```(?:json)?\s*", "", cleaned)
    cleaned = cleaned.replace("```", "").strip()

    decoder = json.JSONDecoder()
    candidates: list[str] = []
    for index, char in enumerate(cleaned):
        if char not in "[{":
            continue
        try:
            _, end = decoder.raw_decode(cleaned[index:])
        except json.JSONDecodeError:
            continue
        candidates.append(cleaned[index:index + end])

    if candidates:
        return max(candidates, key=len)

    raise json.JSONDecodeError("Не найден JSON payload в ответе AI", cleaned, 0)
