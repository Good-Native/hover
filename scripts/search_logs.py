#!/usr/bin/env python3
"""Search captured Fly logs by keyword/regex across one or more apps.

Reads raw captures from a `logs/YYYYMMDD/HHMM_<run-id>/<app>/raw/` directory or
the zipped `raw.zip` produced by monitor cleanup. Prints matching lines with a
source prefix and a per-app/per-run match count summary on stderr.
"""

from __future__ import annotations

import argparse
import re
import sys
import zipfile
from pathlib import Path
from typing import Iterator


def _candidate_run_dirs(root: Path) -> list[Path]:
    """Return run directories under `logs/YYYYMMDD/HHMM_*` sorted oldest -> newest."""
    out: list[Path] = []
    if not root.exists():
        return out
    for date_dir in sorted(root.iterdir()):
        if not date_dir.is_dir() or len(date_dir.name) != 8 or not date_dir.name.isdigit():
            continue
        for run in sorted(date_dir.iterdir()):
            if run.is_dir():
                out.append(run)
    return out


def resolve_runs(run_arg: str | None, root: Path) -> list[Path]:
    """Resolve the --run argument to one or more run directories.

    None / 'latest' -> [most recent run]
    'all'           -> every run
    'YYYYMMDD'      -> runs under that date
    other           -> prefix match against `<date>/<runname>` or run name
    """
    runs = _candidate_run_dirs(root)
    if not runs:
        return []
    if run_arg in (None, "", "latest"):
        return [runs[-1]]
    if run_arg == "all":
        return runs
    target = run_arg.strip("/")
    matched: list[Path] = []
    for r in runs:
        rel = str(r.relative_to(root))
        if rel == target or rel.startswith(target + "/"):
            matched.append(r)
            continue
        if r.parent.name == target or r.name == target or r.name.startswith(target):
            matched.append(r)
    return matched


def _iter_app_dirs(run_dir: Path, app_filter: list[str] | None) -> Iterator[Path]:
    for child in sorted(run_dir.iterdir()):
        if not child.is_dir():
            continue
        if app_filter and child.name not in app_filter:
            continue
        if (child / "raw").exists() or (child / "raw.zip").exists():
            yield child


def iter_lines(app_dir: Path) -> Iterator[tuple[str, str]]:
    """Yield (source_label, line) for every captured raw log line in an app dir."""
    raw_dir = app_dir / "raw"
    if raw_dir.exists():
        for log in sorted(raw_dir.glob("*.log")):
            with log.open("r", encoding="utf-8", errors="ignore") as f:
                for line in f:
                    yield (log.name, line.rstrip("\n"))
    raw_zip = app_dir / "raw.zip"
    if raw_zip.exists():
        with zipfile.ZipFile(raw_zip) as zf:
            for info in sorted(zf.infolist(), key=lambda i: i.filename):
                if info.is_dir() or not info.filename.endswith(".log"):
                    continue
                with zf.open(info) as f:
                    for raw_line in f:
                        yield (
                            Path(info.filename).name,
                            raw_line.decode("utf-8", errors="ignore").rstrip("\n"),
                        )


def search(
    runs: list[Path],
    pattern: re.Pattern,
    app_filter: list[str] | None,
    max_matches: int,
    root: Path,
) -> int:
    total = 0
    stop = False
    for run in runs:
        per_app: dict[str, int] = {}
        rel_run = run.relative_to(root)
        for app_dir in _iter_app_dirs(run, app_filter):
            count = 0
            for source, line in iter_lines(app_dir):
                if pattern.search(line):
                    count += 1
                    total += 1
                    print(f"[{rel_run}][{app_dir.name}][{source}] {line}")
                    if max_matches and total >= max_matches:
                        stop = True
                        break
            per_app[app_dir.name] = count
            if stop:
                break
        for app, n in per_app.items():
            print(f"# {rel_run} {app}: {n} match(es)", file=sys.stderr)
        if stop:
            print("# match cap reached", file=sys.stderr)
            break
    print(f"# total matches: {total}", file=sys.stderr)
    return total


def main() -> int:
    p = argparse.ArgumentParser(description="Search captured Fly logs.")
    p.add_argument("--keyword", action="append", default=[], help="Literal substring (repeatable).")
    p.add_argument("--regex", action="append", default=[], help="Regex pattern (repeatable).")
    p.add_argument("--app", default="", help="Comma-separated app filter (default: all apps in run).")
    p.add_argument("--run", default=None, help="'latest' (default), 'all', a date, or a run dir.")
    p.add_argument("--root", default="logs", help="Logs root directory (default: logs).")
    p.add_argument("-i", "--ignore-case", action="store_true", default=True,
                   help="Match case-insensitively (default: on).")
    p.add_argument("--case-sensitive", action="store_true",
                   help="Force case-sensitive matching (overrides --ignore-case).")
    p.add_argument("--max", type=int, default=0, help="Stop after N total matches (0 = unlimited).")
    args = p.parse_args()

    if not args.keyword and not args.regex:
        print("Provide at least one --keyword or --regex.", file=sys.stderr)
        return 2
    if args.max < 0:
        print("--max must be >= 0 (0 = unlimited).", file=sys.stderr)
        return 2

    parts = [re.escape(k) for k in args.keyword] + list(args.regex)
    flags = 0 if args.case_sensitive else re.IGNORECASE
    try:
        pattern = re.compile("|".join(f"(?:{p})" for p in parts), flags)
    except re.error as exc:
        print(f"Invalid regex: {exc}", file=sys.stderr)
        return 2

    root = Path(args.root)
    runs = resolve_runs(args.run, root)
    if not runs:
        print(f"No runs matched under {root} (run={args.run!r})", file=sys.stderr)
        return 1

    apps = [a.strip() for a in args.app.split(",") if a.strip()] or None
    search(runs, pattern, apps, args.max, root)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
