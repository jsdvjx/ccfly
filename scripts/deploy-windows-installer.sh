#!/usr/bin/env bash
#
# deploy-windows-installer.sh — 把 build-windows-installer.sh 的产物发布到 cc.hn。
#
# 上传到 ccfly-cloud 的 webroot(os.DirFS 每次读盘,scp 即上线,无需重启):
#   dist/ccfly-setup-<ver>-x64.exe -> /opt/ccfly-cloud/webroot/dl/ccfly-setup-<ver>-x64.exe
#                                     + 稳定别名 dl/ccfly-setup-x64.exe(web「添加设备」指引指向它)
#
# 稳定别名的下载 URL: https://cc.hn/dl/ccfly-setup-x64.exe
# (cloud 对非 assets/ 路径的 Cache-Control 是 max-age=86400,新版本最多 1 天后全量生效)
#
# Usage:
#   scripts/deploy-windows-installer.sh [VERSION]   # 缺省取 packages/cli/package.json
#
# Env:
#   HOST      ssh 目标(默认 root@top.pm —— ccfly-cloud 跑在 top.pm,cc.hn 只是反代)
#   WEBROOT   远端 webroot(默认 /opt/ccfly-cloud/webroot)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

HOST="${HOST:-root@top.pm}"
WEBROOT="${WEBROOT:-/opt/ccfly-cloud/webroot}"
VERSION="${1:-$(node -e "process.stdout.write(require('${ROOT_DIR}/packages/cli/package.json').version)")}"

EXE="${ROOT_DIR}/dist/ccfly-setup-${VERSION}-x64.exe"
[[ -f "${EXE}" ]] || {
  echo "deploy-windows-installer: 缺 ${EXE} —— 先跑 scripts/build-windows-installer.sh ${VERSION}" >&2
  exit 1
}

echo "deploy-windows-installer: ${EXE} -> ${HOST}:${WEBROOT}/dl/"
ssh "${HOST}" "mkdir -p '${WEBROOT}/dl'"
scp "${EXE}" "${HOST}:${WEBROOT}/dl/ccfly-setup-${VERSION}-x64.exe"
ssh "${HOST}" "cp '${WEBROOT}/dl/ccfly-setup-${VERSION}-x64.exe' '${WEBROOT}/dl/ccfly-setup-x64.exe'"

echo "deploy-windows-installer: live — https://cc.hn/dl/ccfly-setup-x64.exe (稳定别名) / ccfly-setup-${VERSION}-x64.exe"
