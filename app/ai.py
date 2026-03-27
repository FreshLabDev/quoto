import asyncio
import json
import logging
import re
from dataclasses import dataclass

import httpx

from .config import settings, setup_logging
from .quote_status import REASON_BORING_DAY, REASON_WORTHY

log = setup_logging(logging.getLogger(__name__))

_SCORE_PROMPT = (
    "You are a humor and wit judge for a Telegram group chat. "
    "You will receive a JSON array of messages. "
    "Rate each message on a scale from 0 to 10 based on humor, wit, insight, memorability, and originality. "
    "Respond ONLY with a valid JSON array of objects in this format: "
    "[{\"id\": <message_id>, \"score\": <0-10>}]."
)

_DAY_PROMPT = (
    "You are a humor and wit judge for a Telegram group chat. "
    "You will receive a JSON array of messages from one chat window. "
    "First, rate every message from 0 to 10 based on humor, wit, insight, memorability, and originality. "
    "Then decide whether the day is worthy of a public 'quote of the day' announcement. "
    "If the conversation feels flat, repetitive, generic, or lacking a truly quotable line, set should_publish to false. "
    "Respond ONLY with a valid JSON object in this format: "
    "{\"day\": {\"should_publish\": true, \"reason_code\": \"worthy\", \"reason_text\": \"short reason\"}, "
    "\"messages\": [{\"id\": <message_id>, \"score\": <0-10>}]}."
)


@dataclass
class DayVerdict:
    should_publish: bool = True
    reason_code: str = REASON_WORTHY
    reason_text: str = ""


@dataclass
class EvaluationResult:
    scores: dict[int, float]
    actual_model: str
    day_verdict: DayVerdict | None = None
    day_verdict_error: str | None = None


class DayVerdictParseError(ValueError):
    pass


async def evaluate_messages(
    messages: list[dict[str, str | int]],
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

    user_payload = json.dumps(
        [{"id": m["id"], "author": m["author"], "text": m["text"]} for m in messages],
        ensure_ascii=False,
    )

    body = {
        "model": settings.OPENROUTER_MODEL,
        "messages": [
            {"role": "system", "content": _DAY_PROMPT if include_day_verdict else _SCORE_PROMPT},
            {"role": "user", "content": user_payload},
        ],
    }

    headers = {
        "Authorization": f"Bearer {settings.OPENROUTER_API_KEY}",
        "Content-Type": "application/json",
    }

    max_retries = 3
    retry_delays = [5, 15, 30]

    for attempt in range(max_retries):
        try:
            async with httpx.AsyncClient(timeout=60.0) as client:
                response = await client.post(
                    settings.OPENROUTER_BASE_URL,
                    json=body,
                    headers=headers,
                )
                response.raise_for_status()

            data = response.json()
            actual_model = data.get("model", settings.OPENROUTER_MODEL)
            content = (data.get("choices") or [{}])[0].get("message", {}).get("content", "") or ""
            content = content.strip()

            if not content:
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
                entries, verdict, verdict_error = _parse_day_payload_safely(content)
            else:
                entries = _parse_scores(content)
                verdict = None
                verdict_error = None

            scores = _normalize_scores(messages, entries)
            log.debug(f"🤖 {actual_model} оценил {len(scores)} сообщений")
            return EvaluationResult(
                scores=scores,
                actual_model=actual_model,
                day_verdict=verdict,
                day_verdict_error=verdict_error,
            )

        except httpx.HTTPStatusError as e:
            if e.response.status_code == 429 and attempt < max_retries - 1:
                delay = retry_delays[attempt]
                log.warning(
                    f"⏳ Rate-limit (429), повтор через {delay}с (попытка {attempt + 1}/{max_retries})"
                )
                await asyncio.sleep(delay)
                continue
            log.error(f"OpenRouter HTTP {e.response.status_code}: {e.response.text[:200]}")
        except (json.JSONDecodeError, KeyError, IndexError, ValueError) as e:
            log.error(f"Ошибка парсинга ответа AI: {e}")
        except Exception as e:
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


def _normalize_scores(
    messages: list[dict[str, str | int]],
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


def _parse_day_payload_safely(content: str) -> tuple[list[dict], DayVerdict | None, str | None]:
    try:
        entries, verdict = _parse_day_payload(content)
        return entries, verdict, None
    except (json.JSONDecodeError, DayVerdictParseError, ValueError) as exc:
        log.error(f"Ошибка day verdict ответа AI: {exc}")
        try:
            entries = _parse_scores(content)
        except (json.JSONDecodeError, ValueError):
            entries = []
        return entries, None, str(exc)


def _parse_day_payload(content: str) -> tuple[list[dict], DayVerdict]:
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

    return entries, DayVerdict(
        should_publish=should_publish,
        reason_code=reason_code,
        reason_text=reason_text,
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


def _parse_scores(content: str) -> list[dict]:
    cleaned = _extract_json_payload(content)
    parsed = json.loads(cleaned)

    if isinstance(parsed, dict):
        parsed = parsed.get("messages") or []

    if not isinstance(parsed, list):
        raise json.JSONDecodeError("Expected list payload", cleaned, 0)

    return parsed


def _extract_json_payload(content: str) -> str:
    cleaned = content.strip()
    cleaned = re.sub(r"<think>[\s\S]*?</think>", "", cleaned).strip()
    cleaned = re.sub(r"```(?:json)?\s*", "", cleaned)
    cleaned = cleaned.replace("```", "").strip()

    if cleaned.startswith("{") or cleaned.startswith("["):
        return cleaned

    object_match = re.search(r"\{[\s\S]*\}", cleaned)
    if object_match:
        return object_match.group()

    array_match = re.search(r"\[\s*\{.*?}\s*]", cleaned, re.DOTALL)
    if array_match:
        return array_match.group()

    raise json.JSONDecodeError("Не найден JSON payload в ответе AI", cleaned, 0)
