#!/usr/bin/env bash

# logs.sh — unified Fly log tool: monitor / search / analyse.
#
#   logs.sh monitor [...]   capture logs on a fixed cadence (was monitor_logs.sh)
#   logs.sh search  [...]   grep across captured raw logs (zipped or live)
#   logs.sh analyse [...]   run probes, write analysis.md/json into a run dir
#
# All subcommands accept --help.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

usage_top() {
    cat <<'USAGE'
Usage: logs.sh <command> [options]

Commands:
  monitor   Capture Fly logs on a cadence and aggregate per-minute summaries.
  search    Grep captured raw logs by keyword/regex across one or more apps.
  analyse   Run pre-built probes (severity, panics, HTTP, latency, autoscaler,
            DB, Sentry, heartbeat, ad-hoc) over a run; writes analysis.md/json.

Run `logs.sh <command> --help` for command-specific options.
USAGE
}

# Locate a working Python interpreter shared by search/analyse helpers.
resolve_python() {
    if command -v python3 >/dev/null 2>&1 && python3 -c "import sys" >/dev/null 2>&1; then
        PYTHON_CMD="python3"
        PYTHON_ARGS=()
    elif command -v python >/dev/null 2>&1 && python -c "import sys" >/dev/null 2>&1; then
        PYTHON_CMD="python"
        PYTHON_ARGS=()
    elif command -v py >/dev/null 2>&1 && py -3 -c "import sys" >/dev/null 2>&1; then
        PYTHON_CMD="py"
        PYTHON_ARGS=(-3)
    else
        echo "Python 3 is required for this command but was not found." >&2
        exit 1
    fi
}

cmd_search() {
    resolve_python
    exec env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
        "$SCRIPT_DIR/search_logs.py" "$@"
}

cmd_analyse() {
    resolve_python
    exec env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
        "$SCRIPT_DIR/analyse_logs.py" "$@"
}

