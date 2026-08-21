# E2E Evidence

## 2026-07-06 API-client smoke

Base URL: `http://localhost:8800`

API client login id: `taskmgr`

Secret handling: the one-time API-client secret was used only for the login
request and was not written into this evidence file.

Results:

| Check | Result |
|---|---|
| `GET /api/v1/health` | `200`, body reported `status=ok`, `sessions=1`, `connected=1` |
| `POST /api/v1/auth/login` | `200`, returned bearer token length `43`, `expires_at=null` |
| `GET /api/v1/sessions` with bearer token | `200`, returned `1` session, `1` connected |
| `GET /api/v1/sessions` without token | `401` |
| `GET /api/v1/sessions` with invalid token | `401` |
| `POST /api/v1/messages` | Skipped real delivery: a connected session exists, but `WSM_TEST_TO` was not set |
| `POST /api/v1/auth/logout` | `204` |
| `GET /api/v1/sessions` with revoked token | `401` |

Notes:

- The API-client no-expiry setting was verified through `expires_at=null`.
- Revocation was verified by logging out and retrying a protected endpoint with
  the same bearer token.
- Real WhatsApp delivery was intentionally not attempted without an explicit
  test destination number in `WSM_TEST_TO`.

## 2026-07-06 API-client real-send smoke

Base URL: `http://localhost:8800`

API client login id: `taskmgr`

Destination: `WSM_TEST_TO=+62 851-9067-9041`

Secret handling: the API-client secret was used only for the login request and
was not written into this evidence file.

Message text prefix: `WSM API-client real-send E2E`

Results:

| Check | Result |
|---|---|
| `GET /api/v1/health` | `200`, body reported `status=ok`, `sessions=1`, `connected=1` |
| `POST /api/v1/auth/login` | `200`, returned bearer token length `43`, `expires_at=null` |
| `GET /api/v1/sessions` with bearer token | `200`, returned `1` connected session |
| `POST /api/v1/messages` automatic routing | `200`, `status=sent` |
| Selected session | `fd559ba8f0278ec233ced9d2b4b24b75`, `nomer HP ardi` |
| Message id | `47cde72ee689b6ba1abf71e410b284df` |
| WhatsApp message id | `3EB0E05D0CD94B0F440594` |
| `GET /api/v1/sessions/{id}/messages?limit=5` | `200`, matched message row with `status=sent`, `attempts=1` |
| `POST /api/v1/auth/logout` | `204` |

Notes:

- This run exercised the automatic load-balanced send endpoint against a real
  connected WhatsApp session.
- The persisted audit row matched the API response message id and WhatsApp
  message id.
