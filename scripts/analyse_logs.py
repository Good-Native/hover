#!/usr/bin/env python3
"""Analyse captured Fly logs and emit a deterministic report.

Streams every raw line for each app in a run through a fixed set of probes
(severity, panics, HTTP status, latency, process health, autoscaler, DB, Sentry,
heartbeat, plus ad-hoc keywords). Each finding records `count`, `first_seen`,
`last_seen`, and `peak` (timestamp of the highest-count minute).

Outputs `analysis.json` (machine readable) and `analysis.md` (human readable)
into the run directory.
"""

from __future__ import annotations

import argparse
import json
import re
import statistics
import sys
from collections import Counter, defaultdict, deque
from itertools import pairwise
from datetime import datetime, timedelta
from pathlib import Path
from typing import Iterator

# Reuse run-resolution + line iteration from search_logs.
sys.path.insert(0, str(Path(__file__).parent))
from search_logs import iter_lines, resolve_runs  # noqa: E402

ISO_KEYS = ("time", "timestamp", "@timestamp", "ts", "created_at")
ANSI_RE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
LEADING_TS_RE = re.compile(r"^\s*(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)")


def _strip_ansi(s: str) -> str:
    return ANSI_RE.sub("", s)


def _iso_seconds(rec: dict | None, line: str = "") -> str:
    """Best-effort ISO timestamp: prefer JSON record fields, fall back to the
    leading timestamp token Fly stamps on every raw log line."""
    if rec:
        for k in ISO_KEYS:
            v = rec.get(k)
            if v:
                raw = str(v).replace("Z", "+00:00")
                try:
                    return datetime.fromisoformat(raw).isoformat(timespec="seconds")
                except ValueError:
                    # Malformed value for this key — keep looking. Other ISO
                    # keys or the leading-line fallback below may be valid.
                    continue
    if line:
        m = LEADING_TS_RE.match(line)
        if m:
            raw = m.group(1).replace("Z", "+00:00")
            try:
                return datetime.fromisoformat(raw).isoformat(timespec="seconds")
            except ValueError:
                return raw[:19]
    return ""


def _iso_minute(ts: str) -> str:
    return ts[:16] if len(ts) >= 16 else ts


def _parse_record(line: str) -> dict | None:
    idx = line.find("{")
    if idx == -1:
        return None
    try:
        rec = json.loads(line[idx:])
    except json.JSONDecodeError:
        return None
    return rec if isinstance(rec, dict) else None


class Bucket:
    """Tracks count plus first-seen, last-seen, and peak-minute for a label."""

    __slots__ = ("count", "first", "last", "minute_counts", "samples")

    def __init__(self) -> None:
        self.count = 0
        self.first = ""
        self.last = ""
        self.minute_counts: Counter = Counter()
        self.samples: list[str] = []

    def add(self, ts: str, sample: str | None = None) -> None:
        self.count += 1
        if ts:
            if not self.first or ts < self.first:
                self.first = ts
            if not self.last or ts > self.last:
                self.last = ts
            self.minute_counts[_iso_minute(ts)] += 1
        if sample and len(self.samples) < 3:
            trimmed = sample.strip()
            if len(trimmed) > 240:
                trimmed = trimmed[:237] + "..."
            self.samples.append(trimmed)

    def to_dict(self, label: str) -> dict:
        peak_min, peak_count = ("", 0)
        if self.minute_counts:
            peak_min, peak_count = self.minute_counts.most_common(1)[0]
        return {
            "label": label,
            "count": self.count,
            "first_seen": self.first,
            "last_seen": self.last,
            "peak": peak_min,
            "peak_count": peak_count,
            "samples": list(self.samples),
        }


# --- Probes ----------------------------------------------------------------

class Probe:
    name = ""

    def feed(self, ts: str, line: str, rec: dict | None, level: str, msg: str) -> None:
        raise NotImplementedError

    def report(self) -> dict:
        raise NotImplementedError


class SeverityProbe(Probe):
    name = "Severity"
    LEVELS = ("debug", "info", "warn", "error", "fatal")

    def __init__(self) -> None:
        self.buckets: dict[str, Bucket] = {lvl: Bucket() for lvl in self.LEVELS}
        self.unknown = Bucket()

    def feed(self, ts, line, rec, level, msg):
        if level in self.buckets:
            self.buckets[level].add(ts, msg)
        elif level:
            self.unknown.add(ts, line)

    def report(self) -> dict:
        findings = [
            self.buckets[lvl].to_dict(lvl)
            for lvl in self.LEVELS
            if self.buckets[lvl].count
        ]
        if self.unknown.count:
            findings.append(self.unknown.to_dict("unknown-level"))
        return {"name": self.name, "findings": findings}


