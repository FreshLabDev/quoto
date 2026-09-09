"""Compare quote-of-the-day models by replaying real days from the AI audit log.

Every eval request the bot sends is written to logs/ai_audit.jsonl with its full
payload — system prompt plus the whole day of messages, media already described.
This script takes those saved days, sends the identical payload to several
models in parallel, and shows which quote each one picked, at what cost.

Nothing is written to the database: this is a read-only experiment.

    python scripts/compare_eval_models.py --days 2 \\
        --models z-ai/glm-5.3-flash,poolside/laguna-s-2.1

    python scripts/compare_eval_models.py --date 2026-09-04 --effort medium \\
        --models z-ai/glm-5.3-flash,minimax/minimax-m3:free --json out.json
"""

import argparse
import asyncio
import json
import sys
import time
from collections import OrderedDict
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from app import ai  # noqa: E402
from app.config import settings  # noqa: E402

REQUEST_TIMEOUT_SECONDS = 300.0


@dataclass
class AuditDay:
    """One replayable day: the exact request the bot sent that evening."""

    day: str
    body: dict[str, Any]
    include_day_verdict: bool
    message_count: int
    messages_by_id: dict[int, dict[str, Any]]
    # Filled from whichever attempt that day actually produced a quote.
    production_model: str = "—"
    production_primary_id: int | None = None


@dataclass
class ModelRun:
    day: str
    model: str
    attempt: int = 1
    status: str = "ok"
    error: str = ""
    primary_id: int | None = None
    primary_score: float | None = None
    top_ids: list[int] = field(default_factory=list)
    should_publish: bool | None = None
    reason_code: str = ""
    prompt_tokens: int | None = None
    completion_tokens: int | None = None
    reasoning_tokens: int | None = None
    cost_usd: float | None = None
    elapsed_seconds: float = 0.0


def load_audit_days(path: Path, days: int | None, dates: list[str]) -> list[AuditDay]:
    if not path.exists():
        raise SystemExit(f"Audit log not found: {path}")

    by_day: OrderedDict[str, AuditDay] = OrderedDict()
    for line in path.read_text(encoding="utf-8").splitlines():
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
        if day not in by_day:
            # The payload is identical across a day's attempts and fallback models,
            # so the first entry supplies it and later ones only fill in the outcome.
            by_day[day] = AuditDay(
                day=day,
                body=body,
                include_day_verdict=bool(record.get("include_day_verdict")),
                message_count=int(record.get("message_count") or 0),
                messages_by_id=_index_payload_messages(messages[1].get("content") or ""),
            )
        result = record.get("result") or {}
        quote_choice = result.get("quote_choice") or {}
        if quote_choice.get("primary_id") is not None:
            by_day[day].production_primary_id = quote_choice["primary_id"]
            by_day[day].production_model = str(result.get("actual_model") or body.get("model") or "?")

    available = list(by_day.values())
    if dates:
        missing = [d for d in dates if d not in by_day]
        if missing:
            raise SystemExit(
                f"No audit entries for: {', '.join(missing)}. "
                f"Available: {', '.join(by_day) or 'none'}"
            )
        return [by_day[d] for d in dates]
    return available[-days:] if days else available


def _index_payload_messages(user_content: str) -> dict[int, dict[str, Any]]:
    try:
        parsed = json.loads(user_content)
    except json.JSONDecodeError:
        return {}
    if not isinstance(parsed, list):
        return {}
    indexed: dict[int, dict[str, Any]] = {}
    for item in parsed:
        if isinstance(item, dict) and "i" in item:
            try:
                indexed[int(item["i"])] = item
            except (TypeError, ValueError):
                continue
    return indexed


def describe_message(item: dict[str, Any] | None, limit: int) -> str:
    if not item:
        return "—"
    author = str(item.get("a") or "?")
    text = item.get("t") or item.get("desc") or ""
    text = " ".join(str(text).split())
    if len(text) > limit:
        text = text[: limit - 1] + "…"
    return f"{author}: {text}" if text else author


def build_body(day: AuditDay, model: str, args: argparse.Namespace) -> dict[str, Any]:
    body = {
        "model": model,
        "messages": day.body["messages"],
        "usage": {"include": True},
    }
    if day.body.get("response_format"):
        body["response_format"] = day.body["response_format"]
    max_tokens = args.max_tokens if args.max_tokens is not None else day.body.get("max_tokens")
    if max_tokens:
        body["max_tokens"] = max_tokens
    effort = args.effort
    if effort == "off":
        pass
    elif effort:
        body["reasoning"] = {"enabled": True, "effort": effort, "exclude": True}
    elif day.body.get("reasoning"):
        body["reasoning"] = day.body["reasoning"]
    return body


