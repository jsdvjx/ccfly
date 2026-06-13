#!/usr/bin/env bash
# build-web.sh — build the ccfly node web UI (the ccfly-ttyd-ui SPA) and stage
# it into go/internal/control/webdist/ for //go:embed. Run before `go build` /
# cross-compile.
#
# 前端已统一为 ccfly-ttyd-ui(Vue 双模:节点直连 base=''(本仓 go:embed 同源托管),
# Hub 下 base='/x/<device>'(ccfly-cloud go:embed))——同一份构建产物两仓共用,
# 运行时自适应。源默认在同级仓库 ../ccfly-ttyd-ui,可用 CCFLY_TTYD_UI_DIR 覆盖。
# (旧的 examples/web React 前端已退役,保留备查;本脚本不再构建它。)
#
# Usage:
#   scripts/build-web.sh
#
# Env overrides:
#   CCFLY_TTYD_UI_DIR  ttyd-ui 源码目录(default: ../ccfly-ttyd-ui)
#   SKIP_INSTALL       If "1", skip dependency install.
#
# Notes:
#   - Idempotent: re-running reproduces the same staged webdist.
#   - `go:embed all:webdist` requires the dir to exist with at least one file
#     (index.html) on a fresh checkout — see go/internal/control/webdist/.gitignore.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
WEB_DIR="${CCFLY_TTYD_UI_DIR:-$(cd "${ROOT_DIR}/.." && pwd)/ccfly-ttyd-ui}"
DIST_DIR="${WEB_DIR}/dist"
EMBED_DIR="${ROOT_DIR}/go/internal/control/webdist"

if [[ ! -d "${WEB_DIR}" ]]; then
  echo "build-web: ERROR ttyd-ui not found at ${WEB_DIR} (set CCFLY_TTYD_UI_DIR)" >&2
  exit 1
fi

echo "build-web: installing deps (${WEB_DIR})"
if [[ "${SKIP_INSTALL:-0}" != "1" ]]; then
  ( cd "${WEB_DIR}" && npm install --no-audit --no-fund )
fi

echo "build-web: building ${WEB_DIR}"
( cd "${WEB_DIR}" && npm run build )

if [[ ! -f "${DIST_DIR}/index.html" ]]; then
  echo "build-web: ERROR no ${DIST_DIR}/index.html after build" >&2
  exit 1
fi

echo "build-web: staging ${DIST_DIR} -> ${EMBED_DIR}"
mkdir -p "${EMBED_DIR}"
# wipe generated content but keep the tracked .gitignore
find "${EMBED_DIR}" -mindepth 1 -not -name .gitignore -delete
cp -R "${DIST_DIR}/." "${EMBED_DIR}/"

echo "build-web: done — embedded web assets at ${EMBED_DIR}"
