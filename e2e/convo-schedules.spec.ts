import { test, expect, Page } from "@playwright/test";

// Convo Scheduling (Phase 7's browser verification) — cmd/e2eseed's fixed
// sessions and the corpus/schedule fixtures seedConvoScheduling adds
// alongside them.
//
// cmd/e2eseed's three sessions: SENDER is the "connected" one (its
// LastStatus is only a DB column though — see the harness note below),
// RECEIVER the "disconnected" one. seedConvoScheduling uses both as a real
// schedule's sender/receiver.
const SENDER_ID = "e2e-sess-0001";
const SENDER_NAME = "Nomor Utama Kasir Kemang Selatan";
const SENDER_PHONE = "6285190679041";
const RECEIVER_ID = "e2e-sess-0002";
const RECEIVER_NAME = "Cadangan Outlet Pondok Indah";
const RECEIVER_PHONE = "6281234567890";

// seedConvoScheduling's own fixed ids (cmd/e2eseed/main.go).
const ACTIVE_SCHEDULE_ID = "e2e-convo-sched-active";
const ACTIVE_SCHEDULE_NAME = "Ngopi sore — tim outlet";
const STALLED_SCHEDULE_ID = "e2e-convo-sched-stalled";
const STALLED_REASON = "session e2e-sess-0002: socket disconnected, waiting for reconnect";
// One of the 48 "ngopi" corpus templates seedConvoScheduling inserts,
// picked because it is confirmed (against a live boot, see this file's own
// header note below) to carry outgoing links: assignLinks' base pass wires
// every to-template from SOME from-template, and the opener scene's
// templates are exactly the from-side of the topic's first link.
const LINKED_TEMPLATE_ID = "e2e-convo-corpus-0001";

// THE HARNESS LIMITATION THIS FILE WORKS AROUND, AND WHY.
//
// scripts/e2e-env.sh pins WSM_WA_BACKEND=whatsmeow with no paired device
// store (see e2e/media.spec.ts's own header comment for the same root
// cause hitting /send). Every seeded session therefore reconciles to
// StateLoggedOut at boot, and wa.Snapshot.Connected is never true here —
// confirmed against a live boot of this exact harness:
//
//   $ curl .../self-convo/schedules/new
//   <select name="sender_id" class="select" required>
//           <option value="">Choose a session…</option>
//   </select>                                    (no other <option> at all)
//
// self_convo_schedule_ui.go's scheduleSessions() feeds the create/edit
// form's Sender <select> and Receivers checkboxes from
// connectedSnapshots(h.Manager.Snapshots()) — deliberately the SAME
// connected-only picker trigger.html's own participant picker uses (see
// that function's own doc comment) — so BOTH pickers render with nothing
// to choose in this harness, for a reason that has nothing to do with the
// scheduling feature under test.
//
// validateScheduleForm itself does NOT require Connected — only that
// h.Manager.Get(id) knows the session at all (confirmed the same way: a
// direct POST with sender_id=e2e-sess-0001, receiver_id=e2e-sess-0002
// against a live boot returned 303 to /self-convo/schedules, and the new
// row appeared). The gap is entirely in what the FORM offers to click, not
// in what the handler accepts. selectDisconnectedSender/checkReceiver below
// inject the exact <option>/<input> the form would already contain if this
// harness could ever report a session connected, then drive them through
// Playwright's normal selectOption/check — the real <select>/<input>
// elements and the real native form submission, not a bypass through
// page.request.
async function selectDisconnectedSender(page: Page, id: string, name: string, phone: string) {
  await page.locator('select[name="sender_id"]').evaluate(
    (el: HTMLSelectElement, opt) => {
      const o = document.createElement("option");
      o.value = opt.id;
      o.textContent = `${opt.name} (+${opt.phone})`;
      el.appendChild(o);
    },
    { id, name, phone },
  );
  await page.locator('select[name="sender_id"]').selectOption(id);
}

async function checkDisconnectedReceiver(page: Page, id: string, name: string, phone: string) {
  const container = page
    .locator('span.textfield-label:has-text("Receivers")')
    .locator("xpath=following-sibling::div[1]");
  await container.evaluate(
    (el, opt) => {
      const label = document.createElement("label");
      label.className = "convo-participant";
      const input = document.createElement("input");
      input.type = "checkbox";
      input.name = "receiver_id";
      input.value = opt.id;
      const span = document.createElement("span");
      span.textContent = `${opt.name} (+${opt.phone})`;
      label.appendChild(input);
      label.appendChild(span);
      el.appendChild(label);
    },
    { id, name, phone },
  );
  await container.locator(`input[name="receiver_id"][value="${id}"]`).check();
}

