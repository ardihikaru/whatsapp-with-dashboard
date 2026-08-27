import { test, expect } from "@playwright/test";

// Phase 9 (media send, calls, session operations, webhooks) — WHAT THIS FILE
// CAN AND CANNOT PROVE.
//
// scripts/e2e-env.sh pins WSM_WA_BACKEND=whatsmeow, so every one of these
// surfaces answers wa.ErrUnsupported (session ops, webhooks) or is disarmed
// by its own default (WSM_CALLS_ENABLED=false) here — this suite exercises
// the DEGRADED path, never the real feature. That much was expected going
// in.
//
// It goes narrower than that for /send specifically, and this was NOT
// assumed going in — it was checked against a live boot of this exact
// harness before writing the assertions below:
//
//   $ curl .../login ... && curl .../send
//   -> "No connected sessions." (the send.html {{else}} branch)
//
// and the boot log for the same run:
//
//   WARN boot reconnect failed session=e2e-sess-0001 \
//     err="whatsmeow device missing for paired session (re-pair required)"
//
// cmd/e2eseed's sessions carry a "connected" LastStatus in the DB, but the
// app reconciles LIVE state at boot by asking the backend, and the
// whatsmeow backend here has no paired device store (this suite never pairs
// one) — so every seeded session reconciles to StateLoggedOut regardless of
// what the DB says. wa.ManagedSession.Connected is therefore never true in
// this harness, and send.html gates its ENTIRE tabbed form — text tab AND
// the media tab this file was asked to cover — behind {{if .Connected}}.
// There is no way to reach the media tab, exercise the file input, or
// submit a send from this suite without pairing a real device, which is
// exactly what the hermetic e2e harness is built to avoid needing.
//
// What IS provable, and is everything asserted below: every one of these
// pages answers 200 (not a 500 or a blank screen) and shows the correct
// degraded copy for a backend/config that does not support the feature.
// A green run here is evidence of that, and NOTHING about whether media
// send, calls, session ops or webhooks actually work end to end — that
// requires a live waxum instance, which is out of reach from this
// environment (see the phase report).

const SESSION_ID = "e2e-sess-0001"; // cmd/e2eseed's first fixed session id

test("/send has no connected session in this harness, and says so plainly", async ({ page }) => {
  const res = await page.goto("/send");
  expect(res?.status()).toBe(200);
  await expect(page.getByText("No connected sessions.")).toBeVisible();
  // The media tab itself cannot render without a connected session — see
  // this file's header comment. Pinning its absence here, rather than
  // silently not testing it, is the honest version of "cannot cover this".
  await expect(page.locator("#send-tab-media")).toHaveCount(0);
  await expect(page.locator("#media-file-input")).toHaveCount(0);
});

test("/calls is disabled by default and says so plainly", async ({ page }) => {
  const res = await page.goto("/calls");
  expect(res?.status()).toBe(200);
  await expect(
    page.getByText("Voice calling is disabled on this deployment."),
  ).toBeVisible();
});

test("/sessions/{id}/ops explains that this backend has no session operations", async ({ page }) => {
  const res = await page.goto(`/sessions/${SESSION_ID}/ops`);
  expect(res?.status()).toBe(200);
  // The unsupported message is on the panel's own hx-get target, filled
  // after "load" — see ops_ui.go's sessionOpsPageData doc comment.
  const panel = page.locator("#ops-panel");
  await expect(panel).not.toContainText("Loading", { timeout: 10_000 });
  await expect(panel).toContainText("not supported by this backend");
});

test("/sessions/{id}/webhooks explains that this backend has no webhook management", async ({ page }) => {
  const res = await page.goto(`/sessions/${SESSION_ID}/webhooks`);
  expect(res?.status()).toBe(200);
  // Unlike the ops panel above, GetSessionWebhooks resolves Unsupported
  // synchronously in the page handler itself (webhooks_ui.go), so this is
  // on the initial response, not behind a poll.
  await expect(page.getByText("not supported by this backend")).toBeVisible();
});
