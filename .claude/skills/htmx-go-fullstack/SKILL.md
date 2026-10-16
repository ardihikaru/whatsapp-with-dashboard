---
name: htmx-go-fullstack
description: How to build features in this Go + HTMX dashboard - adding a page, an HTMX partial, a route, a repository method, a migration, or a CSS rule, plus the house test patterns and the Playwright e2e loop. Use whenever writing or reviewing code in whatsapp-with-dashboard.
---

# Building in whatsapp-with-dashboard

Server-rendered Go, HTMX for interactivity, hand-written CSS. `CLAUDE.md` at the
repo root carries the two hard rules (a test that fails without the change;
evidence rather than opinion) and the list of hazards. This file is the how-to.

## Add a full page

Four edits. Miss the third and you get `unknown page` at runtime with a green
test suite.

1. **Template** — `web/templates/<name>.html`, defining `{{define "content"}}`.
   Every in-app URL goes through `{{u "/path"}}`; a literal `/static/...` breaks
   any deployment mounted under a sub-path.
2. **Handler** — `internal/handler/<name>.go`. Build page data by embedding
   `PageBase` and calling `h.newPageBase(r, title, currentPath)`; `currentPath`
   is what highlights the active nav item.
3. **Renderer registration** — add an entry to `pageDefs` in
   `internal/renderer/renderer.go`. Use `append(chrome(), "templates/<name>.html")`
   for a normal page; `chrome()` is the shared base/sidebar/header/theme set.
   Root is `"base"`.
4. **Route** — `internal/handler/routes_ui.go`, wrapped in `page(...)` unless it
   is genuinely public.

Add the nav entry in `internal/handler/nav.go` if it belongs in the sidebar.

## Add an HTMX partial

For anything that swaps a fragment rather than a page.

- Template in `web/templates/partials/`, `{{define "thing_rows"}}`.
- Register the file in `partialFiles` in `internal/renderer/renderer.go`.
- Handler calls `h.Renderer.RenderPartial(w, "thing_rows", data)`.
- Client side: `hx-get`/`hx-post` + `hx-target` + `hx-swap`. Polling is
  `hx-trigger="every 5s"`; see `partials/session_rows.html`.
- Server-side HTMX signals in use: read `HX-Request`, and reply with
  `HX-Redirect` or `HX-Refresh` (see `internal/handler/theme.go`).
- **htmx only swaps 2xx.** `PostLogin` deliberately returns 200 on a failed
  login so the error fragment renders. Returning 401 there would show nothing.

## Add a repository method

Raw `database/sql`, `?` placeholders, context on every call. Repos take
`*sql.DB` and inject their clock as `now func() time.Time` so tests are
deterministic. Timestamps are RFC3339 UTC strings in `VARCHAR(40)`, booleans are
0/1 integers — not native DATETIME or BOOL.

## Add a migration

**Both dialects, same commit:**

- `migrations/000N_name.sql` — goose, MySQL, owns production.
- `internal/db/db.go` — add to `schema` for a new table, or call `ensureColumn`
  for a new column, so the SQLite path used by every test and by `make e2e`
  matches.

Nothing enforces the pairing. A migration in only the first makes the whole test
suite pass against a schema production does not have.

## CSS

Tokens only: every colour is a `:root` variable with a `[data-theme="dark"]`
counterpart. A raw hex inside a rule silently breaks dark mode.

Load order is load-bearing and documented at the top of `base.html`: the first
eight files concatenate to the historical `minimal.css`, and `charts.css`
intentionally carries `@media` rules that override `layout.css`. Prefer adding
rules to `responsive.css` (loaded ninth, last) over editing the eight.

`base.html` and `login.html` each own a complete `<head>`. Every stylesheet or
script goes in **both**.

## Add or change a /api/v1 route or a waxum.Client method

**wsm-mcp must track every change here.** A separate process (`mcp/README.md`,
`internal/mcp/registry`) wraps every `/api/v1` route in `routes_api.go` and
every exported `internal/waxum.Client` method as an MCP/gRPC tool for other AI
agents. `internal/mcp/registry/sync_test.go`'s `TestEveryAPIRouteHasAnMCPTool`
and `TestEveryWaxumClientMethodHasAnMCPTool` parse both sources with `go/ast`
and fail the build if a route or method has no matching tool — so:

- Adding/changing a route in `routes_api.go`, or an exported method on
  `internal/waxum.Client`, needs a matching tool change in
  `internal/mcp/registry` (a `t_wsm_*.go`/`t_waxum_*.go` file, or an entry in
  one of the declarative tables there) in the SAME commit.
- Regenerate `mcp/tools.md`: `go run ./cmd/wsm-mcp tools > mcp/tools.md` (or
  `make mcp-tools`) — `TestToolsMdIsCurrent` fails if it's stale.
- Bump `mcp/VERSION`.
- See CLAUDE.md's "wsm-mcp must track every WSM and waxum change" section for
  the danger-gate list and the redeploy step (`deploy/clinodb/update.sh`).

## Tests

House style, visible in all 30+ existing `_test.go` files:

- **In-package** (`package handler`, not `handler_test`) so helpers can be
  unexported. `internal/renderer` is the exception and uses `renderer_test`.
- **Names are claims**: `TestDateExpiryStampsTheCutoffNotATTL`,
  `TestLoginRefusedAfterTheCutoff`. Reading the name should tell you what broke.
- **Drive the handler directly** with `httptest.NewRequest`/`NewRecorder`. There
  is no `httptest.NewServer` anywhere.
- **Real dependencies, temp SQLite**: `db.Open(filepath.Join(t.TempDir(), …),
  db.SQLiteDialect)` → `db.EnsureSchema` → real repositories → real renderer over
  the embedded FS → `handler.New`. Helpers: `newLogsTestServer(t)`,
  `authedGet(t, srv, sessions, path)`, `loginMux(t, cookieSecure)`.
- **Set `req.RemoteAddr`** in anything touching login or the API, or the per-IP
  throttle keys unpredictably.
- **`bcrypt.MinCost`** in tests. Default cost makes a test suite crawl.

Run: `go test ./...` and `go vet ./...` (or `make test` / `make vet`).

## Browser e2e

See `references/e2e-playwright.md`. In short: `make e2e-install` once, then
`make e2e`. The suite boots its own app on 127.0.0.1:8801 with the app tables in
a throwaway SQLite file, signs in as the admin from `_data/paswd`, and runs at
390×844, 360×740 and 1280×800.

Record browser or manual verification in `docs/e2e-evidence.md`, following the
existing entries: a dated heading, the base URL, a `| Check | Result |` table,
notes, and the standing line confirming no secret was written into the file.
