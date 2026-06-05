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
