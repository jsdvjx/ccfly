#!/usr/bin/env bash
#
# build-web.sh — build the Vite example app (examples/web) and stage its dist
# into the Go embed directory (go/internal/control/webdist) so the ccfly binary
# serves the SPA from memory.
#
# Pipeline:
#   1. pnpm install at the repo root (only if node_modules is missing/stale).
#   2. Build @ccfly/react so the `@ccfly/react: workspace:*` dep resolves to a
#      real dist/ before the example compiles against it.
#   3. Build examples/web (Vite) -> examples/web/dist.
#   4. Replace go/internal/control/webdist/* with the fresh dist, keeping the
#      tracked .gitignore so `go:embed all:webdist` still works on git checkout.
#
# Usage:
#   scripts/build-web.sh
#
# Env overrides:
#   SKIP_INSTALL   If "1", skip the root `pnpm install` step.
#
# Notes:
#   - Idempotent: re-running reproduces the same staged webdist.
#   - All paths are derived from this script's location; safe to invoke from
#     any cwd.
#   - `go:embed all:webdist` requires the dir to exist with at least one file
#     (index.html) on a fresh checkout — see go/internal/control/webdist/.gitignore.

set -euo pipefail

# --- locate repo root (this script lives in scripts/) ----------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

REACT_DIR="${ROOT_DIR}/packages/react"
WEB_DIR="${ROOT_DIR}/examples/web"
WEB_DIST="${WEB_DIR}/dist"
EMBED_DIR="${ROOT_DIR}/go/internal/control/webdist"

if ! command -v pnpm >/dev/null 2>&1; then
  echo "build-web: pnpm is required but not found in PATH" >&2
  exit 1
fi

if [[ ! -d "${WEB_DIR}" ]]; then
  echo "build-web: example app not found at ${WEB_DIR}" >&2
  exit 1
fi

# --- 1) install workspace deps --------------------------------------------
if [[ "${SKIP_INSTALL:-0}" != "1" ]]; then
  echo "build-web: pnpm install (workspace root)"
  pnpm -C "${ROOT_DIR}" install
else
  echo "build-web: SKIP_INSTALL=1 — skipping pnpm install"
fi

# --- 2) build @ccfly/react (the example's workspace dependency) ------------
echo "build-web: building @ccfly/react"
pnpm -C "${REACT_DIR}" run build

# --- 3) build examples/web -------------------------------------------------
# Prefer a workspace-filtered build keyed off the package's own "name" field so
# this keeps working regardless of the directory name. Fall back to a direct
# per-directory build if the package isn't registered in the pnpm workspace.
web_pkg_name=""
if [[ -f "${WEB_DIR}/package.json" ]] && command -v node >/dev/null 2>&1; then
  web_pkg_name="$(node -e "process.stdout.write(require('${WEB_DIR}/package.json').name || '')")"
fi

echo "build-web: building examples/web"
if [[ -n "${web_pkg_name}" ]] && pnpm -C "${ROOT_DIR}" --filter "${web_pkg_name}" exec true >/dev/null 2>&1; then
  pnpm -C "${ROOT_DIR}" --filter "${web_pkg_name}" run build
else
  # Not in the workspace (or name unknown): build straight from the directory.
  pnpm -C "${WEB_DIR}" run build
fi

if [[ ! -d "${WEB_DIST}" ]]; then
  echo "build-web: expected build output at ${WEB_DIST} but it is missing" >&2
  exit 1
fi
if [[ -z "$(ls -A "${WEB_DIST}" 2>/dev/null)" ]]; then
  echo "build-web: build output ${WEB_DIST} is empty" >&2
  exit 1
fi

# --- 4) stage dist into the Go embed dir ----------------------------------
# Wipe everything except the tracked .gitignore, then copy the fresh dist in.
echo "build-web: staging ${WEB_DIST} -> ${EMBED_DIR}"
mkdir -p "${EMBED_DIR}"
find "${EMBED_DIR}" -mindepth 1 -not -name '.gitignore' -delete

# Copy dist contents (not the dist dir itself) into webdist/.
cp -R "${WEB_DIST}/." "${EMBED_DIR}/"

if [[ ! -f "${EMBED_DIR}/index.html" ]]; then
  echo "build-web: WARNING — ${EMBED_DIR}/index.html missing after copy" >&2
  echo "build-web: (go:embed all:webdist + SPA fallback expect an index.html)" >&2
fi

echo "build-web: done — embedded web assets at ${EMBED_DIR}"
