# wsm-mcp

One process exposing every capability of WSM's `/api/v1`
(`internal/handler/routes_api.go`) and of waxum
(every exported method on `internal/waxum.Client`) as tools, over both
**MCP Streamable HTTP** and **gRPC**, sharing one tool registry
(`internal/mcp/registry`). Deployed on `clinodb`, reachable only over
WireGuard. See `mcp/TUTORIAL.md` for worked examples written for another AI
to follow, and `mcp/tools.md` for the full generated tool reference.

## Why one registry, two front doors

`internal/mcp/registry.Registry` holds every `Tool{Name, Desc, Route,
WaxumMethod, Params, Danger, ReadOnly, Run}`. `internal/mcp/mcpsrv` serves it
as standard MCP Streamable HTTP (stateless: one JSON-RPC request per POST,
spec 2025-06-18 / 2025-03-26). `internal/mcp/grpcsrv` serves the SAME
registry as one generic gRPC service (`Health`/`ListTools`/`CallTool`), not
one RPC per tool — `proto/wsmmcp/v1/mcp.proto`. Both doors authenticate every
call with HTTP Basic (`internal/mcp/basicauth`, constant-time compare,
refuses a password under 16 chars).

This is the same shape as `ardi-personal-workspace-ai/apps/hikaru/mcp` (the
reference implementation this was built from), reused here because it had
already proven out stateless Streamable HTTP + a generic gRPC service
sharing one registry.

## Architecture

```
MCP client (Claude Code, another agent)        gRPC client (a backend)
        │ POST /mcp (JSON-RPC)                          │ WSMMCP.CallTool
        ▼                                                ▼
   internal/mcp/mcpsrv                           internal/mcp/grpcsrv
        └──────────────────┬─────────────────────────────┘
                            ▼
                 internal/mcp/registry.Registry
                            │
                            ▼
                 internal/mcp/upstream.Pool  (failover)
                 ┌──────────────┴──────────────┐
                 ▼                              ▼
         host "minipc"                   host "laptop"
     WSM /api/v1 (Basic)              WSM /api/v1 (Basic)
     waxum (SUPERADMIN_TOKEN)         waxum (SUPERADMIN_TOKEN)
```

## Hosts and failover (`internal/mcp/upstream`)

Configured by `WSM_MCP_HOSTS`, a priority-ordered list:

```
WSM_MCP_HOSTS=minipc=http://10.10.0.4:18801|http://10.10.0.4:3451,laptop=http://10.10.0.3:18801|http://10.10.0.3:3451
```

Each entry is `name=wsmBaseURL|waxumBaseURL`. The first host listed is tried
first for every call.

- **WSM auth**: HTTP Basic with one dedicated `api_clients` row
  (`WSM_MCP_WSM_LOGIN_ID`/`WSM_MCP_WSM_SECRET`). `api_clients` rows are
  mirrored across both the mini-PC and laptop DBs, so one credential works on
  every host — bearer tokens are per-DB and would need a separate login on
  each, which Basic avoids.
- **waxum auth**: `WSM_MCP_WAXUM_TOKEN`, the shared `SUPERADMIN_TOKEN` both
  waxum instances carry.
- **Failover rule**:
  - a **read** (GET/HEAD) fails over to the next host on a dial error, a
    timeout, or a 5xx.
  - a **write** fails over **only on a dial error** (the request never left
    the client, so it cannot have been double-sent) — never on a timeout or a
    5xx, where the upstream may already have acted.
  - a **4xx never fails over**, on either kind of call (the same rule nginx's
    `proxy_next_upstream` applies).
- An optional **`target`** argument on every tool pins one host by name and
  disables failover for that call.
- Every tool result is `{"host": "minipc"|"laptop", ...}` — the caller
  always knows which host actually answered.

## session_id vs phone

The same phone number has a **different session_id on each host** (a WSM
session id *is* the waxum session id — `internal/wa/backend_waxum.go` passes
it straight to `waxum.CreateSession` — and each host paired its own device
under its own id). Every session-scoped tool therefore accepts `session_id`
**or** `phone`:

- `phone` (any of `62xxxx`, `0xxxx`, `+62 xxx-xxx-xxxx`) is normalised and
  resolved against `GET /api/v1/sessions` on whichever host answers first
  (cached ~30s), honouring `target` if given.
- `session_id` is looked up on every configured host until the one that
  actually lists it is found, and that host is pinned for the call — passing
  a mini-PC session_id never accidentally calls the laptop.
- Neither given: falls back to `WSM_MCP_DEFAULT_PHONE` (Ardi's own number by
  default in production).
- `wsm_resolve_session` reports the session_id on **every** host at once, for
  when you need to see the whole picture rather than resolve-and-call.

## Danger gate

Destructive/irreversible tools refuse with an `isError` result naming
`WSM_MCP_ALLOW_DANGEROUS` unless that env var is `true` (default `false`).
Gated: WSM session delete, every `calls/*` tool, `spam-report`, webhook
register/delete; `waxum_delete_session`, `waxum_disconnect`,
`waxum_create_session`, `waxum_send_text`, `waxum_send_media`,
`waxum_send_uploaded`, `waxum_upload_media`, call tools, webhook
register/unregister, `waxum_report_spam`. WSM's own sends (`wsm_send_*`,
`wsm_session_send_*`) are **not** gated — they go through WSM's own
auth/routing/queue/log — but are annotated `destructiveHint:true,
idempotentHint:false, readOnlyHint:false` so a calling model still treats
them with care. See `mcp/tools.md` for the exact annotation on every tool.

## Logging

Every tool call logs only the tool name, the host that answered, the
duration, and whether it succeeded — never arguments, phone numbers,
message text, tokens, or results.

## Body size

1 MiB per request by default, enough for every tool except a base64 media
argument; `WSM_MCP_MAX_BODY_BYTES` raises it (140 MiB default in
`mcp.env.example`, comfortably above `WSM_MEDIA_MAX_BYTES`).

## Keeping this in sync (read before changing routes_api.go or internal/waxum)

See CLAUDE.md's **"wsm-mcp must track every WSM and waxum change"** section.
Short version: `internal/mcp/registry/sync_test.go`'s
`TestEveryAPIRouteHasAnMCPTool` and `TestEveryWaxumClientMethodHasAnMCPTool`
fail the build the moment a route or an exported `waxum.Client` method has no
matching tool. Regenerate `mcp/tools.md`
(`go run ./cmd/wsm-mcp tools > mcp/tools.md`), bump `mcp/VERSION`, and
redeploy via `deploy/clinodb/update.sh`.

## Repo layout

| Path | What lives there |
|---|---|
| `cmd/wsm-mcp` | `serve` / `smoke` / `tools` / `version` subcommands |
| `internal/mcp/registry` | The shared tool registry + sync tests |
| `internal/mcp/upstream` | The failover pool, phone/session resolver |
| `internal/mcp/mcpsrv` | MCP Streamable HTTP front door |
| `internal/mcp/grpcsrv` | gRPC front door |
| `internal/mcp/basicauth` | Constant-time Basic auth checker |
| `proto/wsmmcp/v1` | The gRPC contract + generated stubs (`scripts/gen-proto.sh`) |
| `mcp/client` | Typed Go gRPC client |
| `mcp/tools.md` | Generated tool reference |
| `mcp/TUTORIAL.md` | Worked examples for another AI |
| `Dockerfile.mcp`, `deploy/clinodb` | Image + clinodb systemd deploy |
