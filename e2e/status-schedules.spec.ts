import { test, expect, Page } from "@playwright/test";

// Scheduled status posts (/status/schedules) — cmd/e2eseed's six contacts and
// two schedules (seedStatusSchedules). Runs at phone and desktop viewports
// like every other spec. Nothing here posts a status: saving a schedule only
// writes a row, and the scheduler's earliest possible fire is a future time.
//
// The session <select> offers every session (not only connected ones), so
// unlike convo-schedules.spec.ts no option injection is needed.
const SEEDED_CONTACTS = 6;
const SESSION_ID = "e2e-sess-0001";

// /status/schedules fills its tbody over HTMX and shows "Loading…" until then;
// measuring (or asserting on rows) before that sees an empty table that cannot
// overflow — see overflow.spec.ts's waitForContent for the same rule.
async function waitForRows(page: Page) {
  await page.waitForLoadState("networkidle");
  await expect(page.locator("#status-schedule-rows")).not.toContainText("Loading", { timeout: 10_000 });
}

async function overflowOffenders(page: Page): Promise<string[]> {
  // The CONFIGURED viewport width, never window.innerWidth (which inflates to
  // match overflowing content under mobile emulation).
  const vw = page.viewportSize()!.width;
  return page.evaluate((vw) => {
    const out: string[] = [];
    document.querySelectorAll<HTMLElement>("body *").forEach((el) => {
      if (el.closest(".table-scroll")) return;
      const b = el.getBoundingClientRect();
      // visually-hidden checkbox/radio inputs are positioned off the flow on purpose
      if (b.width > 0 && b.right > vw + 1 && getComputedStyle(el).opacity !== "0") {
        out.push(`${el.tagName.toLowerCase()}.${(el.className || "").toString().trim().slice(0, 40)} right=${Math.round(b.right)}`);
      }
    });
    return out.slice(0, 10);
  }, vw);
}

test("the new-schedule form lists every contact, all checked, with select all / none", async ({ page }) => {
  await page.goto("/status/schedules/new");
  const boxes = page.locator('input[name="recipients"]');
  await expect(boxes).toHaveCount(SEEDED_CONTACTS);
  for (let i = 0; i < SEEDED_CONTACTS; i++) {
    await expect(boxes.nth(i)).toBeChecked();
  }
  const count = page.locator("[data-status-recipients-count]");
  await expect(count).toHaveText(`${SEEDED_CONTACTS} selected`);

  await page.locator("[data-status-select-none]").click();
  await expect(count).toHaveText("0 selected");
  await expect(boxes.first()).not.toBeChecked();

  await page.locator("[data-status-select-all]").click();
  await expect(count).toHaveText(`${SEEDED_CONTACTS} selected`);
});

test("the summary line follows the weekday chips and the window", async ({ page }) => {
  await page.goto("/status/schedules/new");
  // Defaults tick Mon-Fri; leave only Mon and Wed.
  for (const day of ["Tue", "Thu", "Fri"]) {
    await page.locator(".ss-chip", { hasText: day }).click();
  }
  await page.locator('input[name="window_start"]').fill("07:00");
  await page.locator('input[name="window_end"]').fill("10:27");
  await expect(page.locator("[data-ss-summary]")).toHaveText(
    "Mon, Wed · once each day, at a random time between 07:00 and 10:27 Asia/Jakarta",
  );
});

test("switching the status type swaps the panel and reveals the caption", async ({ page }) => {
  await page.goto("/status/schedules/new");
  await expect(page.locator('[data-ss-panel="text"]')).toBeVisible();
  await expect(page.locator(".ss-caption")).toBeHidden();
  await page.locator('label[for="ss-kind-image"]').click();
  await expect(page.locator('[data-ss-panel="image"]')).toBeVisible();
  await expect(page.locator('[data-ss-panel="text"]')).toBeHidden();
  await expect(page.locator(".ss-caption")).toBeVisible();
});

test("an all-checked schedule can be created and appears in the list as All contacts", async ({ page }) => {
  await page.goto("/status/schedules/new");
  const name = `E2E ${Date.now()}`;
  await page.locator('input[name="name"]').fill(name);
  await page.locator('select[name="session_id"]').selectOption(SESSION_ID);
  await page.locator('textarea[name="text"]').fill("Halo dari e2e");
  await page.getByRole("button", { name: "Create" }).click();

  await expect(page).toHaveURL(/\/status\/schedules$/);
  await waitForRows(page);
  const row = page.locator("#status-schedule-rows tr", { hasText: name });
  await expect(row).toContainText("All contacts");
  await expect(row).toContainText("active");

  // Clean up through the UI so reruns stay at the seeded two rows.
  page.once("dialog", (d) => d.accept());
  await row.getByRole("button", { name: "Delete" }).click();
  await expect(page.locator("#status-schedule-rows tr", { hasText: name })).toHaveCount(0);
});

