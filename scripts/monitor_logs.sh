#!/usr/bin/env bash

# Back-compat shim — the canonical entry point is `logs.sh`.
# All flags accepted by this script are forwarded to `logs.sh monitor`.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
exec "$SCRIPT_DIR/logs.sh" monitor "$@"
