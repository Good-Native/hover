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
    # Defaults; environment variables of the same name take precedence.
    APP="${APP:-hover,hover-worker,hover-analysis,hover-autoscaler-worker,hover-autoscaler-analysis}"
    INTERVAL="${INTERVAL:-3}"
    SAMPLES="${SAMPLES:-400}"
    ITERATIONS="${ITERATIONS:-1440}"  # ~72 minutes at 3s intervals
    RUN_ID="${RUN_ID:-}"
    OUTPUT_ROOT="${OUTPUT_ROOT:-logs}"
    CLEANUP_OLD="${CLEANUP_OLD:-true}"
    CLEANUP_DAYS="${CLEANUP_DAYS:-1}"
    CLEANUP_MODE="${CLEANUP_MODE:-zip}"
    ANALYSE_EVERY="${ANALYSE_EVERY:-5m}"
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

    require_value() {
        # Bail on missing values for options that take an argument so a stray
        # `logs.sh monitor --app` fails with a readable message instead of
        # tripping `set -u` on the unbound `$2` expansion below.
        if [[ $# -lt 2 || "$2" == -* ]]; then
            echo "Missing value for $1" >&2
            monitor_usage
            exit 2
        fi
    }

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --app)            require_value "$@"; APP="$2"; shift 2 ;;
            --interval)       require_value "$@"; INTERVAL="$2"; shift 2 ;;
            --samples)        require_value "$@"; SAMPLES="$2"; shift 2 ;;
            --iterations)     require_value "$@"; ITERATIONS="$2"; shift 2 ;;
            --run-id)         require_value "$@"; RUN_ID="$2"; shift 2 ;;
            --analyse-every)  require_value "$@"; ANALYSE_EVERY="$2"; shift 2 ;;
            --no-cleanup)     CLEANUP_OLD=false; shift ;;
            --cleanup-days)   require_value "$@"; CLEANUP_DAYS="$2"; shift 2 ;;
            --cleanup-mode)   require_value "$@"; CLEANUP_MODE="$2"; shift 2 ;;
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

    # Output helpers — keep TTY tidy, monitor.log retains every event.
    iso_ts() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
    log_to_file() { echo "[$(iso_ts)] $*" >> "$LOG_FILE"; }
    log_user() {
        # Print a user-facing message and record it to the log too.
        echo "$*"
        echo "[$(iso_ts)] $*" >> "$LOG_FILE"
    }

    USE_TICKER=false
    if [[ -t 1 ]]; then USE_TICKER=true; fi

    # ANSI palette — empty when not on a TTY so non-TTY output stays plain and
    # `monitor.log` writes (which use the *_plain variables) never carry codes.
    if [[ "$USE_TICKER" == "true" ]]; then
        C_BOLD=$'\033[1m'
        C_DIM=$'\033[2m'
        C_CYAN=$'\033[36m'
        C_GREEN=$'\033[32m'
        C_YELLOW=$'\033[33m'
        C_RESET=$'\033[0m'
    else
        C_BOLD="" C_DIM="" C_CYAN="" C_GREEN="" C_YELLOW="" C_RESET=""
    fi

    emit_styled() {
        # Print a styled line to stdout, plain text to the log.
        local plain="$1" styled="$2"
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "%s\n" "$styled"
        else
            echo "$plain"
        fi
        echo "[$(iso_ts)] $plain" >> "$LOG_FILE"
    }

    fmt_duration() {
        local s=$1
        if (( s < 60 )); then printf "%ds" "$s"; return; fi
        if (( s < 3600 )); then printf "%dm%ds" $((s/60)) $((s%60)); return; fi
        printf "%dh%dm" $((s/3600)) $(( (s%3600)/60 ))
    }
    ticker() {
        local plain="$1" styled="$2"
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "\r\033[K%s" "$styled"
        fi
        echo "[$(iso_ts)] $plain" >> "$LOG_FILE"
    }
    ticker_done() {
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "\n"
        fi
    }

    STOP_REQUESTED=false
    on_interrupt() {
        STOP_REQUESTED=true
        ticker_done
        emit_styled \
            "Stop requested — finishing current iteration and writing final report..." \
            "${C_BOLD}${C_YELLOW}Stop requested${C_RESET} — finishing current iteration and writing final report..."
    }
    trap on_interrupt INT TERM

    # Cleanup is now silent on TTY (recorded in monitor.log only) — it ran on
    # almost every invocation and dominated the startup banner.
    if [[ "$CLEANUP_OLD" == "true" ]]; then
        log_to_file "Cleaning up old runs (older than $CLEANUP_DAYS days, mode: $CLEANUP_MODE)"
        if [[ "$(uname)" == "Darwin" ]]; then
            CUTOFF_DATE=$(date -u -v-${CLEANUP_DAYS}d +"%Y%m%d" 2>/dev/null || date -u +"%Y%m%d")
        else
            CUTOFF_DATE=$(date -u -d "$CLEANUP_DAYS days ago" +"%Y%m%d" 2>/dev/null || date -u +"%Y%m%d")
        fi
        if [[ -d "$OUTPUT_ROOT" ]]; then
            find "$OUTPUT_ROOT" -mindepth 2 -maxdepth 2 -type d | while read -r run_dir; do
                date_dir=$(basename "$(dirname "$run_dir")")
                if ! [[ "$date_dir" =~ ^[0-9]{8}$ ]]; then continue; fi
                if [[ "$date_dir" -ge "$CUTOFF_DATE" ]]; then continue; fi
                run_name=$(basename "$run_dir")
                if [[ "$CLEANUP_MODE" == "zip" ]]; then
                    while IFS= read -r raw_dir; do
                        [[ -z "$raw_dir" ]] && continue
                        zip_parent=$(dirname "$raw_dir")
                        [[ -f "$zip_parent/raw.zip" ]] && continue
                        rel=${raw_dir#"$run_dir/"}
                        log_to_file "  Zipping raw logs: $date_dir/$run_name/$rel"
                        (cd "$zip_parent" && zip -q -r "raw.zip" "raw" && rm -rf "raw") || \
                            log_to_file "  Failed to zip raw directory $raw_dir"
                    done < <(find "$run_dir" -type d -name raw 2>/dev/null)
                    iter_files=$(find "$run_dir" -type f -name '*_iter*.json' 2>/dev/null)
                    if [[ -n "$iter_files" ]]; then
                        log_to_file "  Removing iteration JSONs: $date_dir/$run_name"
                        printf '%s\n' "$iter_files" | xargs rm -f || \
                            log_to_file "  Failed to remove iteration JSONs in $run_dir"
                    fi
                else
                    log_to_file "  Deleting: $date_dir/$run_name"
                    rm -rf "$run_dir" || log_to_file "  Failed to delete $run_dir"
                fi
            done
        fi
        log_to_file "Cleanup complete"
    fi

    # Compact startup banner: run dir, app list, and one settings line.
    if [[ "$ITERATIONS" -gt 0 ]]; then
        DURATION_HINT=" (~$(fmt_duration $((ITERATIONS * INTERVAL))))"
    else
        DURATION_HINT=" (forever)"
    fi
    APPS_JOINED=$(IFS=', '; echo "${APPS[*]}")
    if [[ "$ANALYSE_EVERY_SECONDS" -gt 0 ]]; then
        SNAP_HINT="every $ANALYSE_EVERY"
    else
        SNAP_HINT="disabled"
    fi

    emit_styled "Run: $RUN_DIR" "${C_BOLD}${C_CYAN}Run:${C_RESET} $RUN_DIR"
    emit_styled "Apps: $APPS_JOINED" "${C_BOLD}${C_CYAN}Apps:${C_RESET} $APPS_JOINED"
    emit_styled \
        "Interval: ${INTERVAL}s | Iterations: ${ITERATIONS}${DURATION_HINT} | Snapshots: $SNAP_HINT" \
        "${C_BOLD}Interval:${C_RESET} ${INTERVAL}s ${C_DIM}|${C_RESET} ${C_BOLD}Iterations:${C_RESET} ${ITERATIONS}${DURATION_HINT} ${C_DIM}|${C_RESET} ${C_BOLD}Snapshots:${C_RESET} $SNAP_HINT"
    if [[ "$USE_TICKER" == "true" ]]; then
        printf "${C_DIM}Press Ctrl+C to stop early; the final report still writes.${C_RESET}\n"
    fi
    if [[ -z "$PYTHON_CMD" ]]; then
        log_user "Python not found; continuing with raw log capture only"
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
                log_to_file "[$app] Failed to fetch logs from Fly; raw output stored in $raw_file"
                return
            fi
        else
            if ! flyctl logs --app "$app" --no-tail 2>&1 | tail -n "$SAMPLES" > "$raw_file"; then
                log_to_file "[$app] Failed to fetch logs from Fly; raw output stored in $raw_file"
                return
            fi
        fi

        if [[ ! -s "$raw_file" ]]; then
            return
        fi
        if [[ -z "$PYTHON_CMD" ]]; then
            log_to_file "[$app] Captured raw logs only (Python unavailable)"
            return
        fi
        if ! env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/process_logs.py" "$raw_file" "$summary_file" >> "$LOG_FILE" 2>&1; then
            log_to_file "[$app] Failed to process logs (see output above)"
            return
        fi
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1
    }

    run_snapshot_analyse() {
        if [[ -z "$PYTHON_CMD" ]]; then return; fi
        local snap_ts
        snap_ts=$(date -u +"%H%M%SZ")
        mkdir -p "$RUN_DIR/snapshots"
        local run_ref="$(basename "$DATE_DIR")/$(basename "$RUN_DIR")"
        log_to_file "Snapshot analyse → $RUN_DIR/snapshots/analysis_${snap_ts}.md"
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
            "$SCRIPT_DIR/analyse_logs.py" \
            --root "$OUTPUT_ROOT" \
            --run "$run_ref" \
            --out "$RUN_DIR/snapshots/analysis_${snap_ts}" >> "$LOG_FILE" 2>&1 || \
            log_to_file "Snapshot analyse failed (see log)"
    }

    iteration=0
    start_epoch=$(date +%s)
    last_analyse_epoch=$start_epoch
    while true; do
        iteration=$((iteration + 1))
        ts=$(date -u +"%Y%m%dT%H%M%SZ")
        log_to_file "Iteration $iteration: capturing logs"
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

        # Build and emit the ticker line — single self-overwriting status row
        # on a TTY, plain log lines otherwise (CI/redirected output).
        elapsed=$(( $(date +%s) - start_epoch ))
        if [[ "$ITERATIONS" -gt 0 ]]; then
            iter_progress="iter ${iteration}/${ITERATIONS}"
        else
            iter_progress="iter ${iteration}"
        fi
        elapsed_fmt=$(fmt_duration $elapsed)
        if [[ "$ANALYSE_EVERY_SECONDS" -gt 0 ]]; then
            until_snap=$(( ANALYSE_EVERY_SECONDS - ($(date +%s) - last_analyse_epoch) ))
            (( until_snap < 0 )) && until_snap=0
            snap_fmt=$(fmt_duration $until_snap)
            snap_plain=" | next snapshot in $snap_fmt"
            snap_styled=" ${C_DIM}| next snapshot in ${snap_fmt}${C_RESET}"
        else
            snap_plain=""
            snap_styled=""
        fi
        ticker \
            "${iter_progress} | elapsed ${elapsed_fmt}${snap_plain}" \
            "${C_BOLD}${C_CYAN}${iter_progress}${C_RESET} ${C_DIM}|${C_RESET} elapsed ${elapsed_fmt}${snap_styled}"

        if [[ "$STOP_REQUESTED" == "true" ]]; then break; fi
        if [[ "$ITERATIONS" -ne 0 && "$iteration" -ge "$ITERATIONS" ]]; then break; fi
        sleep "$INTERVAL" || true
        if [[ "$STOP_REQUESTED" == "true" ]]; then break; fi
    done

    ticker_done
    trap - INT TERM

    if [[ -z "$PYTHON_CMD" ]]; then
        log_user "Skipping aggregation (Python unavailable)"
    else
        log_to_file "Running final aggregation..."
        for app in "${APPS[@]}"; do
            env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1
        done
        log_to_file "Aggregation complete"

        log_to_file "Running final analyse..."
        run_ref="$(basename "$DATE_DIR")/$(basename "$RUN_DIR")"
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
            "$SCRIPT_DIR/analyse_logs.py" \
            --root "$OUTPUT_ROOT" \
            --run "$run_ref" >> "$LOG_FILE" 2>&1 || \
            log_to_file "Final analyse failed (see log)"
        emit_styled \
            "Done after $iteration iteration(s) — report: $RUN_DIR/analysis.md" \
            "${C_BOLD}${C_GREEN}Done${C_RESET} after $iteration iteration(s) ${C_DIM}—${C_RESET} report: ${C_BOLD}$RUN_DIR/analysis.md${C_RESET}"
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
