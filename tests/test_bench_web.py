import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))

import bench_web  # noqa: E402


def audit_line(created_at: str, model: str, result: dict | None = None) -> str:
    record = {
        "created_at": created_at,
        "message_count": 2,
        "include_day_verdict": True,
        "request": {
            "body": {
                "model": model,
                "max_tokens": 32000,
                "reasoning": {"enabled": True, "effort": "low", "exclude": True},
                "messages": [
                    {"role": "system", "content": "curate"},
                    {
                        "role": "user",
                        "content": json.dumps(
                            [
                                {"i": 11, "a": "Alice", "t": "первое", "re": {"❤": 2}},
                                {"i": 12, "a": "Bob", "kind": "photo", "desc": "кот в коробке"},
                            ]
                        ),
                    },
                ],
            }
        },
    }
    if result:
        record["result"] = result
    return json.dumps(record, ensure_ascii=False)


class StreamParsingTests(unittest.TestCase):
    def test_keepalive_and_blank_lines_are_skipped(self) -> None:
        for line in ("", "   ", ": OPENROUTER PROCESSING", "event: ping"):
            self.assertEqual(bench_web.parse_stream_line(line)[0], "skip")

    def test_done_marker_ends_the_stream(self) -> None:
        self.assertEqual(bench_web.parse_stream_line("data: [DONE]")[0], "done")

    def test_reasoning_and_content_deltas_are_classified(self) -> None:
        reasoning = 'data: {"choices":[{"delta":{"reasoning":"думаю…"}}]}'
        kind, payload = bench_web.parse_stream_line(reasoning)
        self.assertEqual(kind, "reasoning")
        self.assertEqual(payload[0], "думаю…")

        content = 'data: {"choices":[{"delta":{"content":"{\\"day\\":"}}]}'
        kind, payload = bench_web.parse_stream_line(content)
        self.assertEqual(kind, "content")
        self.assertEqual(payload[0], '{"day":')

    def test_usage_chunk_is_returned_for_accounting(self) -> None:
        line = (
            'data: {"choices":[{"delta":{"content":""},"finish_reason":"stop"}],'
            '"usage":{"prompt_tokens":10,"completion_tokens":4,"cost":0.00012,'
            '"completion_tokens_details":{"reasoning_tokens":3}}}'
        )
        kind, chunk = bench_web.parse_stream_line(line)
        self.assertEqual(kind, "chunk")
        usage = bench_web.ai._extract_usage(chunk)
        self.assertEqual(usage.prompt_tokens, 10)
        self.assertEqual(usage.reasoning_tokens, 3)
        self.assertAlmostEqual(usage.cost_usd, 0.00012)

    def test_malformed_json_does_not_raise(self) -> None:
        self.assertEqual(bench_web.parse_stream_line("data: {oops")[0], "skip")


class AuditLoadingTests(unittest.TestCase):
    def setUp(self) -> None:
        self.path = Path(self.enterContext(__import__("tempfile").TemporaryDirectory())) / "audit.jsonl"

    def test_day_keeps_payload_and_takes_outcome_from_successful_attempt(self) -> None:
        self.path.write_text(
            "\n".join(
                [
                    audit_line("2026-09-06T18:00:00+00:00", "dead/model"),
                    audit_line(
                        "2026-09-06T18:01:00+00:00",
                        "fallback/model",
                        {"quote_choice": {"primary_id": 12}, "actual_model": "fallback/model"},
                    ),
                ]
            ),
            encoding="utf-8",
        )
        days = bench_web.load_days(self.path)
        self.assertEqual(list(days), ["2026-09-06"])
        day = days["2026-09-06"]
        self.assertEqual(day.production_primary_id, 12)
        self.assertEqual(day.production_model, "fallback/model")
        view = bench_web.day_view(day)
        self.assertEqual(view["production_pick"]["description"], "кот в коробке")
        self.assertEqual(view["audited_effort"], "low")

    def test_missing_file_yields_no_days(self) -> None:
        self.assertEqual(bench_web.load_days(self.path.with_name("nope.jsonl")), {})