// Same shape as overflow.spec.ts's own waitForContent, narrowed to the
// regions this file's pages actually populate over HTMX.
async function waitForContent(page: Page, ids: string[]) {
  await page.waitForLoadState("networkidle");
  for (const id of ids) {
    const el = page.locator(id);
    if (await el.count()) {
      await expect(el).not.toContainText("Loading", { timeout: 10_000 });
    }
  }
}

// overflow.spec.ts's own per-element overflow scan, duplicated rather than
// imported — this suite's spec files do not share helpers across files
// (see e.g. media.spec.ts/nav-mobile.spec.ts each keeping their own).
async function assertNoElementOverflowsTheViewport(page: Page) {
  const viewportWidth = page.viewportSize()!.width;
  const offenders = await page.evaluate((vw) => {
    const out: string[] = [];
    document.querySelectorAll<HTMLElement>("body *").forEach((el) => {
      if (el.closest(".table-scroll")) return;
      const b = el.getBoundingClientRect();
      if (b.width > 0 && b.right > vw + 1) {
        const cls = (el.className || "").toString().trim().slice(0, 40);
        out.push(`<${el.tagName.toLowerCase()} class="${cls}"> extends to ${Math.round(b.right)}px`);
      }
    });
    return out.slice(0, 8);
  }, viewportWidth);
  expect(offenders, `element(s) overflow a ${viewportWidth}px viewport:\n  ${offenders.join("\n  ")}`).toEqual([]);
}

// activeScheduleRow finds the seeded "active" schedule's row by its href
// (ACTIVE_SCHEDULE_ID), not its display name — robust against the name
// column truncating or the row order changing.
function activeScheduleRow(page: Page) {
  return page.locator("#convo-schedule-rows tr", {
    has: page.locator(`a[href$="/self-convo/schedules/${ACTIVE_SCHEDULE_ID}"]`),
  });
}

test("/self-convo/schedules renders its seeded rows and does not overflow the viewport", async ({ page }) => {
  await page.goto("/self-convo/schedules");
  await waitForContent(page, ["#convo-schedule-rows"]);

  const rows = page.locator("#convo-schedule-rows tr");
  await expect(rows).not.toHaveCount(0);
  await expect(activeScheduleRow(page)).toHaveCount(1);
  await expect(activeScheduleRow(page)).toContainText(ACTIVE_SCHEDULE_NAME);

  await assertNoElementOverflowsTheViewport(page);
});

test("a schedule with no end date renders the \"no end date\" affordance", async ({ page }) => {
  await page.goto("/self-convo/schedules");
  await waitForContent(page, ["#convo-schedule-rows"]);

  await expect(activeScheduleRow(page).locator(".badge-nonstop")).toHaveText("no end date");
});

test("a stalled schedule shows its stall reason", async ({ page }) => {
  await page.goto("/self-convo/schedules");
  await waitForContent(page, ["#convo-schedule-rows"]);

  const row = page.locator("#convo-schedule-rows tr", {
    has: page.locator(`a[href$="/self-convo/schedules/${STALLED_SCHEDULE_ID}"]`),
  });
  await expect(row.locator(".badge-stalled")).toHaveText("stalled");
  await expect(row).toContainText(STALLED_REASON);

  // The schedule's own detail page carries the same reason, in the alert
  // schedule_view.html renders above the settings card.
  await page.goto(`/self-convo/schedules/${STALLED_SCHEDULE_ID}`);
  await expect(page.locator(".alert-error")).toContainText(STALLED_REASON);
});

test("the recurrence radios progressively disclose the weekly/interval sections", async ({ page }) => {
  await page.goto("/self-convo/schedules/new");

  const weeklySection = page.locator('[data-recurrence-section="weekly"]');
  const intervalSection = page.locator('[data-recurrence-section="interval"]');

  // The default form starts on "daily" (newScheduleForm's own default) —
  // neither section applies.
  await expect(weeklySection).toBeHidden();
  await expect(intervalSection).toBeHidden();

  await page.locator('input[name="recurrence"][value="weekly"]').check();
  await expect(weeklySection).toBeVisible();
  await expect(intervalSection).toBeHidden();

  await page.locator('input[name="recurrence"][value="interval"]').check();
  await expect(weeklySection).toBeHidden();
  await expect(intervalSection).toBeVisible();

  await page.locator('input[name="recurrence"][value="daily"]').check();
  await expect(weeklySection).toBeHidden();
  await expect(intervalSection).toBeHidden();
});

