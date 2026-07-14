#!/usr/bin/env bash
#
# build-tmux-macos.sh — build a portable (dependency-free) tmux for macOS and
# stage it as the go:embed blob consumed by go/internal/tmuxbin.
#
# Why: ccfly 的终端/会话控制依赖 tmux,但很多 mac 用户没装(也没有 Homebrew)。
# Windows 走「平台包捆 psmux」;mac 走「把静态 tmux 嵌进 ccfly 二进制,运行时
# 找不到系统 tmux 才释放到 ~/.ccfly/bin」——embed 能覆盖全部分发路径(npm 平台包、
# `ccfly install` 单文件拷贝、手动 go build),散文件模型会在 install 拷贝时丢失。
#
# 产物(gzip 后提交进仓库,go:embed 引用;tmux 升级才需要重跑本脚本):
#   go/internal/tmuxbin/blob/tmux-darwin-arm64.gz
#   go/internal/tmuxbin/blob/tmux-darwin-amd64.gz
#
# 「静态」的准确含义:libevent/ncursesw 静态链接(.a),只动态链 macOS 必然存在的
# libSystem(macOS 不支持完全静态)。脚本末尾用 otool -L 强制校验,混进第三方 dylib
# 依赖直接失败。terminfo 用系统自带 /usr/share/terminfo;default-terminal 定为
# screen-256color(全版本 macOS 都有该词条;tmux-256color 老系统缺失会让会话内程序起不来)。
#
# Usage:
#   scripts/build-tmux-macos.sh            # 双架构(arm64 + x86_64)
#   ARCHS="arm64" scripts/build-tmux-macos.sh
#   WORK_DIR=~/tmux-build scripts/build-tmux-macos.sh   # 复用下载/构建缓存
#
# 需要:Xcode CLT(clang/make/bison)、curl。x86_64 在 arm64 机上是交叉编译
# (clang -arch x86_64 开箱即用),无需 Rosetta。

set -euo pipefail

TMUX_VERSION="3.5a"
LIBEVENT_VERSION="2.1.12-stable"
NCURSES_VERSION="6.5"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BLOB_DIR="${ROOT_DIR}/go/internal/tmuxbin/blob"
WORK_DIR="${WORK_DIR:-$(mktemp -d -t ccfly-tmux-build)}"
ARCHS="${ARCHS:-arm64 x86_64}"

export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-11.0}"

mkdir -p "${WORK_DIR}/src" "${BLOB_DIR}"
echo "build-tmux: work dir ${WORK_DIR}"

# --- fetch sources (cached) --------------------------------------------------
fetch() {
  local url="$1" out="${WORK_DIR}/src/$2"
  if [[ -s "${out}" ]]; then
    echo "build-tmux: cached $2"
    return
  fi
  echo "build-tmux: downloading $2"
  curl -fSL --retry 3 -o "${out}.part" "${url}"
  mv "${out}.part" "${out}"
}

fetch "https://github.com/tmux/tmux/releases/download/${TMUX_VERSION}/tmux-${TMUX_VERSION}.tar.gz" "tmux-${TMUX_VERSION}.tar.gz"
fetch "https://github.com/libevent/libevent/releases/download/release-${LIBEVENT_VERSION}/libevent-${LIBEVENT_VERSION}.tar.gz" "libevent-${LIBEVENT_VERSION}.tar.gz"
fetch "https://ftp.gnu.org/gnu/ncurses/ncurses-${NCURSES_VERSION}.tar.gz" "ncurses-${NCURSES_VERSION}.tar.gz"

# GOARCH token for the blob filename (x86_64 -> amd64).
arch_to_goarch() {
  case "$1" in
    x86_64) echo "amd64" ;;
    *)      echo "$1" ;;
  esac
}

# autotools 三元组:老 config.sub(如 libevent 2.1.12 自带的)不认 arm64-apple-darwin,
# 得用 aarch64-apple-darwin。
arch_to_triple() {
  case "$1" in
    arm64) echo "aarch64-apple-darwin" ;;
    *)     echo "$1-apple-darwin" ;;
  esac
}

NATIVE_ARCH="$(uname -m)"

