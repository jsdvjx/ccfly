#!/usr/bin/env node
"use strict";

/**
 * ccfly CLI launcher.
 *
 * Resolves the prebuilt Go binary for the current platform from the matching
 * optional dependency `ccfly-<os>-<arch>` and execs it, forwarding argv.
 *
 * Follows the esbuild/swc model: the main `ccfly` package ships no binary; it
 * declares one optionalDependency per platform, each carrying a single prebuilt
 * executable plus its own `os`/`cpu` fields so npm installs exactly the one that
 * matches the consumer's machine. This shim is the runtime resolver.
 *
 *   process.platform -> npm "os"  token: darwin | linux | win32
 *   process.arch     -> npm "cpu" token: arm64  | x64
 *
 * Subpackages are named `ccfly-${platform}-${arch}` to match these tokens 1:1.
 */

const { spawnSync } = require("node:child_process");

const SUPPORTED = new Set([
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
]);

/**
 * On musl-based Linux distros (e.g. Alpine) glibc binaries won't run. We don't
 * ship a separate musl build yet, but detect it so the error is actionable
 * instead of a confusing runtime crash. Returns "glibc" | "musl".
 */
function detectLibc() {
  if (process.platform !== "linux") return "glibc";
  try {
    const report =
      typeof process.report?.getReport === "function"
        ? process.report.getReport()
        : null;
    const header = report && report.header;
    if (header) {
      if (header.glibcVersionRuntime) return "glibc";
      // glibcVersionRuntime is absent on musl builds of Node.
      if ("glibcVersionRuntime" in header) return "musl";
    }
  } catch {
    /* fall through to glibc default */
  }
  return "glibc";
}

function packageName() {
  return `ccfly-${process.platform}-${process.arch}`;
}

function resolveBinaryPath() {
  const target = `${process.platform}-${process.arch}`;
  const pkg = packageName();
  const exe = process.platform === "win32" ? "ccfly.exe" : "ccfly";

  if (!SUPPORTED.has(target)) {
    const hint =
      target.startsWith("linux") && detectLibc() === "musl"
        ? " (musl libc detected; only glibc Linux builds are published)"
        : "";
    throw new Error(
      `ccfly: no prebuilt binary for platform "${target}"${hint}.\n` +
        `Supported platforms: ${[...SUPPORTED].join(", ")}.`
    );
  }

  if (process.platform === "linux" && detectLibc() === "musl") {
    throw new Error(
      `ccfly: detected musl libc (e.g. Alpine), but only glibc Linux builds ` +
        `are published. Use a glibc-based image or install glibc compatibility.`
    );
  }

  // `${pkg}/bin/${exe}` is the published layout of each platform subpackage.
  // require.resolve walks node_modules from this file, so it finds the
  // optionalDependency npm installed for this platform.
  try {
    return require.resolve(`${pkg}/bin/${exe}`);
  } catch (err) {
    const e = new Error(
      `ccfly: the platform package "${pkg}" is not installed.\n` +
        `It should be pulled in automatically as an optional dependency of ` +
        `"ccfly". This usually means:\n` +
        `  - the install ran with --no-optional or --omit=optional, or\n` +
        `  - npm could not download "${pkg}" for ${target}.\n` +
        `Try reinstalling:  npm i ccfly   (or directly:  npm i ${pkg})`
    );
    e.cause = err;
    throw e;
  }
}

function main() {
  let binPath;
  try {
    binPath = resolveBinaryPath();
  } catch (err) {
    process.stderr.write((err && err.message ? err.message : String(err)) + "\n");
    process.exit(1);
    return;
  }

  // POSIX: ensure the platform binary is executable before spawning. npm only
  // sets the executable bit on a package's own declared `bin` entries; our
  // platform subpackages declare none (to avoid clashing with this `ccfly`
  // launcher's name), and `pnpm publish` normalizes other files to 0644 — so the
  // shipped binary installs as 0644 and spawnSync would fail with EACCES
  // (notably under `npx ccfly`). Restore +x best-effort; ignore failures
  // (read-only FS / Windows) and let the spawn proceed / surface its own error.
  if (process.platform !== "win32") {
    try {
      require("node:fs").chmodSync(binPath, 0o755);
    } catch {
      /* read-only FS or insufficient perms: fall through to spawn */
    }
  }

  // Default subcommand is `serve` when invoked with no args: `npx ccfly`.
  const forwarded = process.argv.slice(2);
  const args = forwarded.length > 0 ? forwarded : ["serve"];

  const result = spawnSync(binPath, args, {
    stdio: "inherit",
    windowsHide: true,
  });

  if (result.error) {
    if (result.error.code === "ENOENT") {
      process.stderr.write(
        `ccfly: binary not found at ${binPath}. Try reinstalling "ccfly".\n`
      );
    } else {
      process.stderr.write(
        `ccfly: failed to launch binary: ${result.error.message}\n`
      );
    }
    process.exit(1);
    return;
  }

  // Re-raise the child's terminating signal so callers observe it; otherwise
  // exit with the child's status (defaulting to 1 if somehow null).
  if (result.signal) {
    process.kill(process.pid, result.signal);
    return;
  }
  process.exit(result.status == null ? 1 : result.status);
}

main();
