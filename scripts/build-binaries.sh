#!/usr/bin/env bash
#
# build-binaries.sh — cross-compile the ccfly Go control service into each
# per-platform npm subpackage under npm/ccfly-<os>-<arch>/bin/.
#
# Follows the esbuild/swc distribution model: one static binary per platform,
# packed into its own npm package whose os/cpu fields gate installation.
#
# Usage:
#   scripts/build-binaries.sh [VERSION]
#
#   VERSION   Version to stamp into the binary (-X main.version) AND to sync
#             into every subpackage's package.json "version" field, keeping them
#             in lockstep with the main `ccfly` package. Defaults to the version
#             in packages/cli/package.json.
#
# Env overrides:
#   TARGETS   Space-separated "<goos>/<goarch>" list. Default: the 4 published
#             targets. Each maps to npm tokens (amd64 -> x64) for the dir name.
#   CLEAN     If "1", remove existing binaries first.
#
# Notes:
#   - CGO_ENABLED=0 → fully static, no libc linkage; one Linux build serves all
#     glibc distros. (musl is detected & rejected by the bin shim.)
#   - On the local machine you can build just your platform, e.g.:
#       TARGETS="darwin/arm64" scripts/build-binaries.sh

set -euo pipefail

# --- locate repo root (this script lives in scripts/) ----------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_DIR="${ROOT_DIR}/go"
NPM_DIR="${ROOT_DIR}/npm"
GO_PKG="./cmd/ccfly"

# --- resolve version -------------------------------------------------------
CLI_PKG_JSON="${ROOT_DIR}/packages/cli/package.json"
read_cli_version() {
  # Prefer node (always present in this toolchain); fall back to grep.
  if command -v node >/dev/null 2>&1; then
    node -e "process.stdout.write(require('${CLI_PKG_JSON}').version)"
  else
    grep -m1 '"version"' "${CLI_PKG_JSON}" | sed -E 's/.*"version" *: *"([^"]+)".*/\1/'
  fi
}

VERSION="${1:-$(read_cli_version)}"
if [[ -z "${VERSION}" ]]; then
  echo "build-binaries: could not determine version" >&2
  exit 1
fi

# --- target matrix ---------------------------------------------------------
# "<goos>/<goarch>"; arch is the Go token (amd64), dir uses the npm token (x64).
DEFAULT_TARGETS="darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64"
TARGETS="${TARGETS:-$DEFAULT_TARGETS}"

# Map Go GOARCH -> npm cpu token.
goarch_to_npm() {
  case "$1" in
    amd64) echo "x64" ;;
    arm64) echo "arm64" ;;
    386)   echo "ia32" ;;
    *)     echo "$1" ;;
  esac
}

# Map Go GOOS -> npm os token (and decide the exe suffix).
goos_to_npm() {
  case "$1" in
    windows) echo "win32" ;;
    *)       echo "$1" ;;
  esac
}

echo "build-binaries: version=${VERSION}"
echo "build-binaries: targets=${TARGETS}"

# --- build + embed the web UI first ----------------------------------------
# Every per-platform binary embeds go/internal/control/webdist via //go:embed,
# so refresh that staging dir once up front before the cross-compile matrix.
# Set SKIP_WEB=1 to reuse whatever is already staged (e.g. fast Go-only rebuild).
if [[ "${SKIP_WEB:-0}" == "1" ]]; then
  echo "build-binaries: SKIP_WEB=1 — reusing existing go/internal/control/webdist"
elif [[ -d "${CCFLY_TTYD_UI_DIR:-${ROOT_DIR}/../ccfly-ttyd-ui}" ]]; then
  echo "build-binaries: building web UI (scripts/build-web.sh)"
  bash "${SCRIPT_DIR}/build-web.sh"
elif [[ -f "${GO_DIR}/internal/control/webdist/index.html" ]]; then
  # The former ccfly-ttyd-ui repository is no longer part of a clean checkout.
  # Release/CI builds must remain reproducible from this repository alone, so
  # use the reviewed, committed embed when the optional UI source is absent.
  echo "build-binaries: ttyd-ui source unavailable — reusing committed go/internal/control/webdist"
else
  echo "build-binaries: missing both ttyd-ui source and committed webdist" >&2
  exit 1
fi