build_for_arch() {
  local arch="$1"
  local goarch; goarch="$(arch_to_goarch "${arch}")"
  local build="${WORK_DIR}/build-${arch}"
  local prefix="${build}/prefix"
  local cflags="-arch ${arch} -O2"
  # native 架构不传 --host(免入 cross 模式);交叉才传,ncurses 交叉还需 --with-build-cc
  # 让构建期辅助工具用本机编译。
  local hostflags=() ncurses_cross=()
  if [[ "${arch}" != "${NATIVE_ARCH}" ]]; then
    hostflags=(--host="$(arch_to_triple "${arch}")")
    ncurses_cross=(--with-build-cc=cc)
  fi

  echo "== build-tmux: ${arch} =="
  mkdir -p "${build}" "${prefix}"

  # ncurses(wide-char 静态库;禁掉 progs/文档/绑定,只要 libncursesw.a + 头文件)。
  if [[ ! -f "${prefix}/lib/libncursesw.a" ]]; then
    rm -rf "${build}/ncurses"
    mkdir -p "${build}/ncurses"
    tar -xzf "${WORK_DIR}/src/ncurses-${NCURSES_VERSION}.tar.gz" -C "${build}/ncurses" --strip-components 1
    (
      cd "${build}/ncurses"
      ./configure ${hostflags[@]+"${hostflags[@]}"} ${ncurses_cross[@]+"${ncurses_cross[@]}"} --prefix="${prefix}" \
        CC=cc CFLAGS="${cflags}" \
        --enable-widec --without-shared --without-debug --without-ada \
        --without-cxx --without-cxx-binding --without-manpages \
        --without-progs --without-tack --without-tests \
        --disable-db-install \
        --with-default-terminfo-dir=/usr/share/terminfo \
        --with-terminfo-dirs="/usr/share/terminfo" >/dev/null
      make -j"$(sysctl -n hw.ncpu)" libs >/dev/null
      make install.libs install.includes >/dev/null
    )
  fi

  # libevent(静态、无 openssl —— tmux 用不到)。
  if [[ ! -f "${prefix}/lib/libevent_core.a" ]]; then
    rm -rf "${build}/libevent"
    mkdir -p "${build}/libevent"
    tar -xzf "${WORK_DIR}/src/libevent-${LIBEVENT_VERSION}.tar.gz" -C "${build}/libevent" --strip-components 1
    (
      cd "${build}/libevent"
      ./configure ${hostflags[@]+"${hostflags[@]}"} --prefix="${prefix}" \
        CC=cc CFLAGS="${cflags}" \
        --disable-shared --enable-static --disable-openssl \
        --disable-samples --disable-libevent-regress --disable-debug-mode >/dev/null
      make -j"$(sysctl -n hw.ncpu)" >/dev/null
      make install >/dev/null
    )
  fi

  # tmux:直接把两个 .a 喂给 configure(绕开 pkg-config),不给动态库任何机会。
  # LIBEVENT_* / LIBNCURSES* 大小写两套变量名都设,兼容 configure 探测顺序
  # (先 ncursesw 后 ncurses、先 libevent_core 后 libevent)。
  rm -rf "${build}/tmux"
  mkdir -p "${build}/tmux"
  tar -xzf "${WORK_DIR}/src/tmux-${TMUX_VERSION}.tar.gz" -C "${build}/tmux" --strip-components 1
  (
    cd "${build}/tmux"
    ./configure ${hostflags[@]+"${hostflags[@]}"} \
      CC=cc CFLAGS="${cflags} -I${prefix}/include -I${prefix}/include/ncursesw" \
      --with-TERM=screen-256color \
      --disable-utf8proc \
      LIBEVENT_CORE_CFLAGS="-I${prefix}/include" \
      LIBEVENT_CORE_LIBS="${prefix}/lib/libevent_core.a" \
      LIBEVENT_CFLAGS="-I${prefix}/include" \
      LIBEVENT_LIBS="${prefix}/lib/libevent.a" \
      LIBNCURSESW_CFLAGS="-I${prefix}/include/ncursesw" \
      LIBNCURSESW_LIBS="${prefix}/lib/libncursesw.a" \
      LIBNCURSES_CFLAGS="-I${prefix}/include/ncursesw" \
      LIBNCURSES_LIBS="${prefix}/lib/libncursesw.a" >/dev/null
    make -j"$(sysctl -n hw.ncpu)" >/dev/null
  )

  local out="${build}/tmux/tmux"
  strip "${out}"
  codesign -f -s - "${out}" # strip 毁签名;ad-hoc 重签(arm64 内核强制校验)

  # 强校验:架构对、动态依赖只允许 /usr/lib/ 系统库(libSystem/libresolv 等,SIP 保护、
  # 全版本 macOS 必有;混进 /opt/homebrew、/usr/local 的 dylib = 不可移植,直接炸)。
  file "${out}" | grep -q "${arch}" || { echo "build-tmux: FAIL wrong arch"; exit 1; }
  local deps
  deps="$(otool -L "${out}" | tail -n +2 | awk '{print $1}' | grep -v '^/usr/lib/' || true)"
  if [[ -n "${deps}" ]]; then
    echo "build-tmux: FAIL unexpected dynamic deps for ${arch}:"
    echo "${deps}"
    exit 1
  fi

  local blob="${BLOB_DIR}/tmux-darwin-${goarch}.gz"
  gzip -9 -c "${out}" > "${blob}"
  echo "build-tmux: ${arch} OK -> ${blob} ($(du -h "${blob}" | cut -f1 | tr -d ' ')), tmux $("${build}/tmux/tmux" -V 2>/dev/null || echo '(cross, not runnable here)')"
}

for a in ${ARCHS}; do
  build_for_arch "${a}"
done

echo "build-tmux: done"