class CatalogTests(unittest.TestCase):
    def entry(self, **overrides):
        base = {
            "id": "vendor/model",
            "created": 1788552838,
            "context_length": 128000,
            "pricing": {"prompt": "0.000001", "completion": "0.000002"},
            "architecture": {"input_modalities": ["text"], "output_modalities": ["text"]},
            "supported_parameters": ["reasoning", "reasoning_effort", "structured_outputs"],
        }
        base.update(overrides)
        return base

    def test_image_generators_are_hidden(self) -> None:
        generator = self.entry(
            architecture={"input_modalities": ["text", "image"], "output_modalities": ["image", "text"]}
        )
        self.assertIsNone(bench_web.catalog_entry(generator))

    def test_batch_tier_ids_are_hidden(self) -> None:
        # /chat/completions answers 404: "only available through the Batch API".
        self.assertIsNone(bench_web.catalog_entry(self.entry(id="vendor/model:batch")))
        self.assertIsNotNone(bench_web.catalog_entry(self.entry(id="vendor/model:free")))

    def test_supported_efforts_come_from_the_catalog(self) -> None:
        mapped = bench_web.catalog_entry(
            self.entry(
                reasoning={
                    "mandatory": True,
                    "default_enabled": True,
                    "supported_efforts": ["max", "high", "low"],
                    "default_effort": "max",
                }
            )
        )
        self.assertEqual(mapped["supported_efforts"], ["max", "high", "low"])
        self.assertTrue(mapped["reasoning_mandatory"])
        self.assertEqual(mapped["default_effort"], "max")

        plain = bench_web.catalog_entry(self.entry(reasoning={"mandatory": False}))
        self.assertEqual(plain["supported_efforts"], [])
        self.assertTrue(plain["has_reasoning"])

        none_at_all = bench_web.catalog_entry(self.entry())
        self.assertFalse(none_at_all["has_reasoning"])

    def test_models_without_text_input_are_hidden(self) -> None:
        audio_only = self.entry(
            architecture={"input_modalities": ["audio"], "output_modalities": ["text"]}
        )
        self.assertIsNone(bench_web.catalog_entry(audio_only))

    def test_capabilities_are_exposed_for_the_ui(self) -> None:
        mapped = bench_web.catalog_entry(
            self.entry(
                architecture={
                    "input_modalities": ["text", "image", "video"],
                    "output_modalities": ["text"],
                }
            )
        )
        self.assertEqual(mapped["modalities"], ["text", "image", "video"])
        self.assertTrue(mapped["supports_structured"])
        self.assertTrue(mapped["supports_effort"])
        self.assertFalse(mapped["free"])

    def test_free_models_are_flagged_and_broken_pricing_survives(self) -> None:
        free = bench_web.catalog_entry(self.entry(pricing={"prompt": "0", "completion": "0"}))
        self.assertTrue(free["free"])
        broken = bench_web.catalog_entry(self.entry(pricing={"prompt": "n/a"}))
        self.assertTrue(broken["free"])


class SelectionTests(unittest.TestCase):
    def test_plain_ids_inherit_the_global_effort(self) -> None:
        self.assertEqual(
            bench_web.parse_selections(["a/one", "b/two"], "medium"),
            [("a/one", "medium"), ("b/two", "medium")],
        )

    def test_per_model_effort_wins_and_null_follows_global(self) -> None:
        selections = bench_web.parse_selections(
            [
                {"id": "a/one", "effort": "high"},
                {"id": "b/two", "effort": None},
                {"id": "c/three", "effort": ""},
            ],
            "low",
        )
        self.assertEqual(selections, [("a/one", "high"), ("b/two", "low"), ("c/three", "")])

    def test_blanks_and_duplicates_are_dropped(self) -> None:
        selections = bench_web.parse_selections(
            [{"id": "  "}, "a/one", {"id": "a/one", "effort": "max"}, 42, None],
            "",
        )
        self.assertEqual(selections, [("a/one", "")])


