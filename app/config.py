import logging
import os
from pydantic_settings import BaseSettings, SettingsConfigDict


# === НАСТРОЙКА ПЕРЕМЕННЫХ ОКРУЖЕНИЯ ===

class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env", 
        env_file_encoding="utf-8",
        extra="ignore"
    )

    # -- CONFIG SETTINGS --
    LOGS_PATH: str = "logs/"
    COLORS: bool = True
    EMOJIS: bool = True

    # -- BOT SETTINGS --
    BOT_TOKEN: str
    BOT_USERNAME: str
    DEVELOPER_IDS: list[int] = []
    ENABLE_DEVELOPERS_NOTIFY: bool = False

    # -- DATABASE --
    DB_URL: str

    # -- AI SETTINGS --
    OPENROUTER_API_KEY: str = ""
    OPENROUTER_MODEL: str = "google/gemini-3.5-flash"
    OPENROUTER_EVAL_MODEL: str = ""
    OPENROUTER_MEDIA_MODEL: str = "google/gemini-3.1-flash-lite"
    OPENROUTER_BASE_URL: str = "https://openrouter.ai/api/v1/chat/completions"
    OPENROUTER_REASONING_EFFORT: str = "medium"
    OPENROUTER_EVAL_REASONING_EFFORT: str = "medium"
    OPENROUTER_MEDIA_REASONING_EFFORT: str = "medium"
    OPENROUTER_EVAL_MAX_TOKENS: int = 4096

    # -- MEDIA SETTINGS --
    MEDIA_ANALYSIS_ENABLED: bool = True
    MEDIA_PHASH_DISTANCE: int = 5
    MEDIA_CACHE_PROMPT_VERSION: str = "v1"
    MEDIA_IMAGE_MAX_SIDE: int = 1280
    MEDIA_IMAGE_QUALITY: int = 82
    MEDIA_VIDEO_MAX_SECONDS: int = 3600
    MEDIA_VIDEO_LOW_RES_MAX_SECONDS: int = 10800
    MEDIA_VIDEO_MAX_HEIGHT: int = 720
    MEDIA_VIDEO_CRF: int = 30
    MEDIA_VIDEO_FPS: int = 12
    MEDIA_AUDIO_BITRATE: str = "64k"
    MEDIA_AUDIO_SAMPLE_RATE: int = 24000
    MEDIA_COMMAND_TIMEOUT_SECONDS: int = 300

    # -- SCORING WEIGHTS --
    WEIGHT_REACTIONS: float = 0.0
    WEIGHT_AI: float = 1.0
    WEIGHT_LENGTH: float = 0.0
    LENGTH_OPTIMAL_MIN: int = 20
    LENGTH_OPTIMAL_MAX: int = 150

    # -- SCHEDULER --
    QUOTE_HOUR: int = 21
    QUOTE_MINUTE: int = 0
    TIMEZONE: str = "Europe/Kyiv"
    MIN_MESSAGES_FOR_AUTO_REVIEW: int = 10



# === НАСТРОЙКА ЛОГИРОВАНИЯ ===

class Colors:
    # Основные цвета
    RESET = '\033[0m'
    BOLD = '\033[1m'
    DIM = '\033[2m'
    
    # Цвета текста
    BLACK = '\033[30m'
    RED = '\033[31m'
    GREEN = '\033[32m'
    YELLOW = '\033[33m'
    BLUE = '\033[34m'
    MAGENTA = '\033[35m'
    CYAN = '\033[36m'
    WHITE = '\033[37m'
    
    # Яркие цвета
    BRIGHT_BLACK = '\033[90m'
    BRIGHT_RED = '\033[91m'
    BRIGHT_GREEN = '\033[92m'
    BRIGHT_YELLOW = '\033[93m'
    BRIGHT_BLUE = '\033[94m'
    BRIGHT_MAGENTA = '\033[95m'
    BRIGHT_CYAN = '\033[96m'
    BRIGHT_WHITE = '\033[97m'
    
    # Цвета фона
    BG_BLACK = '\033[40m'
    BG_RED = '\033[41m'
    BG_GREEN = '\033[42m'
    BG_YELLOW = '\033[43m'
    BG_BLUE = '\033[44m'
    BG_MAGENTA = '\033[45m'
    BG_CYAN = '\033[46m'
    BG_WHITE = '\033[47m'

