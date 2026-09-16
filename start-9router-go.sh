#!/bin/bash
# Start 9Router Go Proxy Gateway
PORT=${PORT:-20129}
DB_PATH=${DB_PATH:-"$HOME/.9router/db/data.sqlite"}

cd "$(dirname "$0")"
echo "🚀 Starting 9Router Go Proxy at http://localhost:${PORT} (DB: ${DB_PATH}) ..."
exec ./9router-go --port "$PORT" --db-path "$DB_PATH"
