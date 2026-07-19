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

## Verify the SNI egress environment

When the cloud has assigned this device an SNI exit, the local control API can
run a fresh production-path check:

```sh
curl -sS 'http://127.0.0.1:7699/sni-status?probe=1'
```

`probe.path_ok` is true only when the local interception is armed, the in-band
nonce response identifies the configured overlay node and account exit, and a
separate real upstream TLS handshake succeeds. `target_node`,
`target_exit_id`, `target_identity`, and `bound_egress_ipv4` explain the
observed selection when diagnosing a mismatch.

## Mesh endpoint failover

`CCFLY_MESH_ENDPOINTS` optionally supplies an ordered JSON list of mesh
endpoints. `url` remains the WebSocket Host and TLS certificate identity;
`dial_addr` optionally selects a different TCP destination. The cloud-provided
`mesh_url` is always appended as the final compatibility fallback.

```sh
CCFLY_MESH_ENDPOINTS='[{"url":"wss://114.132.213.6/mesh","dial_addr":"138.252.163.7:443"}]'
```

Each endpoint is attempted in order. A failed or disconnected preferred route
falls through to the next entry without changing the device identity or mesh
token. A malformed override is ignored so the legacy route remains available.

## Distribution

CI cross-compiles this into per-platform binaries and packs each into an
`@ccfly/cli-<os>-<cpu>` npm subpackage under `../npm/`. The `ccfly` npm package
pulls in the matching one via optionalDependencies. See `../npm/README.md`.

> Module path: `github.com/jsdvjx/ccfly/go` (matches the GitHub remote
> `github.com/jsdvjx/ccfly`).
