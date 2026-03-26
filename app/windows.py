from dataclasses import dataclass
from datetime import date, datetime, time, timedelta, timezone
from zoneinfo import ZoneInfo

from .config import settings


@dataclass(frozen=True)
class QuoteWindow:
    quote_day: date
    start_local: datetime
    end_local: datetime
    start_utc: datetime
    end_utc: datetime


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def quote_timezone() -> ZoneInfo:
    return ZoneInfo(settings.TIMEZONE)


def _cutoff_time() -> time:
    return time(hour=settings.QUOTE_HOUR, minute=settings.QUOTE_MINUTE)


def _cutoff_at(day: date) -> datetime:
    return datetime.combine(day, _cutoff_time(), tzinfo=quote_timezone())


def get_open_window(now: datetime | None = None) -> QuoteWindow:
    now_utc = now or utc_now()
    now_local = now_utc.astimezone(quote_timezone())
    today_cutoff = _cutoff_at(now_local.date())

    if now_local < today_cutoff:
        quote_day = now_local.date()
    else:
        quote_day = now_local.date() + timedelta(days=1)

    start_local = _cutoff_at(quote_day - timedelta(days=1))

    return QuoteWindow(
        quote_day=quote_day,
        start_local=start_local,
        end_local=now_local,
        start_utc=start_local.astimezone(timezone.utc),
        end_utc=now_local.astimezone(timezone.utc),
    )


def get_closed_window(now: datetime | None = None) -> QuoteWindow:
    now_utc = now or utc_now()
    now_local = now_utc.astimezone(quote_timezone())
    today_cutoff = _cutoff_at(now_local.date())

    if now_local >= today_cutoff:
        quote_day = now_local.date()
    else:
        quote_day = now_local.date() - timedelta(days=1)

    end_local = _cutoff_at(quote_day)
    start_local = _cutoff_at(quote_day - timedelta(days=1))

    return QuoteWindow(
        quote_day=quote_day,
        start_local=start_local,
        end_local=end_local,
        start_utc=start_local.astimezone(timezone.utc),
        end_utc=end_local.astimezone(timezone.utc),
    )
