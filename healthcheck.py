"""Container liveness probe.

The scheduler refreshes a heartbeat file every 30s. If it is missing or stale,
the asyncio event loop is wedged and Docker should restart the container.
"""
import os
import sys
from datetime import datetime, timezone

_MAX_AGE_SECONDS = 120
_DEFAULT_PATH = os.path.join(os.environ.get("LOGS_PATH", "logs"), "heartbeat")


def main() -> int:
    path = os.environ.get("HEARTBEAT_FILE", _DEFAULT_PATH)
    try:
        with open(path, "r", encoding="utf-8") as handle:
            stamp = datetime.fromisoformat(handle.read().strip())
    except Exception as exc:  # missing/unreadable/corrupt
        print(f"heartbeat unavailable: {exc}")
        return 1

    if stamp.tzinfo is None:
        stamp = stamp.replace(tzinfo=timezone.utc)
    age = (datetime.now(timezone.utc) - stamp).total_seconds()
    if age > _MAX_AGE_SECONDS:
        print(f"stale heartbeat: {age:.0f}s old")
        return 1
    print(f"ok ({age:.0f}s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
