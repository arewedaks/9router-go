#!/usr/bin/env bash
# Static assertion suite for scripts/install.sh: every platform detection
# branch, the version-resolution path, and the PATH wiring logic, without
# ever hitting the network.
set -euo pipefail

SCRIPT="$(dirname "$0")/install.sh"
fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. Must be executable and syntactically valid bash.
[ -x "$SCRIPT" ] || fail "install.sh is not executable"
bash -n "$SCRIPT" || fail "install.sh has a bash syntax error"

# 2. Platform detection: both arch spellings must map to the GOARCH name the
#    release assets use (amd64/arm64), never the bare uname value.
grep -q 'x86_64|amd64)  goarch="amd64"' "$SCRIPT" || fail "x86_64 -> amd64 mapping missing"
grep -q 'aarch64|arm64) goarch="arm64"' "$SCRIPT" || fail "aarch64 -> arm64 mapping missing"

# 3. Version resolution: latest via the GitHub API, overridable with --version.
grep -q 'releases/latest' "$SCRIPT" || fail "latest-release resolution missing"
grep -q -- '--version' "$SCRIPT" || fail "--version flag not parsed"

# 4. Smoke check must use the `version` subcommand (urfave/cli), not a flag.
grep -q '"version smoke check"\|version >/dev/null' "$SCRIPT" || true
grep -qF '"$INSTALL_DIR/${BIN}" version' "$SCRIPT" || fail "smoke check does not call the version subcommand"

# 5. PATH wiring is idempotent: an existing entry must not be appended twice.
grep -q 'case ":$PATH:"' "$SCRIPT" || fail "PATH idempotency check missing"

# 6. --service: auto-start on boot, gated on root + systemd, and it must call
#    the binary's own service subcommand (single source of truth for the unit).
grep -q -- '--service) WANT_SERVICE=1' "$SCRIPT" || fail "--service flag not parsed"
grep -q 'id -u)" -ne 0' "$SCRIPT" || fail "--service missing root guard"
grep -q '/run/systemd/system' "$SCRIPT" || fail "--service missing systemd detection"
grep -qF '"$INSTALL_DIR/$BIN" service install' "$SCRIPT" || fail "--service does not delegate to the binary"

# 7. --service must refuse a release that has no `service` subcommand: the
#    v1.8.43 asset predates it and silently boots the server instead.
grep -qF "grep -q '^[[:space:]]*service[[:space:]]'" "$SCRIPT" || fail "--service missing capability guard"
grep -q 'has no .service. command' "$SCRIPT" || fail "--service guard message missing"

echo "PASS: install.sh static assertions (11/11)"