test("the create form submits a valid weekly schedule and lands on a page showing it", async ({ page }) => {
  const name = `E2E weekly schedule ${Date.now()}`;

  await page.goto("/self-convo/schedules/new");

  await page.locator('input[name="name"]').fill(name);
  await selectDisconnectedSender(page, SENDER_ID, SENDER_NAME, SENDER_PHONE);
  await checkDisconnectedReceiver(page, RECEIVER_ID, RECEIVER_NAME, RECEIVER_PHONE);
  await page.locator('select[name="topic"]').selectOption("ngopi");

  await page.locator('input[name="recurrence"][value="weekly"]').check();
  await expect(page.locator('[data-recurrence-section="weekly"]')).toBeVisible();
  await page.locator('input[name="weekday"][value="1"]').check();
  await page.locator('input[name="weekday"][value="3"]').check();

  await page.locator('input[name="sets_per_day"]').fill("1");
  await page.locator('input[name="window_start"]').fill("09:00");
  await page.locator('input[name="window_end"]').fill("17:00");

  await page.locator('button[type="submit"]').click();

  // PostSelfConvoScheduleCreate redirects to the LIST page (self_convo_
  // schedule_ui.go), not the new schedule's own view page — this is what
  // "lands on a page showing the new schedule" means here.
  await page.waitForURL(/\/self-convo\/schedules$/);
  await waitForContent(page, ["#convo-schedule-rows"]);
  const newRow = page.locator("#convo-schedule-rows tr", { has: page.getByRole("link", { name }) });
  await expect(newRow).toHaveCount(1);
  await expect(newRow).toContainText("Weekly");
});

test("/self-convo/templates' topic filter narrows the list and the table does not overflow", async ({ page }) => {
  await page.goto("/self-convo/templates");
  await waitForContent(page, ["#convo-template-rows"]);

  const rows = page.locator("#convo-template-rows tr");
  const unfilteredCount = await rows.count();
  expect(unfilteredCount).toBeGreaterThan(1);

  await page.locator('select[name="topic"]').selectOption("ngopi");
  await expect
    .poll(async () => rows.count(), { message: "topic filter never changed the row count" })
    .toBeLessThan(unfilteredCount);

  const filteredCount = await rows.count();
  // Every remaining row must actually be the picked topic — a filter that
  // merely dropped one unrelated row (rather than narrowing BY topic) would
  // still pass the count-only check above.
  for (let i = 0; i < filteredCount; i++) {
    await expect(rows.nth(i)).toContainText("ngopi");
  }

  await assertNoElementOverflowsTheViewport(page);
});

test("a template view shows its outgoing chain links", async ({ page }) => {
  await page.goto(`/self-convo/templates/${LINKED_TEMPLATE_ID}`);

  const linksTable = page.locator("table", { has: page.locator("th", { hasText: "Target" }) });
  await expect(linksTable).toBeVisible();
  const linkRows = linksTable.locator("tbody tr");
  await expect(linkRows).not.toHaveCount(0);
  // Every outgoing edge links to another template's own view page.
  await expect(linkRows.first().locator('a[href*="/self-convo/templates/"]')).toBeVisible();
});

test("pause then resume round-trips a schedule's state badge", async ({ page }) => {
  await page.goto("/self-convo/schedules");
  await waitForContent(page, ["#convo-schedule-rows"]);

  await expect(activeScheduleRow(page).locator(".badge-active")).toHaveText("active");

  await activeScheduleRow(page).getByRole("button", { name: "Pause" }).click();
  await expect(activeScheduleRow(page).locator(".badge-paused")).toHaveText("paused");

  // Resuming a schedule paused (not stalled) skips Planner.Resume's health
  // re-check entirely (planner.go: the check is gated on
  // `sched.State == convo.ScheduleStalled`) — the only reason this
  // round-trip succeeds without any session ever being Connected in this
  // harness (see this file's own header note).
  await activeScheduleRow(page).getByRole("button", { name: "Resume" }).click();
  await expect(activeScheduleRow(page).locator(".badge-active")).toHaveText("active");
});
