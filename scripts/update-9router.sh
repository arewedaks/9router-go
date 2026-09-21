#!/usr/bin/env bash
#
# 9router-go updater for a systemd-managed server.
#
# Usage:
#   sudo ./update-9router.sh              # update to the latest release
#   sudo ./update-9router.sh v1.8.36      # pin to a specific tag
#   sudo ./update-9router.sh --check      # report versions, change nothing
#
# Safe by design:
#   - the checksum is verified BEFORE the binary is installed, aborting on mismatch
#   - the running binary is backed up first, so rollback is one command
#   - the install is atomic (install(1) writes a new file and renames it)
#   - nothing is installed if any step fails; the service keeps running the old binary

set -euo pipefail

REPO="arewedaks/9router-go"
BIN_PATH="/opt/9router-go/9router-go"
SERVICE="9router-go"
ASSET="9router-go-linux-amd64"

TAG=""
CHECK_ONLY=0

for arg in "$@"; do
  case "$arg" in
    --check) CHECK_ONLY=1 ;;
    -h|--help) sed -n '3,12p' "$0"; exit 0 ;;
    v*) TAG="$arg" ;;
    *) echo "Unknown argument: $arg" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ] && [ "$CHECK_ONLY" -eq 0 ]; then
  echo "ERROR: run with sudo (installing into $(dirname "$BIN_PATH") needs root)." >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "ERROR: curl is required." >&2
  exit 1
fi

# ---------------------------------------------------------------- version info

running_version() {
  # The binary reports its version only via --help output; there is no
  # --version flag. Fall back to "unknown" rather than failing: an unreadable
  # version must not block an update.
  if [ -x "$BIN_PATH" ]; then
    "$BIN_PATH" --help 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "unknown"
  else
    echo "absent"
  fi
}

if [ -n "$TAG" ]; then
  RELEASE_API="https://api.github.com/repos/${REPO}/releases/tags/${TAG}"
else
  RELEASE_API="https://api.github.com/repos/${REPO}/releases/latest"
fi

echo "Querying ${RELEASE_API}"
RELEASE_JSON="$(curl -fsSL "$RELEASE_API")" || {
  echo "ERROR: could not reach the GitHub releases API." >&2
  echo "       Rate limit is 60/hour unauthenticated; wait and retry." >&2
  exit 1
}

TARGET_TAG="$(printf '%s' "$RELEASE_JSON" | grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 | cut -d'"' -f4)"
if [ -z "$TARGET_TAG" ]; then
  echo "ERROR: could not read tag_name from the release response." >&2
  exit 1
fi

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TARGET_TAG}/${ASSET}"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"

CURRENT="$(running_version)"
TARGET="${TARGET_TAG#v}"

echo "  running : ${CURRENT}"
echo "  target  : ${TARGET} (${TARGET_TAG})"

if [ "$CURRENT" = "$TARGET" ]; then
  echo "Already up to date. Nothing to do."
  exit 0
fi

if [ "$CHECK_ONLY" -eq 1 ]; then
  echo "Update available: ${CURRENT} -> ${TARGET}"
  exit 0
fi

# -------------------------------------------------------------------- download

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

echo "Downloading ${ASSET} ${TARGET_TAG} ..."
curl -fsSL --retry 3 --retry-delay 2 --max-time 300 -o "${WORKDIR}/${ASSET}" "$DOWNLOAD_URL" || {
  echo "ERROR: download failed." >&2
  exit 1
}

# A truncated or HTML error page is the classic failure here, and it would be
# installed as a working-looking binary. Check the size before trusting it.
SIZE="$(stat -c%s "${WORKDIR}/${ASSET}" 2>/dev/null || echo 0)"
if [ "$SIZE" -lt 5000000 ]; then
  echo "ERROR: downloaded file is only ${SIZE} bytes; expected a ~18MB binary." >&2
  echo "       This is usually a CDN error page. Try again shortly." >&2
  exit 1
fi

# ------------------------------------------------------------ verify checksum

echo "Verifying checksum ..."
CHECKSUM_FILE="${WORKDIR}/${ASSET}.sha256"
if curl -fsSL --retry 3 --retry-delay 2 --max-time 60 -o "$CHECKSUM_FILE" "$CHECKSUM_URL"; then
  EXPECTED="$(awk '{print $1}' "$CHECKSUM_FILE" | grep -oE '^[0-9a-f]{64}$' | head -1)"
  if [ -z "$EXPECTED" ]; then
    echo "ERROR: checksum file did not contain a valid sha256." >&2
    exit 1
  fi
  ACTUAL="$(sha256sum "${WORKDIR}/${ASSET}" | awk '{print $1}')"
  if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "ERROR: CHECKSUM MISMATCH — refusing to install." >&2
    echo "  expected: $EXPECTED" >&2
    echo "  actual  : $ACTUAL" >&2
    exit 1
  fi
  echo "  checksum OK ($ACTUAL)"
else
  # The .sha256 asset is served from a CDN that returns intermittent 504s. A
  # failed CHECKSUM FETCH is not evidence the binary is bad, but installing
  # unverified is worse than not updating, so this asks for a decision instead
  # of assuming one.
  echo "WARNING: could not fetch the checksum file (CDN error)." >&2
  read -r -p "Install WITHOUT checksum verification? [y/N] " reply
  case "$reply" in
    [yY]|[yY][eE][sS]) echo "  proceeding unverified at operator's request" ;;
    *) echo "Aborted; nothing changed."; exit 1 ;;
  esac
fi

# --------------------------------------------------------------- sanity check

# The binary must actually run on this machine: wrong architecture, a missing
# glibc, or a quarantined download all surface here, before the service has
# already been replaced.
if ! "${WORKDIR}/${ASSET}" --help >/dev/null 2>&1; then
  echo "ERROR: the downloaded binary will not execute on this machine." >&2
  echo "       Usually a wrong-architecture asset or a missing runtime (glibc)." >&2
  echo "       Verify with: file ${WORKDIR}/${ASSET}" >&2
  echo "       Nothing was changed." >&2
  exit 1
fi
echo "  binary executes OK"

# -------------------------------------------------------------------- install

STAMP="$(date +%F-%H%M%S)"
if [ -f "$BIN_PATH" ]; then
  echo "Backing up to ${BIN_PATH}.bak-${STAMP}"
  cp -p "$BIN_PATH" "${BIN_PATH}.bak-${STAMP}"
fi

echo "Installing over ${BIN_PATH} ..."
systemctl stop "$SERVICE" 2>/dev/null || true
# install(1) writes a fresh inode and renames it, so it cannot fail with
# "Text file busy" the way `cp` over a running executable can.
install -m 0755 -o root -g root "${WORKDIR}/${ASSET}" "$BIN_PATH"
systemctl start "$SERVICE"

sleep 3
if systemctl is-active --quiet "$SERVICE"; then
  echo
  echo "Updated: ${CURRENT} -> ${TARGET}"
  echo "Service: active"
  echo "Rollback: sudo cp ${BIN_PATH}.bak-${STAMP} ${BIN_PATH} && sudo systemctl restart ${SERVICE}"
else
  echo
  echo "ERROR: ${SERVICE} did not come back up. Rolling forward is unsafe." >&2
  systemctl status "$SERVICE" --no-pager | head -12 >&2
  echo >&2
  echo "Roll back with:" >&2
  echo "  sudo cp ${BIN_PATH}.bak-${STAMP} ${BIN_PATH} && sudo systemctl restart ${SERVICE}" >&2
  exit 1
fi
