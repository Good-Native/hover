#!/usr/bin/env python3

"""Transform raw Fly.io logs into per-minute summaries and a flat line CSV."""

from __future__ import annotations

import csv
import json
import sys
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, Iterable, Tuple
from zoneinfo import ZoneInfo

# Fields that are always extracted as dedicated columns in the flat CSV.
_CORE_FIELDS = {"time", "timestamp", "@timestamp", "ts", "created_at", "level", "component", "msg", "message"}


def _normalise_timestamp(record: Dict[str, Any]) -> str:
    """Return an ISO minute string (YYYY-MM-DDTHH:MM) for a log record."""
    for key in ("time", "timestamp", "@timestamp", "ts", "created_at"):
        if record.get(key):
            raw = str(record[key])
            break
    else:
        return "unknown"

    cleaned = raw.replace("Z", "+00:00")
    try:
        dt = datetime.fromisoformat(cleaned)
    except ValueError:
        return raw[:16] if len(raw) >= 16 else raw

    return dt.strftime("%Y-%m-%dT%H:%M")


def _normalise_full_timestamp(record: Dict[str, Any]) -> str:
    """Return a full ISO timestamp (seconds precision) for the flat CSV."""
    for key in ("time", "timestamp", "@timestamp", "ts", "created_at"):
        if record.get(key):
            raw = str(record[key])
            break
    else:
        return ""

    cleaned = raw.replace("Z", "+00:00")
    try:
        dt = datetime.fromisoformat(cleaned)
        # Preserve the source timezone offset — two instants with different
        # offsets would otherwise collapse to identical CSV rows.
        return dt.isoformat(timespec="seconds")
    except ValueError:
        return raw[:19] if len(raw) >= 19 else raw


def _iter_records(lines: Iterable[str]) -> Iterable[Tuple[Dict[str, Any], str]]:
    for line in lines:
        idx = line.find("{")
        if idx == -1:
            yield None, line  # type: ignore[misc]
            continue
        payload = line[idx:]
        try:
            data = json.loads(payload)
        except json.JSONDecodeError:
            yield None, line  # type: ignore[misc]
            continue
        yield data, line


def _strip_component_prefix(message: str, component: str) -> str:
    """Remove the [component] prefix only when it matches the known component."""
    if not component or component == "unknown":
        return message
    prefix = f"[{component}]"
    return message[len(prefix):].lstrip() if message.startswith(prefix) else message


def summarise_logs(raw_path: Path, flat_csv_path: Path | None = None) -> Dict[str, Any]:
    level_counts: Dict[str, Counter] = defaultdict(Counter)
    event_counts: Dict[str, Counter] = defaultdict(Counter)  # "component: message" keys
    warn_error_counts: Counter = Counter()  # global "component: message" at warn/error level
    component_counts: Dict[str, Counter] = defaultdict(Counter)

    total = 0
    parsed = 0
    errors = 0

    flat_rows = []

    with raw_path.open("r", encoding="utf-8", errors="ignore") as handle:
        for record, _original in _iter_records(handle):
            total += 1
            if record is None:
                errors += 1
                continue

            parsed += 1
            minute = _normalise_timestamp(record)

            level = str(record.get("level") or "unknown").lower()
            level_counts[minute][level] += 1

            component = str(record.get("component") or "unknown")
            component_counts[minute][component] += 1

            raw_msg = str(record.get("msg") or record.get("message") or "<no message>")
            message = _strip_component_prefix(raw_msg, component)
            event = f"{component}: {message}"
            event_counts[minute][event] += 1
            if level in ("warn", "error"):
                warn_error_counts[event] += 1

            if flat_csv_path is not None:
                extras = {k: v for k, v in record.items() if k not in _CORE_FIELDS}
                flat_rows.append({
                    "timestamp": _normalise_full_timestamp(record),
                    "level": level,
                    "component": component,
                    "message": message,
                    "extras": json.dumps(extras, separators=(",", ":")) if extras else "",
                })

    if flat_csv_path is not None:
        with flat_csv_path.open("w", newline="", encoding="utf-8") as f:
            writer = csv.DictWriter(f, fieldnames=["timestamp", "level", "component", "message", "extras"])
            writer.writeheader()
            if flat_rows:
                writer.writerows(flat_rows)

    event_summary: Dict[str, Any] = {}
    for minute, counter in event_counts.items():
        event_summary[minute] = [
            {"event": event, "count": count}
            for event, count in sorted(counter.items(), key=lambda x: -x[1])
        ]

    melbourne_tz = ZoneInfo("Australia/Melbourne")
    now = datetime.now(melbourne_tz)

    return {
        "meta": {
            "source": str(raw_path),
            "total_lines": total,
            "parsed": parsed,
            "failed_to_parse": errors,
            "generated_at": now.isoformat(),
        },
        "level_counts": {minute: dict(counter) for minute, counter in level_counts.items()},
        "component_counts": {minute: dict(counter) for minute, counter in component_counts.items()},
        "event_counts": event_summary,
        "warn_error_counts": dict(warn_error_counts),
    }


def main() -> int:
    if len(sys.argv) != 3:
        print("Usage: process_logs.py <raw_log_file> <output_json>", file=sys.stderr)
        return 1

    raw_path = Path(sys.argv[1])
    output_path = Path(sys.argv[2])

    if not raw_path.exists():
        print(f"Raw log file not found: {raw_path}", file=sys.stderr)
        return 1

    # Flat CSV lives alongside the JSON summary, same name but .csv
    flat_csv_path = output_path.with_suffix(".csv")

    summary = summarise_logs(raw_path, flat_csv_path)
    output_path.write_text(json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8")

    meta = summary["meta"]
    all_components: Counter = Counter()
    for c in summary["component_counts"].values():
        all_components.update(c)
    component_summary = ", ".join(
        f"{k}:{v}" for k, v in sorted(all_components.items(), key=lambda x: -x[1])
    )
    print(
        f"Processed {meta['parsed']}/{meta['total_lines']} lines from {raw_path.name};"
        f" summary written to {output_path.name}"
    )
    if component_summary:
        print(f"Components: {component_summary}")

    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
