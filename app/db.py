import logging
from pathlib import Path

from alembic.config import Config
from alembic.script import ScriptDirectory
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine

from . import utils
from .config import settings, setup_logging

PROJECT_ROOT = Path(__file__).resolve().parent.parent
ALEMBIC_INI_PATH = PROJECT_ROOT / "alembic.ini"

engine = create_async_engine(
    url=settings.DB_URL,
    pool_size=20,
)

SessionLocal = async_sessionmaker(
    bind=engine,
    class_=AsyncSession,
    expire_on_commit=False,
)

log = setup_logging(logging.getLogger(__name__))


async def init_db():
    try:
        async with engine.begin() as conn:
            await conn.execute(text("SELECT 1"))
            current_revision = await _get_current_revision(conn)
            expected_revision = _get_expected_revision()

            if current_revision != expected_revision:
                raise RuntimeError(
                    f"Database revision mismatch: current={current_revision}, expected={expected_revision}. "
                    "Run `alembic upgrade head` before starting the bot."
                )

            log.info(f"🔍 База данных готова. Текущая ревизия: {current_revision}")
    except Exception as e:
        log.error(f"❌ Ошибка при инициализации базы данных: {e}")
        await utils.notify_developers(f"❌ Ошибка при инициализации базы данных: {e}")
        raise


async def _get_current_revision(conn) -> str:
    exists = await conn.execute(
        text(
            """
            SELECT EXISTS (
                SELECT 1
                FROM information_schema.tables
                WHERE table_schema = 'public'
                  AND table_name = 'alembic_version'
            )
            """
        )
    )
    if not exists.scalar():
        raise RuntimeError("Missing alembic_version table. Run `alembic upgrade head`.")

    result = await conn.execute(text("SELECT version_num FROM alembic_version LIMIT 1"))
    revision = result.scalar()
    if not revision:
        raise RuntimeError("Empty alembic_version. Run `alembic upgrade head`.")

    return str(revision)


def _get_expected_revision() -> str:
    alembic_cfg = Config(str(ALEMBIC_INI_PATH))
    alembic_cfg.set_main_option("script_location", str(PROJECT_ROOT / "alembic"))
    script = ScriptDirectory.from_config(alembic_cfg)
    return str(script.get_current_head())