class RegexBucketProbe(Probe):
    """Generic probe: match a list of (label, regex) and bucket per label."""

    def __init__(self, name: str, patterns: list[tuple[str, str]], flags: int = re.IGNORECASE):
        self.name = name
        self.compiled = [(label, re.compile(pat, flags)) for label, pat in patterns]
        self.buckets: dict[str, Bucket] = defaultdict(Bucket)

    def feed(self, ts, line, rec, level, msg):
        for label, pat in self.compiled:
            m = pat.search(line)
            if m:
                self.buckets[label].add(ts, line)

    def report(self) -> dict:
        findings = [b.to_dict(label) for label, b in self.buckets.items() if b.count]
        findings.sort(key=lambda f: -f["count"])
        return {"name": self.name, "findings": findings}


class PanicProbe(Probe):
    name = "Panics & fatals"

    def __init__(self) -> None:
        self.buckets: dict[str, Bucket] = defaultdict(Bucket)
        self.panic_re = re.compile(r"panic:\s*(.+)", re.IGNORECASE)
        self.fatal_re = re.compile(r"\bfatal(?:\s+error)?:\s*(.+)", re.IGNORECASE)

    def feed(self, ts, line, rec, level, msg):
        for pat, kind in ((self.panic_re, "panic"), (self.fatal_re, "fatal")):
            m = pat.search(line)
            if m:
                rest = m.group(1).strip()
                key = rest.split("\n", 1)[0][:120]
                if not key:
                    key = kind
                self.buckets[f"{kind}: {key}"].add(ts, line)
                return

    def report(self) -> dict:
        findings = sorted(
            (b.to_dict(label) for label, b in self.buckets.items()),
            key=lambda f: -f["count"],
        )
        return {"name": self.name, "findings": findings[:10]}


class HTTPStatusProbe(Probe):
    name = "HTTP status"

    def __init__(self) -> None:
        self.buckets: dict[str, Bucket] = defaultdict(Bucket)
        self.status_field = re.compile(r'\bstatus(?:_code)?[\"\']?\s*[=:]\s*\"?(\d{3})\b')
        self.access_log = re.compile(r'HTTP/\d\.\d"\s+(\d{3})\s+\d+')

    def feed(self, ts, line, rec, level, msg):
        code: str | None = None
        if rec:
            for k in ("status", "status_code", "statusCode", "http_status"):
                v = rec.get(k)
                if isinstance(v, (int, str)) and str(v).isdigit() and len(str(v)) == 3:
                    code = str(v)
                    break
        if code is None:
            m = self.access_log.search(line) or self.status_field.search(line)
            if m:
                code = m.group(1)
        if not code:
            return
        klass = f"{code[0]}xx"
        self.buckets[klass].add(ts)
        if code in ("429", "499", "500", "502", "503", "504"):
            self.buckets[code].add(ts, line)

    def report(self) -> dict:
        findings = [b.to_dict(label) for label, b in sorted(self.buckets.items())]
        findings = [f for f in findings if f["count"]]
        return {"name": self.name, "findings": findings}


