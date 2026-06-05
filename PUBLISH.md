# Publishing ccfly

This is the manual release runbook for **ccfly v0.1.0**. Everything below requires
authentication (npm login / GitHub) and network access, so it is **not** automated
here — run it yourself when you are ready to publish.

> **Placeholders to fill first**
>
> - `jsdvjx` — the GitHub account or org that will host the repo. It appears in every
>   `package.json` (`repository` / `homepage`) and in `README.md`. Replace it before
>   (or right after) publishing:
>   ```sh
>   # from the repo root — preview, then apply
>   grep -rlI 'jsdvjx/ccfly' . --exclude-dir=node_modules --exclude-dir=.git
>   grep -rlI 'jsdvjx/ccfly' . --exclude-dir=node_modules --exclude-dir=.git \
>     | xargs sed -i '' 's#jsdvjx/ccfly#<your-owner>/ccfly#g'   # macOS sed
>   ```
> - `@ccfly` npm org — the scope for `@ccfly/react`. The unscoped CLI package name
>   is just `ccfly`. If `ccfly` (unscoped) is already taken on npm, you must either
>   claim a different CLI name or publish the CLI as `@ccfly/cli` (and update
>   `packages/cli/package.json` `name` + `bin/ccfly.js` references accordingly).

---

## Automated release (changesets) — for every version after 0.1.0

Once the repo and npm org exist, releases are driven by [changesets] and the root
`package.json` scripts. The manual runbook below remains the reference for the very
first publish and for understanding what the scripts do under the hood.

```sh
# 1. While working: record what changed (pick the bump per package).
pnpm changeset                 # writes a .changeset/*.md file

# 2. Cut the release: consume changesets → bump versions, write CHANGELOGs,
#    update the CLI's pinned optionalDependencies, and refresh the lockfile.
#    NOTE: use `pnpm run version` — bare `pnpm version` hits pnpm's built-in
#    version bumper instead of this script.
pnpm run version               # = changeset version && pnpm install --lockfile-only
#    Review/commit the resulting version + CHANGELOG diff.

# 3. Build everything and publish (needs npm auth — NPM_TOKEN in CI, or `npm login`).
pnpm release
```

`pnpm release` runs, in order:

1. `pnpm build` — builds `@ccfly/react` `dist/`.
2. `CLEAN=1 pnpm build:binaries` — embeds the web UI and cross-compiles all four
   `npm/ccfly-<os>-<arch>/bin/ccfly`, syncing their versions to the CLI.
3. `pnpm publish:binaries` — publishes the four platform packages **first**
   (`pnpm -r --filter "./npm/*" publish`). This is a deliberate first phase:
   `changeset publish` publishes every package in parallel (`Promise.all`) with no
   topological ordering, and the CLI's `optionalDependencies` pin these binaries by
   exact version — so they must reach the registry before the CLI does.
4. `changeset publish` — publishes the `ccfly` CLI and `@ccfly/react`, creates git
   tags, and honors `access: public`.

Both publish phases **skip versions already on the registry**, so `pnpm release` is
safe to re-run after a partial failure. (Because the binaries are published in phase 3,
`changeset publish` skips them in phase 4 and therefore does not create their git tags;
the lockstep `ccfly@<version>` tag covers the set. Tag the binaries manually if you need
per-package tags.) After publishing, push the tags: `git push --follow-tags`.

Versions: the `ccfly` CLI and the four `ccfly-<os>-<arch>` packages move in lockstep
(`.changeset/config.json` `fixed`); `@ccfly/react` versions independently.

[changesets]: https://github.com/changesets/changesets

---

## Prerequisites

- Node ≥ 18, pnpm 9, Go ≥ 1.23 (built/tested with Go 1.26).
- An npm account with publish rights, and access to (or ownership of) the `@ccfly` org.
- The build artifacts present locally (regenerate any time — see step 0).

## 0. (Re)build everything

