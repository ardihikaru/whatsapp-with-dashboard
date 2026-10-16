# wsm-mcp tutorial (for another AI)

This is written for an AI agent connecting to wsm-mcp for the first time.
Read `mcp/README.md` first for the architecture; this file is worked
examples.

## Connecting

wsm-mcp is reachable **only over WireGuard**, at:

- MCP Streamable HTTP: `http://10.10.0.5:18812/mcp`
- gRPC: `10.10.0.5:18811`

Every call needs HTTP Basic credentials. The operator keeps them in
`~/.config/wsm-mcp/client.json` on the laptop — **never** in this repo. Ask
the operator for the username/password (or the pre-built
`Authorization: Basic ...` header) rather than looking for them in source.

### Add to Claude Code

```
claude mcp add --transport http wsm http://10.10.0.5:18812/mcp \
  --header "Authorization: Basic <base64 of user:pass>"
```

### Raw curl (JSON-RPC over Streamable HTTP)

```bash
AUTH="Basic $(printf '%s' 'wsm-mcp:REPLACE_ME' | base64)"

# 1. initialize
curl -s http://10.10.0.5:18812/mcp -H "Authorization: $AUTH" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'

# 2. tools/list
curl -s http://10.10.0.5:18812/mcp -H "Authorization: $AUTH" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 3. tools/call
curl -s http://10.10.0.5:18812/mcp -H "Authorization: $AUTH" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wsm_hosts_status","arguments":{}}}'
```

### grpcurl

wsm-mcp registers gRPC **reflection**, so grpcurl needs no `.proto` file:

```bash
grpcurl -plaintext -H "authorization: Basic $(printf '%s' 'wsm-mcp:REPLACE_ME' | base64)" \
  10.10.0.5:18811 list

grpcurl -plaintext -H "authorization: Basic $(printf '%s' 'wsm-mcp:REPLACE_ME' | base64)" \
  10.10.0.5:18811 wsmmcp.v1.WSMMCP/Health

grpcurl -plaintext -H "authorization: Basic $(printf '%s' 'wsm-mcp:REPLACE_ME' | base64)" \
  -d '{"name":"wsm_hosts_status","arguments_json":"{}"}' \
  10.10.0.5:18811 wsmmcp.v1.WSMMCP/CallTool
```

### Go client

```go
c, err := client.Dial("10.10.0.5:18811", "wsm-mcp", os.Getenv("WSM_MCP_PASS"))
if err != nil { /* ... */ }
defer c.Close()

var out map[string]any
err = c.Call(ctx, "wsm_resolve_session", map[string]any{"phone": "085132148259"}, &out)
```

## The host/failover model, in practice

Every tool result carries `{"host": "minipc"|"laptop", ...}`. If you need an
answer from a SPECIFIC host (e.g. you already know a session only exists on
the laptop), pass `"target":"laptop"` as an argument — this disables failover
for that one call and errors if the named host isn't configured.

## The phone-vs-session_id model

Ardi's own number (`6285132148259`) is the configured
`WSM_MCP_DEFAULT_PHONE`, so most session-scoped tools work with **no**
`session_id` or `phone` argument at all — "my number" is the default.
Otherwise:

```json
{"name": "wsm_resolve_session", "arguments": {"phone": "085132148259"}}
```

returns the session_id on **every** configured host — useful because the
SAME phone has a DIFFERENT session_id per host. Once you have one, you can
pass it directly as `session_id` to any session-scoped tool and it will be
looked up on whichever host actually owns it.

## Danger tier

Calling a Danger-gated tool (session delete, calls, raw waxum sends, webhook
register/delete, …) while `WSM_MCP_ALLOW_DANGEROUS` is unset returns a normal
MCP tool result with `isError:true` and a message naming the env var — it is
never a transport-level failure, so read the message and tell the operator
rather than retrying.

## Error semantics

- **MCP**: a malformed JSON-RPC envelope, an unknown method, or
  missing/unknown tool name is a JSON-RPC `error` (e.g. `-32601` method not
  found). A tool that ran and failed (bad arguments, upstream 4xx/5xx, danger
  gate) comes back as a successful JSON-RPC result with
  `result.isError:true` and `result.content[0].text` holding the reason —
  always check `isError`, don't assume a 200-shaped response means success.
- **gRPC**: an unknown tool name is `NotFound`; bad/missing Basic credentials
  are `Unauthenticated`. A tool that ran and failed is a normal
  `CallToolResponse` with `is_error:true` and `error` set, not a gRPC error.

## Worked recipes

**"Send a text to Ardi's number from my number"**
```json
{"name": "wsm_send_message_auto", "arguments": {"to": "6285132148259", "text": "hi"}}
```
(Not gated; goes through WSM's own routing/queue/log. Omit `to` only if you
really mean to send to the default phone, which would be sending to
yourself — don't.)

**"Is everything healthy?"**
```json
{"name": "wsm_hosts_status", "arguments": {}}
```
Reports WSM `/api/v1/health` and waxum `Health` on every configured host with
latency, so you can see at a glance which host (if any) is down before
routing anything through `target`.

**"Read inbound replies since this morning"**
```json
{"name": "wsm_inbound", "arguments": {"query": {"since": "2026-10-16T00:00:00Z"}}}
```

**"Check if these numbers are on WhatsApp"**
```json
{"name": "wsm_contacts_check_auto", "arguments": {"phones": ["6281234567890", "6289900000000"]}}
```

**"Get a pairing QR for a session"**
```json
{"name": "wsm_session_pairing", "arguments": {"session_id": "59c90033bb24b0badf408566b26fe835"}}
```
or, for the raw payload strings to render yourself:
```json
{"name": "waxum_qr", "arguments": {"session_id": "59c90033bb24b0badf408566b26fe835"}}
```
