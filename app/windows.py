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


def cutoff_time() -> time:
    return time(hour=settings.QUOTE_HOUR, minute=settings.QUOTE_MINUTE)


def cutoff_at(day: date, tz: ZoneInfo | None = None, at_time: time | None = None) -> datetime:
    return datetime.combine(day, at_time or cutoff_time(), tzinfo=tz or quote_timezone())


def localize_legacy_datetime(value: datetime, tz: ZoneInfo | None = None) -> datetime:
    target_tz = tz or quote_timezone()
    if value.tzinfo is None:
        return value.replace(tzinfo=target_tz)
    return value.astimezone(target_tz)


def quote_day_from_local(local_dt: datetime, at_time: time | None = None) -> date:
    local_cutoff = cutoff_at(local_dt.date(), tz=local_dt.tzinfo or quote_timezone(), at_time=at_time)
    if local_dt >= local_cutoff:
        return local_dt.date()
    return local_dt.date() - timedelta(days=1)


def closed_window_for_day(
    quote_day: date,
    tz: ZoneInfo | None = None,
    at_time: time | None = None,
) -> QuoteWindow:
    target_tz = tz or quote_timezone()
    end_local = cutoff_at(quote_day, tz=target_tz, at_time=at_time)
    start_local = cutoff_at(quote_day - timedelta(days=1), tz=target_tz, at_time=at_time)

    return QuoteWindow(
        quote_day=quote_day,
        start_local=start_local,
        end_local=end_local,
        start_utc=start_local.astimezone(timezone.utc),
        end_utc=end_local.astimezone(timezone.utc),
    )


def legacy_window_from_created_at(
    created_at: datetime,
    tz: ZoneInfo | None = None,
    at_time: time | None = None,
) -> QuoteWindow:
    local_created_at = localize_legacy_datetime(created_at, tz=tz)
    return closed_window_for_day(
        quote_day_from_local(local_created_at, at_time=at_time),
        tz=local_created_at.tzinfo if isinstance(local_created_at.tzinfo, ZoneInfo) else tz,
        at_time=at_time,
    )


def get_open_window(now: datetime | None = None) -> QuoteWindow:
    now_utc = now or utc_now()
    now_local = now_utc.astimezone(quote_timezone())
    quote_day = quote_day_from_local(now_local) + timedelta(days=1)
    start_local = cutoff_at(quote_day - timedelta(days=1))

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
    return closed_window_for_day(quote_day_from_local(now_local))
