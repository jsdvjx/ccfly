# Per-platform binary subpackages

This directory holds the npm subpackages that ship the prebuilt `ccfly` Go
binary, one per platform. They are the `optionalDependencies` of the `ccfly`
package; npm installs exactly the one matching the consumer's `os`/`cpu` (each
subpackage declares its own `os`/`cpu` fields, so the others are skipped).

This is the esbuild/swc distribution model: the main `ccfly` package contains no
binary, only the `bin/ccfly.js` shim that resolves and execs the right
subpackage at runtime.

Packages:

| package              | os       | cpu     | GOOS / GOARCH    |
| -------------------- | -------- | ------- | ---------------- |
| `ccfly-darwin-arm64` | `darwin` | `arm64` | `darwin/arm64`   |
| `ccfly-darwin-x64`   | `darwin` | `x64`   | `darwin/amd64`   |
| `ccfly-linux-arm64`  | `linux`  | `arm64` | `linux/arm64`    |
| `ccfly-linux-x64`    | `linux`  | `x64`   | `linux/amd64`    |

> The npm `cpu` token `x64` maps to Go's `GOARCH=amd64`; the build script does
> this translation. Linux builds are `CGO_ENABLED=0` (static, glibc-friendly);
> musl (Alpine) is detected and rejected by the shim with a clear message.

## Layout

Each subpackage is:

```
npm/ccfly-<os>-<arch>/
├─ package.json     # name, version (lockstep with ccfly), os, cpu, files:["bin"]
└─ bin/
   ├─ .gitkeep      # placeholder; tracked
   └─ ccfly         # the compiled binary — NOT checked in; filled at build time
```

The binaries themselves are gitignored (see root `.gitignore`); only the
`package.json` and `bin/.gitkeep` are tracked.

## Filled by the build script / CI

Run from the repo root:

```sh
pnpm build:binaries            # all 4 targets
# or just one (e.g. on your dev machine):
TARGETS="darwin/arm64" pnpm build:binaries
```

`scripts/build-binaries.sh` cross-compiles `../go/cmd/ccfly` for each target
(`CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X main.version=<v>"`), writes
the binary to `bin/ccfly`, and syncs each subpackage's `version` to the CLI
version so they stay in lockstep.

CI does the same in `.github/workflows/release.yml`, then changesets publishes
`ccfly`, all four subpackages, and `@ccfly/react` together.

## Versioning

`ccfly` and the four subpackages are a **fixed** changesets group
(`.changeset/config.json`): one bump moves them all. The main package pins each
subpackage as an **exact** same-version `optionalDependency`; changesets
rewrites those exact versions on bump. At runtime `ccfly/bin/ccfly.js` resolves
`ccfly-<os>-<arch>/bin/ccfly` and execs it.
