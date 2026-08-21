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
