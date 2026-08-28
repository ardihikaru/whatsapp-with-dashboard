import { test, expect, Page } from "@playwright/test";

// The six destinations internal/handler/nav.go builds, and the four section
// subheaders it groups them under. The point of the drawer is that a phone user
// loses NONE of them.
const DESTINATIONS = [
  "Dashboard",
  "Sessions",
  "Send Message",
  "Verify Number",
  "Message Log",
  "API Clients",
  "API Docs",
];
const SECTIONS = ["Overview", "WhatsApp", "Admin", "Docs"];

const isPhone = (page: Page) => (page.viewportSize()?.width ?? 0) <= 900;

test.describe("phone portrait: the sidebar is reachable as a drawer", () => {
  test.skip(({ page }) => !isPhone(page), "phone-only behaviour");

  test.beforeEach(async ({ page }) => {
    await page.goto("/dashboard");
    // nav-drawer.js sets this; without it the CSS keeps the sidebar in normal
    // flow and there would be no drawer to test.
    await expect(page.locator("html")).toHaveAttribute("data-nav-js", "");
  });

  test("the drawer starts closed and the hamburger is offered instead", async ({ page }) => {
    const sidebar = page.locator("#app-sidebar");
    const toggle = page.getByRole("button", { name: "Buka menu" });

    await expect(toggle).toBeVisible();
    await expect(sidebar).not.toBeInViewport();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
  });

  test("tapping the hamburger reveals every destination and every section", async ({ page }) => {
    const toggle = page.getByRole("button", { name: "Buka menu" });
    const sidebar = page.locator("#app-sidebar");

    await toggle.tap();

    await expect(sidebar).toBeInViewport();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    for (const name of DESTINATIONS) {
      await expect(
        sidebar.getByRole("link", { name, exact: true }),
        `destination "${name}" must be visible in the drawer`,
      ).toBeVisible();
    }
    for (const section of SECTIONS) {
      await expect(
        sidebar.locator(".nav-subheader", { hasText: new RegExp(`^${section}$`) }),
        `section "${section}" must survive into the drawer`,
      ).toBeVisible();
    }
    // The identity hidden from the header below 480px lives here instead.
    await expect(sidebar.locator(".nav-user")).toContainText("ardihikaru");
  });

  test("every destination is tappable, not merely painted", async ({ page }) => {
    const sidebar = page.locator("#app-sidebar");
    await page.getByRole("button", { name: "Buka menu" }).tap();
    // boundingBox() does not retry, and the drawer slides in over 180ms.
    // toBeInViewport() is satisfied by a sliver, so it can return mid-flight and
    // the hit-test then measures a position the drawer has already left. Poll
    // for the transform to land at its final x = 0 instead.
    await expect
      .poll(async () => (await sidebar.boundingBox())?.x, {
        message: "the drawer never finished sliding open",
      })
      .toBe(0);

    for (const name of DESTINATIONS) {
      const link = sidebar.getByRole("link", { name, exact: true });
      const box = await link.boundingBox();
      expect(box, `"${name}" has no layout box`).not.toBeNull();
      // 44px is the .nav-item height in layout.css and the usual minimum
      // comfortable touch target.
      expect(box!.height, `"${name}" is only ${box!.height}px tall`).toBeGreaterThanOrEqual(36);
      expect(box!.width).toBeGreaterThan(0);
      // Nothing may sit on top of the link at its own centre point.
      const hit = await page.evaluate(([x, y]) => {
        const el = document.elementFromPoint(x, y);
        return el ? !!el.closest(".nav-item") : false;
      }, [box!.x + box!.width / 2, box!.y + box!.height / 2] as [number, number]);
      expect(hit, `"${name}" is covered by another element`).toBe(true);
    }
  });

  test("the backdrop closes the drawer", async ({ page }) => {
    const toggle = page.getByRole("button", { name: "Buka menu" });
    // The backdrop is position: fixed; inset: 0, so it is always geometrically
    // in the viewport. What makes it inert is pointer-events, and that is what
    // must flip — otherwise it would swallow every tap on the page.
    await expect(page.locator(".nav-backdrop")).toHaveCSS("pointer-events", "none");

    await toggle.tap();
    await expect(page.locator("#app-sidebar")).toBeInViewport();
    await expect(page.locator(".nav-backdrop")).toHaveCSS("pointer-events", "auto");

    // Tap the far right edge, which is backdrop and not drawer.
    const size = page.viewportSize()!;
    await page.locator(".nav-backdrop").tap({
      position: { x: size.width - 10, y: size.height / 2 },
    });

    await expect(page.locator("#app-sidebar")).not.toBeInViewport();
    await expect(page.locator(".nav-backdrop")).toHaveCSS("pointer-events", "none");
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
  });

  test("Escape closes the drawer and returns focus to the hamburger", async ({ page }) => {
    const toggle = page.getByRole("button", { name: "Buka menu" });
    await toggle.tap();
    await expect(page.locator("#app-sidebar")).toBeInViewport();

    await page.keyboard.press("Escape");

    await expect(page.locator("#app-sidebar")).not.toBeInViewport();
    await expect(toggle).toBeFocused();
  });

  test("a closed drawer is out of the keyboard tab order", async ({ page }) => {
    // inert is what keeps a keyboard user from tabbing into an invisible menu.
    await expect(page.locator("#app-sidebar")).toHaveAttribute("inert", "");
    await page.getByRole("button", { name: "Buka menu" }).tap();
    await expect(page.locator("#app-sidebar")).not.toHaveAttribute("inert", "");
  });

  test("choosing a destination navigates and leaves the drawer closed", async ({ page }) => {
    await page.getByRole("button", { name: "Buka menu" }).tap();
    await page.locator("#app-sidebar").getByRole("link", { name: "Message Log", exact: true }).tap();

    await page.waitForURL(/\/logs$/);
    await expect(page.locator("#app-sidebar")).not.toBeInViewport();
    // Not "off screen" — the backdrop covers the viewport by design. The check
    // that matters is that it no longer intercepts taps on the new page.
    await expect(page.locator(".nav-backdrop")).toHaveCSS("pointer-events", "none");
    await expect(page.getByRole("button", { name: "Buka menu" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  test("the page behind an open drawer does not scroll", async ({ page }) => {
    await page.getByRole("button", { name: "Buka menu" }).tap();
    const overflow = await page.evaluate(() => getComputedStyle(document.body).overflow);
    expect(overflow).toBe("hidden");
  });
});

test.describe("desktop is unchanged", () => {
  test.skip(({ page }) => isPhone(page), "desktop-only regression guard");

  test("the sidebar stays in the layout and the hamburger stays hidden", async ({ page }) => {
    await page.goto("/dashboard");

    await expect(page.locator("#app-sidebar")).toBeInViewport();
    await expect(page.getByRole("button", { name: "Buka menu" })).toBeHidden();
    // display: none above 900px — the drawer chrome must not exist at all here.
    await expect(page.locator(".nav-backdrop")).toHaveCSS("display", "none");
    await expect(page.locator("#app-sidebar")).not.toHaveAttribute("inert", "");
  });
});