class LatencyProbe(Probe):
    name = "Latency"
    DURATION_KEYS = ("dur_ms", "duration_ms", "latency_ms", "elapsed_ms", "took_ms")

    def __init__(self) -> None:
        self.values_ms: list[float] = []
        self.slowest: list[tuple[float, str, str]] = []  # (ms, ts, line)

    def _record(self, ms: float, ts: str, line: str) -> None:
        if ms < 0 or ms > 3_600_000:
            return
        self.values_ms.append(ms)
        self.slowest.append((ms, ts, line))
        if len(self.slowest) > 200:
            self.slowest.sort(key=lambda x: -x[0])
            self.slowest = self.slowest[:50]

    def feed(self, ts, line, rec, level, msg):
        if not rec:
            return
        for k in self.DURATION_KEYS:
            v = rec.get(k)
            if isinstance(v, (int, float)):
                self._record(float(v), ts, line)
                return
        # Bare `duration` follows Go/zerolog convention: integer nanoseconds.
        # Don't try to infer units from magnitude — sub-millisecond values
        # would otherwise be recorded as milliseconds and skew percentiles.
        # Apps emitting milliseconds should use the explicit `*_ms` keys above.
        d = rec.get("duration")
        if isinstance(d, (int, float)):
            self._record(float(d) / 1_000_000, ts, line)

    def report(self) -> dict:
        if not self.values_ms:
            return {"name": self.name, "findings": [], "note": "no duration fields seen"}
        vs = sorted(self.values_ms)
        n = len(vs)

        def pct(p: float) -> float:
            # Linear interpolation between adjacent ranks (Type 7 / "inclusive"),
            # the same method as `statistics.quantiles(method="inclusive")`.
            # `round()` previously hit banker's-rounding edge cases (e.g. p50
            # of [100, 300] returned 100 instead of 200).
            if n == 1 or p <= 0:
                return float(vs[0])
            if p >= 100:
                return float(vs[-1])
            rank = p / 100 * (n - 1)
            lo = int(rank)
            hi = min(lo + 1, n - 1)
            frac = rank - lo
            return float(vs[lo]) + frac * (float(vs[hi]) - float(vs[lo]))

        self.slowest.sort(key=lambda x: -x[0])
        slowest = [
            {"duration_ms": ms, "timestamp": ts, "line": (line.strip()[:240])}
            for ms, ts, line in self.slowest[:10]
        ]
        return {
            "name": self.name,
            "samples": n,
            "p50_ms": pct(50),
            "p95_ms": pct(95),
            "p99_ms": pct(99),
            "max_ms": vs[-1],
            "mean_ms": statistics.fmean(vs),
            "slowest": slowest,
        }


class HeartbeatProbe(Probe):
    """Detect minutes where info-level traffic dropped to zero mid-run."""

    name = "Heartbeat"

    def __init__(self) -> None:
        self.minute_info: Counter = Counter()
        self.minute_seen: list[str] = []

    def feed(self, ts, line, rec, level, msg):
        if not ts:
            return
        m = _iso_minute(ts)
        if m and (not self.minute_seen or self.minute_seen[-1] != m):
            self.minute_seen.append(m)
        if level == "info":
            self.minute_info[m] += 1

    def report(self) -> dict:
        if not self.minute_seen:
            return {"name": self.name, "findings": [], "note": "no timestamped traffic"}
        minutes = sorted(set(self.minute_seen))
        non_zero = [self.minute_info[m] for m in minutes if self.minute_info.get(m, 0) > 0]
        median = statistics.median(non_zero) if non_zero else 0
        gap_minutes: set[str] = set()
        # Two ways a minute can be a heartbeat gap:
        #   (1) observed in minute_seen (had warn/error traffic) but zero info,
        #   (2) entirely missing — fell between two observed minutes with no
        #       log lines at all, so `minute_seen` doesn't include it.
        if median >= 1:
            # Skip the first and last observed minutes — they're typically
            # partial windows (capture started or stopped mid-minute), so a
            # zero info-count there is expected, not a real heartbeat gap.
            for m in minutes[1:-1]:
                if self.minute_info.get(m, 0) == 0:
                    gap_minutes.add(m)
            if len(minutes) >= 2:
                for prev, curr in pairwise(minutes):
                    try:
                        p = datetime.strptime(prev, "%Y-%m-%dT%H:%M")
                        c = datetime.strptime(curr, "%Y-%m-%dT%H:%M")
                    except ValueError:
                        continue
                    step = p + timedelta(minutes=1)
                    while step < c:
                        gap_minutes.add(step.strftime("%Y-%m-%dT%H:%M"))
                        step += timedelta(minutes=1)
        gaps: list[dict] = [
            {"minute": m, "expected_min": int(median)}
            for m in sorted(gap_minutes)[:200]
        ]
        return {
            "name": self.name,
            "median_info_per_minute": median,
            "first_minute": minutes[0],
            "last_minute": minutes[-1],
            "gap_minutes": gaps[:20],
        }


