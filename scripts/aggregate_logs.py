#!/usr/bin/env python3
"""
Aggregate JSON log summaries into time-series CSVs for agent debugging.

Outputs:
  time_series.csv            — timestamp | debug | info | warn | error
  events_per_minute.csv      — timestamp | component: message | ...
  components_per_minute.csv  — timestamp | component | ...
  summary.md                 — human-readable overview

Usage:
    python3 scripts/aggregate_logs.py logs/20251105/0750_run/
    python3 scripts/aggregate_logs.py logs/20251105/0750_run/ --watch
    python3 scripts/aggregate_logs.py logs/20251105/0750_run/ --full

Incremental mode (the default) fingerprints each input file by mtime+size and
only re-ingests files whose fingerprint changed. That keeps incremental runs
cheap, with two important caveats:

1. If an input is *rewritten in place* with the same mtime and size, the
   fingerprint does not change and the new content is silently skipped.
2. When a fingerprint DOES change and the file is reprocessed, its previous
   contribution is NOT subtracted from the aggregates — the new counts are
   added on top of the old ones, which inflates totals for any event/level
   that appears in both the old and new version of the file.

Both cases only matter when the upstream producer may overwrite existing
files. Pass `--full` to force a clean reprocess in those situations. Pure
append-only workloads (new files added to the directory, existing files never
rewritten) are always handled correctly by incremental mode.
"""

import csv
import json
import sys
import time
from pathlib import Path
from collections import defaultdict
from datetime import datetime
from zoneinfo import ZoneInfo

# Lossless state persisted between incremental runs.
DATA_FILE_NAME = ".aggregate_data.json"


def _empty_minute():
    return {
        "level_counts": defaultdict(int),
        "event_counts": defaultdict(int),  # "component: message" -> count
        "component_counts": defaultdict(int),
        "samples": 0,
    }


def _empty_totals():
    return {"total_lines": 0, "failed_to_parse": 0, "warn_error_counts": defaultdict(int)}


def load_data(log_dir):
    """Load full aggregate state from the lossless JSON data file."""
    data_file = log_dir / DATA_FILE_NAME
    if data_file.exists():
        try:
            with open(data_file) as f:
                saved = json.load(f)

            by_minute = defaultdict(_empty_minute)
            for minute, bucket in saved.get("by_minute", {}).items():
                by_minute[minute]["samples"] = bucket.get("samples", 0)
                by_minute[minute]["level_counts"] = defaultdict(
                    int, bucket.get("level_counts", {})
                )
                by_minute[minute]["event_counts"] = defaultdict(
                    int, bucket.get("event_counts", {})
                )
                by_minute[minute]["component_counts"] = defaultdict(
                    int, bucket.get("component_counts", {})
                )

            totals = {
                "total_lines": saved.get("total_lines", 0),
                "failed_to_parse": saved.get("failed_to_parse", 0),
                "warn_error_counts": defaultdict(int, saved.get("warn_error_counts", {})),
            }
            # processed_files is a {basename: "mtime:size"} fingerprint map.
            # Older runs stored a plain list; coerce that into an empty map so
            # rewritten files are reprocessed rather than silently skipped.
            raw_processed = saved.get("processed_files", {})
            if isinstance(raw_processed, dict):
                processed_files = dict(raw_processed)
            else:
                processed_files = {}
            return by_minute, totals, processed_files
        except (OSError, json.JSONDecodeError, KeyError, TypeError) as e:
            print(f"Warning: Could not load data file: {e}", file=sys.stderr)

    return defaultdict(_empty_minute), _empty_totals(), {}


def _file_fingerprint(path):
    """Return a cheap content-ish fingerprint (mtime + size) for incremental skips."""
    try:
        stat = path.stat()
    except OSError:
        return ""
    return f"{int(stat.st_mtime_ns)}:{stat.st_size}"


def save_data(log_dir, by_minute, totals, processed_files):
    """Persist full aggregate state to the lossless JSON data file."""
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    data = {
        "last_update": datetime.now(melbourne_tz).isoformat(),
        "processed_files": dict(sorted(processed_files.items())),
        "total_lines": totals["total_lines"],
        "failed_to_parse": totals["failed_to_parse"],
        "warn_error_counts": dict(totals["warn_error_counts"]),
        "by_minute": {
            minute: {
                "samples": bucket["samples"],
                "level_counts": dict(bucket["level_counts"]),
                "event_counts": dict(bucket["event_counts"]),
                "component_counts": dict(bucket["component_counts"]),
            }
            for minute, bucket in by_minute.items()
        },
    }
    data_file = log_dir / DATA_FILE_NAME
    tmp_file = log_dir / f"{DATA_FILE_NAME}.tmp"
    with open(tmp_file, "w") as f:
        json.dump(data, f, separators=(",", ":"))
    tmp_file.replace(data_file)


