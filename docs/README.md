# WhatsApp with Dashboard — Docs

A local Go + HTMX app that manages WhatsApp sessions and exposes a JSON
API for machine callers.

- [Auth and API clients](auth-and-api-clients.md)
- [Load-balanced sends](load-balancing.md)
- [Curl examples](curl-examples.md)
- [E2E evidence](e2e-evidence.md)

Browser admin pages use the local admin login cookie. JSON REST endpoints under
`/api/v1/*` use API-client bearer tokens only, except public
`GET /api/v1/health` and `POST /api/v1/auth/login`.