# --- sync subpackage versions to lockstep ----------------------------------
sync_subpkg_version() {
  local pkg_json="$1" ver="$2"
  if command -v node >/dev/null 2>&1; then
    node -e '
      const fs = require("fs");
      const p = process.argv[1], v = process.argv[2];
      const j = JSON.parse(fs.readFileSync(p, "utf8"));
      j.version = v;
      fs.writeFileSync(p, JSON.stringify(j, null, 2) + "\n");
    ' "$pkg_json" "$ver"
  else
    # Best-effort sed fallback.
    sed -i.bak -E "s/(\"version\" *: *\")[^\"]+(\")/\1${ver}\2/" "$pkg_json"
    rm -f "${pkg_json}.bak"
  fi
}

# --- bundle psmux for Windows (tmux-compatible multiplexer) ----------------
PSMUX_VERSION="v3.3.6"

bundle_psmux() {
  local bin_dir="$1" goarch="$2"
  local tmux_exe="${bin_dir}/tmux.exe"

  if [[ -f "${tmux_exe}" && "${CLEAN:-0}" != "1" ]]; then
    echo "build-binaries: psmux already bundled at ${tmux_exe}"
    return
  fi

  local arch_token
  case "${goarch}" in
    amd64) arch_token="x64" ;;
    arm64) arch_token="arm64" ;;
    386)   arch_token="x86" ;;
    *)     echo "build-binaries: SKIP psmux — unsupported arch ${goarch}" >&2; return ;;
  esac

  local zip_name="psmux-${PSMUX_VERSION}-windows-${arch_token}.zip"
  local zip_url="https://github.com/psmux/psmux/releases/download/${PSMUX_VERSION}/${zip_name}"
  local tmp_zip
  tmp_zip="$(mktemp -t psmux-XXXXXX).zip"
  local tmp_dir
  tmp_dir="$(mktemp -d -t psmux-extract-XXXXXX)"

  echo "build-binaries: downloading psmux ${PSMUX_VERSION} for windows/${goarch}"
  if curl -ksSL -o "${tmp_zip}" "${zip_url}"; then
    unzip -qo "${tmp_zip}" -d "${tmp_dir}"
    cp "${tmp_dir}/tmux.exe" "${tmux_exe}"
    chmod +x "${tmux_exe}"
    echo "build-binaries: bundled tmux.exe (psmux) -> ${tmux_exe}"
  else
    echo "build-binaries: WARNING — failed to download psmux, tmux will not be bundled" >&2
  fi

  rm -rf "${tmp_zip}" "${tmp_dir}"
}

# --- build loop ------------------------------------------------------------
built=0
for target in ${TARGETS}; do
  goos="${target%%/*}"
  goarch="${target##*/}"

  npm_os="$(goos_to_npm "${goos}")"
  npm_cpu="$(goarch_to_npm "${goarch}")"
  pkg_dir="${NPM_DIR}/ccfly-${npm_os}-${npm_cpu}"

  if [[ ! -d "${pkg_dir}" ]]; then
    echo "build-binaries: SKIP ${target} — no subpackage at ${pkg_dir}" >&2
    continue
  fi

  exe="ccfly"
  [[ "${goos}" == "windows" ]] && exe="ccfly.exe"
  out="${pkg_dir}/bin/${exe}"

  mkdir -p "${pkg_dir}/bin"
  [[ "${CLEAN:-0}" == "1" ]] && rm -f "${pkg_dir}/bin/ccfly" "${pkg_dir}/bin/ccfly.exe"

  echo "build-binaries: GOOS=${goos} GOARCH=${goarch} -> ${out}"
  (
    cd "${GO_DIR}"
    CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
      go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o "${out}" \
        "${GO_PKG}"
  )
  chmod +x "${out}"

  # Bundle psmux (tmux-compatible multiplexer) for Windows targets
  if [[ "${goos}" == "windows" ]]; then
    bundle_psmux "${pkg_dir}/bin" "${goarch}"
  fi

  sync_subpkg_version "${pkg_dir}/package.json" "${VERSION}"
  built=$((built + 1))
done

if [[ "${built}" -eq 0 ]]; then
  echo "build-binaries: nothing built (no matching subpackages)" >&2
  exit 1
fi

echo "build-binaries: done — ${built} binary/binaries built at version ${VERSION}"
