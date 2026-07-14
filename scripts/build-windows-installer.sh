#!/usr/bin/env bash
#
# build-windows-installer.sh — 用 NSIS 在 macOS/Linux 上交叉构建 ccfly 的
# Windows x64 安装器(ccfly-setup-<version>-x64.exe)。
#
# 产物做什么:铺 ccfly.exe + tmux.exe(psmux)到 %LOCALAPPDATA%\Programs\ccfly、
# 加用户 PATH、建开始菜单快捷方式、注册「应用和功能」卸载项;完成页一键跑
# `ccfly install`(配对 + 注册计划任务)。安装器工程见 scripts/windows/。
#
# Usage:
#   scripts/build-windows-installer.sh [VERSION]
#
#   VERSION   缺省取 packages/cli/package.json 的 version。
#
# Env:
#   REBUILD=1   强制重新交叉编译 windows/amd64 二进制(缺省复用已有产物)
#
# 依赖:makensis(brew install makensis)、node、sips(macOS 自带,缩图标用)。
# 输出:dist/ccfly-setup-<version>-x64.exe

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
WIN_DIR="${SCRIPT_DIR}/windows"
BIN_DIR="${ROOT_DIR}/npm/ccfly-win32-x64/bin"
DIST_DIR="${ROOT_DIR}/dist"

command -v makensis >/dev/null 2>&1 || {
  echo "build-windows-installer: makensis 未安装(brew install makensis)" >&2
  exit 1
}

VERSION="${1:-$(node -e "process.stdout.write(require('${ROOT_DIR}/packages/cli/package.json').version)")}"

# --- 确保 windows/amd64 二进制存在(含捆绑的 tmux.exe) ----------------------
if [[ ! -f "${BIN_DIR}/ccfly.exe" || "${REBUILD:-0}" == "1" ]]; then
  echo "build-windows-installer: 交叉编译 windows/amd64"
  TARGETS="windows/amd64" bash "${SCRIPT_DIR}/build-binaries.sh" "${VERSION}"
fi
[[ -f "${BIN_DIR}/tmux.exe" ]] || {
  echo "build-windows-installer: 缺 ${BIN_DIR}/tmux.exe(psmux 未捆绑,重跑 build-binaries.sh)" >&2
  exit 1
}

# --- 图标:webdist 的 PNG → 256px → 单图 ICO(PNG 直嵌,Vista+ 原生支持) ----
ICON_SRC="${ROOT_DIR}/go/internal/control/webdist/icon-512.png"
ICON_PNG="$(mktemp -t ccfly-ico-XXXXXX).png"
if [[ -f "${ICON_SRC}" ]] && command -v sips >/dev/null 2>&1; then
  sips -z 256 256 "${ICON_SRC}" --out "${ICON_PNG}" >/dev/null
  node "${WIN_DIR}/make-ico.js" "${ICON_PNG}" "${WIN_DIR}/ccfly.ico"
  rm -f "${ICON_PNG}"
elif [[ ! -f "${WIN_DIR}/ccfly.ico" ]]; then
  echo "build-windows-installer: 无法生成图标(缺 ${ICON_SRC} 或 sips)" >&2
  exit 1
fi

# --- 估算「应用和功能」里显示的安装体积(KB) --------------------------------
SIZE_KB="$(du -k "${BIN_DIR}/ccfly.exe" "${BIN_DIR}/tmux.exe" | awk '{s+=$1} END {print s}')"

mkdir -p "${DIST_DIR}"
OUT="${DIST_DIR}/ccfly-setup-${VERSION}-x64.exe"

echo "build-windows-installer: version=${VERSION} -> ${OUT}"
(
  cd "${WIN_DIR}" # installer.nsi 里的相对 File 指令按 CWD 解析
  makensis \
    -DVERSION="${VERSION}" \
    -DBINDIR="${BIN_DIR}" \
    -DOUTFILE="${OUT}" \
    -DESTIMATED_SIZE_KB="${SIZE_KB}" \
    installer.nsi
)

echo "build-windows-installer: done — $(du -h "${OUT}" | cut -f1 | tr -d ' ') ${OUT}"