def _build_probes(extra_keywords: list[str], extra_regexes: list[str]) -> list[Probe]:
    probes: list[Probe] = [
        SeverityProbe(),
        PanicProbe(),
        HTTPStatusProbe(),
        LatencyProbe(),
        HeartbeatProbe(),
        RegexBucketProbe(
            "Process health",
            [
                ("starting machine", r"starting machine"),
                ("stopping machine", r"stopping machine"),
                ("exited with code", r"exited with code\s+\d+"),
                ("out of memory", r"out of memory|oom[- ]?killed"),
                ("killed by signal", r"killed by signal|signal:\s*killed"),
                ("health check failed", r"health check.*fail"),
                ("restart", r"\brestart(ing)?\b"),
            ],
        ),
        RegexBucketProbe(
            "Autoscaler",
            [
                ("reconciling", r'"msg":\s*"reconciling"|reconciling\s+app'),
                ("scale up", r"scal(e|ing)\s*up|adding machine"),
                ("scale down", r"scal(e|ing)\s*down|removing machine"),
                ("target=N", r"target\s*[=:]\s*\d+|\"target\":\s*\{"),
                ("queue depth", r"queue[_ ]depth|backlog\s*[=:]\s*\d+"),
                ("no-op", r"no scale change|already at target"),
            ],
        ),
        RegexBucketProbe(
            "Database / external",
            [
                ("pgx error", r"\bpgx\b.*error|pgx:.*"),
                ("pq error", r"\bpq:\s"),
                ("connection refused", r"connection refused"),
                ("context deadline exceeded", r"context deadline exceeded"),
                ("i/o timeout", r"i/o timeout"),
                ("connection reset", r"connection reset"),
                ("too many connections", r"too many connections"),
            ],
        ),
        RegexBucketProbe(
            "Sentry",
            [
                ("event sent", r"sentry.*event\b|event sent to sentry"),
                ("send failed", r"sentry.*(?:fail|error)"),
            ],
        ),
    ]
    if extra_keywords or extra_regexes:
        patterns = [(f"keyword:{k}", re.escape(k)) for k in extra_keywords]
        patterns += [(f"regex:{r}", r) for r in extra_regexes]
        probes.append(RegexBucketProbe("Ad-hoc keywords", patterns))
    return probes


_DEDUPE_WINDOW = 50_000


def _analyse_app(app_dir: Path, extra_keywords: list[str], extra_regexes: list[str]) -> dict:
    probes = _build_probes(extra_keywords, extra_regexes)
    total_lines = 0
    unique_lines = 0
    parsed_records = 0
    first_ts = ""
    last_ts = ""
    # Bounded sliding-window dedupe: capture overlap is between adjacent
    # iterations, so a recent-window LRU catches all real duplicates without
    # unbounded memory growth on long, high-volume runs. Capture-time cursor
    # filtering already removes most dupes; this is the secondary safety net.
    seen: set[str] = set()
    seen_order: deque[str] = deque()

    for _source, raw_line in iter_lines(app_dir):
        total_lines += 1
        line = _strip_ansi(raw_line).rstrip()
        if not line or line in seen:
            continue
        seen.add(line)
        seen_order.append(line)
        if len(seen_order) > _DEDUPE_WINDOW:
            seen.discard(seen_order.popleft())
        unique_lines += 1
        rec = _parse_record(line)
        if rec is not None:
            parsed_records += 1
        ts = _iso_seconds(rec, line)
        level = ""
        msg = ""
        if rec:
            level = str(rec.get("level") or "").lower()
            msg = str(rec.get("msg") or rec.get("message") or "")
        if ts:
            if not first_ts or ts < first_ts:
                first_ts = ts
            if not last_ts or ts > last_ts:
                last_ts = ts
        for probe in probes:
            try:
                probe.feed(ts, line, rec, level, msg)
            except Exception as exc:  # don't let one bad line abort the whole run
                print(f"warn: probe {probe.name} on {app_dir.name}: {exc}", file=sys.stderr)

    return {
        "app": app_dir.name,
        "total_lines": total_lines,
        "unique_lines": unique_lines,
        "parsed_records": parsed_records,
        "first_seen": first_ts,
        "last_seen": last_ts,
        "probes": [p.report() for p in probes],
    }


# --- Reporting -------------------------------------------------------------

def _fmt_window(d: dict) -> str:
    first = d.get("first_seen") or "-"
    last = d.get("last_seen") or "-"
    peak = d.get("peak") or "-"
    pc = d.get("peak_count") or 0
    return f"first={first} last={last} peak={peak}({pc})"