generate_run_slug() {
    # Deterministic-free, no-deps friendly slug — adjective-colour shape so
    # concurrent runs are easy to distinguish at a glance in the logs/ tree.
    local adjs=(grumpy happy lazy quick brave silent loud sleepy hungry tiny
                spicy mellow plucky witty bright stormy frosty sunny rusty merry
                gentle clumsy chatty curious eager fancy giddy nimble proud sturdy)
    local colours=(orange purple sky river panda cobra falcon meadow ember hazel
                   crimson teal indigo amber slate olive rose mint coral cobalt
                   ivory ochre azure plum lilac mango onyx pearl sage saffron)
    local seed=$(( ( $(date +%s) ^ $$ ) & 0x7FFFFFFF ))
    local a=$(( seed % ${#adjs[@]} ))
    local c=$(( (seed / ${#adjs[@]}) % ${#colours[@]} ))
    echo "${adjs[$a]}-${colours[$c]}"
}

# parse_duration "5m" -> 300; accepts plain seconds, Ns, Nm, Nh.
parse_duration() {
    local v="$1"
    if [[ "$v" =~ ^([0-9]+)$ ]]; then echo "${BASH_REMATCH[1]}"; return; fi
    if [[ "$v" =~ ^([0-9]+)s$ ]]; then echo "${BASH_REMATCH[1]}"; return; fi
    if [[ "$v" =~ ^([0-9]+)m$ ]]; then echo $(( ${BASH_REMATCH[1]} * 60 )); return; fi
    if [[ "$v" =~ ^([0-9]+)h$ ]]; then echo $(( ${BASH_REMATCH[1]} * 3600 )); return; fi
    echo "invalid duration: $v (use 30s, 5m, 1h, or plain seconds)" >&2
    exit 1
}

cmd_monitor() {
    APP="hover,hover-worker,hover-analysis,hover-autoscaler-worker,hover-autoscaler-analysis"
    INTERVAL=3
    SAMPLES=400
    ITERATIONS=1440  # ~72 minutes at 3s intervals
    RUN_ID=""
    OUTPUT_ROOT="logs"
    CLEANUP_OLD=true
    CLEANUP_DAYS=1
    CLEANUP_MODE="zip"
    ANALYSE_EVERY="5m"
    PYTHON_CMD=""
    PYTHON_ARGS=()

    monitor_usage() {
        cat <<'USAGE'
Usage: logs.sh monitor [options]

Fetch recent Fly logs on a fixed cadence, archive the raw output, and write
per-minute summaries describing how often each log level/message occurred.

Automatic cleanup (enabled by default):
  - Zips raw logs and iteration JSONs from runs older than 1 day
  - Keeps summary.md, summary.json, and monitor.log
  - Use --no-cleanup to disable or --cleanup-mode delete to remove everything

Options:
  --app NAMES           Fly application name(s), comma-separated
                        (default: hover,hover-worker,hover-analysis,
                        hover-autoscaler-worker,hover-autoscaler-analysis)
  --interval SECONDS    Seconds to wait between samples (default: 3)
  --samples N           Number of log lines to request each run (default: 400)
  --iterations N        Number of iterations to perform (0 = run forever,
                        default: 1440 = ~72 minutes at 3s intervals)
  --run-id ID           Identifier used when naming output directories
                        (default: auto-generated <adjective>-<colour> slug)
  --analyse-every DUR   Run analyse to write a snapshot every DUR (default: 5m).
                        Accepts plain seconds, Ns, Nm, or Nh. Use 0 to disable.
  --no-cleanup          Disable automatic cleanup (default: enabled)
  --cleanup-days N      Clean runs older than N days (default: 1)
  --cleanup-mode MODE   How to clean: 'zip' or 'delete' (default: zip)
                        zip: archives raw/ and iteration JSONs, keeps summaries
                        delete: removes entire run directory
  -h, --help            Show this message and exit

Environment variables with the same names (APP, INTERVAL, SAMPLES, ITERATIONS,
RUN_ID) override the defaults as well.
USAGE
    }

    # Allow environment variables to override defaults.
    APP=${APP:-$APP}
    INTERVAL=${INTERVAL:-$INTERVAL}
    SAMPLES=${SAMPLES:-$SAMPLES}
    ITERATIONS=${ITERATIONS:-$ITERATIONS}
    RUN_ID=${RUN_ID:-$RUN_ID}

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --app)            APP="$2"; shift 2 ;;
            --interval)       INTERVAL="$2"; shift 2 ;;
            --samples)        SAMPLES="$2"; shift 2 ;;
            --iterations)     ITERATIONS="$2"; shift 2 ;;
            --run-id)         RUN_ID="$2"; shift 2 ;;
            --analyse-every)  ANALYSE_EVERY="$2"; shift 2 ;;
            --no-cleanup)     CLEANUP_OLD=false; shift ;;
            --cleanup-days)   CLEANUP_DAYS="$2"; shift 2 ;;
            --cleanup-mode)   CLEANUP_MODE="$2"; shift 2 ;;
            -h|--help)        monitor_usage; exit 0 ;;
            *)
                echo "Unknown option: $1" >&2
                monitor_usage
                exit 1
                ;;
        esac
    done

    if ! [[ "$INTERVAL" =~ ^[0-9]+$ && "$INTERVAL" -gt 0 ]]; then
        echo "interval must be a positive integer" >&2
        exit 1
    fi
    if ! [[ "$SAMPLES" =~ ^[0-9]+$ && "$SAMPLES" -ge 1 && "$SAMPLES" -le 10000 ]]; then
        echo "samples must be an integer between 1 and 10000" >&2
        exit 1
    fi
    if ! [[ "$ITERATIONS" =~ ^[0-9]+$ ]]; then
        echo "iterations must be an integer >= 0" >&2
        exit 1
    fi
    if ! [[ "$CLEANUP_DAYS" =~ ^[0-9]+$ && "$CLEANUP_DAYS" -ge 0 ]]; then
        echo "cleanup-days must be a non-negative integer" >&2
        exit 1
    fi
    if [[ "$CLEANUP_MODE" != "zip" && "$CLEANUP_MODE" != "delete" ]]; then
        echo "cleanup-mode must be 'zip' or 'delete'" >&2
        exit 1
    fi

    IFS=',' read -r -a APPS <<< "$APP"
    for i in "${!APPS[@]}"; do
        APPS[i]="${APPS[i]// /}"
    done
    if [[ ${#APPS[@]} -eq 0 || -z "${APPS[0]}" ]]; then
        echo "at least one app name is required" >&2
        exit 1
    fi

    if command -v python3 >/dev/null 2>&1 && python3 -c "import sys" >/dev/null 2>&1; then
        PYTHON_CMD="python3"
    elif command -v python >/dev/null 2>&1 && python -c "import sys" >/dev/null 2>&1; then
        PYTHON_CMD="python"
    elif command -v py >/dev/null 2>&1 && py -3 -c "import sys" >/dev/null 2>&1; then
        PYTHON_CMD="py"
        PYTHON_ARGS=(-3)
    fi

    # Auto-generate settings suffix with appropriate units.
    if [[ "$INTERVAL" -ge 60 ]]; then
        INTERVAL_MINUTES=$(( INTERVAL / 60 ))
        INTERVAL_STR="${INTERVAL_MINUTES}m"
    else
        INTERVAL_STR="${INTERVAL}s"
    fi

    if [[ "$ITERATIONS" -eq 0 ]]; then
        SETTINGS_SUFFIX="${INTERVAL_STR}_forever"
    else
        DURATION_SECONDS=$(( ITERATIONS * INTERVAL ))
        if [[ "$DURATION_SECONDS" -ge 86400 ]]; then
            DURATION_DAYS=$(( (DURATION_SECONDS + 43200) / 86400 ))
            DURATION_STR="${DURATION_DAYS}d"
        elif [[ "$DURATION_SECONDS" -ge 3600 ]]; then
            DURATION_HOURS=$(( (DURATION_SECONDS + 1800) / 3600 ))
            DURATION_STR="${DURATION_HOURS}h"
        else
            DURATION_MINUTES=$(( (DURATION_SECONDS + 30) / 60 ))
            DURATION_STR="${DURATION_MINUTES}m"
        fi
        SETTINGS_SUFFIX="${INTERVAL_STR}_${DURATION_STR}"
    fi

    if [[ -z "$RUN_ID" ]]; then
        RUN_ID="$(generate_run_slug)_${SETTINGS_SUFFIX}"
    else
        RUN_ID="${RUN_ID}_${SETTINGS_SUFFIX}"
    fi

    ANALYSE_EVERY_SECONDS=$(parse_duration "$ANALYSE_EVERY")

    DATE_DIR="$OUTPUT_ROOT/$(date +"%Y%m%d")"
    TIME_PREFIX=$(date +"%H%M")
    RUN_DIR="$DATE_DIR/${TIME_PREFIX}_${RUN_ID}"
    LOG_FILE="$RUN_DIR/monitor.log"

    mkdir -p "$RUN_DIR"
    for app in "${APPS[@]}"; do
        mkdir -p "$RUN_DIR/$app/raw"
    done

    if [[ "$CLEANUP_OLD" == "true" ]]; then
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Cleaning up old runs (older than $CLEANUP_DAYS days, mode: $CLEANUP_MODE)" | tee -a "$LOG_FILE"
        if [[ "$(uname)" == "Darwin" ]]; then
            CUTOFF_DATE=$(date -u -v-${CLEANUP_DAYS}d +"%Y%m%d" 2>/dev/null || date -u +"%Y%m%d")
        else
            CUTOFF_DATE=$(date -u -d "$CLEANUP_DAYS days ago" +"%Y%m%d" 2>/dev/null || date -u +"%Y%m%d")
        fi
        if [[ -d "$OUTPUT_ROOT" ]]; then
            find "$OUTPUT_ROOT" -mindepth 2 -maxdepth 2 -type d | while read -r run_dir; do
                date_dir=$(basename "$(dirname "$run_dir")")
                if ! [[ "$date_dir" =~ ^[0-9]{8}$ ]]; then
                    continue
                fi
                if [[ "$date_dir" -ge "$CUTOFF_DATE" ]]; then
                    continue
                fi
                run_name=$(basename "$run_dir")
                if [[ "$CLEANUP_MODE" == "zip" ]]; then
                    while IFS= read -r raw_dir; do
                        [[ -z "$raw_dir" ]] && continue
                        zip_parent=$(dirname "$raw_dir")
                        [[ -f "$zip_parent/raw.zip" ]] && continue
                        rel=${raw_dir#"$run_dir/"}
                        echo "  Zipping raw logs: $date_dir/$run_name/$rel" | tee -a "$LOG_FILE"
                        (cd "$zip_parent" && zip -q -r "raw.zip" "raw" && rm -rf "raw") || {
                            echo "  Failed to zip raw directory $raw_dir" | tee -a "$LOG_FILE"
                        }
                    done < <(find "$run_dir" -type d -name raw 2>/dev/null)
                    iter_files=$(find "$run_dir" -type f -name '*_iter*.json' 2>/dev/null)
                    if [[ -n "$iter_files" ]]; then
                        echo "  Removing iteration JSONs: $date_dir/$run_name" | tee -a "$LOG_FILE"
                        printf '%s\n' "$iter_files" | xargs rm -f || {
                            echo "  Failed to remove iteration JSONs in $run_dir" | tee -a "$LOG_FILE"
                        }
                    fi
                else
                    echo "  Deleting: $date_dir/$run_name" | tee -a "$LOG_FILE"
                    rm -rf "$run_dir" || {
                        echo "  Failed to delete $run_dir" | tee -a "$LOG_FILE"
                    }
                fi
            done
        fi
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Cleanup complete" | tee -a "$LOG_FILE"
    fi

    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Starting log monitor" | tee -a "$LOG_FILE"
    echo "Apps: ${APPS[*]} | Interval: ${INTERVAL}s | Samples: $SAMPLES | Iterations: $ITERATIONS" | tee -a "$LOG_FILE"
    echo "Run directory: $RUN_DIR" | tee -a "$LOG_FILE"
    for app in "${APPS[@]}"; do
        echo "  [$app] raw: $RUN_DIR/$app/raw  summaries: $RUN_DIR/$app" | tee -a "$LOG_FILE"
    done
    if [[ -z "$PYTHON_CMD" ]]; then
        echo "Python not found; continuing with raw log capture only" | tee -a "$LOG_FILE"
    fi

    capture_app() {
        local app="$1" ts="$2" iter="$3"
        local raw_file="$RUN_DIR/$app/raw/${ts}_iter${iter}.log"
        local summary_file="$RUN_DIR/$app/${ts}_iter${iter}.json"
        local cursor_file="$RUN_DIR/$app/.cursor"

        # `flyctl logs --no-tail` returns the same recent window every call, so
        # filter against a per-app cursor to keep only lines newer than the
        # last iteration. Falls back to the unfiltered capture when Python is
        # unavailable.
        if [[ -n "$PYTHON_CMD" ]]; then
            if ! flyctl logs --app "$app" --no-tail 2>&1 \
                | tail -n "$SAMPLES" \
                | env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
                    "$SCRIPT_DIR/filter_since.py" "$cursor_file" \
                > "$raw_file"; then
                echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Failed to fetch logs from Fly; raw output stored in $raw_file" | tee -a "$LOG_FILE"
                return
            fi
        else
            if ! flyctl logs --app "$app" --no-tail 2>&1 | tail -n "$SAMPLES" > "$raw_file"; then
                echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Failed to fetch logs from Fly; raw output stored in $raw_file" | tee -a "$LOG_FILE"
                return
            fi
        fi

        # Empty filtered output means nothing new since last cursor — skip the
        # downstream summary step (process_logs.py would just produce an empty
        # iteration JSON).
        if [[ ! -s "$raw_file" ]]; then
            return
        fi

        if [[ -z "$PYTHON_CMD" ]]; then
            echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Captured raw logs only (Python unavailable)" | tee -a "$LOG_FILE"
            return
        fi

        if ! env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/process_logs.py" "$raw_file" "$summary_file" >> "$LOG_FILE" 2>&1; then
            echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] [$app] Failed to process logs (see output above)" | tee -a "$LOG_FILE"
            return
        fi

        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1
    }

    run_snapshot_analyse() {
        # Run analyse against the in-progress run; output goes to snapshots/.
        if [[ -z "$PYTHON_CMD" ]]; then
            return
        fi
        local snap_ts
        snap_ts=$(date -u +"%H%M%SZ")
        mkdir -p "$RUN_DIR/snapshots"
        local run_ref="$(basename "$DATE_DIR")/$(basename "$RUN_DIR")"
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Snapshot analyse → $RUN_DIR/snapshots/analysis_${snap_ts}.md" | tee -a "$LOG_FILE"
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
            "$SCRIPT_DIR/analyse_logs.py" \
            --root "$OUTPUT_ROOT" \
            --run "$run_ref" \
            --out "$RUN_DIR/snapshots/analysis_${snap_ts}" >> "$LOG_FILE" 2>&1 || {
            echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Snapshot analyse failed (see log)" | tee -a "$LOG_FILE"
        }
    }

    iteration=0
    last_analyse_epoch=$(date +%s)
    while true; do
        iteration=$((iteration + 1))
        ts=$(date -u +"%Y%m%dT%H%M%SZ")
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Iteration $iteration: capturing logs" | tee -a "$LOG_FILE"
        for app in "${APPS[@]}"; do
            capture_app "$app" "$ts" "$iteration"
        done

        if [[ "$ANALYSE_EVERY_SECONDS" -gt 0 ]]; then
            now_epoch=$(date +%s)
            if (( now_epoch - last_analyse_epoch >= ANALYSE_EVERY_SECONDS )); then
                run_snapshot_analyse
                last_analyse_epoch=$now_epoch
            fi
        fi

        if [[ "$ITERATIONS" -ne 0 && "$iteration" -ge "$ITERATIONS" ]]; then
            break
        fi
        sleep "$INTERVAL"
    done

    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Monitoring finished after $iteration iteration(s)" | tee -a "$LOG_FILE"

    if [[ -z "$PYTHON_CMD" ]]; then
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Skipping aggregation (Python unavailable)" | tee -a "$LOG_FILE"
    else
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Running final aggregation..." | tee -a "$LOG_FILE"
        for app in "${APPS[@]}"; do
            env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1
        done
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Aggregation complete" | tee -a "$LOG_FILE"

        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Running final analyse..." | tee -a "$LOG_FILE"
        run_ref="$(basename "$DATE_DIR")/$(basename "$RUN_DIR")"
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
            "$SCRIPT_DIR/analyse_logs.py" \
            --root "$OUTPUT_ROOT" \
            --run "$run_ref" >> "$LOG_FILE" 2>&1 || {
            echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Final analyse failed (see log)" | tee -a "$LOG_FILE"
        }
        echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Analyse complete: $RUN_DIR/analysis.md" | tee -a "$LOG_FILE"
    fi
}

# Default subcommand is `monitor`. Bare `logs.sh`, or any invocation whose
# first positional starts with a dash (i.e. a flag, not a subcommand), runs
# monitor with the supplied flags.
if [[ $# -eq 0 ]]; then
    cmd_monitor
    exit 0
fi

case "$1" in
    -h|--help|help)     usage_top; exit 0 ;;
    monitor)            shift; cmd_monitor "$@" ;;
    search)             shift; cmd_search "$@" ;;
    analyse|analyze)    shift; cmd_analyse "$@" ;;
    -*)                 cmd_monitor "$@" ;;
    *)
        echo "Unknown command: $1" >&2
        usage_top
        exit 1
        ;;
esac
