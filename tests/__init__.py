import os


os.environ.setdefault("BOT_TOKEN", "123456:TESTTOKEN1234567890")
os.environ.setdefault("BOT_USERNAME", "quoto_test_bot")
os.environ.setdefault("DB_URL", "postgresql+asyncpg://quoto:quoto@localhost:5432/quoto")
# The suite talks to OpenRouter mocks unless a test opts into Claude, so a key in
# a developer's local .env must not route every test through Anthropic.
os.environ.setdefault("ANTHROPIC_API_KEY", "")
