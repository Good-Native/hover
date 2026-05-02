# Monitor Fly Logs

Capture, search, and analyse Fly.io logs via `scripts/logs.sh`.

## Default usage

```bash
./scripts/logs.sh
```

Captures from all five Fly apps (`hover`, `hover-worker`, `hover-analysis`,
`hover-autoscaler-worker`, `hover-autoscaler-analysis`) every 3s, runs an
analyse snapshot every 5 minutes, and writes a final report when the run
finishes (~72 minutes by default). Press Ctrl+C to stop early — the final report
still writes.

## Subcommands

```bash
./scripts/logs.sh monitor [...]   # explicit form of the default
./scripts/logs.sh search  [...]   # grep captured raw logs
./scripts/logs.sh analyse [...]   # run probes, write analysis.md/json
```

## Common options

```bash
./scripts/logs.sh --interval 5 --iterations 720       # 5s × 1h
./scripts/logs.sh --run-id "incident-pr349"           # custom slug
./scripts/logs.sh --analyse-every 30s                 # tighter snapshots
./scripts/logs.sh --analyse-every 0                   # disable snapshots
./scripts/logs.sh --app hover,hover-worker            # subset of apps

./scripts/logs.sh search --keyword panic --keyword pgx
./scripts/logs.sh search --regex 'status[":]+5\d\d' --app hover

./scripts/logs.sh analyse --keyword "deadline exceeded"
./scripts/logs.sh analyse --run 20260502/1430_mellow-rose_3s_1h
```

## Output structure

```text
logs/YYYYMMDD/HHMM_<slug>_<settings>/
├── <app>/raw/*.log          # cursor-filtered captures (one per iteration)
├── <app>/.cursor            # last-seen ISO timestamp per app
├── snapshots/
│   ├── analysis_<HHMMSS>Z.md
│   └── analysis_<HHMMSS>Z.json
├── analysis.md              # final probe report
├── analysis.json
└── monitor.log              # verbose run history
```

## Probes (analyse)

Severity, panics & fatals, HTTP status, latency (p50/p95/p99 + slowest),
heartbeat, process health, autoscaler, database/external errors, Sentry, plus
any ad-hoc `--keyword`/`--regex`. Every finding records `count`, `first_seen`,
`last_seen`, and `peak` (timestamp of the highest-count minute).

The legacy `scripts/monitor_logs.sh` still works — it forwards to
`./scripts/logs.sh monitor`.