def process_json_file(json_file, by_minute, totals):
    """Merge a single iteration JSON into the running aggregation."""
    try:
        with open(json_file) as f:
            data = json.load(f)

        meta = data.get("meta", {})
        file_total_lines = int(meta.get("total_lines", 0))
        file_failed = int(meta.get("failed_to_parse", 0))

        # Stage all changes locally so a mid-parse error leaves no partial state.
        staged: dict = defaultdict(_empty_minute)
        staged_warn_error: dict = defaultdict(int)

        for ts, levels in data.get("level_counts", {}).items():
            minute_key = ts[:16]
            staged[minute_key]["samples"] += 1
            for level, count in levels.items():
                staged[minute_key]["level_counts"][level] += count

        # event_counts: list of {"event": "component: message", "count": N}
        for ts, events in data.get("event_counts", {}).items():
            minute_key = ts[:16]
            for item in events:
                event = item.get("event", "unknown")
                count = item.get("count", 0)
                staged[minute_key]["event_counts"][event] += count

        for ts, components in data.get("component_counts", {}).items():
            minute_key = ts[:16]
            for component, count in components.items():
                staged[minute_key]["component_counts"][component] += count

        for event, count in data.get("warn_error_counts", {}).items():
            staged_warn_error[event] += int(count)

        # Parsing succeeded — commit atomically.
        totals["total_lines"] += file_total_lines
        totals["failed_to_parse"] += file_failed
        for event, count in staged_warn_error.items():
            totals["warn_error_counts"][event] += count
        for minute_key, bucket in staged.items():
            by_minute[minute_key]["samples"] += bucket["samples"]
            for level, count in bucket["level_counts"].items():
                by_minute[minute_key]["level_counts"][level] += count
            for event, count in bucket["event_counts"].items():
                by_minute[minute_key]["event_counts"][event] += count
            for component, count in bucket["component_counts"].items():
                by_minute[minute_key]["component_counts"][component] += count

        return True
    except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as e:
        # Mirror the narrow catch used by load_data() so an AttributeError
        # from a real bug in the aggregation logic above propagates instead
        # of being swallowed as a "just another unparseable file" warning.
        print(f"Error processing {json_file}: {e}", file=sys.stderr)
        return False


def write_time_series(csv_path, by_minute):
    """timestamp | debug | info | warn | error"""
    with open(csv_path, "w") as f:
        f.write("timestamp,debug,info,warn,error\n")
        for minute in sorted(by_minute.keys()):
            levels = by_minute[minute]["level_counts"]
            f.write(
                f"{minute},"
                f"{levels.get('debug', 0)},"
                f"{levels.get('info', 0)},"
                f"{levels.get('warn', 0)},"
                f"{levels.get('error', 0)}\n"
            )


def write_events_csv(csv_path, by_minute, top_n=50):
    """timestamp | component: message | component: message | ...

    Columns are the top-N events by total count across all minutes.
    """
    totals = defaultdict(int)
    for bucket in by_minute.values():
        for event, count in bucket["event_counts"].items():
            totals[event] += count

    top_events = [e for e, _ in sorted(totals.items(), key=lambda x: -x[1])[:top_n]]

    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["timestamp", *top_events])
        for minute in sorted(by_minute.keys()):
            counts = [by_minute[minute]["event_counts"].get(e, 0) for e in top_events]
            writer.writerow([minute, *counts])


def write_components_csv(csv_path, by_minute):
    """timestamp | component | component | ..."""
    all_components = sorted(
        {c for bucket in by_minute.values() for c in bucket["component_counts"]}
    )
    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["timestamp", *all_components])
        for minute in sorted(by_minute.keys()):
            counts = [by_minute[minute]["component_counts"].get(c, 0) for c in all_components]
            writer.writerow([minute, *counts])


def write_summary(summary_path, by_minute, totals, new_files_count):
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    now = datetime.now(melbourne_tz)

    total_lines = totals["total_lines"]
    total_failed = totals["failed_to_parse"]

    event_totals = defaultdict(int)
    for bucket in by_minute.values():
        for event, count in bucket["event_counts"].items():
            event_totals[event] += count

    # Time range excludes any non-timestamp sentinel keys (defensive).
    ts_keys = sorted(by_minute.keys())

    with open(summary_path, "w") as f:
        f.write("# Log Aggregation Summary\n\n")
        f.write(f"**Generated:** {now.isoformat()}\n\n")
        f.write(f"**New files processed:** {new_files_count}\n\n")
        if ts_keys:
            f.write(f"**Time range:** {ts_keys[0]} to {ts_keys[-1]}\n\n")
        f.write(f"- Total log lines: **{total_lines:,}**\n")
        f.write(
            f"- Parse success rate: **{100 * (1 - total_failed / max(total_lines, 1)):.1f}%**\n\n"
        )

        f.write("## Log Levels by Minute\n\n")
        f.write("| Timestamp | Debug | Info | Warn | Error |\n")
        f.write("|-----------|-------|------|------|-------|\n")
        for minute in ts_keys[-30:]:
            levels = by_minute[minute]["level_counts"]
            f.write(
                f"| {minute} | {levels.get('debug', 0)} | {levels.get('info', 0)} |"
                f" {levels.get('warn', 0)} | {levels.get('error', 0)} |\n"
            )

        f.write("\n## Top 30 Events\n\n")
        f.write("| Count | Event |\n")
        f.write("|-------|-------|\n")
        # Precompute the pipe-escaped label outside the f-string: backslashes
        # inside f-string expressions are a SyntaxError on Python 3.11 and
        # earlier (PEP 701 only relaxed that in 3.12).
        for event, count in sorted(event_totals.items(), key=lambda x: -x[1])[:30]:
            escaped_event = event[:80].replace("|", "\\|")
            f.write(f"| {count:,} | {escaped_event} |\n")

        warn_and_error = sorted(
            totals["warn_error_counts"].items(), key=lambda x: -x[1]
        )
        if warn_and_error:
            f.write("\n## Errors & Warnings\n\n")
            f.write("| Count | Event |\n")
            f.write("|-------|-------|\n")
            for event, count in warn_and_error[:20]:
                escaped_event = event[:80].replace("|", "\\|")
                f.write(f"| {count:,} | {escaped_event} |\n")


