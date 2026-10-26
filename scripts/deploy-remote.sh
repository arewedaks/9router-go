#!/usr/bin/env bash
# Deploy the local build to a remote 9router-go host.
#
#   scripts/deploy-remote.sh user@host [version]
#   ARCH=arm64 scripts/deploy-remote.sh root@1.2.3.4
#
# Builds a linux/$ARCH binary from the current tree, ships it, swaps it in,
# restarts the service and proves the new build answers /api/version.
# Env: VERSION (default: version.json), ARCH (default amd64), PORT (default 20128),
#      SERVICE (default 9router-go), REMOTE_BIN (default /usr/local/bin/9router-go).
set -euo pipefail

target=${1:?usage: $0 user@host [version]}
version=${2:-${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || echo dev)}}
version=${version#v}
arch=${ARCH:-amd64}
port=${PORT:-20128}
service=${SERVICE:-9router-go}
remote_bin=${REMOTE_BIN:-/usr/local/bin/9router-go}
bin=9router-go-linux-$arch

echo "== build $bin v$version (linux/$arch)"
GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
  go build -ldflags="-s -w -X '9router/proxy/internal/updater.CurrentVersion=$version'" \
  -o "$bin" ./cmd/9router-go/
local_sum=$(sha256sum "$bin" | cut -d' ' -f1)
echo "   sha256 $local_sum  $(stat -c%s "$bin") bytes"

echo "== ship to $target"
scp -q "$bin" "$target:/tmp/9router-go.new"

echo "== swap + restart + verify"
ssh -t "$target" bash -s <<EOF
set -euo pipefail
sudo install -m 0755 /tmp/9router-go.new $remote_bin
rm -f /tmp/9router-go.new
if systemctl list-unit-files 2>/dev/null | grep -q '^$service\.service'; then
  sudo systemctl restart $service
  sleep 3
  systemctl is-active $service
else
  echo "no $service.service — restarting by pid on :$port"
  pid=\$(ss -tlnp 2>/dev/null | grep ":$port" | grep -oE 'pid=[0-9]+' | cut -d= -f2 | head -1 || true)
  if [ -n "\$pid" ]; then sudo kill "\$pid"; sleep 2; fi
  sudo setsid nohup $remote_bin >/var/log/$service.log 2>&1 < /dev/null &
  sleep 4
fi
echo "sha256 \$(sha256sum $remote_bin | cut -d' ' -f1)"
echo "health \$(curl -s -o /dev/null -w '%{http_code}' -m5 http://127.0.0.1:$port/health)"
echo "version \$(curl -s -m5 http://127.0.0.1:$port/api/version)"
EOF

echo "== deployed v$version to $target (local sha256 $local_sum)"
