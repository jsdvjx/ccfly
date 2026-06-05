# Changesets

This folder is managed by [changesets](https://github.com/changesets/changesets).

Run `pnpm changeset` to add a changeset describing your change. On release,
`pnpm run version` consumes the changesets to bump versions and write changelogs,
and `pnpm release` publishes to npm.

(Use `pnpm run version`, not bare `pnpm version` — the latter invokes pnpm's
built-in version bumper instead of the `version` script.)
