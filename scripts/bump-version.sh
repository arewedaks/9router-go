#!/usr/bin/env bash
set -euo pipefail

# Central version bump — source of truth: version.json
# Usage: ./scripts/bump-version.sh 1.8.9
# Updates: version.json, internal/updater/updater.go (fallback), VERSION (legacy)

if [ $# -ne 1 ]; then
  echo "Usage: $0 <new-version>  e.g. $0 1.8.9"
  exit 1
fi

NEW_VER="$1"
if ! [[ "$NEW_VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]]; then
  echo "Invalid version format: $NEW_VER (expected e.g. 1.8.9)"
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

echo "Bumping version to $NEW_VER ..."

# 1. version.json — what the Makefile, the Dockerfile and the dashboard's
#    update check all read. This is the authority.
if command -v python3 >/dev/null 2>&1; then
  python3 -c "
import json, pathlib
p = pathlib.Path('version.json')
data = json.loads(p.read_text())
data['latestVersion'] = '$NEW_VER'
# keep releaseNotes generic, user can edit
p.write_text(json.dumps(data, indent=2) + '\n')
"
else
  # fallback sed
  sed -i.bak "s/\"latestVersion\": *\"[^\"]*\"/\"latestVersion\": \"$NEW_VER\"/" version.json
  rm -f version.json.bak
fi
echo "  → version.json"

# 2. VERSION — legacy. The build no longer reads it, but external scripts and
#    muscle memory may, so it is kept in step rather than left to rot again.
echo -n "$NEW_VER" > VERSION
echo "  → VERSION (legacy)"

# 3. internal/updater/updater.go fallback
# Update the default var CurrentVersion = "x.y.z"
if grep -q 'var CurrentVersion = "' internal/updater/updater.go; then
  # Use a temp file for BSD sed compatibility
  sed -i.bak "s/var CurrentVersion = \".*\"/var CurrentVersion = \"$NEW_VER\"/" internal/updater/updater.go
  rm -f internal/updater/updater.go.bak
  echo "  → internal/updater/updater.go"
fi

# 4. Dockerfile fallback (optional, now reads VERSION, but keep comment in sync)
if grep -q 'ARG VERSION' Dockerfile; then
  echo "  → Dockerfile (reads VERSION at build, no hardcoded fallback needed)"
fi

echo ""
echo "Done. Version is now $NEW_VER (source of truth: version.json)"
echo "Next: git add version.json VERSION internal/updater/updater.go && git commit -m \"chore: bump version to $NEW_VER\" && git tag v$NEW_VER"
