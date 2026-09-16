#!/bin/bash
# Start 9Router Next.js Web Dashboard
STANDALONE_DIR="/data/data/com.termux/files/home/.npm/_npx/8a91ba84f7d0cb57/node_modules/9router/app"
PORT=${PORT:-20128}
HOSTNAME=${HOSTNAME:-0.0.0.0}

if [ ! -d "$STANDALONE_DIR" ]; then
  echo "Downloading 9Router dashboard package..."
  npx -y 9router --version >/dev/null 2>&1
fi

echo "🚀 Starting 9Router Dashboard at http://localhost:${PORT}/login ..."
cd "$STANDALONE_DIR" && PORT=$PORT HOSTNAME=$HOSTNAME exec node server.js
