# wsm-mcp on clinodb

wsm-mcp runs as a systemd **user** unit on clinodb (sudoless), from this
repo at `/home/clinoid/devel/projects/whatsapp-with-dashboard`. It is a
separate process from the main WSM app: same module and image family
(`Dockerfile.mcp`, not `Dockerfile`), different binary, different ports,
deployed independently.

## Binds

- MCP Streamable HTTP: `WSM_MCP_HTTP_ADDR=10.10.0.5:18812`
- gRPC: `WSM_MCP_GRPC_ADDR=10.10.0.5:18811`

Both default to loopback in code (`127.0.0.1:...`) — `~/.config/wsm-mcp/mcp.env`
on clinodb is what raises them to the WireGuard address. `--network host` in
the unit means the container binds that address directly; there is no port
publish/remap to get wrong. Nothing here is reachable off the WireGuard
mesh — there is no public vhost for wsm-mcp, unlike WSM's own edge
allowlist for `wa.mfardiansyah.id`.

## First install

```
ssh clinodb
cd ~/devel/projects/whatsapp-with-dashboard
# write ~/.config/wsm-mcp/mcp.env (0600) BEFORE first install — see
# mcp/mcp.env.example for every variable. Never commit real values.
bash deploy/clinodb/install.sh
```

`install.sh` builds `wsm-mcp:<VERSION>` (from `mcp/VERSION`) and
`wsm-mcp:current`, keeps the previous image as `wsm-mcp:prev`, installs/enables
the systemd user unit, restarts it, and polls `/healthz` for up to 20s.

## Update (after a merge to main)

```
ssh clinodb
bash ~/devel/projects/whatsapp-with-dashboard/deploy/clinodb/update.sh
```

## Rollback

```
ssh clinodb
bash ~/devel/projects/whatsapp-with-dashboard/deploy/clinodb/rollback.sh
```

Runs `wsm-mcp:prev` again. Only one previous image is kept — a second
rollback in a row has nothing further back to go to.

## The sync rule

Any change to `internal/handler/routes_api.go` or an exported method on
`internal/waxum.Client` MUST land with a matching tool change in
`internal/mcp/registry`, a regenerated `mcp/tools.md`
(`go run ./cmd/wsm-mcp tools > mcp/tools.md`), a bumped `mcp/VERSION`, and a
redeploy here via `update.sh`. See CLAUDE.md's "wsm-mcp must track every WSM
and waxum change" section — `internal/mcp/registry/sync_test.go` fails the
build if a route or method goes unwrapped.