test("the seeded schedules render, pause and resume swap just their row", async ({ page }) => {
  await page.goto("/status/schedules");
  await waitForRows(page);
  const active = page.locator("#status-schedule-rows tr", { hasText: "Sapaan pagi" });
  const paused = page.locator("#status-schedule-rows tr", { hasText: "Promo akhir pekan" });
  await expect(active).toContainText("active");
  await expect(paused).toContainText("paused");
  await expect(paused).toContainText("socket disconnected");

  await active.getByRole("button", { name: "Pause" }).click();
  await expect(page.locator("#status-schedule-rows tr", { hasText: "Sapaan pagi" })).toContainText("paused");
  await page.locator("#status-schedule-rows tr", { hasText: "Sapaan pagi" }).getByRole("button", { name: "Resume" }).click();
  await expect(page.locator("#status-schedule-rows tr", { hasText: "Sapaan pagi" })).toContainText("active");
});

for (const path of ["/status/schedules", "/status/schedules/new", "/status/schedules/e2e-status-sched-paused/edit"]) {
  test(`${path} does not scroll sideways`, async ({ page }) => {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
    if (path === "/status/schedules") await waitForRows(page);
    expect(await overflowOffenders(page)).toEqual([]);
  });
}

test("light and dark screenshots of the form", async ({ page }, info) => {
  await page.goto("/status/schedules/new");
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: info.outputPath("form-light.png"), fullPage: true });
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.screenshot({ path: info.outputPath("form-dark.png"), fullPage: true });
});

// ---- AI-written posts -------------------------------------------------------
// scripts/e2e-env.sh wires dummy WSM_CODEX_CLI_* so the switch is enabled. The
// URL refuses connections, so nothing here ever reaches a real AI service.

test("the AI switch reveals the topic field, relabels the text as a fallback and updates the summary", async ({ page }) => {
  await page.goto("/status/schedules/new");
  const topic = page.locator('textarea[name="ai_topic"]');
  const sw = page.locator("label.ss-switch");
  await expect(page.locator("#ss-ai")).toBeEnabled();
  await expect(topic).toBeHidden();
  await expect(page.locator('[data-ss-panel="text"] .ss-lbl-plain')).toBeVisible();

  await sw.click();
  await expect(page.locator("#ss-ai")).toBeChecked();
  await expect(topic).toBeVisible();
  await expect(page.locator('[data-ss-panel="text"] .ss-lbl-ai')).toBeVisible();
  await expect(page.locator('[data-ss-panel="text"] .ss-lbl-plain')).toBeHidden();
  await expect(page.locator("[data-ss-summary]")).toContainText("AI-written");

  const count = page.locator("[data-ss-ai-count]");
  await expect(count).toHaveText("0 / 500");
  await topic.fill("Semangat pagi");
  await expect(count).toHaveText("13 / 500");

  await sw.click();
  await expect(topic).toBeHidden();
  await expect(page.locator("[data-ss-summary]")).not.toContainText("AI-written");
});

test("a hidden topic field cannot be reached with the keyboard", async ({ page }) => {
  await page.goto("/status/schedules/new");
  await page.locator('textarea[name="text"]').focus();
  await page.keyboard.press("Tab");
  // Off: Tab leaves the text field and never lands on the (display:none) topic.
  expect(await page.evaluate(() => document.activeElement?.getAttribute("name"))).not.toBe("ai_topic");
});

test("an AI schedule needs a topic, saves without static text, and round-trips on edit", async ({ page }) => {
  await page.goto("/status/schedules/new");
  const name = `E2E AI ${Date.now()}`;
  await page.locator('input[name="name"]').fill(name);
  await page.locator('select[name="session_id"]').selectOption(SESSION_ID);
  await page.locator("label.ss-switch").click();
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.locator(".alert-error")).toContainText("topic is required");

  await page.locator('textarea[name="ai_topic"]').fill("Tips menjaga baju putih");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page).toHaveURL(/\/status\/schedules$/);
  await waitForRows(page);
  const row = page.locator("#status-schedule-rows tr", { hasText: name });
  await expect(row).toContainText("AI");

  await row.getByRole("link", { name: "Edit" }).click();
  await expect(page.locator("#ss-ai")).toBeChecked();
  await expect(page.locator('textarea[name="ai_topic"]')).toHaveValue("Tips menjaga baju putih");
  await expect(page.locator("[data-ss-summary]")).toContainText("AI-written");

  await page.goto("/status/schedules");
  await waitForRows(page);
  page.once("dialog", (d) => d.accept());
  await page.locator("#status-schedule-rows tr", { hasText: name }).getByRole("button", { name: "Delete" }).click();
  await expect(page.locator("#status-schedule-rows tr", { hasText: name })).toHaveCount(0);
});

test("the form with the AI switch ON does not scroll sideways", async ({ page }) => {
  await page.goto("/status/schedules/new");
  await page.waitForLoadState("networkidle");
  await page.locator("label.ss-switch").click();
  await page.locator('textarea[name="ai_topic"]').fill("x ".repeat(240));
  await expect(page.locator('textarea[name="ai_topic"]')).toBeVisible();
  expect(await overflowOffenders(page)).toEqual([]);
  await page.screenshot({ path: test.info().outputPath("form-ai-on.png"), fullPage: true });
});
