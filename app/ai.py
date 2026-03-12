import asyncio
import json
import logging
import re
import httpx

from .config import settings, setup_logging

log = setup_logging(logging.getLogger(__name__))

# Системный промпт для оценки сообщений
_SYSTEM_PROMPT = (
    "You are a humor and wit judge for a Telegram group chat. "
    "You will receive a JSON array of messages. "
    "Rate each message on a scale from 0 to 10 based on:\n"
    "- Humor and wit\n"
    "- Insight and depth\n"
    "- Memorability and quotability\n"
    "- Originality\n\n"
    "Respond ONLY with a valid JSON array of objects: [{\"id\": <message_id>, \"score\": <0-10>}]\n"
    "No explanations, no markdown, no extra text — just the JSON array."
)


async def evaluate_messages(messages: list[dict[str, str | int]]) -> dict[int, float]:
    """Batch-оценка сообщений через OpenRouter API.

    Args:
        messages: Список словарей ``{id, text, author}``.

    Returns:
        Маппинг ``{message_id: normalized_score}`` (0.0 – 1.0).
        При ошибке все сообщения получают нейтральную оценку 0.5.
    """
    if not messages:
        return {}

    neutral = {msg["id"]: 0.5 for msg in messages}

    if not settings.OPENROUTER_API_KEY:
        log.warning("⚠️ OPENROUTER_API_KEY не задан — AI-оценка пропущена")
        return neutral

    # Формируем пользовательский промпт с массивом сообщений
    user_payload = json.dumps(
        [{"id": m["id"], "author": m["author"], "text": m["text"]} for m in messages],
        ensure_ascii=False,
    )

    body = {
        "model": settings.OPENROUTER_MODEL,
        "messages": [
            {"role": "user", "content": f"{_SYSTEM_PROMPT}\n\n{user_payload}"},
        ],
        "temperature": 0.3,
        "max_tokens": 1024,
    }

    headers = {
        "Authorization": f"Bearer {settings.OPENROUTER_API_KEY}",
        "Content-Type": "application/json",
    }

    max_retries = 3
    retry_delays = [5, 15, 30]  # секунды между попытками

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
            # Название реально использованной модели
            actual_model = data.get("model", settings.OPENROUTER_MODEL)
            
            content = (data.get("choices") or [{}])[0].get("message", {}).get("content", "") or ""
            content = content.strip()

            if not content:
                if attempt < max_retries - 1:
                    delay = retry_delays[attempt]
                    log.warning(f"⏳ Пустой ответ AI, повтор через {delay}с (попытка {attempt + 1}/{max_retries})")
                    await asyncio.sleep(delay)
                    continue
                log.warning("⚠️ AI вернул пустой ответ после всех попыток")
                return neutral

            scores = _parse_scores(content)

            # Нормализация: 0–10 → 0.0–1.0
            result: dict[int, float] = {}
            known_ids = {m["id"] for m in messages}
            for entry in scores:
                msg_id = int(entry["id"])
                if msg_id in known_ids:
                    raw = max(0.0, min(10.0, float(entry["score"])))
                    result[msg_id] = raw / 10.0

            for msg in messages:
                if msg["id"] not in result:
                    result[msg["id"]] = 0.5

            log.debug(f"🤖 AI оценил {len(result)} сообщений")
            return result

        except httpx.HTTPStatusError as e:
            if e.response.status_code == 429 and attempt < max_retries - 1:
                delay = retry_delays[attempt]
                log.warning(f"⏳ Rate-limit (429), повтор через {delay}с (попытка {attempt + 1}/{max_retries})")
                await asyncio.sleep(delay)
                continue
            log.error(f"OpenRouter HTTP {e.response.status_code}: {e.response.text[:200]}")
        except (json.JSONDecodeError, KeyError, IndexError) as e:
            log.error(f"Ошибка парсинга ответа AI: {e}")
        except Exception as e:
            log.error(f"Ошибка при запросе к OpenRouter: {e}")

        break

    return neutral


def _parse_scores(content: str) -> list[dict]:
    """Извлекает JSON-массив оценок из ответа AI.

    Обрабатывает:
    - ``<think>...</think>`` блоки (DeepSeek R1 и другие reasoning-модели)
    - Markdown-обёртки (```json ... ```)
    - Произвольный текст вокруг JSON-массива
    """
    cleaned = content.strip()

    # Убираем <think>...</think> блоки (reasoning-модели)
    cleaned = re.sub(r"<think>[\s\S]*?</think>", "", cleaned).strip()

    # Убираем ```json ... ```
    cleaned = re.sub(r"```(?:json)?\s*", "", cleaned)
    cleaned = cleaned.replace("```", "").strip()

    # Пробуем напрямую
    try:
        return json.loads(cleaned)
    except json.JSONDecodeError:
        pass

    # Fallback: ищем JSON-массив регулярным выражением
    match = re.search(r"\[\s*\{.*?}\s*]", cleaned, re.DOTALL)
    if match:
        return json.loads(match.group())

    raise json.JSONDecodeError("Не найден JSON-массив в ответе AI", cleaned, 0)

