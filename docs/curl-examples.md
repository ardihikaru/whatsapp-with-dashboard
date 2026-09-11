# Curl Examples

Set the base URL:

```sh
BASE=http://localhost:8800
```

Log in with an API client:

```sh
TOKEN=$(curl -sS -X POST "$BASE/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"login_id":"taskmgr","secret":"replace-with-secret"}' \
  | jq -r .token)
```

List sessions:

```sh
curl -sS "$BASE/api/v1/sessions" \
  -H "Authorization: Bearer $TOKEN"
```

Send with automatic routing:

```sh
curl -sS -X POST "$BASE/api/v1/messages" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"New task mention\n\nhttps://example.test/task/123"}'
```

## Routing scopes

Automatic routing picks among sessions with **no group code**, skipping any
flagged master. Three optional fields change that — see
`docs/load-balancing.md` for the full candidate table.

Pin a send to the master, with no fallback (ops alerting, where "which number
paged me at 3am" is part of reading the page):

```sh
curl -sS -X POST "$BASE/api/v1/messages" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"ALERT: disk 90%","master_only":true}'
```

Route inside one cluster — only sessions carrying that `group_code` are
candidates, and a session carrying one is invisible to every request that does
not name it:

```sh
curl -sS -X POST "$BASE/api/v1/messages" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"Order ready","group_code":"kemang"}'
```

That cluster's own master, which is what `group_code` + `master_only` means:

```sh
curl -sS -X POST "$BASE/api/v1/messages" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"ALERT","group_code":"kemang","master_only":true}'
```

On `/api/v1/messages/media` the field is a **multipart form field**, not a JSON
key — that endpoint is multipart end to end and has no JSON body to add one to:

```sh
curl -sS -X POST "$BASE/api/v1/messages/media" \
  -H "Authorization: Bearer $TOKEN" \
  -F file=@/path/to/photo.jpg -F kind=image -F to=+6281200000000 \
  -F group_code=kemang
```

Every successful send reports the decision it made, so a caller can prove which
number sent without re-deriving it from its own request:

```json
{"id":"...","status":"queued","session_id":"...","session_name":"Kemang Gateway",
 "session_group_code":"kemang","session_is_master":true,"routing":"group_master_only"}
```

`session_group_code` is always present and is `""` when the chosen session is
ungrouped. When no candidate exists the answer is 409, and the body names the
real cause — a scoped send failing while other groups are healthy says so
explicitly rather than reading as a gateway outage:

```
no connected session for group_code "kemang" (the gateway is not down — other
sessions are connected outside this group_code; omit group_code to route
through any connected session)
```

Assign or clear a session's group code:

```sh
curl -sS -X PUT "$BASE/api/v1/sessions/$SID/group-code" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"group_code":"kemang"}'          # "" clears it, back to the default pool
```

A code is trimmed and lower-cased, may contain only `a-z`, `0-9`, `-` and `_`,
and is capped at 32 characters; anything else is a 400 rather than a silently
different cluster. Note this route is **not published at the edge** — it is
reachable on the private network only.

Send through an explicit session:

```sh
curl -sS -X POST "$BASE/api/v1/sessions/$SID/messages" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"+6281200000000","text":"Explicit route test"}'
```

Missing or invalid token check:

```sh
curl -i "$BASE/api/v1/sessions"
curl -i "$BASE/api/v1/sessions" -H "Authorization: Bearer invalid"
```

Expired token check:

```sh
# Create an API client with a short custom duration, wait until it expires,
# then call any protected API.
curl -i "$BASE/api/v1/sessions" -H "Authorization: Bearer $TOKEN"
```

Revoked token check:

```sh
curl -i -X POST "$BASE/api/v1/auth/logout" \
  -H "Authorization: Bearer $TOKEN"

curl -i "$BASE/api/v1/sessions" \
  -H "Authorization: Bearer $TOKEN"
```

## Media, session operations, webhooks (Phase 9)

These three answer 501 unless `WSM_WA_BACKEND=waxum` (the default in
`docker-compose.yml`; `make e2e` pins `whatsmeow` instead, so they 501 there).
`$TOKEN` and `$SID` are placeholders — never paste a real token into an
example.

Send a media message, auto-routed to a connected session:

```sh
curl -sS -X POST "$BASE/api/v1/messages/media" \
  -H "Authorization: Bearer $TOKEN" \
  -F file=@/path/to/photo.jpg \
  -F kind=image \
  -F to=+6281200000000 \
  -F caption="From the dashboard"
```

Pause a session's send/receive (a session operation — auto-reconnect,
history-sync and appstate resync follow the same
`/api/v1/sessions/{id}/...` shape):

```sh
curl -sS -X POST "$BASE/api/v1/sessions/$SID/pause" \
  -H "Authorization: Bearer $TOKEN"
```

Register a webhook:

```sh
curl -sS -X POST "$BASE/api/v1/sessions/$SID/webhooks" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.test/hook","events":["message","connected","disconnected"],"secret":"replace-with-a-real-secret"}'
```

waxum's SSRF guard refuses any callback URL that resolves to loopback,
RFC1918, ULA, CGNAT or link-local — a `url` pointing at a private address
(e.g. an operator's own receiver sitting on the same private network) is
rejected, not silently accepted. Use a publicly-resolvable callback URL, or
consume events over the SSE-backed pump this app already runs instead of a
webhook.

Check whether numbers are registered on WhatsApp, auto-routed to a
connected session:

```sh
curl -sS -X POST "$BASE/api/v1/contacts/check" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"phones":["+6281200000000","6289900000000"]}'
```

Same, against an explicit session:

```sh
curl -sS -X POST "$BASE/api/v1/sessions/$SID/contacts/check" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"phones":["+6281200000000","6289900000000"]}'
```

Each entry in the response's `results` gets its own `status`
(`registered`/`not_registered`/`invalid`/`unknown`) in the same order as the
`phones` sent — `unknown` means waxum simply did not answer about that
number, which is a different thing from `not_registered` and is reported as
such rather than guessed.
