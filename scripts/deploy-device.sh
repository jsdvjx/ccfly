#!/usr/bin/env bash
#
# deploy-device.sh — build the ccfly control binary for THIS machine and install
# it over the running device-side agent (the LaunchDaemon `com.ccfly.agent`).
#
# This is the repeatable form of the upgrade flow documented in CLAUDE.md
# ("升级流程"). It exists to keep the installed binary in lockstep with the
# repo — historically the device lagged (deployed 0.5.6 while packages/cli was
# already 0.6.1) because the upgrade was a hand-typed one-off.
#
# What it does:
#   1. Cross/native-compile go/cmd/ccfly for the host GOOS/GOARCH, stamping the
#      version from packages/cli/package.json into `main.version` (so
#      `ccfly version` reports the real version, not "0.0.0-dev").
#   2. Show installed-vs-new version.
#   3. sudo-install it to the system bin and kickstart the daemons. On macOS,
#      restart the root SNI helper first because it shares this binary and has
#      a versioned control protocol with the user agent.
#
# Usage:
#   scripts/deploy-device.sh                 # build + install + restart
#   scripts/deploy-device.sh --no-restart    # build + install, leave daemon
#   scripts/deploy-device.sh --print-only    # build, then PRINT the sudo steps
#                                            #   (for non-interactive / agent use)
#   scripts/deploy-device.sh --version 0.6.1 # override the stamped version
#
# Options:
#   --bin PATH        target binary path        (default /usr/local/bin/ccfly)
#   --service LABEL   launchd service to kick    (default system/com.ccfly.agent)
#   --version V       version string to stamp    (default: packages/cli version)
#   --no-restart      install but do not kickstart the daemon
#   --print-only      build only; print the (sudo) install+restart commands
#
# Notes:
#   - The install + kickstart need root. Run this yourself in an interactive
#     shell so sudo can prompt; the agent should use --print-only and hand you
#     the sudo commands to run.
#   - CGO_ENABLED=0 → fully static, matches the published binaries.

set -euo pipefail

# --- locate repo root (this script lives in scripts/) ----------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_DIR="${ROOT_DIR}/go"
GO_PKG="./cmd/ccfly"
CLI_PKG_JSON="${ROOT_DIR}/packages/cli/package.json"

# --- defaults --------------------------------------------------------------
BIN_PATH="/usr/local/bin/ccfly"
SERVICE="system/com.ccfly.agent"
HELPER_SERVICE="system/com.ccfly.sni-helper"
HELPER_PLIST="/Library/LaunchDaemons/com.ccfly.sni-helper.plist"
VERSION=""
NO_RESTART=0
PRINT_ONLY=0

# --- args ------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin)        BIN_PATH="$2"; shift 2 ;;
    --service)    SERVICE="$2"; shift 2 ;;
    --version)    VERSION="$2"; shift 2 ;;
    --no-restart) NO_RESTART=1; shift ;;
    --print-only) PRINT_ONLY=1; shift ;;
    -h|--help)    sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "deploy-device: unknown arg: $1" >&2; exit 2 ;;
  esac
done

# --- resolve version from packages/cli/package.json (same as build-binaries) -
read_cli_version() {
  if command -v node >/dev/null 2>&1; then
    node -e "process.stdout.write(require('${CLI_PKG_JSON}').version)"
  else
    grep -m1 '"version"' "${CLI_PKG_JSON}" | sed -E 's/.*"version" *: *"([^"]+)".*/\1/'
  fi
}
[[ -z "${VERSION}" ]] && VERSION="$(read_cli_version)"
if [[ -z "${VERSION}" ]]; then
  echo "deploy-device: could not determine version" >&2
  exit 1
fi

GOOS="$(cd "${GO_DIR}" && go env GOOS)"
GOARCH="$(cd "${GO_DIR}" && go env GOARCH)"

echo "deploy-device: host=${GOOS}/${GOARCH} version=${VERSION}"
echo "deploy-device: target bin=${BIN_PATH} service=${SERVICE}"

# --- build (static, version-stamped) ---------------------------------------
TMP_BIN="$(mktemp -t ccfly-deploy.XXXXXX)"
trap 'rm -f "${TMP_BIN}"' EXIT
echo "deploy-device: building ${GO_PKG} -> ${TMP_BIN}"
(
  cd "${GO_DIR}"
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o "${TMP_BIN}" "${GO_PKG}"
)
NEW_VER="$("${TMP_BIN}" version 2>/dev/null || echo "ccfly ?")"
echo "deploy-device: built -> ${NEW_VER}"

# --- show what is installed now --------------------------------------------
if [[ -x "${BIN_PATH}" ]]; then
  echo "deploy-device: installed now -> $("${BIN_PATH}" version 2>/dev/null || echo '(unknown)')"
else
  echo "deploy-device: installed now -> (none at ${BIN_PATH})"
fi

# --- install + restart -----------------------------------------------------
install_cmd="sudo install -m755 '${TMP_BIN}' '${BIN_PATH}'"
restart_cmd="sudo launchctl kickstart -k '${SERVICE}'"
restart_helper_cmd="sudo launchctl kickstart -k '${HELPER_SERVICE}'"

if [[ "${PRINT_ONLY}" == "1" ]]; then
  # Keep the built binary so the printed commands stay valid for the operator.
  KEEP_BIN="$(mktemp -t ccfly-deploy-keep.XXXXXX)"
  cp "${TMP_BIN}" "${KEEP_BIN}"
  echo
  echo "deploy-device: --print-only — run these yourself (sudo will prompt):"
  echo "  sudo install -m755 '${KEEP_BIN}' '${BIN_PATH}'"
  if [[ "${NO_RESTART}" == "0" ]]; then
    [[ "${GOOS}" == "darwin" && -f "${HELPER_PLIST}" ]] && echo "  ${restart_helper_cmd}"
    echo "  ${restart_cmd}"
  fi
  echo "  ${BIN_PATH} version   # expect: ccfly ${VERSION}"
  exit 0
fi

echo "deploy-device: ${install_cmd}"
eval "${install_cmd}"

if [[ "${NO_RESTART}" == "1" ]]; then
  echo "deploy-device: --no-restart — daemon NOT kicked; run when ready:"
  [[ "${GOOS}" == "darwin" && -f "${HELPER_PLIST}" ]] && echo "  ${restart_helper_cmd}"
  echo "  ${restart_cmd}"
else
  if [[ "${GOOS}" == "darwin" && -f "${HELPER_PLIST}" ]]; then
    echo "deploy-device: ${restart_helper_cmd}"
    eval "${restart_helper_cmd}"
  fi
  echo "deploy-device: ${restart_cmd}"
  eval "${restart_cmd}"
fi

echo "deploy-device: now installed -> $("${BIN_PATH}" version 2>/dev/null || echo '(unknown)')"
echo "deploy-device: done — log at ~/.ccfly/ccfly.log"
