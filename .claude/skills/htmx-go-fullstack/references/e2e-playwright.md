# Playwright e2e: login, run, evidence

## What the harness is

`playwright.config.ts` defines four projects:

| Project | Viewport | Purpose |
|---|---|---|
| `setup` | — | Signs in once, saves `e2e/.auth/admin.json` |
| `mobile-portrait` | 390×844, touch | iPhone-class phone portrait |
| `mobile-small` | 360×740, touch | Small-Android floor, where layouts break first |
| `desktop` | 1280×800 | Regression guard: the drawer must not leak here |

`webServer` runs `scripts/e2e-env.sh`, which:

- reads `_data/paswd` and pipes it into `go run ./cmd/hashpw` (stdin, never
  argv) to produce `WSM_ADMIN_PASSWORD_HASH`;
- sets `WSM_APP_DSN=sqlite:$state/app.db` so **no MySQL is contacted** — the
  only reachable MySQL is the shared production server;
- listens on **8801**, not 8800, so a running dev instance is untouched;
- is readiness-probed on `GET /api/v1/health`, which is public by design.

## Secret handling — not optional

`_data/paswd` is a real plaintext password.

- Only `e2e/auth.setup.ts` ever types it, and the `setup` project forces
  `trace`, `screenshot` and `video` to `off` so no DOM snapshot can capture it.
- Strip the trailing newline (`.replace(/[\r\n]+$/, "")`). The server hashed the
  file's bytes without it, so leaving it in fails every login.
- Never put it in a spec fixture, a report, a commit, or `docs/e2e-evidence.md`.

## Running

```
make e2e-install     # once: npm install + npx playwright install chromium
make e2e             # the suite
make e2e-headed      # watch it
make e2e-report      # last HTML report
make e2e-serve       # just the app, port 8801, SQLite, no tests
```

### If the browser will not start

`error while loading shared libraries: libatk-1.0.so.0` means Chromium's system
dependencies are missing. The correct fix needs root:

```
npx playwright install-deps chromium
```

Where root is unavailable or the dpkg lock is held, unpack the same packages
into a private prefix — no system change, and `scripts/e2e-run.sh` picks it up
automatically:

```
mkdir -p ~/.cache/ms-playwright-libs && cd "$(mktemp -d)"
apt-get download $(npx playwright install-deps --dry-run chromium | tail -n +2)
for d in *.deb; do dpkg -x "$d" ~/.cache/ms-playwright-libs; done
```

## Writing specs against this app

- **Wait for CSS transitions.** The drawer slides over 180ms. `toBeInViewport()`
  is satisfied by a one-pixel sliver and returns mid-flight; `boundingBox()`
  does not retry at all. Poll for the final position:
  `await expect.poll(async () => (await sidebar.boundingBox())?.x).toBe(0)`.
- **`position: fixed; inset: 0` is always "in the viewport".** `.nav-backdrop`
  is never off-screen; what changes is `pointer-events`. Assert
  `toHaveCSS("pointer-events", "none"|"auto")`.
- **Reuse the stored session.** The login throttle is 5 attempts then one per
  12 s per IP (`auth.NewThrottle(1.0/12.0, 5)`). A suite that signs in per test
  throttles itself into failures that look like app bugs.
- **`waitForLoadState("networkidle")` before measuring layout** on `/dashboard`:
  ECharts hydrates after load and resizes itself, so an earlier measurement is a
  lie.
- **Skip by viewport, do not duplicate specs.**
  `test.skip(({ page }) => (page.viewportSize()?.width ?? 0) > 900, …)` keeps one
  spec file honest across all three projects.

## Evidence

Append to `docs/e2e-evidence.md` in the existing style: a dated heading, the base
URL, a `| Check | Result |` table, a notes list, and the standing confirmation
that no secret was written into the file. Screenshots go in
`docs/e2e-evidence/`; `e2e/screenshots.spec.ts` regenerates them.
