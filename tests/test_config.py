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
            with self.assertRaises(ValueError):
                config._load_settings()

    def test_load_settings_strips_leading_at_from_bot_username(self) -> None:
        fake_settings = SimpleNamespace(BOT_USERNAME=" @quoto_test_bot ")

        with patch.object(config, "Settings", return_value=fake_settings):
            loaded = config._load_settings()

        self.assertEqual(loaded.BOT_USERNAME, "quoto_test_bot")
