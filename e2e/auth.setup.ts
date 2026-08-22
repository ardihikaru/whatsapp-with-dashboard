import { test as setup, expect } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";

const AUTH_FILE = "e2e/.auth/admin.json";

// The one place the plaintext password is used. This project runs with trace,
// screenshot and video forced OFF in playwright.config.ts so no DOM snapshot of
// the filled form can ever be written to a report.
setup("sign in as the admin", async ({ page, baseURL }) => {
  const pwFile = process.env.WSM_E2E_PASSWORD_FILE ?? "_data/paswd";
  if (!fs.existsSync(pwFile)) {
    throw new Error(
      `${pwFile} not found. The admin password lives there and is gitignored; ` +
        `scripts/e2e-env.sh hashes the same file for the server.`,
    );
  }
  // Trailing newline stripped for the same reason the shell script strips it:
  // the server hashed the file's bytes without it.
  const password = fs.readFileSync(pwFile, "utf8").replace(/[\r\n]+$/, "");
  const username = process.env.WSM_ADMIN_USER ?? "ardihikaru";

  await page.goto("/login");
  await expect(page.getByRole("heading", { level: 4 })).toBeVisible();

  await page.locator('input[name="username"]').fill(username);
  await page.locator('input[name="password"]').fill(password);
  await page.getByRole("button", { name: /masuk|sign in|login/i }).click();

  // The dashboard is the post-login landing page; reaching it proves the
  // bcrypt check in internal/auth/admin.go accepted the credential.
  await page.waitForURL(/\/dashboard$/, { timeout: 15_000 });
  await expect(page.locator("#app-sidebar")).toBeAttached();

  fs.mkdirSync(path.dirname(AUTH_FILE), { recursive: true });
  await page.context().storageState({ path: AUTH_FILE });
});
