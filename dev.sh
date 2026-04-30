#!/bin/bash
set -e

# Smart parameter detection
if [ "$1" = "debug" ] || [ "$2" = "debug" ]; then
    DEBUG_MODE="debug"
else
    DEBUG_MODE=""
fi

if [ "$1" = "pc" ] || [ "$2" = "pc" ]; then
    PLATFORM="pc"
elif [ "$1" = "mac" ] || [ "$2" = "mac" ]; then
    PLATFORM="mac"
elif [ "$1" = "debug" ]; then
    PLATFORM="mac"  # Default to mac if only debug specified
else
    PLATFORM=${1:-mac}  # Use first param or default to mac
fi

if [ "$DEBUG_MODE" = "debug" ]; then
    echo "Starting Hover development environment (platform: $PLATFORM, debug mode)..."
    export LOG_LEVEL=debug
else
    echo "Starting Hover development environment (platform: $PLATFORM)..."
    export LOG_LEVEL=info
fi

# Check if Docker is running
if ! docker ps >/dev/null 2>&1; then
    echo "Error: Docker is not running. Please start Docker first."
    echo "Download: https://docs.docker.com/desktop/"
    exit 1
fi

# Check if Supabase CLI is installed
if ! command -v supabase >/dev/null 2>&1; then
    echo "Error: Supabase CLI is not installed."
    if [[ "$OSTYPE" == "darwin"* ]]; then
        echo "Install with: brew install supabase/tap/supabase"
    else
        echo "Install with: npm install -g supabase"
    fi
    echo "Or download from: https://supabase.com/docs/guides/cli"
    exit 1
fi

# Configure Air for platform
if [ "$PLATFORM" = "pc" ]; then
    echo "Configuring Air for Windows..."
    sed -i.bak \
        -e 's/^cmd = "go build -o \.\/tmp\/main "/# cmd = "go build -o .\/tmp\/main "/' \
        -e 's/^bin = "tmp\/main"/# bin = "tmp\/main"/' \
        -e 's/^# cmd = "go build -o \.\/tmp\/main\.exe/cmd = "go build -o .\/tmp\/main.exe/' \
        -e 's/^# bin = "tmp\/main\.exe"/bin = "tmp\/main.exe"/' \
        .air.toml
else
    echo "Configuring Air for Mac/Linux..."
    sed -i.bak \
        -e 's/^cmd = "go build -o \.\/tmp\/main\.exe/# cmd = "go build -o .\/tmp\/main.exe/' \
        -e 's/^bin = "tmp\/main\.exe"/# bin = "tmp\/main.exe"/' \
        -e 's/^# cmd = "go build -o \.\/tmp\/main "/cmd = "go build -o .\/tmp\/main "/' \
        -e 's/^# bin = "tmp\/main"/bin = "tmp\/main"/' \
        .air.toml
fi

# Start local Redis for the broker (used by worker service)
echo "Starting local Redis..."
if docker ps --format '{{.Names}}' | grep -q '^hover-redis$'; then
    echo "ℹ️  Redis container already running"
else
    docker run -d --name hover-redis -p 6379:6379 redis:7-alpine >/dev/null 2>&1 \
        || docker start hover-redis >/dev/null 2>&1
    echo "✅ Redis started on localhost:6379"
fi

# Start Supabase (will be no-op if already running)
echo "Starting local Supabase..."
supabase start

# Generate .env.local from supabase status if it doesn't exist
if [ ! -f ".env.local" ]; then
    echo "Generating .env.local from supabase status..."
    SUPA_ENV=$(supabase status --output env 2>/dev/null)
    API_URL=$(echo "$SUPA_ENV" | grep '^API_URL=' | cut -d'"' -f2)
    DB_URL=$(echo "$SUPA_ENV" | grep '^DB_URL=' | cut -d'"' -f2)
    PUBLISHABLE_KEY=$(echo "$SUPA_ENV" | grep '^PUBLISHABLE_KEY=' | cut -d'"' -f2)
    SERVICE_ROLE_KEY=$(echo "$SUPA_ENV" | grep '^SERVICE_ROLE_KEY=' | cut -d'"' -f2)
    if [ -z "$API_URL" ] || [ -z "$DB_URL" ] || [ -z "$PUBLISHABLE_KEY" ] || [ -z "$SERVICE_ROLE_KEY" ]; then
        echo "⚠️  Could not extract required values from supabase status"
        echo "    Ensure Supabase is running, or create .env.local manually"
        exit 1
    fi
    (umask 077; cat > .env.local <<EOF
# Local development overrides — not committed to git
# Generated from: supabase status

APP_ENV=development
LOG_LEVEL=info

DATABASE_URL=${DB_URL}

SUPABASE_URL=${API_URL}
SUPABASE_AUTH_URL=${API_URL}
SUPABASE_PUBLISHABLE_KEY=${PUBLISHABLE_KEY}
SUPABASE_SERVICE_ROLE_KEY=${SERVICE_ROLE_KEY}

# Redis broker (local Docker container started by dev.sh)
REDIS_URL=redis://localhost:6379
REDIS_TLS_ENABLED=false

# Pressure controller — match production thresholds so local dev
# sheds load at the same latency marks as prod (defaults are 500/100).
GNH_PRESSURE_HIGH_MARK_MS=80
GNH_PRESSURE_LOW_MARK_MS=40
EOF
)
    echo "✅ .env.local created"
