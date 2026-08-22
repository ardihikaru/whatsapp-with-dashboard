import { test, expect, Page } from "@playwright/test";

// Not assertions — this file exists to produce the images that accompany the
// evidence entry in docs/e2e-evidence.md.
//
// OPT-IN. It writes binaries into a tracked directory, so a plain `make e2e`
// must not churn them. Run it deliberately:
//   make e2e-evidence          (or: WSM_E2E_EVIDENCE=1 ./scripts/e2e-run.sh)
const isPhone = (page: Page) => (page.viewportSize()?.width ?? 0) <= 900;

test("capture the nav evidence", async ({ page }, testInfo) => {
  test.skip(
    !process.env.WSM_E2E_EVIDENCE,
    "evidence capture is opt-in; set WSM_E2E_EVIDENCE=1",
  );
  const tag = testInfo.project.name;
  await page.goto("/dashboard");
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: `docs/e2e-evidence/${tag}-dashboard-closed.png`, fullPage: false });

  if (isPhone(page)) {
    await page.getByRole("button", { name: "Buka menu" }).tap();
    await expect.poll(async () => (await page.locator("#app-sidebar").boundingBox())?.x).toBe(0);
    await page.screenshot({ path: `docs/e2e-evidence/${tag}-drawer-open.png`, fullPage: false });
  }

  // fullPage, because the riskiest region on this page — the nine-column
  // message table inside its scroller — sits well below the fold. A
  // viewport-only shot would leave the thing most likely to break out of the
  // evidence entirely.
  await page.goto("/logs");
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: `docs/e2e-evidence/${tag}-logs.png`, fullPage: true });

  // And the table on its own, scrolled to its start.
  const table = page.locator(".table-scroll:has(.table-wide)");
  await table.scrollIntoViewIfNeeded();
  await table.screenshot({ path: `docs/e2e-evidence/${tag}-logs-table.png` });
});
