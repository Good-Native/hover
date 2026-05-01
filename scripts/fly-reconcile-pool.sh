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

MACHINES_JSON=$(flyctl machines list -a "$APP" --json)

# Phase 1 — destroy stale-image machines. Match on image label suffix
# rather than full image name so registry-host changes don't trip false
# positives. fly-autoscaler-managed clones inherit the parent's image, so
# they're identified by the same label.
STALE_IDS=$(echo "$MACHINES_JSON" \
  | jq -r --arg label "$IMAGE_LABEL" '.[] | select(.config.image | endswith(":" + $label) | not) | .id')

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
CURRENT_COUNT=$(echo "$MACHINES_JSON" | jq -r 'length')
RUNNING_ID=$(echo "$MACHINES_JSON" | jq -r 'first(.[] | select(.state == "started") | .id) // empty')

if [ -z "$RUNNING_ID" ]; then
  echo "❌ No started machine found on $APP — cannot clone pool. Did the deploy succeed?" >&2
  exit 1
fi

if [ "$CURRENT_COUNT" -ge "$POOL_SIZE" ]; then
  echo "✅ Pool already at $CURRENT_COUNT machines (target: $POOL_SIZE) — no top-up needed."
  exit 0
fi

NEEDED=$((POOL_SIZE - CURRENT_COUNT))
echo "➕ Cloning $NEEDED stopped machine(s) from $RUNNING_ID to reach pool size $POOL_SIZE."

for i in $(seq 1 "$NEEDED"); do
  echo "  Clone $i/$NEEDED..."
  CLONE_JSON=$(flyctl machine clone "$RUNNING_ID" -a "$APP" --region syd --json)
  CLONE_ID=$(echo "$CLONE_JSON" | jq -r '.id')
  if [ -z "$CLONE_ID" ] || [ "$CLONE_ID" = "null" ]; then
    echo "  ❌ Clone failed: $CLONE_JSON" >&2
    exit 1
  fi
  flyctl machine stop "$CLONE_ID" -a "$APP"
  echo "  ✅ Cloned and stopped: $CLONE_ID"
done

echo "✅ $APP pool reconciled to $POOL_SIZE machines."
