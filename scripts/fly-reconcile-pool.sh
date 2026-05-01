#!/bin/bash
# Reconcile a Fly app's machine pool to (1 running + N-1 stopped) on the
# just-released image. Replaces the historical `flyctl scale count` line in
# CI release jobs (PR #369): keeps the stale-image reap behaviour while
# allowing pre-provisioned stopped machines for fly-autoscaler to start.
#
# Idempotent — safe to run on every deploy. If the pool is already correct,
# the script is a no-op.
#
# Usage:
#   fly-reconcile-pool.sh APP IMAGE_LABEL POOL_SIZE
#
# Example:
#   fly-reconcile-pool.sh hover-worker deployment-12345-1 5
#
# Behaviour:
#   1. Destroys any machine whose image label != IMAGE_LABEL (running or
#      stopped). This reaps the stragglers PR #369 was guarding against.
#   2. Tops up the pool to POOL_SIZE by cloning the live machine and
#      stopping the clones. New clones inherit the just-released image,
#      so they're already on the latest version.
#
# Requires: flyctl, jq.

set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 APP IMAGE_LABEL POOL_SIZE" >&2
  exit 2
fi

APP="$1"
IMAGE_LABEL="$2"
POOL_SIZE="$3"

if ! [[ "$POOL_SIZE" =~ ^[0-9]+$ ]] || [ "$POOL_SIZE" -lt 1 ]; then
  echo "POOL_SIZE must be a positive integer, got: $POOL_SIZE" >&2
  exit 2
fi

echo "🔍 Reconciling $APP to pool size $POOL_SIZE on image label $IMAGE_LABEL"

# Phase 0 — wait for all machines to reach a stable state before checking
# image labels. `flyctl deploy` with --immediate strategy returns as soon
# as the config update is accepted; the machine may still be in `replacing`
# state with its old image label still reported. Without this wait, a
# stable in-place update gets misclassified as stale and the live machine
# is destroyed.
TRANSIENT_STATES_FILTER='select(.state == "replacing" or .state == "starting" or .state == "stopping" or .state == "created")'
for attempt in $(seq 1 24); do
  MACHINES_JSON=$(flyctl machines list -a "$APP" --json)
  TRANSIENT_COUNT=$(echo "$MACHINES_JSON" | jq -r "[.[] | $TRANSIENT_STATES_FILTER] | length")
  if [ "$TRANSIENT_COUNT" = "0" ]; then
    break
  fi
  echo "⏳ $TRANSIENT_COUNT machine(s) still in transitional state (attempt $attempt/24) — waiting 5s..."
  sleep 5
done
if [ "$TRANSIENT_COUNT" != "0" ]; then
  echo "❌ $TRANSIENT_COUNT machine(s) still transitioning after 120s — aborting before we destroy a live update." >&2
  flyctl machines list -a "$APP" >&2 || true
  exit 1
fi

# Phase 1 — destroy stale-image machines. Match on image label suffix
# rather than full image name so registry-host changes don't trip false
# positives. fly-autoscaler-managed clones inherit the parent's image, so
# they're identified by the same label.
#
# Safety guard: if zero machines currently report the target IMAGE_LABEL,
# something is off (caller passed wrong label, Fly API hasn't yet caught
# up). Aborting beats wiping the entire pool.
TARGET_MATCH_COUNT=$(echo "$MACHINES_JSON" \
  | jq -r --arg label "$IMAGE_LABEL" '[.[] | select((.config.image // "") | endswith(":" + $label))] | length')
if [ "$TARGET_MATCH_COUNT" -eq 0 ]; then
  echo "❌ No machine on $APP currently reports image label $IMAGE_LABEL — aborting before we destroy the running pool." >&2
  flyctl machines list -a "$APP" >&2 || true
  exit 1
fi

STALE_IDS=$(echo "$MACHINES_JSON" \
  | jq -r --arg label "$IMAGE_LABEL" '.[] | select((.config.image // "") | endswith(":" + $label) | not) | .id')

if [ -n "$STALE_IDS" ]; then
  echo "🗑  Destroying stale-image machines:"
  echo "$STALE_IDS"
  for id in $STALE_IDS; do
    flyctl machine destroy "$id" -a "$APP" --force
  done
  # Refresh after destroys.
  MACHINES_JSON=$(flyctl machines list -a "$APP" --json)