async def run_model(
    client: httpx.AsyncClient,
    day: AuditDay,
    model: str,
    args: argparse.Namespace,
    semaphore: asyncio.Semaphore,
    attempt: int = 1,
) -> ModelRun:
    run = ModelRun(day=day.day, model=model, attempt=attempt)
    body = build_body(day, model, args)
    started = time.monotonic()
    async with semaphore:
        try:
            response = await client.post(
                settings.OPENROUTER_BASE_URL,
                json=body,
                headers=ai._openrouter_headers(),
            )
            response.raise_for_status()
            data = response.json()
        except httpx.HTTPStatusError as exc:
            run.status = f"http_{exc.response.status_code}"
            run.error = ai._redact(exc.response.text)[:200]
            run.elapsed_seconds = time.monotonic() - started
            return run
        except Exception as exc:  # noqa: BLE001 — a comparison run must not abort the batch
            run.status = type(exc).__name__
            run.error = ai._redact(str(exc))[:200]
            run.elapsed_seconds = time.monotonic() - started
            return run

    run.elapsed_seconds = time.monotonic() - started
    usage = ai._extract_usage(data)
    run.prompt_tokens = usage.prompt_tokens
    run.completion_tokens = usage.completion_tokens
    run.reasoning_tokens = usage.reasoning_tokens
    run.cost_usd = usage.cost_usd

    content = ((data.get("choices") or [{}])[0].get("message") or {}).get("content") or ""
    content = content.strip()
    if not content:
        run.status = "empty_response"
        return run

    try:
        if day.include_day_verdict:
            entries, verdict, quote_choice, _language, verdict_error = ai._parse_day_payload_safely(
                content, require_language=False
            )
            if verdict_error:
                run.error = verdict_error[:200]
            if verdict:
                run.should_publish = verdict.should_publish
                run.reason_code = verdict.reason_code
        else:
            entries, quote_choice = ai._parse_score_payload(content)
    except (json.JSONDecodeError, ValueError) as exc:
        run.status = "parse_failed"
        run.error = str(exc)[:200]
        return run

    scores = _scores_from_entries(entries)
    ranked = sorted(scores.items(), key=lambda pair: pair[1], reverse=True)
    run.top_ids = [msg_id for msg_id, _score in ranked[:3]]
    run.primary_id = (quote_choice.primary_id if quote_choice else None) or (
        ranked[0][0] if ranked else None
    )
    if run.primary_id is not None:
        run.primary_score = scores.get(run.primary_id)
    if run.primary_id is None:
        run.status = "no_choice"
    return run


def _scores_from_entries(entries: list[dict]) -> dict[int, float]:
    scores: dict[int, float] = {}
    for entry in entries or []:
        try:
            scores[int(entry["id"])] = max(0.0, min(10.0, float(entry["score"]))) / 10.0
        except (KeyError, TypeError, ValueError):
            continue
    return scores


def format_cost(value: float | None) -> str:
    if value is None:
        return "n/a"
    if value == 0:
        return "free"
    return f"${value:.5f}"


def print_day(day: AuditDay, runs: list[ModelRun], args: argparse.Namespace) -> None:
    print()
    print(f"══ {day.day} · {day.message_count} сообщений · прод: {day.production_model}")
    if day.production_primary_id:
        print(f"   прод выбрал: {describe_message(day.messages_by_id.get(day.production_primary_id), args.quote_chars)}")
    print()
    by_model: dict[str, list[ModelRun]] = {}
    for run in runs:
        by_model.setdefault(run.model, []).append(run)

    for model, model_runs in by_model.items():
        picks = [r.primary_id for r in model_runs if r.status == "ok" and r.primary_id is not None]
        stability = ""
        if len(model_runs) > 1 and picks:
            most_common = max(set(picks), key=picks.count)
            stability = f"  [стабильность {picks.count(most_common)}/{len(model_runs)}]"
        print(f"  {model}{stability}")
        for run in model_runs:
            prefix = f"    #{run.attempt}" if len(model_runs) > 1 else "   "
            if run.status != "ok":
                print(f"{prefix} ✖ {run.status}: {run.error or '—'}  ({run.elapsed_seconds:.1f}s)")
                continue
            quote = describe_message(day.messages_by_id.get(run.primary_id), args.quote_chars)
            agree = " ⭑" if run.primary_id == day.production_primary_id else ""
            print(f"{prefix} «{quote}»{agree}")
            verdict = ""
            if run.should_publish is not None:
                verdict = " · публиковать: да" if run.should_publish else f" · публиковать: нет ({run.reason_code})"
            elif day.include_day_verdict:
                verdict = " · вердикт не разобран"
            print(
                f"{prefix}   балл {run.primary_score if run.primary_score is not None else '?'}"
                f"{verdict}"
                f" · токены {run.prompt_tokens}/{run.completion_tokens}"
                f" (reasoning {run.reasoning_tokens})"
                f" · {format_cost(run.cost_usd)} · {run.elapsed_seconds:.1f}s"
            )