def aggregate_logs(log_dir, incremental=True):
    log_path = Path(log_dir)
    if not log_path.exists():
        print(f"Error: Directory {log_dir} does not exist")
        return False

    csv_path = log_path / "time_series.csv"
    events_csv_path = log_path / "events_per_minute.csv"
    components_csv_path = log_path / "components_per_minute.csv"
    summary_path = log_path / "summary.md"

    if incremental:
        by_minute, totals, processed_map = load_data(log_path)
    else:
        by_minute, totals, processed_map = defaultdict(_empty_minute), _empty_totals(), {}

    all_json_files = sorted(f for f in log_path.glob("*.json") if not f.name.startswith("."))
    # Compare against a fingerprint (mtime+size) rather than basename alone so
    # regenerated files are reprocessed instead of silently skipped. Cache the
    # fingerprint so we don't stat the file a second time when recording it,
    # closing the race window where the file could change between checks.
    fingerprints: dict[str, str] = {}
    new_files = []
    changed_known_file = False
    for f in all_json_files:
        fp = _file_fingerprint(f)
        prev = processed_map.get(f.name)
        if prev != fp:
            # If we had already processed this exact filename with a different
            # fingerprint, the file was rotated/rewritten. Incremental re-add
            # would double-count its lines, so force a cold rebuild instead.
            if prev is not None and prev != "":
                changed_known_file = True
            fingerprints[f.name] = fp
            new_files.append(f)

    if changed_known_file and incremental:
        print(
            "Detected rewritten log file(s); discarding cached aggregate and"
            " rebuilding from scratch to avoid double-counting."
        )
        by_minute, totals, processed_map = defaultdict(_empty_minute), _empty_totals(), {}
        # Reprocess every known JSON file, not just the changed ones.
        new_files = list(all_json_files)
        fingerprints = {f.name: _file_fingerprint(f) for f in new_files}

    if not new_files and not incremental:
        # Cold run with nothing to process — bail out without creating empty
        # artefacts. Incremental mode falls through so previously computed
        # aggregates still refresh their derived outputs.
        print(f"No JSON files found in {log_dir}")
        return False

    success = 0
    if new_files:
        print(f"Processing {len(new_files)} new or changed files...")
        for json_file in new_files:
            if process_json_file(json_file, by_minute, totals):
                processed_map[json_file.name] = fingerprints[json_file.name]
                success += 1
                if success % 10 == 0:
                    print(f"  {success}/{len(new_files)}...")
        print(f"Successfully processed {success}/{len(new_files)} new or changed files")
    else:
        # Incremental no-op: load_data() already rehydrated by_minute/totals,
        # so we still rewrite the CSVs and summary.md from the restored state
        # rather than forcing the operator to rerun with --full.
        print(
            f"No new files to process (already processed {len(processed_map)} files);"
            " refreshing derived outputs from cached aggregate"
        )

    write_time_series(csv_path, by_minute)
    write_events_csv(events_csv_path, by_minute, top_n=50)
    write_components_csv(components_csv_path, by_minute)
    write_summary(summary_path, by_minute, totals, success)

    save_data(log_path, by_minute, totals, processed_map)

    print("\nOutputs:")
    print(f"  {csv_path}")
    print(f"  {events_csv_path}")
    print(f"  {components_csv_path}")
    print(f"  {summary_path}")
    return True


def watch_mode(log_dir, interval=10):
    print(f"Watch mode: {log_dir} (every {interval}s) — Ctrl+C to stop\n")
    try:
        while True:
            aggregate_logs(log_dir, incremental=True)
            time.sleep(interval)
    except KeyboardInterrupt:
        print("\nWatch mode stopped")


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Aggregate JSON log summaries")
    parser.add_argument("log_dir", help="Directory containing JSON log files")
    parser.add_argument("--watch", action="store_true", help="Continuously process new files")
    parser.add_argument("--interval", type=int, default=10, help="Check interval in watch mode (default: 10s)")
    parser.add_argument("--full", action="store_true", help="Full reprocess (ignore state)")
    args = parser.parse_args()

    try:
        if args.watch:
            watch_mode(args.log_dir, args.interval)
        else:
            aggregate_logs(args.log_dir, incremental=not args.full)
    except KeyboardInterrupt:
        sys.exit(130)