def _render_md(report: dict) -> str:
    out: list[str] = []
    run = report["run"]
    out.append(f"# Log analysis — {run}")
    out.append("")
    out.append(f"Generated: {report['generated_at']}")
    if report.get("extra_keywords") or report.get("extra_regexes"):
        out.append(
            f"Ad-hoc terms: keywords={report.get('extra_keywords')} regexes={report.get('extra_regexes')}"
        )
    out.append("")

    for app in report["apps"]:
        out.append(f"## {app['app']}")
        unique = app.get("unique_lines", app["total_lines"])
        out.append(
            f"- lines: {app['total_lines']:,} captured / {unique:,} unique"
            f" (parsed JSON: {app['parsed_records']:,})"
            f" | window: {app.get('first_seen') or '-'} → {app.get('last_seen') or '-'}"
        )
        for probe in app["probes"]:
            out.append("")
            out.append(f"### {probe['name']}")
            if probe.get("note"):
                out.append(f"_{probe['note']}_")
            findings = probe.get("findings")
            if findings is None and probe["name"] == "Latency":
                if probe.get("samples"):
                    out.append(
                        f"- samples: {probe['samples']} | "
                        f"p50={probe['p50_ms']:.0f}ms p95={probe['p95_ms']:.0f}ms "
                        f"p99={probe['p99_ms']:.0f}ms max={probe['max_ms']:.0f}ms"
                    )
                    if probe.get("slowest"):
                        out.append("")
                        out.append("Slowest:")
                        for s in probe["slowest"][:5]:
                            out.append(
                                f"  - {s['duration_ms']:.0f}ms @ {s['timestamp']}: {s['line']}"
                            )
                continue
            if probe["name"] == "Heartbeat":
                out.append(
                    f"- median info/min: {probe.get('median_info_per_minute')} | "
                    f"window: {probe.get('first_minute')} → {probe.get('last_minute')}"
                )
                gaps = probe.get("gap_minutes") or []
                if gaps:
                    out.append(f"- {len(gaps)} zero-info minute(s):")
                    for g in gaps[:10]:
                        out.append(f"  - {g['minute']}")
                continue
            if not findings:
                out.append("- (no matches)")
                continue
            for f in findings:
                line = f"- {f['label']}: {f['count']} | {_fmt_window(f)}"
                out.append(line)
                for s in f.get("samples", [])[:1]:
                    out.append(f"    sample: {s}")
        out.append("")
    return "\n".join(out)


def _select_app_dirs(run: Path, app_filter: list[str] | None) -> list[Path]:
    out = []
    for child in sorted(run.iterdir()):
        if not child.is_dir():
            continue
        if app_filter and child.name not in app_filter:
            continue
        if (child / "raw").exists() or (child / "raw.zip").exists():
            out.append(child)
    return out


def main() -> int:
    p = argparse.ArgumentParser(description="Analyse captured Fly logs.")
    p.add_argument("--run", default=None, help="'latest' (default), 'all', a date, or a run dir.")
    p.add_argument("--root", default="logs", help="Logs root directory (default: logs).")
    p.add_argument("--app", default="", help="Comma-separated app filter (default: all apps).")
    p.add_argument("--keyword", action="append", default=[], help="Ad-hoc keyword (repeatable).")
    p.add_argument("--regex", action="append", default=[], help="Ad-hoc regex (repeatable).")
    p.add_argument("--out", default=None, help="Override output base path (writes .json + .md).")
    p.add_argument("--stdout", action="store_true", help="Print markdown to stdout in addition to writing files.")
    args = p.parse_args()

    root = Path(args.root)
    runs = resolve_runs(args.run, root)
    if not runs:
        print(f"No runs matched under {root} (run={args.run!r})", file=sys.stderr)
        return 1
    if len(runs) > 1 and args.out:
        # `--out` shares a single base path across the loop below, so multiple
        # runs would silently overwrite each other. Without `--out` each run
        # writes to its own `<run>/analysis.{md,json}` and multi-run selection
        # is fine.
        print(
            f"Resolved {len(runs)} runs; --out requires a single run because "
            f"combined output isn't implemented.",
            file=sys.stderr,
        )
        return 2

    apps = [a.strip() for a in args.app.split(",") if a.strip()] or None

    rc = 0
    for run in runs:
        app_dirs = _select_app_dirs(run, apps)
        if not app_dirs:
            print(f"No app dirs with raw logs under {run}", file=sys.stderr)
            rc = rc or 1
            continue
        app_reports = [_analyse_app(d, args.keyword, args.regex) for d in app_dirs]
        report = {
            "run": str(run.relative_to(root)),
            "generated_at": datetime.now().astimezone().isoformat(timespec="seconds"),
            "extra_keywords": args.keyword,
            "extra_regexes": args.regex,
            "apps": app_reports,
        }
        if args.out:
            base = Path(args.out)
        else:
            base = run / "analysis"
        base.parent.mkdir(parents=True, exist_ok=True)
        json_path = base.with_suffix(".json")
        md_path = base.with_suffix(".md")
        json_path.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")
        md = _render_md(report)
        md_path.write_text(md, encoding="utf-8")
        print(f"wrote {md_path}")
        print(f"wrote {json_path}")
        if args.stdout:
            print(md)
    return rc


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