def print_summary(days: list[AuditDay], runs: list[ModelRun]) -> None:
    by_model: dict[str, list[ModelRun]] = {}
    for run in runs:
        by_model.setdefault(run.model, []).append(run)
    agreement = {day.day: day.production_primary_id for day in days}

    print()
    print("══ Итого по моделям")
    print(f"  {'модель':<44} {'ок':>7} {'совпало':>7} {'стабильно':>9} {'цена':>10} {'сек':>9}")
    for model, model_runs in by_model.items():
        ok = [r for r in model_runs if r.status == "ok"]
        matched = sum(1 for r in ok if r.primary_id == agreement.get(r.day))
        costs = [r.cost_usd for r in model_runs if r.cost_usd is not None]
        total_cost = sum(costs) if costs else None
        avg_seconds = sum(r.elapsed_seconds for r in model_runs) / len(model_runs)
        picks_per_day = {}
        for run in ok:
            picks_per_day.setdefault(run.day, []).append(run.primary_id)
        stable_days = sum(
            1 for picks in picks_per_day.values() if len(picks) > 1 and len(set(picks)) == 1
        )
        repeated_days = sum(1 for picks in picks_per_day.values() if len(picks) > 1)
        stability = f"{stable_days}/{repeated_days}" if repeated_days else "—"
        print(
            f"  {model:<44} {len(ok):>3}/{len(model_runs):<3} {matched:>7} {stability:>9}"
            f" {format_cost(total_cost):>10} {avg_seconds:>8.1f}"
        )
    grand_total = sum(r.cost_usd for r in runs if r.cost_usd)
    print(f"\n  Прогон стоил {format_cost(grand_total)} за {len(days)} дн. × {len(by_model)} моделей.")


async def main_async(args: argparse.Namespace) -> None:
    if not settings.OPENROUTER_API_KEY:
        raise SystemExit("OPENROUTER_API_KEY is empty — set it in .env first.")

    days = load_audit_days(Path(args.audit), args.days if not args.date else None, args.date)
    if not days:
        raise SystemExit("No replayable days found in the audit log.")
    models = [m.strip() for m in args.models.split(",") if m.strip()]
    if not models:
        raise SystemExit("Pass at least one model via --models.")

    print(f"Дни: {', '.join(d.day for d in days)}")
    print(f"Модели: {', '.join(models)}")
    print(f"Эффорт: {args.effort or 'как в записи'} · max_tokens: {args.max_tokens or 'как в записи'}")

    semaphore = asyncio.Semaphore(args.concurrency)
    async with httpx.AsyncClient(timeout=REQUEST_TIMEOUT_SECONDS) as client:
        tasks = [
            run_model(client, day, model, args, semaphore, attempt)
            for day in days
            for model in models
            for attempt in range(1, args.runs + 1)
        ]
        results = await asyncio.gather(*tasks)

    for day in days:
        print_day(day, [r for r in results if r.day == day.day], args)
    print_summary(days, list(results))

    if args.json:
        payload = [
            {**run.__dict__, "quote": describe_message(
                next(d for d in days if d.day == run.day).messages_by_id.get(run.primary_id), 400
            )}
            for run in results
        ]
        Path(args.json).write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"\nJSON: {args.json}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--audit", default=str(Path(settings.LOGS_PATH) / "ai_audit.jsonl"))
    parser.add_argument("--days", type=int, default=2, help="How many most recent audited days to replay.")
    parser.add_argument("--date", action="append", default=[], help="Replay a specific YYYY-MM-DD; repeatable.")
    parser.add_argument("--models", required=True, help="Comma-separated OpenRouter model ids.")
    parser.add_argument(
        "--effort",
        default="",
        choices=["", "off", "minimal", "low", "medium", "high", "xhigh", "max", "none"],
        help="Reasoning effort for every model; 'off' omits the reasoning block, empty keeps the audited one.",
    )
    parser.add_argument("--max-tokens", type=int, default=None)
    parser.add_argument("--concurrency", type=int, default=4)
    parser.add_argument(
        "--runs",
        type=int,
        default=1,
        help="Repeat each model on each day N times to see how stable its pick is.",
    )
    parser.add_argument("--quote-chars", type=int, default=140)
    parser.add_argument("--json", default="", help="Also dump raw results to this file.")
    return parser.parse_args()


if __name__ == "__main__":
    asyncio.run(main_async(parse_args()))
