# ccfly Go control service

The local control service behind the `ccfly` CLI. It:

- tails the Claude Code jsonl transcripts under `~/.claude`,
- drives the session's `tmux` pane,
- serves a local HTTP/WS API consumed by `@ccfly/react`,
- serves its own **terminal WebSocket** at `GET /term?session=<tmux>[&cwd=&cmd=]`:
  a PTY running `tmux new-session -A -s <session>` speaking a ttyd-compatible
  frame protocol (handshake `{AuthToken,columns,rows}`, `'0'`=output/input,
  `'1'`=resize). The live terminal mirror needs **no external ttyd**.

## Build

```sh
go build -o ../bin/ccfly ./cmd/ccfly
```

or from the repo root:

```sh
pnpm build:go
```

## Distribution

CI cross-compiles this into per-platform binaries and packs each into an
`@ccfly/cli-<os>-<cpu>` npm subpackage under `../npm/`. The `ccfly` npm package
pulls in the matching one via optionalDependencies. See `../npm/README.md`.

> Module path uses a placeholder org: `github.com/ccfly/ccfly/go`. Update it to
> the real GitHub org/repo before publishing.
