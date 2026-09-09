import os
from types import SimpleNamespace
import unittest
from unittest.mock import patch

os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")

from app import config


class ConfigTests(unittest.TestCase):
    def test_load_settings_rejects_blank_bot_username(self) -> None:
        with patch.object(config, "Settings", return_value=SimpleNamespace(BOT_USERNAME="   ")):
            with self.assertRaises(SystemExit):
                config._load_settings()

    def test_load_settings_strips_leading_at_from_bot_username(self) -> None:
        fake_settings = SimpleNamespace(
            BOT_USERNAME=" @quoto_test_bot ",
            OPENROUTER_EVAL_MODEL="vendor/model",
        )

        with patch.object(config, "Settings", return_value=fake_settings):
            loaded = config._load_settings()

        self.assertEqual(loaded.BOT_USERNAME, "quoto_test_bot")

    def test_load_settings_falls_back_to_default_eval_model_when_blank(self) -> None:
        fake_settings = SimpleNamespace(
            BOT_USERNAME="quoto_test_bot",
            OPENROUTER_EVAL_MODEL="   ",
        )

        with patch.object(config, "Settings", return_value=fake_settings):
            loaded = config._load_settings()

        self.assertEqual(loaded.OPENROUTER_EVAL_MODEL, config.DEFAULT_EVAL_MODEL)

    def test_validate_runtime_warns_about_renamed_env_keys(self) -> None:
        with patch.object(config, "_configured_env_keys", return_value={"OPENROUTER_MODEL"}):
            with self.assertLogs(config.__name__, level="WARNING") as captured:
                config.validate_runtime()

        self.assertTrue(
            any("OPENROUTER_MODEL" in line and "OPENROUTER_EVAL_MODEL" in line for line in captured.output),
            captured.output,
        )
