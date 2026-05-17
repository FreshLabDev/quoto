import asyncio
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
    "You are a humor and wit judge for a Telegram group chat. "
    "You will receive a JSON array of messages. Each message has an internal `id`; use ONLY that `id` "
    "for `primary_id`, `context_ids`, and message scores. `message_id` is Telegram metadata for reference only. "
    "Use reactions as lightweight context when present. "
    "Rate each message on a scale from 0 to 10 based on humor, wit, insight, memorability, and originality. "
    "A quote can be one standalone message or a short dialogue. If the punchline depends on setup, "
    "a previous line, or a reply chain, include the minimal context block instead of publishing the punchline alone. "
    "Use `context_ids` only for consecutive messages or a real reply thread. Keep them in reading order, include "
    "`primary_id`, and set `context_needed` to true when `context_ids` contains more than one id. "
    "Never combine unrelated messages. Max 5 context messages. "
    "Respond ONLY with a valid JSON object in this format: "
    "{\"quote\": {\"primary_id\": <id>, \"context_ids\": [<id>], \"context_needed\": <true|false>}, "
    "\"messages\": [{\"id\": <id>, \"score\": <0-10>}]}."
)

_DAY_PROMPT = (
    "You are a humor and wit judge for a Telegram group chat. "
    "You will receive a JSON array of messages from one quote day. Each message has an internal `id`; use ONLY "
    "that `id` for `primary_id`, `context_ids`, and message scores. `message_id` is Telegram metadata for "
    "reference only. "
    "Use reactions as lightweight context when present. "
    "First, rate every message from 0 to 10 based on humor, wit, insight, memorability, and originality. "
    "Then decide whether the day is worthy of a public 'quote of the day' announcement. "
    "If the conversation feels flat, repetitive, generic, or lacking a truly quotable line, set should_publish to false. "
    "A quote can be one standalone message or a short dialogue. If the punchline depends on setup, "
    "a previous line, or a reply chain, include the minimal context block instead of publishing the punchline alone. "
    "Use `context_ids` only for consecutive messages or a real reply thread. Keep them in reading order, include "
    "`primary_id`, and set `context_needed` to true when `context_ids` contains more than one id. "
    "Never combine unrelated messages. Max 5 context messages. "
    "Respond ONLY with a valid JSON object in this format: "
    "{\"day\": {\"should_publish\": true, \"reason_code\": \"worthy\", \"reason_text\": \"short reason\"}, "
    "\"quote\": {\"primary_id\": <id>, \"context_ids\": [<id>], \"context_needed\": <true|false>}, "
    "\"messages\": [{\"id\": <id>, \"score\": <0-10>}]}."
    "Respond in the language of the messages for reason_text."
)


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
    day_verdict: DayVerdict | None = None
    day_verdict_error: str | None = None
    quote_choice: QuoteContextChoice | None = None


class DayVerdictParseError(ValueError):
    pass


def _is_retryable_http_status(status_code: int) -> bool:
    return status_code == 429 or 500 <= status_code < 600


async def evaluate_messages(
    messages: list[dict[str, Any]],
    include_day_verdict: bool = False,
) -> EvaluationResult:
    if not messages:
        return EvaluationResult(scores={}, actual_model=settings.OPENROUTER_MODEL)

    neutral_scores = {int(msg["id"]): 0.5 for msg in messages}
    default_model = settings.OPENROUTER_MODEL
    default_verdict = DayVerdict()

    if not settings.OPENROUTER_API_KEY:
        log.warning("⚠️ OPENROUTER_API_KEY не задан — AI-оценка пропущена")
        return EvaluationResult(
            scores=neutral_scores,
            actual_model=default_model,
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
        "model": settings.OPENROUTER_MODEL,
        "messages": [
            {"role": "system", "content": _DAY_PROMPT if include_day_verdict else _SCORE_PROMPT},
            {"role": "user", "content": user_payload},
        ],
    }
    if settings.OPENROUTER_REASONING_EFFORT:
        body["reasoning"] = {
            "enabled": True,
            "effort": settings.OPENROUTER_REASONING_EFFORT,
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
            actual_model = data.get("model", settings.OPENROUTER_MODEL)
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

    return EvaluationResult(
        scores=neutral_scores,
        actual_model=default_model,
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
        "id": message["id"],
        "author": message["author"],
        "text": message["text"],
    }
    reactions = message.get("reactions")
    if reactions:
        payload["reactions"] = reactions
    if message.get("message_id") is not None:
        payload["message_id"] = message["message_id"]
    if message.get("reply_to_message_id") is not None:
        payload["reply_to_message_id"] = message["reply_to_message_id"]
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
