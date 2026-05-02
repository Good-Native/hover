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

# Probe a candidate interpreter and confirm it's Python 3.10+. The Python
# helpers use `itertools.pairwise` (3.10+) so 3.9 would import-fail at runtime.
_is_python3() {
    "$@" -c "import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)" \
        >/dev/null 2>&1
}

# Locate a working Python 3.10+ interpreter shared by search/analyse helpers.
resolve_python() {
    if command -v python3 >/dev/null 2>&1 && _is_python3 python3; then
        PYTHON_CMD="python3"
        PYTHON_ARGS=()
    elif command -v python >/dev/null 2>&1 && _is_python3 python; then
        PYTHON_CMD="python"
        PYTHON_ARGS=()
    elif command -v py >/dev/null 2>&1 && _is_python3 py -3; then
        PYTHON_CMD="py"
        PYTHON_ARGS=(-3)
    else
        echo "Python 3.10+ is required for this command but was not found." >&2
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
    if [[ ${#APPS[@]} -eq 0 ]]; then
        echo "at least one app name is required" >&2
        exit 1
    fi
    # Reject every empty entry — `--app "hover,,worker"` or a trailing comma
    # would otherwise create `$RUN_DIR//raw` and call `flyctl logs --app ""`.
    for app_name in "${APPS[@]}"; do
        if [[ -z "$app_name" ]]; then
            echo "app list contains an empty value; check comma placement in --app/$APP" >&2
            exit 1
        fi
    done

    if command -v python3 >/dev/null 2>&1 && _is_python3 python3; then
        PYTHON_CMD="python3"
    elif command -v python >/dev/null 2>&1 && _is_python3 python; then
        PYTHON_CMD="python"
    elif command -v py >/dev/null 2>&1 && _is_python3 py -3; then
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

    # `--run-id` is interpolated into a filesystem path; reject anything that
    # could escape the advertised `logs/YYYYMMDD/HHMM_<slug>_<settings>/`
    # layout (path separators, traversal segments, leading dot/dash).
    if [[ -n "$RUN_ID" && ! "$RUN_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
        echo "run-id may only contain letters, numbers, dot, underscore, and hyphen, and may not start with '.' or '-'" >&2
        exit 1
    fi
    if [[ -z "$RUN_ID" ]]; then
        RUN_SLUG="$(generate_run_slug)"
    else
        RUN_SLUG="$RUN_ID"
    fi
    RUN_ID="${RUN_SLUG}_${SETTINGS_SUFFIX}"

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
        # Bright-black foreground (a.k.a. grey) rather than \033[2m dim — many
        # terminals (VS Code's included) render the dim attribute by altering
        # the background, which produces a visible black box behind the text.
        C_DIM=$'\033[90m'
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
        if (( s < 3600 )); then printf "%dm %ds" $((s/60)) $((s%60)); return; fi
        printf "%dh %dm" $((s/3600)) $(( (s%3600)/60 ))
    }
    ticker_done() {
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "\n"
        fi
    }

    # ── Animated ticker ──────────────────────────────────────────────────
    # The ticker line is redrawn at 5Hz by a background process while the
    # main loop captures logs. State is shared via a small file the main
    # loop writes once per iteration; the animator reads it on every redraw
    # so the spinner, elapsed time, and snapshot countdown all keep ticking
    # even while flyctl calls are in flight.

    TICKER_STATE_FILE="$RUN_DIR/.ticker_state"
    TICKER_ANIMATOR_PID=""

    write_ticker_state() {
        # Single line:
        # iter iter_max start_epoch last_analyse_epoch analyse_every captured_total
        printf '%d %d %d %d %d %d\n' \
            "$iteration" "$ITERATIONS" "$start_epoch" "$last_analyse_epoch" \
            "$ANALYSE_EVERY_SECONDS" "$CAPTURED_TOTAL" \
            > "$TICKER_STATE_FILE"
    }

    ticker_animator() {
        local idx=0
        local frames=(◐ ◓ ◑ ◒)
        local n=${#frames[@]}
        local iter_num iter_max s_epoch a_epoch a_every captured
        local now elapsed elapsed_fmt iter_styled iter_max_part snap_part until_snap snap_fmt spin
        while [[ -f "$TICKER_STATE_FILE" ]]; do
            if read -r iter_num iter_max s_epoch a_epoch a_every captured \
                < "$TICKER_STATE_FILE" 2>/dev/null \
                && [[ -n "${iter_num:-}" ]]; then
                now=$(date +%s)
                elapsed=$(( now - s_epoch ))
                elapsed_fmt=$(fmt_duration $elapsed)
                if [[ "$iter_max" -gt 0 ]]; then
                    iter_max_part=" / ${iter_max}"
                else
                    iter_max_part=""
                fi
                if [[ "$a_every" -gt 0 ]]; then
                    until_snap=$(( a_every - (now - a_epoch) ))
                    (( until_snap < 0 )) && until_snap=0
                    snap_fmt=$(fmt_duration $until_snap)
                    snap_part=" ${C_DIM}-${C_RESET} ${C_DIM}next snapshot ${snap_fmt}${C_RESET}"
                else
                    snap_part=""
                fi
                spin="${frames[$idx]}"
                printf "\r\033[K   ${C_BOLD}${C_CYAN}%s${C_RESET} ${C_BOLD}${C_CYAN}%s${C_RESET} ${C_DIM}-${C_RESET} ${C_BOLD}${C_CYAN}%s${C_RESET}%s ${C_DIM}-${C_RESET} ${C_BOLD}${C_CYAN}%s${C_RESET} logs%s" \
                    "$spin" "$elapsed_fmt" "$iter_num" "$iter_max_part" "$captured" "$snap_part"
            fi
            idx=$(( (idx + 1) % n ))
            sleep 0.2
        done
    }

    stop_ticker_animator() {
        if [[ -n "$TICKER_ANIMATOR_PID" ]]; then
            kill "$TICKER_ANIMATOR_PID" 2>/dev/null || true
            wait "$TICKER_ANIMATOR_PID" 2>/dev/null || true
            TICKER_ANIMATOR_PID=""
        fi
        rm -f "$TICKER_STATE_FILE"
    }

    STOP_REQUESTED=false
    on_interrupt() {
        STOP_REQUESTED=true
        stop_ticker_animator
        ticker_done
        emit_styled \
            "Stop requested — finishing up..." \
            "${C_BOLD}${C_YELLOW}Stop requested${C_RESET} — final iteration & report..."
    }
    trap on_interrupt INT TERM

    # Sleep but watch stdin for `q` so the user has a clean alternative to
    # Ctrl+C. Any other keystroke is ignored (we keep polling). Falls back to
    # a plain sleep when stdin isn't an interactive TTY (CI, redirected input).
    poll_quit_or_sleep() {
        local duration=$1
        if [[ "$USE_TICKER" != "true" || ! -t 0 ]]; then
            sleep "$duration" || true
            return
        fi
        local target=$(( $(date +%s) + duration ))
        local key=""
        while (( $(date +%s) < target )); do
            local rem=$(( target - $(date +%s) ))
            (( rem < 1 )) && rem=1
            if read -rsn1 -t "$rem" key 2>/dev/null; then
                case "$key" in
                    q|Q)
                        STOP_REQUESTED=true
                        stop_ticker_animator
                        ticker_done
                        emit_styled \
                            "Stop requested (q) — final iteration & report..." \
                            "${C_BOLD}${C_YELLOW}Stop requested${C_RESET} (q) — final iteration & report..."
                        return
                        ;;
                    *) ;;  # ignore stray keystrokes, keep polling
                esac
            fi
        done
    }

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
                    # Null-delimited so a `--run-id` containing spaces (or any
                    # other whitespace) doesn't split filenames mid-token.
                    if find "$run_dir" -type f -name '*_iter*.json' -print -quit 2>/dev/null | grep -q .; then
                        log_to_file "  Removing iteration JSONs: $date_dir/$run_name"
                        find "$run_dir" -type f -name '*_iter*.json' -print0 2>/dev/null | xargs -0 rm -f || \
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
    # Comma-space join. `${APPS[*]}` only honours the first IFS char, so build
    # the joined string explicitly to keep the space after each comma.
    printf -v APPS_JOINED '%s, ' "${APPS[@]}"
    APPS_JOINED="${APPS_JOINED%, }"
    if [[ "$ANALYSE_EVERY_SECONDS" -gt 0 ]]; then
        SNAP_HINT="every $ANALYSE_EVERY"
    else
        SNAP_HINT="disabled"
    fi

    # Rule + key/value helpers for the bracketed banner / ticker / done layout.
    # All rules share one width so they line up; separate BANNER_WIDTH governs
    # apps-list wrapping so a narrow rule doesn't force aggressive wraps.
    RULE_WIDTH=60
    BANNER_WIDTH=60
    rule_chars() { printf '─%.0s' $(seq 1 "$1"); }

    print_top_rule() {
        local label="$1"
        local pad=$(( RULE_WIDTH - 4 - ${#label} ))
        (( pad < 0 )) && pad=0
        local trail
        trail=$(rule_chars "$pad")
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "${C_DIM}── ${C_RESET}${C_BOLD}${C_CYAN}%s${C_RESET}${C_DIM} %s${C_RESET}\n" "$label" "$trail"
        else
            printf -- "── %s %s\n" "$label" "$trail"
        fi
        echo "[$(iso_ts)] ── $label ──" >> "$LOG_FILE"
    }
    print_rule() {
        local line
        line=$(rule_chars "$RULE_WIDTH")
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "${C_DIM}%s${C_RESET}\n" "$line"
        else
            printf "%s\n" "$line"
        fi
        echo "[$(iso_ts)] $line" >> "$LOG_FILE"
    }
    print_kv() {
        # Indented key/value line. Key in default weight; value dimmed (grey)
        # so the eye lands on the slug + ticker, not the static config block.
        # 3-space indent so the key column aligns with the slug label in the
        # top rule (which sits at column 3, after `── `).
        local key="$1" value="$2"
        if [[ "$USE_TICKER" == "true" ]]; then
            printf "   %-10s  ${C_DIM}%s${C_RESET}\n" "$key" "$value"
        else
            printf "   %-10s  %s\n" "$key" "$value"
        fi
        echo "[$(iso_ts)] $key: $value" >> "$LOG_FILE"
    }
    print_kv_wrapped() {
        # Like print_kv but wraps `value` at word boundaries onto continuation
        # lines indented to align under the value column.
        local key="$1" value="$2"
        local cont_indent="               "  # 3 + 10 + 2 = 15 spaces
        local wrap_width=$(( BANNER_WIDTH - 15 ))
        local first=true
        while IFS= read -r line; do
            if $first; then
                print_kv "$key" "$line"
                first=false
            else
                if [[ "$USE_TICKER" == "true" ]]; then
                    printf "%s${C_DIM}%s${C_RESET}\n" "$cont_indent" "$line"
                else
                    printf "%s%s\n" "$cont_indent" "$line"
                fi
                echo "[$(iso_ts)]   $line" >> "$LOG_FILE"
            fi
        done < <(printf "%s\n" "$value" | fold -s -w "$wrap_width")
    }

    print_top_rule "$RUN_SLUG"
    print_kv "Run" "$RUN_DIR"
    print_kv_wrapped "Apps" "$APPS_JOINED"
    print_kv "Interval" "${INTERVAL}s"
    print_kv "Samples" "${SAMPLES} lines/fetch"
    print_kv "Iterations" "${ITERATIONS}${DURATION_HINT}"
    print_kv "Snapshots" "$SNAP_HINT"
    if [[ "$USE_TICKER" == "true" && -t 0 ]]; then
        print_kv "Quit" "press q or Ctrl/CMD + C "
    fi
    print_rule
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
        if ! env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1; then
            log_to_file "[$app] Failed to aggregate logs (see output above)"
        fi
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
    CAPTURED_TOTAL=0

    # Kick off the background animator before the first capture so the
    # ticker line is alive from t=0.
    if [[ "$USE_TICKER" == "true" ]]; then
        write_ticker_state
        ticker_animator &
        TICKER_ANIMATOR_PID=$!
    fi

    while true; do
        iteration=$((iteration + 1))
        iter_start_epoch=$(date +%s)
        ts=$(date -u +"%Y%m%dT%H%M%SZ")
        log_to_file "Iteration $iteration: capturing logs"

        # Capture all apps in parallel — flyctl calls are independent, and
        # running them sequentially made each iteration ~5-10s instead of the
        # advertised INTERVAL. Track each PID explicitly so we wait only for
        # the capture children — bare `wait` would also block on the ticker
        # animator, which is an intentional infinite loop.
        local capture_pids=()
        for app in "${APPS[@]}"; do
            capture_app "$app" "$ts" "$iteration" &
            capture_pids+=($!)
        done
        for pid in "${capture_pids[@]}"; do
            wait "$pid" 2>/dev/null || true
        done

        # Tally lines persisted this iteration (cursor-filtered, so reflects
        # genuinely new log lines, not the flyctl --no-tail window).
        for app in "${APPS[@]}"; do
            raw="$RUN_DIR/$app/raw/${ts}_iter${iteration}.log"
            if [[ -f "$raw" ]]; then
                n=$(wc -l < "$raw" 2>/dev/null | tr -d ' ')
                CAPTURED_TOTAL=$(( CAPTURED_TOTAL + ${n:-0} ))
            fi
        done

        if [[ "$ANALYSE_EVERY_SECONDS" -gt 0 ]]; then
            now_epoch=$(date +%s)
            if (( now_epoch - last_analyse_epoch >= ANALYSE_EVERY_SECONDS )); then
                run_snapshot_analyse
                last_analyse_epoch=$now_epoch
            fi
        fi

        # Hand the latest counters to the animator; it picks them up on its
        # next 200ms redraw.
        write_ticker_state
        log_to_file "iter ${iteration}/${ITERATIONS} · elapsed $(fmt_duration $(( $(date +%s) - start_epoch )))"

        if [[ "$STOP_REQUESTED" == "true" ]]; then break; fi
        if [[ "$ITERATIONS" -ne 0 && "$iteration" -ge "$ITERATIONS" ]]; then break; fi

        # `--interval` is the wall-clock period between iteration starts; if
        # capture+analyse took longer than INTERVAL we run back-to-back (and
        # log a warning) instead of compounding the lag.
        iter_elapsed=$(( $(date +%s) - iter_start_epoch ))
        remaining=$(( INTERVAL - iter_elapsed ))
        if (( remaining > 0 )); then
            poll_quit_or_sleep "$remaining"
        else
            log_to_file "Iteration $iteration took ${iter_elapsed}s (>= interval ${INTERVAL}s); no sleep"
        fi
        if [[ "$STOP_REQUESTED" == "true" ]]; then break; fi
    done

    stop_ticker_animator
    ticker_done
    trap - INT TERM

    if [[ -z "$PYTHON_CMD" ]]; then
        log_user "Skipping aggregation (Python unavailable)"
    else
        log_to_file "Running final aggregation..."
        for app in "${APPS[@]}"; do
            if ! env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" "$SCRIPT_DIR/aggregate_logs.py" "$RUN_DIR/$app" >> "$LOG_FILE" 2>&1; then
                log_to_file "[$app] Final aggregation failed (see output above)"
            fi
        done
        log_to_file "Aggregation complete"

        log_to_file "Running final analyse..."
        run_ref="$(basename "$DATE_DIR")/$(basename "$RUN_DIR")"
        env PYTHONUTF8=1 "$PYTHON_CMD" "${PYTHON_ARGS[@]+"${PYTHON_ARGS[@]}"}" \
            "$SCRIPT_DIR/analyse_logs.py" \
            --root "$OUTPUT_ROOT" \
            --run "$run_ref" >> "$LOG_FILE" 2>&1 || \
            log_to_file "Final analyse failed (see log)"
        print_rule
        emit_styled \
            "   ✓ Done after $iteration iteration(s)" \
            "   ${C_BOLD}${C_GREEN}✓ Done${C_RESET} after ${C_BOLD}${C_CYAN}$iteration${C_RESET} iteration(s)"
        emit_styled \
            "     Report: $RUN_DIR/analysis.md" \
            "     ${C_BOLD}Report:${C_RESET} $RUN_DIR/analysis.md"
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
