#!/usr/bin/env bash
#
# install.sh — one-line installer for 9router-go.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/arewedaks/9router-go/feat/go-dashboard/scripts/install.sh | bash
#   curl -fsSL .../install.sh | bash -s -- --version 1.8.43   # pin a version
#   curl -fsSL .../install.sh | bash -s -- --service           # + systemd unit: auto-start on boot
#
# What it does (all stdlib curl + uname, no dependencies):
#   1. Detect OS/arch -> pick the release asset
#   2. Download the binary from the GitHub release
#   3. chmod +x, verify it runs, install to ~/.local/bin/9router-go
#   4. Add ~/.local/bin to PATH via the user's shell rc (idempotent)
#   5. With --service (as root): install the systemd unit + enable at boot
#
set -euo pipefail

REPO="arewedaks/9router-go"
# The installer ships on the branch that is currently the source of truth.
# feat/go-dashboard is where the dashboard + install.sh live; it will be merged
# to main on the next release, after which the URL below should be switched to
# main. Until then, pointing at main 404s (main is still the v1.8.13 fork point).
SCRIPT_REF="feat/go-dashboard"
BIN="9router-go"
INSTALL_DIR="${HOME}/.local/bin"

# ---- parse optional --version flag -------------------------------------
WANT_VERSION=""
WANT_SERVICE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) WANT_VERSION="${2:-}"; shift 2 ;;
    --version=*) WANT_VERSION="${1#--version=}"; shift ;;
    --service) WANT_SERVICE=1; shift ;;
    -h|--help)
      grep '^#' "$0" | sed 's/^# \?//' | tail -n +2; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

# ---- detect platform ----------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in
  linux)  goos="linux" ;;
  darwin) goos="darwin" ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64)  goarch="amd64" ;;
  aarch64|arm64) goarch="arm64" ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

# ---- resolve version + asset ---------------------------------------------
if [ -z "$WANT_VERSION" ]; then
  # Latest release via GitHub API (no gh dependency).
  LATEST="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep -o '"tag_name": *"[^"]*"' | cut -d'"' -f4)"
  if [ -z "$LATEST" ]; then
    echo "could not resolve the latest release of $REPO" >&2; exit 1
  fi
  VER="$LATEST"
else
  VER="$WANT_VERSION"
fi
VER="${VER#v}"  # normalise a leading v, if any

if [ "$goos" = "windows" ]; then
  ASSET="${BIN}-windows-${goarch}.exe"
else
  ASSET="${BIN}-${goos}-${goarch}"
fi
URL="https://github.com/$REPO/releases/download/v${VER}/${ASSET}"

echo "==> 9router-go v${VER} for ${goos}/${goarch}"
echo "==> downloading $URL"

# ---- download to a temp file ---------------------------------------------
TMP="$(mktemp "${TMPDIR:-/tmp}/${BIN}.XXXXXX")"
trap 'rm -f "$TMP"' EXIT
curl -fsSL --progress-bar -o "$TMP" "$URL"

# ---- install ---------------------------------------------------------------
chmod +x "$TMP"
mkdir -p "$INSTALL_DIR"
# Move the temp file into place atomically enough for our purposes.
mv "$TMP" "$INSTALL_DIR/${BIN}"
trap - EXIT

# ---- sanity check the binary actually runs --------------------------------
# `version` is a urfave/cli subcommand (not a flag), so the smoke check must
# run it that way.
if ! "$INSTALL_DIR/${BIN}" version >/dev/null 2>&1; then
  echo "ERROR: installed binary failed its version smoke check" >&2
  echo "       (wrong platform asset? check the release page for $ASSET)" >&2
  exit 1
fi

echo "==> installed to $INSTALL_DIR/${BIN}"
"$INSTALL_DIR/${BIN}" version || true

# ---- PATH wiring (idempotent) ---------------------------------------------
need_path=0
case ":$PATH:" in
  *":$INSTALL_DIR:"*) need_path=0 ;;
  *) need_path=1 ;;
esac

if [ "$need_path" -eq 1 ]; then
  # Pick the rc file the running shell would actually source.
  RC=""
  case "${SHELL:-}" in
    *zsh)  RC="${HOME}/.zshrc" ;;
    *bash) RC="${HOME}/.bashrc" ;;
  esac
  [ -z "$RC" ] && RC="${HOME}/.bashrc"
  LINE="export PATH=\"\$HOME/.local/bin:\$PATH\""
  if ! grep -qsF "$LINE" "$RC"; then
    {
      echo ""
      echo "# 9router-go installer"
      echo "$LINE"
    } >> "$RC"
    echo "==> added $INSTALL_DIR to PATH via $RC"
    echo "    (open a new shell, or run:  source $RC)"
  else
    echo "==> $INSTALL_DIR already on PATH"
  fi
else
  echo "==> $INSTALL_DIR already on PATH"
fi

echo ""
echo "Done. Start it with:"
echo "  9router-go            # serves on :20128, dashboard at /"
echo "  9router-go --port 9090"

# ---- optional: systemd service so it survives a reboot -----------------
if [ "$WANT_SERVICE" -eq 1 ]; then
  if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: --service needs root (writes /etc/systemd/system). Re-run with sudo." >&2
    exit 1
  fi
  if [ ! -d /run/systemd/system ]; then
    echo "ERROR: systemd is not running on this machine; --service needs it." >&2
    exit 1
  fi
  echo ""
  echo "==> installing systemd service (auto-start on boot)"
  "$INSTALL_DIR/$BIN" service install
else
  # A silent "no autostart" is how an operator ends up rebooting into a dead
  # gateway. Say it out loud, and say it again after every install.
  if [ -d /run/systemd/system ]; then
    echo ""
    echo "NOTE: this machine has systemd, but no service was installed —"
    echo "      9router-go will NOT come back after a reboot."
    echo "      Enable auto-start with:"
    echo "        sudo $INSTALL_DIR/$BIN service install"
    echo "      (or re-run this installer with: sudo bash -s -- --service)"
  fi
fi
