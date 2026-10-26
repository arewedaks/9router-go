#!/usr/bin/env bash
# Deploy the local build to a remote 9router-go host.
#
#   scripts/deploy-remote.sh user@host [version]
#   ARCH=arm64 scripts/deploy-remote.sh root@1.2.3.4
#   VERSION=1.9.45-test SSH_KEY=~/lightsail.pem REMOTE_BIN=/opt/9router/9router-go \
#     scripts/deploy-remote.sh ubuntu@1.2.3.4          # throwaway test build, no release
#
# Builds a linux/$ARCH binary from the current tree, ships it, backs up the old
# binary, swaps it in, restarts the service and proves the new build answers
# /health and serves the new dashboard UI.
# Env: VERSION (default: current tag), ARCH (amd64), PORT (20128),
#      SERVICE (9router-go), REMOTE_BIN (/usr/local/bin/9router-go),
#      SSH_KEY (default: ssh's own key choice).
set -euo pipefail

target=${1:?usage: $0 user@host [version]}
version=${2:-${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || echo dev)}}
version=${version#v}
arch=${ARCH:-amd64}
port=${PORT:-20128}
service=${SERVICE:-9router-go}
remote_bin=${REMOTE_BIN:-/usr/local/bin/9router-go}
key_opt=()
[ -n "${SSH_KEY:-}" ] && key_opt=(-i "$SSH_KEY")
bin=9router-go-linux-$arch

echo "== build $bin v$version (linux/$arch)"
GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
  go build -ldflags="-s -w -X '9router/proxy/internal/updater.CurrentVersion=$version'" \
  -o "$bin" ./cmd/9router-go/
local_sum=$(sha256sum "$bin" | cut -d' ' -f1)
echo "   sha256 $local_sum  $(stat -c%s "$bin") bytes"

echo "== ship to $target"
scp -q "${key_opt[@]}" "$bin" "$target:/tmp/9router-go.new"

echo "== swap + restart + verify"
ssh "${key_opt[@]}" "$target" \
  REMOTE_BIN="$remote_bin" SERVICE="$service" PORT="$port" \
  bash -s <<'EOF'
set -euo pipefail
stamp=$(date +%Y%m%d-%H%M%S)
if [ -f "$REMOTE_BIN" ]; then
  sudo cp -a "$REMOTE_BIN" "$REMOTE_BIN.bak-$stamp"
  echo "   backup $REMOTE_BIN.bak-$stamp"
fi
sudo install -m 0755 /tmp/9router-go.new "$REMOTE_BIN"
rm -f /tmp/9router-go.new

if sudo systemctl cat "$SERVICE" >/dev/null 2>&1; then
  sudo systemctl restart "$SERVICE"
  sleep 3
  echo "   unit $(systemctl is-active "$SERVICE") $(systemctl show "$SERVICE" -p MainPID --value) started $(systemctl show "$SERVICE" -p ExecMainStartTimestamp --value)"
else
  echo "   no $SERVICE unit — restarting by pid on :$PORT"
  pid=$(sudo ss -tlnp 2>/dev/null | grep ":$PORT" | grep -oE 'pid=[0-9]+' | cut -d= -f2 | head -1 || true)
  if [ -n "$pid" ]; then sudo kill "$pid"; sleep 2; fi
  sudo setsid nohup "$REMOTE_BIN" >"/tmp/$SERVICE.log" 2>&1 < /dev/null &
  sleep 4
fi

echo "   sha256 $(sudo sha256sum "$REMOTE_BIN" | cut -d' ' -f1)"
echo "   health $(curl -s -o /dev/null -w '%{http_code}' -m5 "http://127.0.0.1:$PORT/health")"
ui=$(curl -s -m10 "http://127.0.0.1:$PORT/login" || true)
echo "   ui-bytes ${#ui} accountStrategyControls $(printf '%s' "$ui" | grep -c accountStrategyControls || true)"
EOF

echo "== deployed v$version to $target (local sha256 $local_sum)"
