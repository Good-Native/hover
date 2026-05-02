#!/usr/bin/env python3
"""Filter Fly log lines from stdin against a per-app timestamp cursor.

Keeps only lines whose leading ISO timestamp is strictly greater than the
cursor stored at the given path, then updates the cursor to the max timestamp
seen. Used by `logs.sh monitor` so overlapping `flyctl logs --no-tail` captures
only persist new lines per iteration.

Lines without a parseable leading timestamp (typically multi-line stack-trace
continuations) are emitted only when the most recent timestamped header was
emitted. That preserves multi-line records when their header survives the
cursor while preventing stale continuation blocks from re-appearing in
overlapping `flyctl logs --no-tail` windows.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ANSI_RE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
LEADING_TS_RE = re.compile(
    r"^\s*(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)"
)


def _normalise(ts: str) -> str:
    """Normalise to a lexicographically-comparable form (Z -> +00:00)."""
    return ts.replace("Z", "+00:00")


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: filter_since.py <cursor_file>", file=sys.stderr)
        return 2

    cursor_path = Path(sys.argv[1])
    cursor = ""
    if cursor_path.exists():
        cursor = cursor_path.read_text(encoding="utf-8").strip()

    new_max = cursor
    out = sys.stdout
    # Multi-line records (e.g. Go panic stack traces) only stamp a timestamp on
    # the first line. Track whether the most recent header was emitted so we
    # don't re-persist continuation lines whose header was dropped by the
    # cursor — that would reintroduce duplicates this filter exists to remove.
    header_emitted = False
    for line in sys.stdin:
        clean = ANSI_RE.sub("", line)
        m = LEADING_TS_RE.match(clean)
        if not m:
            if header_emitted:
                out.write(line)
            continue
        ts = _normalise(m.group(1))
        if cursor and ts <= cursor:
            header_emitted = False
            continue
        out.write(line)
        header_emitted = True
        if ts > new_max:
            new_max = ts

    if new_max and new_max != cursor:
        cursor_path.parent.mkdir(parents=True, exist_ok=True)
        cursor_path.write_text(new_max, encoding="utf-8")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        # Bash forwards SIGINT to every child in the pipe. Exit quietly so
        # `logs.sh`'s own trap can write the final report without a traceback
        # bleeding into the terminal.
        sys.exit(130)
