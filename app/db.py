from sqlalchemy.ext.asyncio import create_async_engine, async_sessionmaker, AsyncSession
from sqlalchemy import text
import logging

from .models import Base
from . import utils
from .config import settings, setup_logging

engine = create_async_engine(
    url=settings.DB_URL,
    pool_size=20
)

SessionLocal = async_sessionmaker(
    bind=engine,
    class_=AsyncSession,
    expire_on_commit=False
)

log = setup_logging(logging.getLogger(__name__))

async def init_db():
    try:
        async with engine.begin() as conn:
            result = await conn.execute(text("""
                SELECT table_name 
                FROM information_schema.tables 
                WHERE table_schema = 'public' 
                AND table_type = 'BASE TABLE'
            """))
            
            tables = [row[0] for row in result.fetchall()]
            if not tables:
                log.warning("🛠️ Таблицы не найдены. Пересоздание...")
                await utils.notify_developers("⚠️ <b>Таблицы в базе данных не найдены.</b>\nВыполняется пересоздание...")
                await conn.run_sync(Base.metadata.create_all)
                
                result = await conn.execute(text("""
                    SELECT table_name 
                    FROM information_schema.tables 
                    WHERE table_schema = 'public' 
                    AND table_type = 'BASE TABLE'
                """))
                
                tables = [row[0] for row in result.fetchall()]
                log.info(f"📋 Созданные таблицы: {', '.join(tables)}.")
            else:
                log.info(f"🔍 Найдены таблицы: {', '.join(tables)}.")

    except Exception as e:
        log.error(f"❌ Ошибка при перезагрузке базы данных: {e}")
        await utils.notify_developers(f"❌ Ошибка при перезагрузке базы данных: {e}")
        raise e
    