```sh
# from repo root
pnpm install
pnpm -r build                 # builds @ccfly/react dist/
CLEAN=1 bash scripts/build-binaries.sh   # cross-compiles all 4 npm/ccfly-<os>-<arch>/bin/ccfly
```

Verify the four binaries before publishing:

```sh
for p in darwin-arm64 darwin-x64 linux-arm64 linux-x64; do
  file "npm/ccfly-$p/bin/ccfly"
done
```

Optional sanity — confirm each tarball actually contains the binary / dist:

```sh
( cd packages/react       && npm pack --dry-run )   # expect dist/index.js, dist/style.css, dist/index.d.ts
( cd packages/cli         && npm pack --dry-run )   # expect bin/ccfly.js
for p in darwin-arm64 darwin-x64 linux-arm64 linux-x64; do
  ( cd "npm/ccfly-$p"     && npm pack --dry-run )   # expect bin/ccfly (~6–7 MB)
done
```

## 1. Log in to npm

```sh
npm login
npm whoami   # confirm you are the expected user
```

## 2. Create the `@ccfly` org

Either on the website — <https://www.npmjs.com/org/create> — or via CLI:

```sh
npm org create ccfly        # creates the @ccfly scope/org
```

(Only the scoped package `@ccfly/react` needs the org. The unscoped `ccfly` CLI does not.)

## 3. Publish — in this exact order

Platform binary subpackages **first** (the CLI's `optionalDependencies` point at
them at their pinned `0.1.0` version, so they must exist on the registry before
the CLI is installable):

```sh
npm publish ./npm/ccfly-darwin-arm64
npm publish ./npm/ccfly-darwin-x64
npm publish ./npm/ccfly-linux-arm64
npm publish ./npm/ccfly-linux-x64
```

Then the CLI (unscoped → public by default):

```sh
npm publish ./packages/cli
```

Then the React package (scoped → must be published public):

```sh
npm publish --access public ./packages/react
```

> `@ccfly/react` also sets `publishConfig.access = "public"`, so the `--access public`
> flag is belt-and-suspenders; keep it to be explicit.

### Smoke test after publishing

```sh
npx ccfly@0.1.0 version          # should print: ccfly 0.1.0
npx ccfly@0.1.0 serve --help     # should print the serve flags
```

## 4. Create the GitHub repo and push

`gh` is **not installed** on this machine. Pick one path.

**Path A — install gh and let it create + push:**

```sh
brew install gh
gh auth login
gh repo create jsdvjx/ccfly --public --source=. --push --description "npx control + terminal server for Claude Code sessions, plus @ccfly/react"
```

**Path B — create the repo on github.com, then push manually:**

1. Create an empty public repo at `https://github.com/jsdvjx/ccfly` (no README/license — this repo already has them).
2. ```sh
   git remote add origin https://github.com/jsdvjx/ccfly.git
   git push -u origin main
   ```

(After the repo exists, remember the `jsdvjx` placeholder replacement from the top of
this file if you have not done it yet, and commit/push that change.)

## 5. Tag the release (optional but recommended)

```sh
git tag v0.1.0
git push origin v0.1.0
```

---

## Notes

- **Versions are in lockstep.** The CLI (`ccfly`) and the four `ccfly-<os>-<arch>`
  packages must always share the same version (enforced by `.changeset/config.json`
  `fixed`). `scripts/build-binaries.sh` syncs the subpackage versions from
  `packages/cli/package.json` at build time.
- **`files` fields matter.** `.gitignore` deliberately ignores `bin/` and `dist/`,
  so each publishable package declares an explicit `files` allowlist
  (`["bin"]` for the platform packages, `["dist"]` for `@ccfly/react`) to make sure
  the binaries / build output are actually included in the tarball.
- This repo uses [changesets](https://github.com/changesets/changesets) for future
  version bumps; for the initial `0.1.0` the manual `npm publish` flow above is fine.