else
    echo "ℹ️  .env.local already exists — checking for missing vars"
    # Append each var independently so partial .env.local files get patched.
    for VAR_LINE in \
        "REDIS_URL=redis://localhost:6379" \
        "REDIS_TLS_ENABLED=false" \
        "GNH_PRESSURE_HIGH_MARK_MS=80" \
        "GNH_PRESSURE_LOW_MARK_MS=40"; do
        VAR_NAME="${VAR_LINE%%=*}"
        if ! grep -q "^${VAR_NAME}=" .env.local 2>/dev/null; then
            (umask 077; echo "$VAR_LINE" >> .env.local)
            echo "  ✅ Appended ${VAR_NAME} to .env.local"
        fi
    done
fi

inject_op_secrets() {
    if ! command -v op >/dev/null 2>&1; then
        echo "⚠️  1Password CLI (op) not found — external secrets not loaded."
        echo "   Install: brew install 1password-cli"
        echo "   Missing: SLACK_CLIENT_SECRET, WEBFLOW_CLIENT_SECRET,"
        echo "            GOOGLE_CLIENT_SECRET, LOOPS_API_KEY,"
        echo "            STRIPE_SECRET_KEY, STRIPE_PUBLISHABLE_KEY"
        echo "   For Stripe webhooks locally, also run:"
        echo "     stripe listen --forward-to localhost:8080/v1/webhooks/stripe"
        return
    fi

    if ! op whoami >/dev/null 2>&1; then
        echo "⚠️  Not signed in to 1Password CLI — external secrets not loaded."
        echo "   Sign in with: op signin"
        return
    fi

    # Strip previously injected keys before re-injecting (avoids duplicates on re-run).
    for key in SLACK_CLIENT_SECRET WEBFLOW_CLIENT_SECRET GOOGLE_CLIENT_SECRET \
               LOOPS_API_KEY STRIPE_SECRET_KEY STRIPE_PUBLISHABLE_KEY; do
        sed -i.bak "/^${key}=/d" .env.local
    done
    rm -f .env.local.bak

    echo "Loading external secrets from 1Password..."
    if op inject -i .env.op >> .env.local 2>/dev/null; then
        echo "✅ External secrets loaded from 1Password"
        echo "   For Stripe webhooks locally, also run:"
        echo "     stripe listen --forward-to localhost:8080/v1/webhooks/stripe"
        echo "   Then add STRIPE_WEBHOOK_SECRET=<whsec_...> to .env.local"
    else
        echo "⚠️  Failed to load secrets from 1Password — check op vault access"
    fi
}

inject_op_secrets

# Start Air with hot reloading and migration watching
echo "Starting development server with hot reloading..."
echo "Watching for migration changes - will auto-reset database when needed..."

# Start Air in background
air &
AIR_PID=$!

# Watch for migration changes and auto-reset database
watch_migrations() {
    if command -v fswatch >/dev/null 2>&1; then
        # Use fswatch if available (install with: brew install fswatch)
        fswatch -o supabase/migrations/ | while read f; do
            echo "Migration change detected - resetting database..."
            supabase db reset
        done
    else
        # Fallback to polling
        echo "Note: Install 'fswatch' for better migration watching (brew install fswatch)"
        last_mod=$(stat -c %Y supabase/migrations/*.sql 2>/dev/null | sort -n | tail -1)
        while true; do
            sleep 2
            new_mod=$(stat -c %Y supabase/migrations/*.sql 2>/dev/null | sort -n | tail -1)
            if [ "$new_mod" != "$last_mod" ]; then
                echo "Migration change detected - resetting database..."
                supabase db reset
                last_mod=$new_mod
            fi
        done
    fi
}

# Start migration watcher in background
watch_migrations &
WATCH_PID=$!

# Wait for Air to finish
wait $AIR_PID

# Clean up background processes
kill $WATCH_PID 2>/dev/null
