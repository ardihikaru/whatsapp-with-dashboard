import { test, expect, Page } from "@playwright/test";

// Wait for HTMX-populated regions to hold real content.
//
// networkidle is NOT sufficient here. /sessions fills its tbody from
// hx-trigger="load, every 5s", and the placeholder row says "Loading…" until it
// does. Measuring during that window finds an EMPTY table, which of course does
// not overflow — a layout bug on this page was invisible to this file for
// exactly that reason. Wait for the placeholder to go.
async function waitForContent(page: Page) {
  await page.waitForLoadState("networkidle");
  const rows = page.locator("#session-rows");
  if (await rows.count()) {
    await expect(rows).not.toContainText("Loading", { timeout: 10_000 });
  }
}

// Every authenticated page. If any of them scrolls sideways on a phone, or
// cannot be scrolled DOWN to its own content, the layout is broken regardless
// of whether the menu works.
const PAGES = [
  "/dashboard",
  "/sessions",
  "/sessions/new",
  "/send",
  "/logs",
  "/admin/api-clients",
];

for (const path of PAGES) {
  // Measured PER ELEMENT, not via document.documentElement.scrollWidth.
  //
  // The document-level reading is not trustworthy: a page-level
  // `overflow-x: hidden` clamps it to the viewport, so the assertion passes
  // *because* something is masking the overflow. That exact rule once lived in
  // responsive.css and made this file green while the phone layout was
  // unusable. An element's own bounding box cannot be clamped that way.
  test(`${path} has no element wider than the viewport`, async ({ page }) => {
    await page.goto(path);
    // The dashboard hydrates ECharts after load and resizes itself; /sessions
    // fills its tbody over HTMX. Measuring earlier reads a layout that is not
    // the one the user sees.
    await waitForContent(page);

    // The viewport width is passed IN from Playwright's own configuration and
    // never read from window.innerWidth.
    //
    // On mobile emulation innerWidth INFLATES to match overflowing content: a
    // 360px phone rendering a 755px table reports innerWidth 756, so
    // `right > innerWidth` is false and the check can never fail. Measuring
    // against the fixed configured width is the only reading that stays honest.
    const viewportWidth = page.viewportSize()!.width;
    const offenders = await page.evaluate((vw) => {
      const out: string[] = [];
      document.querySelectorAll<HTMLElement>("body *").forEach((el) => {
        // Content inside a horizontal scroller is SUPPOSED to be wider than the
        // screen — that is what .table-scroll is for.
        if (el.closest(".table-scroll")) return;
        const b = el.getBoundingClientRect();
        if (b.width > 0 && b.right > vw + 1) {
          const cls = (el.className || "").toString().trim().slice(0, 40);
          out.push(`<${el.tagName.toLowerCase()} class="${cls}"> extends to ${Math.round(b.right)}px`);
        }
      });
      return out.slice(0, 8);
    }, viewportWidth);

    expect(
      offenders,
      `${path}: element(s) overflow a ${viewportWidth}px viewport:\n  ${offenders.join("\n  ")}`,
    ).toEqual([]);

    // Same trap, document level: body.scrollWidth is not clamped by a
    // page-level overflow-x rule the way documentElement.scrollWidth is, and it
    // is compared against the configured width rather than innerWidth.
    const bodyWidth = await page.evaluate(() => document.body.scrollWidth);
    expect(
      bodyWidth,
      `${path}: the document is ${bodyWidth}px wide in a ${viewportWidth}px viewport`,
    ).toBeLessThanOrEqual(viewportWidth + 1);
  });

  // The counterpart bug, and the nastier one: a page that does not scroll at
  // all still reports zero horizontal overflow. Prove the content is reachable.
  test(`${path} can be scrolled to its bottom`, async ({ page }) => {
    await page.goto(path);
    await waitForContent(page);

    const contentHeight = await page.evaluate(() => document.body.scrollHeight);
    const viewportHeight = page.viewportSize()!.height;
    test.skip(contentHeight <= viewportHeight + 1, "page fits on one screen");

    await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
    await page.waitForFunction(() => window.scrollY > 0, undefined, { timeout: 5_000 })
      .catch(() => {
        /* fall through to the assertion below, which reports the real numbers */
      });

    const { scrollY, docScrollHeight } = await page.evaluate(() => ({
      scrollY: window.scrollY,
      docScrollHeight: document.documentElement.scrollHeight,
    }));

    expect(
      scrollY,
      `${path}: body is ${contentHeight}px tall in a ${viewportHeight}px viewport but ` +
        `the page will not scroll (scrollY=${scrollY}). documentElement.scrollHeight ` +
        `is ${docScrollHeight} — if that equals the viewport height, something is ` +
        `clamping the scroll container (overflow-x on both html and body does this).`,
    ).toBeGreaterThan(0);
  });
}

test("wide tables scroll inside their card, not the document", async ({ page }) => {
  await page.goto("/logs");
  await waitForContent(page);

  // /logs renders SEVERAL .table-scroll elements: one per "Rincian" breakdown
  // card (a plain .table, no width floor) and one for the wide message table.
  // Only the last has the 900px floor, so select on that rather than .first().
  const scroller = page.locator(".table-scroll:has(.table-wide)");
  await expect(scroller).toHaveCount(1);

  if ((page.viewportSize()?.width ?? 0) <= 900) {
    const { scrollWidth, clientWidth } = await scroller.evaluate((el) => ({
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
    }));
    expect(
      scrollWidth,
      `the wide log table should overflow its card so it can be scrolled ` +
        `(scrollWidth ${scrollWidth} vs clientWidth ${clientWidth})`,
    ).toBeGreaterThan(clientWidth);
  }
});

// Every table in the app must live in a scroller. This is the general form of a
// bug found on /sessions, whose table was the one that shipped unwrapped.
test("every table is inside a horizontal scroller", async ({ page }) => {
  for (const path of ["/sessions", "/logs", "/admin/api-clients"]) {
    await page.goto(path);
    await waitForContent(page);
    const unwrapped = await page.evaluate(() =>
      [...document.querySelectorAll("table")]
        .filter((t) => !t.closest(".table-scroll"))
        .map((t) => `<table class="${t.className}">`),
    );
    expect(unwrapped, `${path} has a table outside .table-scroll`).toEqual([]);
  }
});