#? Можно ли изменять? Да!
class Changeable:
    # Цвета для разных уровней логирования
    COLORS = {
        'DEBUG': Colors.BRIGHT_BLACK,
        'INFO': Colors.BRIGHT_GREEN,
        'WARNING': Colors.BRIGHT_YELLOW,
        'ERROR': Colors.BRIGHT_RED,
        'CRITICAL': Colors.BRIGHT_RED + Colors.BOLD
    }

    # Эмодзи для разных уровней логирования
    EMOJIS = {
        'DEBUG': '🔍 ',
        'INFO': 'ℹ️  ',
        'WARNING': '⚠️  ',
        'ERROR': '❌ ',
        'CRITICAL': '🚨 '
    }

    # Форматы даты и времени
    FILE_DATEFMT = '%Y-%m-%d %H:%M:%S'
    CONSOLE_DATEFMT = '%H:%M:%S'

class ColoredFormatter(logging.Formatter):
    def format(self, record):
        # Базовое форматирование
        log_message = super().format(record)
        
        # Добавляем цвета для консольного вывода
        if hasattr(record, 'levelname') and record.levelname in Changeable.COLORS:
            color = Changeable.COLORS[record.levelname]
            reset = Colors.RESET

            # Эмодзи для разных уровней
            if Changeable.EMOJIS:
                emoji = Changeable.EMOJIS.get(record.levelname, '')
            else:
                emoji = ''
            
            # Сообщение с цветом и эмодзи
            formatted_message = f"{color}{emoji}{log_message}{reset}"
            return formatted_message
        
        return log_message

def setup_logging(logger):
    logger.setLevel(logging.DEBUG)
    logger.handlers.clear()

    path = settings.LOGS_PATH
    os.makedirs(path, exist_ok=True)
    
    # Форматтер для файла (без цветов)
    file_formatter = logging.Formatter(
        '%(asctime)s | %(name)s | %(levelname)s | %(message)s',
        datefmt=Changeable.FILE_DATEFMT
    )
    
    # Форматтер для консоли (с цветами)
    if Changeable.COLORS:
        console_formatter = ColoredFormatter(
            '%(asctime)s [%(levelname)s] %(message)s',
            datefmt=Changeable.CONSOLE_DATEFMT
        )
    else:
        console_formatter = logging.Formatter(
        '%(asctime)s [%(levelname)s] %(message)s',
        datefmt=Changeable.CONSOLE_DATEFMT
    )
    
    # Обработчик для файла
    file_handler = logging.FileHandler(
        path + f"{logger.name}.log", 
        encoding='utf-8'
    )
    file_handler.setFormatter(file_formatter)
    file_handler.setLevel(logging.DEBUG)
    
    # Обработчик для консоли
    console_handler = logging.StreamHandler()
    console_handler.setFormatter(console_formatter)
    console_handler.setLevel(logging.DEBUG)
    
    logger.addHandler(file_handler)
    logger.addHandler(console_handler)
    
    return logger


def _load_settings() -> Settings:
    loaded = Settings()
    loaded.BOT_USERNAME = loaded.BOT_USERNAME.strip().lstrip("@")
    if not loaded.BOT_USERNAME:
        raise ValueError("BOT_USERNAME must be configured with the bot public username.")
    if not getattr(loaded, "OPENROUTER_EVAL_MODEL", ""):
        loaded.OPENROUTER_EVAL_MODEL = getattr(loaded, "OPENROUTER_MODEL", "google/gemini-3.5-flash")
    loaded.OPENROUTER_MODEL = loaded.OPENROUTER_EVAL_MODEL
    return loaded


settings = _load_settings()
