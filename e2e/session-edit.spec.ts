import { test, expect } from "@playwright/test";

// Session groups + self-reminder (this phase) — drives the real Edit/
// Delete dialogs against cmd/e2eseed's fixed sessions:
//   e2e-sess-0001 "Nomor Utama Kasir Kemang Selatan" — self-reminder flag.
//   e2e-sess-0002 "Cadangan Outlet Pondok Indah"     — groups kemang+selatan.
//   e2e-sess-0003 "Sesi Uji Coba"                     — ungrouped.
const SELF_REMINDER_ID = "e2e-sess-0001";
const SELF_REMINDER_NAME = "Nomor Utama Kasir Kemang Selatan";
const GROUPED_ID = "e2e-sess-0002";
const GROUPED_NAME = "Cadangan Outlet Pondok Indah";
const UNGROUPED_ID = "e2e-sess-0003";
const UNGROUPED_NAME = "Sesi Uji Coba";

test("sessions list renders a badge per group and the self-reminder badge", async ({ page }) => {
  await page.goto("/sessions");
  await expect(page.locator("#session-rows")).not.toContainText("Loading", { timeout: 10_000 });

  const groupedRow = page.locator("tr", { hasText: GROUPED_NAME });
  await expect(groupedRow.locator(".badge-group")).toHaveCount(2);
  await expect(groupedRow).toContainText("kemang");
  await expect(groupedRow).toContainText("selatan");

  const selfReminderRow = page.locator("tr", { hasText: SELF_REMINDER_NAME });
  await expect(selfReminderRow.locator(".badge-self-reminder")).toHaveCount(1);
});

test("the Edit dialog shows the grouped session's existing chips", async ({ page }) => {
  // Read-only: GROUPED_ID's fixture state (kemang+selatan) is also asserted
  // by the badge test above across every viewport project sharing this one
  // app instance, so this test must not mutate it.
  await page.goto("/sessions");
  await expect(page.locator("#session-rows")).not.toContainText("Loading", { timeout: 10_000 });

  const groupedRow = page.locator("tr", { hasText: GROUPED_NAME });
  await groupedRow.getByRole("button", { name: "Edit" }).click();

  const dialog = page.locator("#session-edit-dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog.locator(".se-chip")).toHaveCount(2);
  await expect(dialog).toContainText("kemang");
  await expect(dialog).toContainText("selatan");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
});

test("the Edit dialog's chip input can add and remove a group, round-tripping to no groups", async ({ page }) => {
  // Uses UNGROUPED_ID exclusively and ends the test back at zero groups —
  // idempotent across repeated runs and safe to share with the "sessions
  // list renders ... " test above, which asserts this session carries no
  // group badge.
  await page.goto("/sessions");
  await expect(page.locator("#session-rows")).not.toContainText("Loading", { timeout: 10_000 });

  const row = page.locator("tr", { hasText: UNGROUPED_NAME });
  await row.getByRole("button", { name: "Edit" }).click();

  const dialog = page.locator("#session-edit-dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog.locator(".se-chip")).toHaveCount(0);

  await dialog.locator("[data-chip-input]").fill("temp-e2e");
  await dialog.locator("[data-chip-input]").press("Enter");
  await expect(dialog.locator(".se-chip")).toHaveCount(1);
  await expect(dialog).toContainText("temp-e2e");

  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toBeHidden({ timeout: 5_000 });
  const refreshedRow = page.locator("tr", { hasText: UNGROUPED_NAME });
  await expect(refreshedRow).toContainText("temp-e2e");

  // Revert: remove the chip and save again, leaving the fixture ungrouped.
  await refreshedRow.getByRole("button", { name: "Edit" }).click();
  const dialog2 = page.locator("#session-edit-dialog");
  await expect(dialog2.locator(".se-chip")).toHaveCount(1);
  await dialog2.locator(".se-chip-remove").click();
  await expect(dialog2.locator(".se-chip")).toHaveCount(0);
  await dialog2.getByRole("button", { name: "Save" }).click();
  await expect(dialog2).toBeHidden({ timeout: 5_000 });
  await expect(page.locator("tr", { hasText: UNGROUPED_NAME }).locator(".badge-group")).toHaveCount(0);
});

test("the Delete dialog stays disabled until the session name is typed", async ({ page }) => {
  await page.goto("/sessions");
  await expect(page.locator("#session-rows")).not.toContainText("Loading", { timeout: 10_000 });

  const row = page.locator("tr", { hasText: UNGROUPED_NAME });
  await row.locator(".row-menu summary").click();
  await row.getByRole("button", { name: "Delete…" }).click();

  const dialog = page.locator("#session-delete-dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("does NOT unlink");
  await expect(dialog).toContainText("Linked devices");

  const deleteButton = dialog.locator("[data-delete-confirm-button]");
  await expect(deleteButton).toBeDisabled();
  await dialog.locator("[data-delete-confirm-input]").fill(UNGROUPED_NAME);
  await expect(deleteButton).toBeEnabled();

  // Cancel rather than actually deleting the fixture — this file only
  // proves the guard, not the delete itself (covered at the handler
  // layer, internal/handler/sessions_test.go).
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
});