else
  echo "✅ No stale-image machines to reap."
fi

# Phase 2 — top up the pool. Pick any running machine as the clone source;
# fly machine clone copies the source's image + config, so as long as the
# source is on IMAGE_LABEL (post-Phase 1 it must be), the clone is too.
#
# On a brand-new app's first deploy, `flyctl deploy --ha=false` returns as
# soon as the machine is created — it doesn't wait for the started state
# when the app has no [http_service] health check. Poll for up to 60s so
# we don't race the launch.
RUNNING_ID=""
SOURCE_REGION=""
for attempt in $(seq 1 12); do
  MACHINES_JSON=$(flyctl machines list -a "$APP" --json)
  RUNNING_ID=$(echo "$MACHINES_JSON" | jq -r 'first(.[] | select(.state == "started") | .id) // empty')
  if [ -n "$RUNNING_ID" ]; then
    SOURCE_REGION=$(echo "$MACHINES_JSON" | jq -r --arg id "$RUNNING_ID" 'first(.[] | select(.id == $id) | .region) // empty')
    break
  fi
  echo "⏳ No started machine yet (attempt $attempt/12) — waiting 5s..."
  sleep 5
done

if [ -z "$RUNNING_ID" ]; then
  echo "❌ No started machine found on $APP after 60s — cannot clone pool. Did the deploy succeed?" >&2
  flyctl machines list -a "$APP" >&2 || true
  exit 1
fi
if [ -z "$SOURCE_REGION" ]; then
  echo "❌ Could not determine region of source machine $RUNNING_ID." >&2
  exit 1
fi

CURRENT_COUNT=$(echo "$MACHINES_JSON" | jq -r 'length')

if [ "$CURRENT_COUNT" -ge "$POOL_SIZE" ]; then
  echo "✅ Pool already at $CURRENT_COUNT machines (target: $POOL_SIZE) — no top-up needed."
  exit 0
fi

NEEDED=$((POOL_SIZE - CURRENT_COUNT))
echo "➕ Cloning $NEEDED stopped machine(s) from $RUNNING_ID to reach pool size $POOL_SIZE."

# flyctl machine clone has no --json flag, so identify the new machine
# by diffing the machine list before and after each clone. Retry on
# transient Fly platform errors (e.g. "machine is on an unreachable host")
# which can hit any single clone in a long sequence.
for i in $(seq 1 "$NEEDED"); do
  CLONE_ID=""
  for clone_attempt in 1 2 3; do
    echo "  Clone $i/$NEEDED (attempt $clone_attempt/3)..."
    BEFORE_IDS=$(flyctl machines list -a "$APP" --json | jq -r '.[].id' | sort)
    if flyctl machine clone "$RUNNING_ID" -a "$APP" --region "$SOURCE_REGION"; then
      # Clone command accepted. Poll the machine list for visibility —
      # never re-run the clone command on this path, since that would
      # create a duplicate if the API is just slow to surface the new ID.
      for visibility_attempt in 1 2 3 4 5; do
        AFTER_IDS=$(flyctl machines list -a "$APP" --json | jq -r '.[].id' | sort)
        CLONE_ID=$(comm -13 <(echo "$BEFORE_IDS") <(echo "$AFTER_IDS") | head -n1)
        if [ -n "$CLONE_ID" ]; then
          break 2
        fi
        echo "  ⏳ Clone accepted but the new machine is not visible yet (check $visibility_attempt/5) — waiting 3s..." >&2
        sleep 3
      done
      echo "  ❌ Clone command succeeded but the new machine never appeared — aborting to avoid duplicate cloning." >&2
      exit 1
    else
      echo "  ⚠️  Clone command failed (likely transient Fly error) — retrying after 10s..." >&2
      sleep 10
    fi
  done
  if [ -z "$CLONE_ID" ]; then
    echo "  ❌ Clone failed after 3 attempts." >&2
    exit 1
  fi
  flyctl machine stop "$CLONE_ID" -a "$APP"
  echo "  ✅ Cloned and stopped: $CLONE_ID"
done

echo "✅ $APP pool reconciled to $POOL_SIZE machines."