class ContextTests(unittest.TestCase):
    def day(self, ids=(11, 12, 13, 14, 15, 16, 17)) -> "bench_web.AuditDay":
        return bench_web.AuditDay(
            day="2026-09-06",
            message_count=len(ids),
            include_day_verdict=True,
            body={},
            messages=[{"i": i, "a": f"a{i}", "t": f"текст {i}"} for i in ids],
        )

    def choice(self, **kwargs):
        return bench_web.ai.QuoteContextChoice(**kwargs)

    def test_context_keeps_the_model_order_and_marks_the_primary(self) -> None:
        rows = bench_web.context_messages(
            self.day(),
            self.choice(primary_id=13, context_ids=[12, 13, 14], context_needed=True),
            13,
        )
        self.assertEqual([r["id"] for r in rows], [12, 13, 14])
        self.assertEqual([r["is_primary"] for r in rows], [False, True, False])

    def test_primary_is_appended_when_the_model_left_it_out(self) -> None:
        rows = bench_web.context_messages(
            self.day(),
            self.choice(primary_id=15, context_ids=[12, 11], context_needed=True),
            15,
        )
        self.assertEqual([r["id"] for r in rows], [12, 11, 15])

    def test_unknown_ids_are_dropped_and_the_window_is_capped(self) -> None:
        rows = bench_web.context_messages(
            self.day(),
            self.choice(primary_id=14, context_ids=[11, 12, 13, 14, 15, 16, 17, 999], context_needed=True),
            14,
        )
        self.assertEqual(len(rows), bench_web.scoring._MAX_CONTEXT_MESSAGES)
        self.assertIn(14, [r["id"] for r in rows])
        self.assertNotIn(999, [r["id"] for r in rows])

    def test_no_context_when_the_model_did_not_ask_for_it(self) -> None:
        self.assertEqual(
            bench_web.context_messages(self.day(), self.choice(primary_id=13, context_ids=[]), 13),
            [],
        )
        self.assertEqual(bench_web.context_messages(self.day(), None, 13), [])


class MediaLibraryTests(unittest.IsolatedAsyncioTestCase):
    def _jpeg(self, size: tuple[int, int] = (2400, 1600)) -> bytes:
        from PIL import Image

        buffer = io.BytesIO()
        Image.new("RGB", size, (200, 180, 160)).save(buffer, format="JPEG", quality=95)
        return buffer.getvalue()

    def test_kind_is_detected_from_the_extension(self) -> None:
        self.assertEqual(bench_web.detect_kind("clip.MP4"), "video")
        self.assertEqual(bench_web.detect_kind("note.opus"), "voice")
        self.assertEqual(bench_web.detect_kind("meme.gif"), "animation")
        self.assertEqual(bench_web.detect_kind("what.unknown"), "photo")

    def test_every_kind_maps_to_an_input_modality(self) -> None:
        for kind, modality in bench_web.KIND_MODALITY.items():
            self.assertIn(modality, {"image", "video", "audio"}, kind)

    async def test_upload_is_normalized_and_listed(self) -> None:
        raw = self._jpeg()
        with tempfile.TemporaryDirectory() as tmp:
            media_dir = Path(tmp)
            meta = await bench_web.store_upload(media_dir, "shot.jpg", "photo", raw)

            self.assertEqual(meta["modality"], "image")
            self.assertEqual(meta["raw_bytes"], len(raw))
            # The bot downsizes images before sending them; the copy must be smaller.
            self.assertLess(meta["normalized_bytes"], meta["raw_bytes"])
            self.assertTrue((media_dir / meta["id"] / meta["normalized_file"]).exists())

            library = bench_web.load_media_library(media_dir)
            self.assertEqual([item["id"] for item in library], [meta["id"]])


