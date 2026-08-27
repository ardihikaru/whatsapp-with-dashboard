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