class MediaRunStreamTests(unittest.IsolatedAsyncioTestCase):
    async def test_stream_carries_models_and_archives_the_run(self) -> None:
        from unittest.mock import patch

        from aiohttp.test_utils import TestClient, TestServer
        from PIL import Image

        tmp = Path(self.enterContext(tempfile.TemporaryDirectory()))
        media_dir, history_dir = tmp / "media", tmp / "runs"
        buffer = io.BytesIO()
        Image.new("RGB", (640, 480), (10, 20, 30)).save(buffer, format="JPEG")
        item = await bench_web.store_upload(media_dir, "shot.jpg", "photo", buffer.getvalue())

        async def fake_stream(client, media_item, media_part, model, attempt, options, queue, semaphore):
            await queue.put(
                {
                    "model": model,
                    "attempt": attempt,
                    "key": f"{model}#{attempt}",
                    "effort": options.get("effort") or "",
                    "type": "result",
                    "status": "ok",
                    "description": f"{model} описал картинку",
                    "seconds": 1.0,
                    "usage": {"prompt_tokens": 5, "completion_tokens": 7, "cost_usd": 0.0001},
                }
            )

        app = bench_web.build_app(tmp / "audit.jsonl", history_dir, media_dir)
        client = TestClient(TestServer(app))
        await client.start_server()
        self.addAsyncCleanup(client.close)

        with (
            patch.object(bench_web.settings, "OPENROUTER_API_KEY", "test-key"),
            patch.object(bench_web, "stream_media_model", fake_stream),
        ):
            response = await client.post(
                "/api/media/run",
                json={
                    "media_id": item["id"],
                    "models": [{"id": "a/one", "effort": "high"}, "b/two"],
                    "effort": "low",
                },
            )
            self.assertEqual(response.status, 200)
            body = await response.text()

        events = [json.loads(line[6:]) for line in body.splitlines() if line.startswith("data: ")]
        start = next(e for e in events if e["type"] == "run_start")
        # The UI reads run_start.models — a media run must send it like an eval run.
        self.assertEqual(start["models"], ["a/one", "b/two"])
        self.assertEqual(start["media"]["id"], item["id"])

        results = [e for e in events if e["type"] == "result"]
        self.assertEqual({r["model"] for r in results}, {"a/one", "b/two"})
        self.assertEqual({r["effort"] for r in results}, {"high", "low"})

        done = next(e for e in events if e["type"] == "run_done")
        self.assertAlmostEqual(done["total_cost_usd"], 0.0002)
        archived = json.loads((history_dir / f"{done['history_id']}.json").read_text(encoding="utf-8"))
        self.assertEqual(archived["kind"], "media")
        self.assertEqual(len(archived["results"]), 2)


class RequestBodyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.day = bench_web.AuditDay(
            day="2026-09-06",
            message_count=2,
            include_day_verdict=True,
            body={
                "messages": [{"role": "system", "content": "x"}],
                "response_format": {"type": "json_schema"},
                "max_tokens": 32000,
                "reasoning": {"enabled": True, "effort": "low", "exclude": True},
            },
        )

    def test_body_never_sets_temperature(self) -> None:
        for options in (
            {},
            {"effort": "high"},
            {"effort": "off"},
            {"effort": "medium", "max_tokens": 8000, "show_reasoning": False},
        ):
            body = bench_web.build_body(self.day, "vendor/model", options)
            self.assertNotIn("temperature", body)

    def test_effort_override_and_reasoning_visibility(self) -> None:
        body = bench_web.build_body(self.day, "vendor/model", {"effort": "high", "show_reasoning": True})
        self.assertEqual(body["reasoning"], {"enabled": True, "effort": "high", "exclude": False})

        hidden = bench_web.build_body(self.day, "vendor/model", {"effort": "high", "show_reasoning": False})
        self.assertTrue(hidden["reasoning"]["exclude"])

        audited = bench_web.build_body(self.day, "vendor/model", {"show_reasoning": True})
        self.assertEqual(audited["reasoning"]["effort"], "low")

        without = bench_web.build_body(self.day, "vendor/model", {"effort": "off"})
        self.assertNotIn("reasoning", without)

    def test_body_streams_and_asks_for_usage(self) -> None:
        body = bench_web.build_body(self.day, "vendor/model", {})
        self.assertTrue(body["stream"])
        self.assertEqual(body["usage"], {"include": True})
        self.assertEqual(body["max_tokens"], 32000)


if __name__ == "__main__":
    unittest.main()
